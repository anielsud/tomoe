package main

import (
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
	"github.com/sosuke-ai/tomoe-pc/internal/eval"
	"github.com/sosuke-ai/tomoe-pc/internal/models"
	"github.com/sosuke-ai/tomoe-pc/internal/session"
)

// sweepOptions is the diarization settings grid `tomoe eval --sweep` tries.
type sweepOptions struct {
	thresholds []float64 // clustering threshold: higher merges more
	minOns     []float64 // shortest speech turn kept (s)
	merges     []float64 // post-merge cosine similarity; 0 = no merge step
}

// sweepResult is one diarization configuration's scores.
type sweepResult struct {
	Threshold float64 `json:"threshold"`
	MinOn     float64 `json:"min_duration_on"`
	Merge     float64 `json:"merge"`

	Initial     eval.SpeakerScore `json:"initial_diarization"`
	Diarization eval.SpeakerScore `json:"refined_diarization"`
	Split       eval.SpeakerScore `json:"final_split"`

	// Speaker accuracy by word for the initial and refined diarization and
	// the final split transcript (see eval.WordAlignment).
	WordsInitial eval.WordSpeakerScore `json:"word_speakers_initial"`
	WordsRefined eval.WordSpeakerScore `json:"word_speakers_refined"`
	WordsSplit   eval.WordSpeakerScore `json:"word_speakers_final_split"`
	WhoSaidWhat  eval.ErrorCounts      `json:"who_said_what_split"`
	Exchanges    eval.ExchangeScore    `json:"quick_exchanges_split"`
	Overlaps     eval.OverlapScore     `json:"annotated_overlaps_split"`
	// OverlapHeard is how much annotated overlap the diarization itself
	// labeled with two voices.
	OverlapHeard float64 `json:"overlap_heard_seconds"`
	Current      bool    `json:"current"` // the settings Tomoe ships with

	diar *cachedDiarization
}

const (
	currentThreshold = 1.1
	currentMinOn     = 0.3
	currentMinOff    = 0.5
	currentMerge     = 0.55
)

// runSweep runs the default pipeline once and diarization once per raw
// setting (threshold x minimum turn length), applies each merge setting to
// every raw result, and scores every combination. Raw and merged results
// are cached, so extending the grid only runs what's new.
func runSweep(opts evalOptions, cfg *config.Config, status *models.Status, samples []float32, ref *eval.Reference, cache *evalCache, outDir string) error {
	sw := opts.sweep
	type rawKey struct{ threshold, minOn float64 }
	var raws []rawKey
	for _, t := range sw.thresholds {
		for _, m := range sw.minOns {
			raws = append(raws, rawKey{t, m})
		}
	}
	workers := max(1, runtime.NumCPU()/5)
	threads := opts.threads
	if threads <= 0 {
		threads = max(2, runtime.NumCPU()/(workers+1))
	}
	fmt.Printf("Sweep: %d diarization settings x %d merge settings = %d configurations, %d diarizations at a time with %d threads each\n",
		len(raws), len(sw.merges), len(raws)*len(sw.merges), workers, threads)

	var wg sync.WaitGroup
	run := &evalRun{Name: "default", Tuning: "single-pass; threshold 0.65, sticky and short-segment rules off"}
	var pipeErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		pipeErr = runPipeline(run, cfg, status, samples, threads, cache)
	}()

	var mu sync.Mutex
	var results []*sweepResult
	var firstErr error
	jobs := make(chan rawKey)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := range jobs {
				raw, err := sweepRaw(status, samples, k.threshold, k.minOn, threads, cache)
				if err == nil {
					for _, m := range sw.merges {
						var d *cachedDiarization
						if d, err = sweepMerged(status, samples, raw, k.threshold, k.minOn, m, cache); err != nil {
							break
						}
						mu.Lock()
						results = append(results, &sweepResult{Threshold: k.threshold, MinOn: k.minOn, Merge: m, diar: d,
							Current: k.threshold == currentThreshold && k.minOn == currentMinOn && m == currentMerge})
						mu.Unlock()
					}
				}
				if err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = fmt.Errorf("diarization threshold %.2f min-on %.2f: %w", k.threshold, k.minOn, err)
					}
					mu.Unlock()
				}
			}
		}()
	}
	for _, k := range raws {
		jobs <- k
	}
	close(jobs)
	wg.Wait()
	if pipeErr != nil {
		return pipeErr
	}
	if firstErr != nil {
		return firstErr
	}
	if cache != nil {
		_ = cache.save()
	}

	var refWords []string
	for _, t := range ref.Turns {
		refWords = append(refWords, eval.Words(t.Text)...)
	}
	scoreRun(run, ref, refWords, opts.collar) // the run's word alignment, shared below
	for _, r := range results {
		d := r.diar
		initial := diarLabeled(d.Raw, d.RawMap)
		r.Initial = eval.ScoreSpeakers(ref, initial, opts.collar)
		r.WordsInitial = diarWordScore(run, initial, r.Initial.Mapping)
		labeled := diarLabeled(d.Merged, d.MergedMap)
		r.Diarization = eval.ScoreSpeakers(ref, labeled, opts.collar)
		r.WordsRefined = diarWordScore(run, labeled, r.Diarization.Mapping)
		r.OverlapHeard = eval.ScoreAnnotatedOverlaps(ref, labeled, r.Diarization.Mapping, 3).DetectedSeconds
		split, _ := session.SplitByDiarization(append([]session.Segment(nil), run.segs...), d.Merged, d.MergedMap)
		lab := segmentsLabeled(split)
		r.Split = eval.ScoreSpeakers(ref, lab, opts.collar)
		r.WordsSplit = run.align.Score(segmentSpeakers(split), r.Split.Mapping)
		r.WhoSaidWhat = eval.SpeakerAttributedErrors(ref, lab, r.Split.Mapping)
		r.Exchanges = eval.ScoreQuickExchanges(ref, lab, r.Split.Mapping, 8, 6, 3)
		r.Overlaps = eval.ScoreAnnotatedOverlaps(ref, lab, r.Split.Mapping, 3)
	}
	sort.Slice(results, func(i, j int) bool {
		if a, b := results[i].WordsSplit.Accuracy(), results[j].WordsSplit.Accuracy(); a != b {
			return a > b
		}
		return results[i].WordsSplit.Buckets[0].Accuracy() > results[j].WordsSplit.Buckets[0].Accuracy()
	})

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	text := formatSweep(results, ref)
	if err := os.WriteFile(filepath.Join(outDir, "sweep.txt"), []byte(text), 0o644); err != nil {
		return err
	}
	js, _ := json.MarshalIndent(results, "", "  ")
	if err := os.WriteFile(filepath.Join(outDir, "sweep.json"), js, 0o644); err != nil {
		return err
	}
	fmt.Println()
	fmt.Print(text)
	fmt.Printf("\nWrote %s (sweep.txt, sweep.json)\n", outDir)
	return nil
}

func sweepRawKey(status *models.Status, threshold, minOn float64) string {
	return fmt.Sprintf("raw|%s|%s|%v|%v|%v", status.SpeakerSegmentationPath, status.SpeakerEmbeddingPath, threshold, minOn, currentMinOff)
}

// sweepRaw runs (or loads) diarization without the merge step.
func sweepRaw(status *models.Status, samples []float32, threshold, minOn float64, threads int, cache *evalCache) (*cachedDiarization, error) {
	key := sweepRawKey(status, threshold, minOn)
	if cache != nil {
		if d, ok := cache.loadDiarization(key); ok {
			return d, nil
		}
	}
	began := time.Now()
	raw, rawMap, err := session.Diarize(samples, session.DiarizeConfig{
		SegmentationModelPath: status.SpeakerSegmentationPath,
		EmbeddingModelPath:    status.SpeakerEmbeddingPath,
		Threshold:             float32(threshold),
		NumThreads:            threads,
		MinDurationOn:         minOn,
		MinDurationOff:        currentMinOff,
	})
	if err != nil {
		return nil, err
	}
	d := &cachedDiarization{Raw: raw, RawMap: rawMap}
	fmt.Printf("  diarization threshold %.2f, min turn %.2fs: %d speakers in %s\n", threshold, minOn, len(rawMap), formatDuration(time.Since(began).Seconds()))
	if cache != nil {
		_ = cache.storeDiarization(key, d)
	}
	return d, nil
}

// sweepMerged applies the similar-speaker merge (or none, for merge 0).
func sweepMerged(status *models.Status, samples []float32, raw *cachedDiarization, threshold, minOn, merge float64, cache *evalCache) (*cachedDiarization, error) {
	if merge == 0 {
		return &cachedDiarization{Raw: raw.Raw, RawMap: raw.RawMap, Merged: raw.Raw, MergedMap: raw.RawMap}, nil
	}
	key := fmt.Sprintf("%s|merge=%v", sweepRawKey(status, threshold, minOn), merge)
	if cache != nil {
		if d, ok := cache.loadDiarization(key); ok {
			return d, nil
		}
	}
	merged, mergedMap := session.MergeSimilarSpeakers(append([]session.DiarizeSegment(nil), raw.Raw...), copyMap(raw.RawMap), samples, status.SpeakerEmbeddingPath, merge, false)
	d := &cachedDiarization{Raw: raw.Raw, RawMap: raw.RawMap, Merged: merged, MergedMap: mergedMap}
	if cache != nil {
		_ = cache.storeDiarization(key, d)
	}
	return d, nil
}

func formatSweep(results []*sweepResult, ref *eval.Reference) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Diarization sweep: %d configurations, %d reference speakers. Sorted by final (split) right speaker by word.\n", len(results), len(ref.Speakers()))
	fmt.Fprintln(&b, "thresh: clustering threshold (higher merges more). min-on: shortest turn kept. merge: post-merge similarity (0 = off).")
	fmt.Fprintln(&b, "Each pass: right speaker by word, overall / in 1-3 word turns. people: speakers with a cluster of their own. * = current.")
	if len(results) > 0 {
		w := results[0].WordsSplit
		fmt.Fprintf(&b, "Words scored: %d of %d reference words (%d in 1-3 word turns).\n", w.Words, w.RefWords, w.Buckets[0].Words)
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "  thresh min-on merge |  initial diarization   |  refined diarization          |  final, split           | overlap")
	fmt.Fprintln(&b, "                      | overall  short  people | overall  short  people clusters | overall  short  4-15w  | heard  (refined: words with nobody)")
	for _, r := range results {
		mark := " "
		if r.Current {
			mark = "*"
		}
		fmt.Fprintf(&b, "%s  %5.2f  %4.2f  %4.2f | %s %s   %d/%d  | %s %s   %d/%d   %4d    | %s %s %s | %4.1fs  %s\n",
			mark, r.Threshold, r.MinOn, r.Merge,
			pct(r.WordsInitial.Accuracy()), pct(r.WordsInitial.Buckets[0].Accuracy()), r.Initial.RefSpeakersMatched, r.Initial.RefSpeakers,
			pct(r.WordsRefined.Accuracy()), pct(r.WordsRefined.Buckets[0].Accuracy()), r.Diarization.RefSpeakersMatched, r.Diarization.RefSpeakers, r.Diarization.HypSpeakers,
			pct(r.WordsSplit.Accuracy()), pct(r.WordsSplit.Buckets[0].Accuracy()), pct(r.WordsSplit.Buckets[1].Accuracy()),
			r.OverlapHeard, pct(ratio(r.WordsRefined.Unlabeled, r.WordsRefined.Words)))
	}
	return b.String()
}
