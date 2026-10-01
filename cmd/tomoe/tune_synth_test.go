package main

import (
	"encoding/gob"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sosuke-ai/tomoe-pc/internal/config"
	"github.com/sosuke-ai/tomoe-pc/internal/diarize"
	"github.com/sosuke-ai/tomoe-pc/internal/eval"
	"github.com/sosuke-ai/tomoe-pc/internal/models"
	"github.com/sosuke-ai/tomoe-pc/internal/session"
	"github.com/sosuke-ai/tomoe-pc/internal/videohint"
)

// TestTuneSynthetic checks tomoe tune end to end on a synthetic session:
// a real recording's transcript and fingerprints (TUNE_MEDIA, with the eval
// cache warm) and fabricated looks that name whoever the reviewed
// transcript (TUNE_REF) has speaking, 3% misread. Near-perfect hints
// should name nearly every word right. Writes the report to TUNE_OUT.
// Skips without TUNE_MEDIA: recordings stay local.
func TestTuneSynthetic(t *testing.T) {
	media, refPath := os.Getenv("TUNE_MEDIA"), os.Getenv("TUNE_REF")
	if media == "" {
		t.Skip()
	}
	cfg, _ := config.Load(config.Path())
	status := models.NewManager(cfg.Transcription.ModelPath).Check()
	en, _ := models.SpeakerModelByID(models.SpeakerModelEres2NetEn)
	status.SpeakerEmbeddingPath = status.SpeakerModelPath(en)
	samples, err := session.DecodeToFloat32(media)
	if err != nil {
		t.Fatal(err)
	}
	cache, err := openEvalCache(samples, textKeyFor(cfg, status))
	if err != nil {
		t.Fatal(err)
	}
	run, _ := evalRunNamed("default")
	if err := runPipeline(run, cfg, status, samples, 4, cache); err != nil {
		t.Fatal(err)
	}
	prep, err := preparedDiarization(status.SpeakerSegmentationPath, status.SpeakerEmbeddingPath, samples, 7, 2, cache)
	if err != nil {
		t.Fatal(err)
	}
	f, _ := os.Open(refPath)
	ref, _ := eval.ParseTeamsTranscript(f, float64(len(samples))/16000)
	f.Close()

	t.Setenv("XDG_DATA_HOME", t.TempDir())
	store := session.NewStore(config.SessionDir())
	t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	sess := &session.Session{ID: "synthetic-tune", Title: "synthetic", CreatedAt: t0, Duration: float64(len(samples)) / 16000, Segments: run.segs, Language: "en"}
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(config.SessionDir(), sess.ID)
	g, _ := os.Create(filepath.Join(dir, "diarization.gob"))
	gob.NewEncoder(g).Encode(prep)
	g.Close()
	params := diarize.DefaultParams()
	params.Threshold, params.MergeSimilarity = en.StreamThreshold, en.StreamMerge
	info, _ := json.Marshal(diarize.StreamInfo{Stride: 1, Params: params})
	os.WriteFile(filepath.Join(dir, "diarization.json"), info, 0o644)

	// Looks every 0.35 s naming whoever the reference has speaking 0.5 s
	// earlier, 3% of them misread as someone else.
	speakers := ref.Speakers()
	rng := rand.New(rand.NewSource(1))
	log := videohint.NewLookLog(dir)
	id := 0
	for ts := 0.0; ts < sess.Duration; ts += 0.35 {
		id++
		l := videohint.Look{ID: id, Time: t0.Add(time.Duration(ts * float64(time.Second))), Stage: videohint.StageNoRingMatch, Cost: videohint.LookCost{Capture: 10, Detect: 4}}
		if who := eval.SpeakersAt(refLabeled(ref), []float64{ts - 0.5})[0]; len(who) == 1 {
			name := who[0]
			if rng.Float64() < 0.03 {
				name = speakers[rng.Intn(len(speakers))]
			}
			l.Stage, l.Name, l.Usable, l.Cost.OCR = videohint.StageOCRHit, name, true, 16
		}
		log.Write(l)
	}
	if err := runTune(sess.ID, refPath, filepath.Join(os.Getenv("TUNE_OUT")), 0, true, 2); err != nil {
		t.Fatal(err)
	}
}

func refLabeled(ref *eval.Reference) []eval.Labeled {
	out := make([]eval.Labeled, len(ref.Turns))
	for i, t := range ref.Turns {
		out[i] = eval.Labeled{Start: t.Start, End: t.End, Speaker: t.Speaker}
	}
	return out
}

func textKeyFor(cfg *config.Config, status *models.Status) string {
	return fmt.Sprintf("%s|%s|%s|%d|%s|%v|%v", status.EncoderPath, status.DecoderPath, cfg.Transcription.DecodingMethod,
		cfg.Transcription.MaxActivePaths, cfg.Transcription.HotwordsFile, cfg.Transcription.HotwordsScore, cfg.Transcription.GPUEnabled)
}
