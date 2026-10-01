package main

import (
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/sosuke-ai/tomoe-pc/internal/session"
	"github.com/sosuke-ai/tomoe-pc/internal/transcribe"
)

// evalCache keeps `tomoe eval` results that don't change between runs, so
// only the first eval of a recording pays for them: each utterance's
// transcription (keyed by its audio and the model settings) and the
// diarization output (keyed by the recording and diarization settings).
// When tuning speaker clustering, a re-run only redoes clustering and
// scoring.
//
// It lives under the user cache dir, outside the repo: it holds the
// recording's transcript text.
type evalCache struct {
	dir     string
	textKey string

	mu       sync.Mutex
	text     map[string]*transcribe.Result
	inflight map[string]chan struct{}
	dirty    bool

	hits, misses atomic.Int64
}

// hashSamples fingerprints audio samples.
func hashSamples(s []float32) string {
	if len(s) == 0 {
		return "empty"
	}
	b := unsafe.Slice((*byte)(unsafe.Pointer(&s[0])), len(s)*4)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:12])
}

func hashString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

// openEvalCache opens (creating if needed) the cache for one recording.
// textKey identifies everything that affects transcription besides the
// audio (model files, decoding settings).
func openEvalCache(audio []float32, textKey string) (*evalCache, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(base, "tomoe", "eval", hashSamples(audio))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	c := &evalCache{dir: dir, textKey: hashString(textKey), text: map[string]*transcribe.Result{}, inflight: map[string]chan struct{}{}}
	if f, err := os.Open(c.textPath()); err == nil {
		if err := gob.NewDecoder(f).Decode(&c.text); err != nil {
			fmt.Printf("Note: ignoring unreadable transcription cache: %v\n", err)
			c.text = map[string]*transcribe.Result{}
		}
		f.Close()
	}
	return c, nil
}

func (c *evalCache) textPath() string {
	return filepath.Join(c.dir, "text-"+c.textKey+".gob")
}

// save writes new transcription results to disk.
func (c *evalCache) save() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.dirty {
		return nil
	}
	tmp := c.textPath() + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := gob.NewEncoder(f).Encode(c.text); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	c.dirty = false
	return os.Rename(tmp, c.textPath())
}

// wrap returns e with TranscribeDirect (the call the live pipeline makes
// per utterance) served from the cache. Concurrent runs asking for the same
// utterance share one decode.
func (c *evalCache) wrap(e transcribe.Engine) transcribe.Engine {
	return &cachedEngine{Engine: e, c: c}
}

type cachedEngine struct {
	transcribe.Engine
	c *evalCache
}

func (e *cachedEngine) TranscribeDirect(samples []float32) (*transcribe.Result, error) {
	c, key := e.c, hashSamples(samples)
	for {
		c.mu.Lock()
		if r, ok := c.text[key]; ok {
			c.mu.Unlock()
			c.hits.Add(1)
			cp := *r
			return &cp, nil
		}
		if wait, ok := c.inflight[key]; ok {
			c.mu.Unlock()
			<-wait
			continue // served from the cache next time round, or decoded here if it failed
		}
		done := make(chan struct{})
		c.inflight[key] = done
		c.mu.Unlock()

		r, err := e.Engine.TranscribeDirect(samples)
		c.misses.Add(1)
		c.mu.Lock()
		delete(c.inflight, key)
		if err == nil && r != nil {
			cp := *r
			c.text[key] = &cp
			c.dirty = true
		}
		c.mu.Unlock()
		close(done)
		return r, err
	}
}

// cachedDiarization is one recording's diarization output, before and
// after merging similar speakers.
type cachedDiarization struct {
	Raw       []session.DiarizeSegment `json:"raw"`
	RawMap    map[int]string           `json:"raw_map"`
	Merged    []session.DiarizeSegment `json:"merged"`
	MergedMap map[int]string           `json:"merged_map"`
}

func (c *evalCache) diarizationPath(key string) string {
	return filepath.Join(c.dir, "diarization-"+hashString(key)+".json")
}

func (c *evalCache) loadDiarization(key string) (*cachedDiarization, bool) {
	b, err := os.ReadFile(c.diarizationPath(key))
	if err != nil {
		return nil, false
	}
	var d cachedDiarization
	if json.Unmarshal(b, &d) != nil {
		return nil, false
	}
	return &d, true
}

func (c *evalCache) storeDiarization(key string, d *cachedDiarization) error {
	b, err := json.Marshal(d)
	if err != nil {
		return err
	}
	return os.WriteFile(c.diarizationPath(key), b, 0o600)
}
