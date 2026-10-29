package diarize

import (
	"github.com/sosuke-ai/tomoe-pc/internal/session"
	"github.com/sosuke-ai/tomoe-pc/internal/voices"
)

// doubtAbove is how alike a speaker the meeting window named and another
// person's profile must be to ask which is right, when the voice isn't
// enough to overrule the window (voices.Params.Threshold) and is more
// like that other person than like the window's person's own profile.
const doubtAbove = 0.5

// voiceCall is what the voice profiles say about a meeting's speakers.
type voiceCall struct {
	// named are the speakers a profile names: ones the window didn't name
	// (or named only a room), and ones it named someone else.
	named map[int]voices.Matched
	// window is, for a named speaker the window had named someone else,
	// the window's name.
	window map[int]string
	// doubts are speakers the window named whose voice is more like
	// someone else's profile than their own, though not enough to name
	// them.
	doubts map[int]voices.Matched
}

// callVoices names speakers (vs, by key) from the voice profiles, over
// the meeting window's names (names, by key) when they clearly disagree:
// the window's highlight can sit on the wrong tile, and a person can join
// from someone else's laptop. Speakers the window named are settled
// first (a profile that agrees leaves the window's name); then the ones it
// didn't name (or named only a room) may take a name nobody else has.
// taken says a name is already someone else's for sure (the user's); it
// doesn't stop a voice overruling the window, as one person's speech is
// often split between two speakers.
func callVoices(store *voices.Store, vs map[int]voices.Voice, names map[int]string, taken func(name string) bool) voiceCall {
	call := voiceCall{named: map[int]voices.Matched{}, window: map[int]string{}, doubts: map[int]voices.Matched{}}
	if store == nil || len(vs) == 0 {
		return call
	}
	isTaken := func(name string) bool { return taken != nil && taken(name) }
	windowNamed := func(k int) bool { return names[k] != "" && !IsRoomTile(names[k]) }

	byWindow, open := map[int]voices.Voice{}, map[int]voices.Voice{}
	for k, v := range vs {
		if windowNamed(k) {
			byWindow[k] = v
		} else {
			open[k] = v
		}
	}
	for k, m := range store.Match(byWindow, nil) {
		if !SameName(names[k], m.Name) {
			call.named[k], call.window[k] = m, names[k]
		}
	}
	for k, m := range store.Likest(byWindow) {
		// Only against a profile of the window's person: without one,
		// someone else's is always likest.
		if _, ok := call.named[k]; ok || SameName(names[k], m.Name) || m.Similarity < doubtAbove || !store.Knows(names[k]) {
			continue
		}
		call.doubts[k] = m
	}

	// Names now someone's: the user's, the window's that stand, and the
	// ones voices just gave.
	for k, m := range store.Match(open, func(name string) bool {
		if isTaken(name) {
			return true
		}
		for j, w := range names {
			if windowNamed(j) && call.window[j] == "" && SameName(w, name) {
				return true
			}
		}
		for _, m := range call.named {
			if SameName(m.Name, name) {
				return true
			}
		}
		return false
	}) {
		call.named[k] = m
	}
	return call
}

// unsureLabels are the labels whose name the window and the voice
// disagree on (sess.VoiceNames with a window name, and unresolved
// sess.VoiceDoubts): their speech teaches no profile from the window's
// tiles until the user says who it is.
func unsureLabels(sess *session.Session) map[string]bool {
	out := map[string]bool{}
	for _, vn := range sess.VoiceNames {
		if vn.Window != "" {
			out[vn.Label] = true
		}
	}
	for _, vd := range sess.VoiceDoubts {
		out[vd.Label] = true
	}
	return out
}

// unsureAt reports whether session time t is in a line labelled one of
// labels.
func unsureAt(segs []session.Segment, labels map[string]bool) func(t float64) bool {
	if len(labels) == 0 {
		return nil
	}
	at := SpeakerAt(segs, func(label string) string {
		if labels[label] {
			return label
		}
		return ""
	})
	return func(t float64) bool { return at(t) != "" }
}
