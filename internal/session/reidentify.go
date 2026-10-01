package session

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"sort"

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
	// Merging is greedy, so the order matters: sort for the same result
	// every run (map order is random).
	sort.Ints(speakers)

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

	// For each transcript segment, find the diarization segment with the most overlap.
	count := 0
	for i := range sess.Segments {
		seg := &sess.Segments[i]
		if seg.Speaker == "You" || seg.Source == "mic" {
			continue
		}

		bestOverlap := 0.0
		bestSpeaker := -1

		for _, ds := range diarSegments {
			overlapStart := math.Max(seg.StartTime, ds.Start)
			overlapEnd := math.Min(seg.EndTime, ds.End)
			overlap := overlapEnd - overlapStart
			if overlap > bestOverlap {
				bestOverlap = overlap
				bestSpeaker = ds.Speaker
			}
		}

		if bestSpeaker >= 0 && bestOverlap > 0 {
			seg.Speaker = speakerMap[bestSpeaker]
			count++
			if cfg.Verbose {
				fmt.Printf("  transcript seg %d [%.1fs-%.1fs] → %s (overlap %.1fs)\n",
					i, seg.StartTime, seg.EndTime, seg.Speaker, bestOverlap)
			}
		}
	}

	return count, nil
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
