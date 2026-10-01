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
	"github.com/sosuke-ai/tomoe-pc/internal/diarize"
	"github.com/sosuke-ai/tomoe-pc/internal/eval"
	"github.com/sosuke-ai/tomoe-pc/internal/models"
	"github.com/sosuke-ai/tomoe-pc/internal/session"
)

// onlineOptions is `tomoe eval --online`: simulate running Tomoe's own
// diarizer during the meeting (see docs/speaker-pipeline-design.md).
// Segmentation and embeddings come from the cached full run; each
// recluster sees only the windows finished by then.
type onlineOptions struct {
	intervals []float64 // seconds between reclusterings
	strides   []int     // embed every nth window
	params    diarize.Params
}

// onlineResult scores one stride and recluster interval.
type onlineResult struct {
	Stride     int     `json:"stride"`
	Interval   float64 `json:"interval_seconds"`
	Embeddings int     `json:"embeddings"`

	// Final is every word's label after the last recluster; FirstShown
	// the label each word got from the first recluster that covered it.
	// Words take the timeline's speaker, and words in a gap their line's
	// (session.SplitByDiarization); FinalTimeline is the timeline alone.
	Final         eval.WordSpeakerScore `json:"final"`
	FinalTimeline eval.WordSpeakerScore `json:"final_timeline_only"`
	FirstShown    eval.WordSpeakerScore `json:"first_shown"`
	// Relabeled is the share of words whose label changed after it was
	// first shown.
	Relabeled float64 `json:"relabeled"`
	// DelayMedian/DelayP90: seconds from a word's end to its first label.
	DelayMedian float64 `json:"delay_median_seconds"`
	DelayP90    float64 `json:"delay_p90_seconds"`
	Reclusters  int     `json:"reclusters"`
	// ClusterMaxSecs is the slowest recluster (the last, with the most
	// embeddings); ClusterTotalSecs all of them.
	ClusterMaxSecs   float64 `json:"cluster_max_seconds"`
	ClusterTotalSecs float64 `json:"cluster_total_seconds"`
	People           int     `json:"people_with_own_cluster"`
	RefSpeakers      int     `json:"reference_speakers"`
	Clusters         int     `json:"clusters"`
}

func runOnlineSim(opts evalOptions, cfg *config.Config, status *models.Status, samples []float32, ref *eval.Reference, cache *evalCache, outDir string) error {
	threads := max(1, opts.threads)
	workers := max(1, runtime.NumCPU()/threads)
	if opts.workers > 0 {
		workers = opts.workers
	}
	run, _ := evalRunNamed("default")
	var pipeErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		pipeErr = runPipeline(run, cfg, status, samples, threads, cache)
	}()
	fmt.Printf("Diarization embedding model: %s\n", filepath.Base(status.SpeakerEmbeddingPath))
	prep, err := preparedDiarization(status.SpeakerSegmentationPath, status.SpeakerEmbeddingPath, samples, workers, threads, cache)
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

	var times []float64 // each transcribed word's midpoint and end
	var ends []float64
	for _, s := range run.segs {
		for _, w := range s.Words {
			times = append(times, wordAt(w))
			ends = append(ends, w.End)
		}
	}

	o := opts.online
	var results []*onlineResult
	for _, st := range o.strides {
		for _, iv := range o.intervals {
			results = append(results, &onlineResult{Stride: st, Interval: iv})
		}
	}
	jobs := make(chan *onlineResult)
	var jw sync.WaitGroup
	for w := 0; w < min(runtime.NumCPU(), len(results)); w++ {
		jw.Add(1)
		go func() {
			defer jw.Done()
			for r := range jobs {
				simulateOnline(r, prep.EveryNth(r.Stride), o.params, run, ref, times, ends, opts.collar)
			}
		}()
	}
	began := time.Now()
	for _, r := range results {
		jobs <- r
	}
	close(jobs)
	jw.Wait()
	fmt.Printf("Simulated %d settings in %s\n", len(results), formatDuration(time.Since(began).Seconds()))

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	text := formatOnline(results, o.params, filepath.Base(status.SpeakerEmbeddingPath), len(prep.Embeddings))
	if err := os.WriteFile(filepath.Join(outDir, "online.txt"), []byte(text), 0o644); err != nil {
		return err
	}
	js, _ := json.MarshalIndent(results, "", "  ")
	if err := os.WriteFile(filepath.Join(outDir, "online.json"), js, 0o644); err != nil {
		return err
	}
	fmt.Println()
	fmt.Print(text)
	fmt.Printf("\nWrote %s (online.txt, online.json)\n", outDir)
	return nil
}

// simulateOnline reclusters every r.Interval seconds on the windows
// finished by then, keeping speaker numbers stable, and scores the labels
// words got when first shown and at the end.
func simulateOnline(r *onlineResult, prep *diarize.Prepared, params diarize.Params, run *evalRun, ref *eval.Reference, times, ends []float64, collar float64) {
	r.Embeddings = len(prep.Embeddings)
	stable := diarize.NewStableLabels()
	first := make([][]string, len(times))
	delay := make([]float64, len(times))
	var final []eval.Labeled
	var finalSplit [][]string
	total := float64(prep.NumSamples) / 16000
	for t := r.Interval; ; t += r.Interval {
		last := t >= total
		if last {
			t = total
		}
		snap := prep.Truncate(int(t * 16000))
		if len(snap.Embeddings) > 0 {
			cb := time.Now()
			clusters := snap.Cluster(params.Threshold, 0)
			if params.MergeSimilarity > 0 {
				clusters = snap.MergeClusters(clusters, params.MergeSimilarity)
			}
			secs := time.Since(cb).Seconds()
			r.ClusterTotalSecs += secs
			r.ClusterMaxSecs = max(r.ClusterMaxSecs, secs)
			r.Reclusters++
			ids := stable.Assign(snap.Pairs, clusters)
			segs := snap.ReconstructClusters(clusters, params)
			lab := make([]eval.Labeled, 0, len(segs))
			for _, s := range segs {
				lab = append(lab, eval.Labeled{Start: s.Start, End: s.End, Speaker: fmt.Sprintf("S%d", ids[s.Speaker])})
			}
			sortLabeled(lab)
			covered := float64(snap.NumSamples) / 16000
			labels := splitLabels(run, segs, ids)
			for i := range times {
				if first[i] == nil && ends[i] <= covered {
					first[i] = labels[i]
					if first[i] == nil {
						first[i] = []string{}
					}
					delay[i] = t - ends[i]
				}
			}
			final = lab
			finalSplit = labels
		}
		if last {
			break
		}
	}
	finalScore := eval.ScoreSpeakers(ref, final, collar)
	r.People, r.RefSpeakers = finalScore.RefSpeakersMatched, finalScore.RefSpeakers
	distinct := map[string]bool{}
	for _, l := range final {
		distinct[l.Speaker] = true
	}
	r.Clusters = len(distinct)
	r.FinalTimeline = run.align.Score(eval.SpeakersAt(final, times), finalScore.Mapping)
	finalLabels := finalSplit
	for i := range first {
		if first[i] == nil { // never covered before the end
			first[i] = finalLabels[i]
		}
	}
	r.Final = run.align.Score(finalLabels, finalScore.Mapping)
	r.FirstShown = run.align.Score(first, finalScore.Mapping)
	changed, n := 0, 0
	for i := range first {
		if len(finalLabels[i]) == 0 && len(first[i]) == 0 {
			continue
		}
		n++
		if strings.Join(first[i], ",") != strings.Join(finalLabels[i], ",") {
			changed++
		}
	}
	if n > 0 {
		r.Relabeled = float64(changed) / float64(n)
	}
	d := append([]float64(nil), delay...)
	sort.Float64s(d)
	if len(d) > 0 {
		r.DelayMedian, r.DelayP90 = d[len(d)/2], d[min(len(d)-1, len(d)*9/10)]
	}
}

func formatOnline(results []*onlineResult, p diarize.Params, embName string, fullEmbeddings int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Online diarization simulation (%s; threshold %v, merge %v): recluster every interval on the windows finished so far.\n", embName, p.Threshold, p.MergeSimilarity)
	fmt.Fprintf(&b, "stride: embed every nth window (cost about 1/n of embedding all %d). first shown: the label each word got from the first\n", fullEmbeddings)
	fmt.Fprintln(&b, "recluster covering it; final: after the last. relabeled: words whose label changed after first shown. delay: word end to first label.")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "final: words take the timeline's speaker, words in a gap their line's (as line splitting does); timeline: the timeline alone.")
	fmt.Fprintln(&b, "  stride interval embeddings | timeline | final: overall 1-3w  4-15w  people clusters | first shown: overall 4-15w | relabeled | delay med  p90 | recluster max  total")
	for _, r := range results {
		fmt.Fprintf(&b, "  %4d   %5.0fs   %6d     |  %s  |        %s %s %s   %d/%d  %4d     |              %s %s  |  %s   |  %5.1fs %5.1fs |     %6.2fs %6.1fs\n",
			r.Stride, r.Interval, r.Embeddings, pct(r.FinalTimeline.Accuracy()),
			pct(r.Final.Accuracy()), pct(r.Final.Buckets[0].Accuracy()), pct(r.Final.Buckets[1].Accuracy()), r.People, r.RefSpeakers, r.Clusters,
			pct(r.FirstShown.Accuracy()), pct(r.FirstShown.Buckets[1].Accuracy()), pct(r.Relabeled),
			r.DelayMedian, r.DelayP90, r.ClusterMaxSecs, r.ClusterTotalSecs)
	}
	return b.String()
}

// splitLabels labels the run's words from diarization turns segs (Speaker
// a cluster index, ids its stable ID) as the app would: each word the
// turn's speaker, words between turns their line's.
func splitLabels(run *evalRun, segs []session.DiarizeSegment, ids map[int]int) [][]string {
	names := map[int]string{}
	for k, id := range ids {
		names[k] = fmt.Sprintf("S%d", id)
	}
	split, _ := session.SplitByDiarization(append([]session.Segment(nil), run.segs...), segs, names)
	return segmentSpeakers(split)
}
