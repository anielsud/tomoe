package speaker

import (
	"fmt"
	"sync"
	"time"
)

// DefaultThreshold is the default cosine similarity threshold for same-speaker assignment.
const DefaultThreshold = 0.65

// stickyGraceWindow and stickyThresholdMargin implement a "sticky
// speaker" continuity heuristic: VAD splits one person's continuous
// turn into several short segments whenever they pause for a beat
// between sentences (even under a second), and a short segment's
// embedding is measurably noisier than a longer one's — observed live
// as one real speaker's turn fragmenting into a fresh "Person N" for
// almost every sentence, well before any other speaker had actually
// started talking. If the best-matching centroid is also whoever was
// just assigned, and not long ago, a near-miss on similarity is far
// more likely to be "same person, noisier embedding" than "a different
// person who happens to sound similar," so it's accepted as the same
// speaker. The centroid itself is NOT updated on a sticky-only match
// (see Assign) — only accepting it into the running average on a full,
// confident match keeps a fragmented sentence from ever dragging a
// good centroid toward a bad one.
const (
	stickyGraceWindow     = 3 * time.Second
	stickyThresholdMargin = 0.15
)

// Tracker performs online speaker clustering using cosine similarity of embeddings.
// Speakers are labeled "Person 1", "Person 2", etc. — optionally suffixed
// with a real name in parens (e.g. "Person 2 (Nazanin Rame...)") once a
// hint attaches that cluster via SetHintForRecent; see its doc comment.
type Tracker struct {
	mu        sync.Mutex
	threshold float64
	centroids [][]float32 // one centroid per known speaker
	counts    []int       // number of embeddings merged into each centroid
	hints     []string    // one optional name-hint per speaker, parallel to centroids

	// lastAssignedIdx/At track the most recent successful Assign, so a
	// video hint (which has no direct link to a cluster ID — it only
	// knows "this name is active right now") can be attributed to
	// "whoever was probably just speaking" via SetHintForRecent, and so
	// Assign itself can apply the sticky-speaker grace window above.
	lastAssignedIdx int
	lastAssignedAt  time.Time

	// nowFn is time.Now by default; overridable in tests so the
	// sticky-speaker grace window is deterministically testable
	// without sleeping.
	nowFn func() time.Time
}

// NewTracker creates a Tracker with the given cosine similarity threshold.
// Embeddings with similarity >= threshold to a centroid are assigned to that speaker.
func NewTracker(threshold float64) *Tracker {
	if threshold <= 0 || threshold > 1 {
		threshold = DefaultThreshold
	}
	return &Tracker{
		threshold: threshold,
		nowFn:     time.Now,
	}
}

// Assign assigns an embedding to a speaker, creating a new speaker if no
// match is found. Returns a label like "Person 1", or "Person 1 (Name)"
// if a video hint has already been attached to that speaker via
// SetHintForRecent, plus needsHint: true if this speaker still has no
// hint attached, so a caller (internal/live's Coordinator) can signal
// that a video-hint check is worth doing right away rather than waiting
// for videohint.Poll's next scheduled tick — see Coordinator.HintNeeded.
func (t *Tracker) Assign(embedding []float32) (label string, needsHint bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if len(embedding) == 0 {
		return "Unknown", false
	}

	// Find the best matching centroid
	bestIdx := -1
	bestSim := 0.0

	for i, centroid := range t.centroids {
		sim := CosineSimilarity(embedding, centroid)
		if sim > bestSim {
			bestSim = sim
			bestIdx = i
		}
	}

	now := t.nowFn()

	if bestIdx >= 0 && bestSim >= t.threshold {
		// Confident match: fold it into the running average.
		t.updateCentroid(bestIdx, embedding)
		t.lastAssignedIdx = bestIdx
		t.lastAssignedAt = now
		return t.label(bestIdx), t.hints[bestIdx] == ""
	}

	sticky := bestIdx >= 0 && bestIdx == t.lastAssignedIdx &&
		!t.lastAssignedAt.IsZero() && now.Sub(t.lastAssignedAt) <= stickyGraceWindow &&
		bestSim >= t.threshold-stickyThresholdMargin

	if sticky {
		// Near-miss on similarity, but this is whoever was just
		// speaking, moments ago -- treat it as the same speaker
		// without folding it into the centroid (see doc comment above
		// stickyGraceWindow for why not).
		t.lastAssignedAt = now
		return t.label(bestIdx), t.hints[bestIdx] == ""
	}

	// New speaker
	newCentroid := make([]float32, len(embedding))
	copy(newCentroid, embedding)
	t.centroids = append(t.centroids, newCentroid)
	t.counts = append(t.counts, 1)
	t.hints = append(t.hints, "")
	idx := len(t.centroids) - 1
	t.lastAssignedIdx = idx
	t.lastAssignedAt = now
	return t.label(idx), true
}

// label builds the display label for speaker idx: "Person N", or
// "Person N (Name)" if a hint has been attached.
func (t *Tracker) label(idx int) string {
	base := fmt.Sprintf("Person %d", idx+1)
	if idx < len(t.hints) && t.hints[idx] != "" {
		return fmt.Sprintf("%s (%s)", base, t.hints[idx])
	}
	return base
}

// SetHintForRecent attaches name as a hint to whichever speaker was most
// recently assigned an embedding, provided that assignment happened
// within maxAge. This is how a video hint — which only knows "this name
// is active right now," not which cluster ID it corresponds to — gets
// attributed to a specific speaker: "whoever the audio pipeline most
// recently heard" is the best available proxy for "whoever the ring is
// currently around," since the video hint and the audio pipeline are
// both keyed to the same monitor-source (other participants') audio.
// Reports whether it found a recent-enough assignment to attach to.
func (t *Tracker) SetHintForRecent(name string, maxAge time.Duration) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	if len(t.centroids) == 0 || t.lastAssignedAt.IsZero() || t.nowFn().Sub(t.lastAssignedAt) > maxAge {
		return false
	}
	t.hints[t.lastAssignedIdx] = name
	return true
}

// Reset clears all speaker centroids and hints.
func (t *Tracker) Reset() {
	t.mu.Lock()
	t.centroids = nil
	t.counts = nil
	t.hints = nil
	t.lastAssignedIdx = 0
	t.lastAssignedAt = time.Time{}
	t.mu.Unlock()
}

// NumSpeakers returns the number of identified speakers.
func (t *Tracker) NumSpeakers() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.centroids)
}

// updateCentroid updates a centroid with a new embedding using running average.
func (t *Tracker) updateCentroid(idx int, embedding []float32) {
	count := float32(t.counts[idx])
	newCount := count + 1

	for i := range t.centroids[idx] {
		t.centroids[idx][i] = (t.centroids[idx][i]*count + embedding[i]) / newCount
	}
	t.counts[idx] = int(newCount)
}
