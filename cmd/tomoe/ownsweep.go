package main

import (
	"encoding/gob"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sosuke-ai/tomoe-pc/internal/config"
	"github.com/sosuke-ai/tomoe-pc/internal/diarize"
	"github.com/sosuke-ai/tomoe-pc/internal/eval"
	"github.com/sosuke-ai/tomoe-pc/internal/models"
	"github.com/sosuke-ai/tomoe-pc/internal/session"
)

// ownSweepOptions is the settings grid for `tomoe eval --sweep
// --own-diarizer`, which runs Tomoe's own step-by-step diarization
// (internal/diarize): segmentation and embeddings are computed once and
// cached, so each setting costs only clustering and reconstruction.
type ownSweepOptions struct {
	thresholds []float64 // complete-linkage cosine-distance cut
	merges     []float64 // centroid merge similarity; 0 = none
	roundings  []float64 // where the averaged speaker count rounds up
	minOns     []float64 // shortest turn kept (s)
}

// ownSweepResult is one setting's scores.
type ownSweepResult struct {
	Params   diarize.Params `json:"params"`
	Clusters int            `json:"clusters"`

	Refined      eval.SpeakerScore     `json:"diarization"`
	WordsRefined eval.WordSpeakerScore `json:"word_speakers_diarization"`
	Split        eval.SpeakerScore     `json:"final_split"`
	WordsSplit   eval.WordSpeakerScore `json:"word_speakers_final_split"`
	Overlaps     eval.OverlapScore     `json:"annotated_overlaps_split"`
	OverlapHeard float64               `json:"overlap_heard_seconds"`
	// WorstNeighbor is the lowest final word accuracy among settings one
	// grid step away on any axis: high means a plateau, not a cliff.
	WorstNeighbor float64 `json:"worst_neighbor_word_accuracy"`

	grid [4]int // index along each axis
}

func runOwnSweep(opts evalOptions, cfg *config.Config, status *models.Status, samples []float32, ref *eval.Reference, cache *evalCache, outDir string) error {
	sw := opts.ownSweep
	threads := opts.threads
	if threads <= 0 {
		threads = 2
	}
	workers := max(1, runtime.NumCPU()/threads)

	var wg sync.WaitGroup
	run := &evalRun{Name: "default", Tuning: "single-pass; threshold 0.65, sticky and short-segment rules off"}
	var pipeErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		pipeErr = runPipeline(run, cfg, status, samples, threads, cache)
	}()
	embModel := status.SpeakerEmbeddingPath
	if opts.embeddingModel != "" {
		embModel = opts.embeddingModel
	}
	fmt.Printf("Diarization embedding model: %s\n", filepath.Base(embModel))
	prep, err := preparedDiarization(status.SpeakerSegmentationPath, embModel, samples, workers, threads, cache)
	wg.Wait()
	if err != nil {
		return err
	}
	if pipeErr != nil {
		return pipeErr
	}
	if cache != nil {
		_ = cache.save()
	}
	var refWords []string
	for _, t := range ref.Turns {
		refWords = append(refWords, eval.Words(t.Text)...)
	}
	scoreRun(run, ref, refWords, opts.collar)

	began := time.Now()
	type clusterKey struct{ t, m int }
	clustered := map[clusterKey][]int{}
	for ti, t := range sw.thresholds {
		base := prep.Cluster(t, 0)
		for mi, m := range sw.merges {
			if m > 0 {
				clustered[clusterKey{ti, mi}] = prep.MergeClusters(base, m)
			} else {
				clustered[clusterKey{ti, mi}] = base
			}
		}
	}
	fmt.Printf("Clustered %d threshold x %d merge settings in %s\n", len(sw.thresholds), len(sw.merges), formatDuration(time.Since(began).Seconds()))

	var results []*ownSweepResult
	for ti, t := range sw.thresholds {
		for mi, m := range sw.merges {
			for ri, r := range sw.roundings {
				for oi, o := range sw.minOns {
					results = append(results, &ownSweepResult{
						Params: diarize.Params{Threshold: t, MergeSimilarity: m, SpeakerCountRounding: r, MinDurationOn: o, MinDurationOff: 0.5},
						grid:   [4]int{ti, mi, ri, oi},
					})
				}
			}
		}
	}
	jobs := make(chan *ownSweepResult)
	var sw2 sync.WaitGroup
	for w := 0; w < runtime.NumCPU(); w++ {
		sw2.Add(1)
		go func() {
			defer sw2.Done()
			for res := range jobs {
				clusters := clustered[clusterKey{res.grid[0], res.grid[1]}]
				segs, labels := prep.Reconstruct(clusters, res.Params)
				res.Clusters = len(labels)
				lab := diarLabeled(segs, labels)
				res.Refined = eval.ScoreSpeakers(ref, lab, opts.collar)
				res.WordsRefined = diarWordScore(run, lab, res.Refined.Mapping)
				res.OverlapHeard = eval.ScoreAnnotatedOverlaps(ref, lab, res.Refined.Mapping, 3).DetectedSeconds
				split, _ := session.SplitByDiarization(append([]session.Segment(nil), run.segs...), segs, labels)
				sl := segmentsLabeled(split)
				res.Split = eval.ScoreSpeakers(ref, sl, opts.collar)
				res.WordsSplit = run.align.Score(segmentSpeakers(split), res.Split.Mapping)
				res.Overlaps = eval.ScoreAnnotatedOverlaps(ref, sl, res.Split.Mapping, 3)
			}
		}()
	}
	for _, r := range results {
		jobs <- r
	}
	close(jobs)
	sw2.Wait()
	fmt.Printf("Scored %d settings in %s\n", len(results), formatDuration(time.Since(began).Seconds()))

	byGrid := map[[4]int]*ownSweepResult{}
	for _, r := range results {
		byGrid[r.grid] = r
	}
	for _, r := range results {
		worst := r.WordsSplit.Accuracy()
		for axis := 0; axis < 4; axis++ {
			for _, step := range []int{-1, 1} {
				g := r.grid
				g[axis] += step
				if n, ok := byGrid[g]; ok {
					worst = min(worst, n.WordsSplit.Accuracy())
				}
			}
		}
		r.WorstNeighbor = worst
	}
	sort.Slice(results, func(i, j int) bool { return results[i].WordsSplit.Accuracy() > results[j].WordsSplit.Accuracy() })

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	text := formatOwnSweep(results, prep, ref, filepath.Base(embModel))
	if err := os.WriteFile(filepath.Join(outDir, "own-sweep.txt"), []byte(text), 0o644); err != nil {
		return err
	}
	js, _ := json.MarshalIndent(results, "", "  ")
	if err := os.WriteFile(filepath.Join(outDir, "own-sweep.json"), js, 0o644); err != nil {
		return err
	}
	fmt.Println()
	fmt.Print(text)
	fmt.Printf("\nWrote %s (own-sweep.txt, own-sweep.json)\n", outDir)
	return nil
}

// preparedDiarization runs (or loads from cache) the expensive diarization
// steps for samples.
func preparedDiarization(segModel, embModel string, samples []float32, workers, threads int, cache *evalCache) (*diarize.Prepared, error) {
	var path string
	if cache != nil {
		path = filepath.Join(cache.dir, "prepared-"+hashString(segModel+"|"+embModel)+".gob")
		if f, err := os.Open(path); err == nil {
			var p diarize.Prepared
			err := gob.NewDecoder(f).Decode(&p)
			f.Close()
			if err == nil {
				fmt.Println("Diarization segmentation and embeddings loaded from cache")
				return &p, nil
			}
		}
	}
	fmt.Printf("Preparing diarization (segmentation and embeddings, %d workers x %d threads)...\n", workers, threads)
	began := time.Now()
	p, err := diarize.Prepare(samples, segModel, embModel, workers, threads)
	if err != nil {
		return nil, err
	}
	fmt.Printf("  prepared %d window-speaker embeddings in %s\n", len(p.Embeddings), formatDuration(time.Since(began).Seconds()))
	if path != "" {
		if f, err := os.Create(path); err == nil {
			if err := gob.NewEncoder(f).Encode(p); err != nil {
				fmt.Printf("Note: couldn't cache prepared diarization: %v\n", err)
			}
			f.Close()
		}
	}
	return p, nil
}

func formatOwnSweep(results []*ownSweepResult, prep *diarize.Prepared, ref *eval.Reference, embName string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Own diarizer sweep: %d settings from %d cached window-speaker embeddings (%s), %d reference speakers.\n", len(results), len(prep.Embeddings), embName, len(ref.Speakers()))
	fmt.Fprintln(&b, "Sorted by final (split) right speaker by word. thresh: clustering cut (higher merges more). merge: centroid merge")
	fmt.Fprintln(&b, "similarity (0 = none). round: where the averaged speaker count rounds up (lower keeps more overlap). min-on: shortest turn.")
	fmt.Fprintln(&b, "worst nbr: lowest final word accuracy one grid step away (plateau vs cliff). people: speakers with their own cluster.")
	if len(results) > 0 {
		w := results[0].WordsSplit
		fmt.Fprintf(&b, "Words scored: %d of %d (%d in 1-3 word turns, %d in 4-15).\n", w.Words, w.RefWords, w.Buckets[0].Words, w.Buckets[1].Words)
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "  thresh merge round min-on | diarization: overall short people clusters | final split: overall 1-3w  4-15w  worst-nbr interj | overlap heard")
	limit := min(len(results), 40)
	for _, r := range results[:limit] {
		p := r.Params
		fmt.Fprintf(&b, "  %5.2f  %4.2f  %4.2f  %4.2f  |              %s %s  %d/%d  %4d    |             %s %s %s  %s    %d/%d  | %4.1fs\n",
			p.Threshold, p.MergeSimilarity, p.SpeakerCountRounding, p.MinDurationOn,
			pct(r.WordsRefined.Accuracy()), pct(r.WordsRefined.Buckets[0].Accuracy()), r.Refined.RefSpeakersMatched, r.Refined.RefSpeakers, r.Clusters,
			pct(r.WordsSplit.Accuracy()), pct(r.WordsSplit.Buckets[0].Accuracy()), pct(r.WordsSplit.Buckets[1].Accuracy()), pct(r.WorstNeighbor),
			r.Overlaps.RightSpeaker, r.Overlaps.Interjections, r.OverlapHeard)
	}
	if len(results) > limit {
		fmt.Fprintf(&b, "  ... %d more in own-sweep.json\n", len(results)-limit)
	}
	return b.String()
}
