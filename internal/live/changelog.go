package live

import (
	"sort"
	"sync"
)

// ChangeLog collects speaker-change signals during a meeting (session
// seconds) for Config.SpeakerChanged: a voice starting that the
// diarizer's segmentation hadn't heard just before, or the meeting
// window's highlight moving to another name. Safe for concurrent use.
type ChangeLog struct {
	mu       sync.Mutex
	times    []float64
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

// NameSeen records a change at t if name differs from the last name seen.
func (l *ChangeLog) NameSeen(t float64, name string) {
	if name == "" {
		return
	}
	l.mu.Lock()
	changed := l.lastName != "" && name != l.lastName
	l.lastName = name
	l.mu.Unlock()
	if changed {
		l.Add(t)
	}
}

// Between reports whether a change was recorded in [from, to].
func (l *ChangeLog) Between(from, to float64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	i := sort.SearchFloat64s(l.times, from)
	return i < len(l.times) && l.times[i] <= to
}
