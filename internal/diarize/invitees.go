package diarize

import "unicode/utf8"

// SnapToInvitee corrects a name read from the meeting window to the
// spelling of the meeting's invitee it is a misread of: the one invitee
// within a small edit distance of the read (case and trailing OCR junk
// ignored), at most a fifth of the longer name's letters and never more
// than two. A read that is no invitee's within that distance, or is as
// close to two invitees, is returned unchanged: a read is never made into
// a name outside the invitee list, and a guest who wasn't invited keeps
// the name the window showed.
//
// Names of four letters or fewer allow no edit at all, so short first
// names ("Ana", "Ann") are never snapped to one another.
func SnapToInvitee(read string, invitees []string) string {
	r := normalizeName(read)
	if r == "" {
		return read
	}
	best, bestDist, tie := "", -1, false
	for _, inv := range invitees {
		n := normalizeName(inv)
		if n == "" {
			continue
		}
		d := editDistance(r, n)
		if d > snapLimit(r, n) {
			continue
		}
		switch {
		case bestDist < 0 || d < bestDist:
			best, bestDist, tie = trimNameJunk(inv), d, false
		case d == bestDist && normalizeName(best) != n:
			tie = true
		}
	}
	if bestDist < 0 || tie {
		return read
	}
	return best
}

// snapLimit is the most edits a read and an invitee's name may differ by
// to be one name: a fifth of the longer's letters, at most two.
func snapLimit(a, b string) int {
	n := max(utf8.RuneCountInString(a), utf8.RuneCountInString(b))
	return min(2, n/5)
}

// SnapHints snaps every name read in hints to the invitees (see
// SnapToInvitee), in place, and returns how many reads changed. With no
// invitees it does nothing.
func SnapHints(hints []Hint, invitees []string) int {
	if len(invitees) == 0 {
		return 0
	}
	changed := 0
	snap := func(s string) string {
		if s == "" {
			return s
		}
		out := SnapToInvitee(s, invitees)
		if out != s {
			changed++
		}
		return out
	}
	for i := range hints {
		hints[i].Name = snap(hints[i].Name)
		for j := range hints[i].Candidates {
			hints[i].Candidates[j] = snap(hints[i].Candidates[j])
		}
	}
	return changed
}
