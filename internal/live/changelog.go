package live

import (
	"sort"
	"sync"

	"github.com/sosuke-ai/tomoe-pc/internal/diarize"
)

// DefaultHighlightLag is how long (seconds) a meeting window's highlight
// is taken to trail the voice it marks, for Config.HighlightLag.
const DefaultHighlightLag = 0.5

// ChangeLog collects speaker-change signals during a meeting (session
// seconds) for Config.SpeakerChanged: a voice starting that the
// diarizer's segmentation hadn't heard just before, or the meeting
// window's highlight moving to another name. Safe for concurrent use.
type ChangeLog struct {
	mu       sync.Mutex
	times    []float64
	names    []float64 // just the highlight's name changes
	lastName string
}

// Add records a change at t.
func (l *ChangeLog) Add(t float64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	i := sort.SearchFloat64s(l.times, t)
	l.times = append(l.times, 0)
	copy(l.times[i+1:], l.times[i:])
	l.times[i] = t
}

// NameSeen records a change at t if name is another person than the last
// name seen. Reads of one truncated tile differ ("Jennifer Hem...",
// "Jennifer Hem.."), so names are compared as diarize.SameName does: a
// briefing counted 345 changes in 52 minutes from such flips alone.
func (l *ChangeLog) NameSeen(t float64, name string) {
	if name == "" {
		return
	}
	l.mu.Lock()
	changed := l.lastName != "" && !diarize.SameName(name, l.lastName)
	if l.lastName == "" || changed {
		l.lastName = name
	}
	l.mu.Unlock()
	if changed {
		l.Add(t)
		l.mu.Lock()
		i := sort.SearchFloat64s(l.names, t)
		l.names = append(l.names, 0)
		copy(l.names[i+1:], l.names[i:])
		l.names[i] = t
		l.mu.Unlock()
	}
}

// NameChangesIn returns the times in [from, to] at which the meeting
// window's highlight moved to another name.
func (l *ChangeLog) NameChangesIn(from, to float64) []float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	i := sort.SearchFloat64s(l.names, from)
	var out []float64
	for ; i < len(l.names) && l.names[i] <= to; i++ {
		out = append(out, l.names[i])
	}
	return out
}

// Between reports whether a change was recorded in [from, to].
func (l *ChangeLog) Between(from, to float64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	i := sort.SearchFloat64s(l.times, from)
	return i < len(l.times) && l.times[i] <= to
}
