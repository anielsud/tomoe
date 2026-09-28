package session

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"regexp"
	"slices"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"

	"github.com/sosuke-ai/tomoe-pc/internal/sigfix"
	"github.com/sosuke-ai/tomoe-pc/internal/speaker"
)

const pcmSampleRate = 16000

// DiarizeConfig holds configuration for diarization-based speaker re-identification.
type DiarizeConfig struct {
	SegmentationModelPath string
	EmbeddingModelPath    string
	NumSpeakers           int     // 0 = auto-detect
	Threshold             float32 // clustering threshold (used when NumSpeakers=0)
	MergeThreshold        float64 // cosine similarity threshold for post-merge (0 = disabled)
	UseGPU                bool    // use CUDA execution provider if available
	Verbose               bool
}

// DiarizeSegment represents a speaker-labeled segment from diarization.
type DiarizeSegment struct {
	Start   float64
	End     float64
	Speaker int
}

// Diarize runs neural speaker diarization on raw audio samples.
// Returns diarization segments and a speaker ID → label map ("Person 1", "Person 2", etc.).
func Diarize(samples []float32, cfg DiarizeConfig) ([]DiarizeSegment, map[int]string, error) {
	provider := "cpu"
	numThreads := 4
	if cfg.UseGPU {
		provider = "cuda"
		numThreads = 1
	}

	sdConfig := sherpa.OfflineSpeakerDiarizationConfig{}
	sdConfig.Segmentation.Pyannote.Model = cfg.SegmentationModelPath
	sdConfig.Segmentation.NumThreads = numThreads
	sdConfig.Segmentation.Provider = provider
	sdConfig.Embedding.Model = cfg.EmbeddingModelPath
	sdConfig.Embedding.NumThreads = numThreads
	sdConfig.Embedding.Provider = provider

	if cfg.NumSpeakers > 0 {
		sdConfig.Clustering.NumClusters = cfg.NumSpeakers
	} else {
		sdConfig.Clustering.NumClusters = 0
		sdConfig.Clustering.Threshold = cfg.Threshold
		if sdConfig.Clustering.Threshold <= 0 {
			sdConfig.Clustering.Threshold = 1.1
		}
	}

	sdConfig.MinDurationOn = 0.3
	sdConfig.MinDurationOff = 0.5

	sd := sherpa.NewOfflineSpeakerDiarization(&sdConfig)
	if sd == nil {
		return nil, nil, fmt.Errorf("failed to create diarization engine (check model paths)")
	}
	// ONNX Runtime re-installs SIGSEGV without SA_ONSTACK on each create;
	// re-patch so a stray segfault in cgo lands on the alt-stack instead
	// of trampling Go's signal-handling.
	sigfix.AfterSherpa()
	defer sherpa.DeleteOfflineSpeakerDiarization(sd)

	if sd.SampleRate() != pcmSampleRate {
		return nil, nil, fmt.Errorf("diarization expects %dHz, audio is %dHz", sd.SampleRate(), pcmSampleRate)
	}

	if cfg.Verbose {
		fmt.Printf("Running diarization on %.1fs of audio...\n", float64(len(samples))/float64(pcmSampleRate))
	}

	rawSegments := sd.Process(samples)

	if cfg.Verbose {
		fmt.Printf("Diarization found %d segments:\n", len(rawSegments))
		for _, ds := range rawSegments {
			fmt.Printf("  %.1fs - %.1fs  speaker_%d\n", ds.Start, ds.End, ds.Speaker)
		}
	}

	// Convert to our type and build speaker map
	segments := make([]DiarizeSegment, len(rawSegments))
	speakerMap := make(map[int]string)
	nextLabel := 1
	for i, ds := range rawSegments {
		segments[i] = DiarizeSegment{Start: float64(ds.Start), End: float64(ds.End), Speaker: ds.Speaker}
		if _, ok := speakerMap[ds.Speaker]; !ok {
			speakerMap[ds.Speaker] = fmt.Sprintf("Person %d", nextLabel)
			nextLabel++
		}
	}

	// Post-processing: merge similar speaker clusters if configured
	if cfg.MergeThreshold > 0 && len(speakerMap) > 1 {
		segments, speakerMap = MergeSimilarSpeakers(segments, speakerMap, samples,
			cfg.EmbeddingModelPath, cfg.MergeThreshold, cfg.Verbose)
	}

	return segments, speakerMap, nil
}

// MergeSimilarSpeakers post-processes diarization results by extracting per-cluster
// embeddings and merging clusters whose mean embeddings are very similar.
// This fixes over-segmentation where one speaker gets split into multiple clusters.
// mergeThreshold is the cosine similarity above which two clusters are merged (e.g., 0.65).
func MergeSimilarSpeakers(segments []DiarizeSegment, speakerMap map[int]string,
	samples []float32, embeddingModelPath string, mergeThreshold float64, verbose bool) ([]DiarizeSegment, map[int]string) {

	if len(speakerMap) <= 1 {
		return segments, speakerMap
	}

	embedder, err := speaker.NewEmbedder(embeddingModelPath)
	if err != nil {
		if verbose {
			fmt.Printf("  merge: failed to create embedder: %v\n", err)
		}
		return segments, speakerMap
	}
	defer embedder.Close()

	// Collect audio for each speaker cluster
	clusterAudio := make(map[int][]float32)
	for _, seg := range segments {
		startIdx := int(seg.Start * pcmSampleRate)
		endIdx := int(seg.End * pcmSampleRate)
		if startIdx < 0 {
			startIdx = 0
		}
		if endIdx > len(samples) {
			endIdx = len(samples)
		}
		if startIdx >= endIdx {
			continue
		}
		clusterAudio[seg.Speaker] = append(clusterAudio[seg.Speaker], samples[startIdx:endIdx]...)
	}

	// Compute embedding per cluster (use up to 30s of audio per cluster)
	const maxSamples = 30 * pcmSampleRate
	clusterEmbeddings := make(map[int][]float32)
	for spk, audio := range clusterAudio {
		if len(audio) > maxSamples {
			audio = audio[:maxSamples]
		}
		emb, err := embedder.Extract(audio)
		if err != nil {
			if verbose {
				fmt.Printf("  merge: embedding failed for speaker %d: %v\n", spk, err)
			}
			continue
		}
		clusterEmbeddings[spk] = emb
	}

	if len(clusterEmbeddings) <= 1 {
		return segments, speakerMap
	}

	// Find pairs to merge via cosine similarity
	// mergeMap[old] = canonical — maps merged speaker IDs to the cluster they merge into
	mergeMap := make(map[int]int)
	speakers := make([]int, 0, len(clusterEmbeddings))
	for spk := range clusterEmbeddings {
		speakers = append(speakers, spk)
	}

	for i := 0; i < len(speakers); i++ {
		if _, merged := mergeMap[speakers[i]]; merged {
			continue
		}
		for j := i + 1; j < len(speakers); j++ {
			if _, merged := mergeMap[speakers[j]]; merged {
				continue
			}
			sim := speaker.CosineSimilarity(clusterEmbeddings[speakers[i]], clusterEmbeddings[speakers[j]])
			if verbose {
				fmt.Printf("  merge: speaker %d vs %d: cosine=%.3f\n", speakers[i], speakers[j], sim)
			}
			if sim >= mergeThreshold {
				mergeMap[speakers[j]] = speakers[i]
				if verbose {
					fmt.Printf("  merge: merging speaker %d → %d (sim=%.3f >= %.3f)\n",
						speakers[j], speakers[i], sim, mergeThreshold)
				}
			}
		}
	}

	if len(mergeMap) == 0 {
		return segments, speakerMap
	}

	// Resolve transitive merges: if A→B and B→C, then A→C
	resolve := func(id int) int {
		for {
			target, ok := mergeMap[id]
			if !ok {
				return id
			}
			id = target
		}
	}

	// Apply merges to segments
	for i := range segments {
		segments[i].Speaker = resolve(segments[i].Speaker)
	}

	// Rebuild speaker map with sequential labels
	newMap := make(map[int]string)
	nextLabel := 1
	for _, seg := range segments {
		if _, ok := newMap[seg.Speaker]; !ok {
			newMap[seg.Speaker] = fmt.Sprintf("Person %d", nextLabel)
			nextLabel++
		}
	}

	if verbose {
		fmt.Printf("  merge: %d clusters → %d after merging\n", len(speakerMap), len(newMap))
	}

	return segments, newMap
}

// ReidentifyByDiarization re-identifies speakers in a session using neural diarization.
func ReidentifyByDiarization(sess *Session, cfg DiarizeConfig) (int, error) {
	if sess.AudioPath == "" {
		return 0, fmt.Errorf("session has no recorded audio")
	}
	if _, err := os.Stat(sess.AudioPath); err != nil {
		return 0, fmt.Errorf("audio file not found: %s", sess.AudioPath)
	}
	if !slices.ContainsFunc(sess.Segments, diarizable) {
		return 0, nil
	}

	// Extract monitor audio (format-aware: M4A track extraction or legacy MP3 midpoint split)
	samples, err := extractMonitorAudio(sess.AudioPath, sess)
	if err != nil {
		return 0, fmt.Errorf("extracting monitor audio: %w", err)
	}
	if samples == nil {
		return 0, nil
	}

	if cfg.Verbose {
		fmt.Printf("Monitor audio: %d samples, %.1fs for diarization\n",
			len(samples), float64(len(samples))/float64(pcmSampleRate))
	}

	diarSegments, speakerMap, err := Diarize(samples, cfg)
	if err != nil {
		return 0, err
	}

	if len(diarSegments) == 0 {
		return 0, nil
	}

	return relabelByDiarization(sess.Segments, diarSegments, speakerMap, cfg.Verbose), nil
}

// diarizable reports whether a transcript segment's speaker label should
// come from diarization: not the mic ("You"), and not "System Audio",
// which live transcription uses for a whole-system audio tap (macOS's
// "Everything" source) precisely because per-speaker clustering isn't
// meaningful there.
func diarizable(seg Segment) bool {
	return seg.Source != "mic" && seg.Speaker != "You" && seg.Speaker != "System Audio"
}

// hintLabel matches a live speaker label with a video-hint name attached,
// "Person N (Name)" (see speaker.Tracker).
var hintLabel = regexp.MustCompile(`^Person \d+ \((.+)\)$`)

// relabelByDiarization gives each diarizable segment the label of the
// diarization speaker it overlaps most, returning how many it relabeled.
//
// Diarization only knows anonymous clusters, but live labels can carry a
// real name from a video hint. A cluster whose segments were live-labeled
// with a name keeps it, "Person K (Name)"; if its segments carried
// different names, the one with the most speaking time wins (on a tie,
// the longer name, so a full name beats a truncated read of it).
func relabelByDiarization(segs []Segment, diar []DiarizeSegment, speakerMap map[int]string, verbose bool) int {
	assigned := make([]int, len(segs))
	nameTime := make(map[int]map[string]float64)
	for i, seg := range segs {
		assigned[i] = -1
		if !diarizable(seg) {
			continue
		}

		bestOverlap := 0.0
		for _, ds := range diar {
			overlap := math.Min(seg.EndTime, ds.End) - math.Max(seg.StartTime, ds.Start)
			if overlap > bestOverlap {
				bestOverlap = overlap
				assigned[i] = ds.Speaker
			}
		}
		if assigned[i] < 0 {
			continue
		}
		if m := hintLabel.FindStringSubmatch(seg.Speaker); m != nil {
			if nameTime[assigned[i]] == nil {
				nameTime[assigned[i]] = make(map[string]float64)
			}
			nameTime[assigned[i]][m[1]] += seg.EndTime - seg.StartTime
		}
	}

	labels := make(map[int]string, len(speakerMap))
	for spk, label := range speakerMap {
		if name := topName(nameTime[spk]); name != "" {
			label = fmt.Sprintf("%s (%s)", label, name)
		}
		labels[spk] = label
	}

	count := 0
	for i := range segs {
		label, ok := labels[assigned[i]]
		if assigned[i] < 0 || !ok {
			continue
		}
		segs[i].Speaker = label
		count++
		if verbose {
			fmt.Printf("  transcript seg %d [%.1fs-%.1fs] → %s\n", i, segs[i].StartTime, segs[i].EndTime, label)
		}
	}
	return count
}

// topName returns the name with the most speaking time (ties: the longer
// name, then alphabetical, so the result is deterministic).
func topName(nameTime map[string]float64) string {
	best, bestTime := "", -1.0
	for name, t := range nameTime {
		if t > bestTime || (t == bestTime && (len(name) > len(best) || (len(name) == len(best) && name < best))) {
			best, bestTime = name, t
		}
	}
	return best
}

// extractMonitorAudio returns the monitor audio samples for diarization.
// For M4A files: extracts the appropriate track directly (track 1 for dual-source, track 0 for single-source).
// For legacy MP3 files: falls back to midpoint split for dual-source backward compat.
// Returns nil if the session has no monitor source.
func extractMonitorAudio(audioPath string, sess *Session) ([]float32, error) {
	hasMic := false
	hasMonitor := false
	for _, src := range sess.Sources {
		if src == "mic" {
			hasMic = true
		}
		if src == "monitor" {
			hasMonitor = true
		}
	}

	if !hasMonitor {
		return nil, nil
	}

	if IsM4A(audioPath) {
		if hasMic {
			// Dual-source M4A: track 0=mixed, track 1=mic, track 2=monitor
			return DecodeTrackToFloat32(audioPath, 2)
		}
		// Single-source M4A: monitor is track 0
		return DecodeTrackToFloat32(audioPath, 0)
	}

	// Legacy MP3 format
	allSamples, err := DecodeToFloat32(audioPath)
	if err != nil {
		return nil, err
	}

	if hasMic {
		// Legacy dual-source MP3: midpoint split
		return allSamples[len(allSamples)/2:], nil
	}

	// Legacy monitor-only MP3
	return allSamples, nil
}

// DecodeToFloat32 decodes an audio file (MP3, WAV, etc.) to 16kHz mono float32 PCM using ffmpeg.
func DecodeToFloat32(path string) ([]float32, error) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return nil, fmt.Errorf("ffmpeg not found: install ffmpeg")
	}

	cmd := exec.Command("ffmpeg",
		"-i", path,
		"-ar", fmt.Sprintf("%d", pcmSampleRate),
		"-ac", "1",
		"-f", "s16le",
		"-acodec", "pcm_s16le",
		"-",
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("creating stdout pipe: %w", err)
	}
	cmd.Stderr = nil // suppress ffmpeg log output

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting ffmpeg: %w", err)
	}

	data, err := io.ReadAll(stdout)
	if err != nil {
		return nil, fmt.Errorf("reading ffmpeg output: %w", err)
	}

	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("ffmpeg exited with error: %w", err)
	}

	// Convert s16le bytes to float32
	numSamples := len(data) / 2
	samples := make([]float32, numSamples)
	for i := range numSamples {
		s16 := int16(binary.LittleEndian.Uint16(data[i*2 : i*2+2]))
		samples[i] = float32(s16) / float32(math.MaxInt16)
	}

	return samples, nil
}
