package diarize

import (
	"math"
	"sort"
	"strings"

	"github.com/sosuke-ai/tomoe-pc/internal/session"
)

// Hint is a name the meeting window showed under the active-speaker ring
// at T (seconds): evidence that whoever was speaking then is called Name.
type Hint struct {
	T    float64
	Name string
	// Candidates, instead of Name, when several tiles were lit at once:
	// one of them is the speaker (every lit tile's name was read).
	Candidates []string
}

// NameParams are the rules for naming speakers from hints (tuned with
// `tomoe tune`).
type NameParams struct {
	// Lag shifts a hint back to when the voice it belongs to was heard:
	// the ring lights a moment after someone starts talking and lingers
	// after they stop (seconds).
	Lag float64
	// Reads of a name within one Bucket (seconds) count as one.
	Bucket float64
	// A speaker is named once its top name has at least MinReads
	// independent reads and at least MinShare of all of them.
	MinReads int
	MinShare float64
}

// DefaultNameParams are the naming rules the app uses.
func DefaultNameParams() NameParams {
	return NameParams{Lag: 0.5, Bucket: 5, MinReads: 2, MinShare: 0.6}
}

// hintLag is the default ring lag (seconds), used where a NameParams isn't
// passed.
const hintLag = 0.5

// normalizeName folds case, spaces and a trailing ellipsis (truncated
// tiles).
func normalizeName(s string) string {
	return strings.ToLower(trimNameJunk(s))
}

// nameJunk is what OCR leaves after a name (a truncation ellipsis, a
// stray quote or dot from the tile's edge): never part of the name.
const nameJunk = "….\"'`,:;!? "

// trimNameJunk drops spaces and trailing junk (see nameJunk).
func trimNameJunk(s string) string {
	return strings.TrimSpace(strings.TrimRight(strings.TrimSpace(s), nameJunk))
}

// canonicalNames maps each name read to one spelling per person. A tile
// can show "Natalia Rami…" one time and the full name the next, and OCR
// misreads a letter now and then ("Ortlz"), so names fold together when
// one is a prefix of the other (at least 4 characters) or they differ by
// a letter or two; each group is spelled the way it was read most often
// (ties: the longer).
func canonicalNames(names []string) map[string]string {
	count := map[string]int{}      // normalized -> reads
	display := map[string]string{} // normalized -> a spelling as read
	truncated := map[string]bool{} // normalized -> read with a trailing ellipsis
	for _, n := range names {
		k := normalizeName(n)
		if k == "" {
			continue
		}
		count[k]++
		t := strings.TrimSpace(n)
		if strings.HasSuffix(t, "…") || strings.HasSuffix(t, "...") {
			truncated[k] = true
		}
		if cur, ok := display[k]; !ok || len(n) > len(cur) {
			display[k] = trimNameJunk(n)
		}
	}
	keys := make([]string, 0, len(count))
	for k := range count {
		keys = append(keys, k)
	}
	// Most-read first, so each group gathers around its best spelling.
	sort.Slice(keys, func(i, j int) bool {
		if count[keys[i]] != count[keys[j]] {
			return count[keys[i]] > count[keys[j]]
		}
		if len(keys[i]) != len(keys[j]) {
			return len(keys[i]) > len(keys[j])
		}
		return keys[i] < keys[j]
	})
	head := map[string]string{} // normalized -> its group's head
	var heads []string
	for _, k := range keys {
		joined := false
		for _, h := range heads {
			if sameName(k, h) {
				head[k], joined = h, true
				break
			}
		}
		if !joined {
			head[k] = k
			heads = append(heads, k)
		}
	}
	// A head that was read truncated ("Natalia Rami…") takes the fullest
	// spelling its group was read with; otherwise the most-read spelling
	// stands (a longer one is more likely OCR junk than the real name).
	spelling := map[string]string{}
	for k, h := range head {
		if !truncated[h] {
			continue
		}
		if b, ok := spelling[h]; (!ok || len(k) > len(b)) && strings.HasPrefix(k, h) && len(k) > len(h) {
			spelling[h] = k
		}
	}
	out := map[string]string{}
	for _, n := range names {
		if k := normalizeName(n); k != "" {
			h := head[k]
			sp := h
			if b, ok := spelling[h]; ok {
				sp = b
			}
			out[n] = display[sp]
		}
	}
	return out
}

// SameName reports whether two names as read or written are one
// person's (see sameName).
func SameName(a, b string) bool { return sameName(normalizeName(a), normalizeName(b)) }

// sameName reports whether two normalized names are one person's: a
// prefix of the other (at least 4 characters) or within an edit or two.
func sameName(a, b string) bool {
	if a == b {
		return true
	}
	short, long := a, b
	if len(short) > len(long) {
		short, long = long, short
	}
	if len(short) >= 4 && strings.HasPrefix(long, short) {
		return true
	}
	return len(short) >= 5 && editDistance(a, b) <= max(1, len(long)/8)
}

// editDistance is the Levenshtein distance between a and b.
func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

// activeAt lists the speakers turns has talking at t.
func activeAt(turns []session.DiarizeSegment, t float64) []int {
	var out []int
	for _, tr := range turns {
		if tr.Start <= t && t < tr.End {
			out = append(out, tr.Speaker)
		}
	}
	return out
}

// nameSpeakers names timeline speakers from hints by vote: each hint
// votes for the one speaker talking when it was taken (hints during
// silence or overlapping speech don't vote), reads of one name close
// together count once, and a speaker takes a name only when it has at
// least MinReads independent reads and MinShare of the speaker's
// reads. Hints after through (not yet covered) are ignored.
func nameSpeakers(turns []session.DiarizeSegment, hints []Hint, through float64) map[int]string {
	return NameSpeakers(DefaultNameParams(), turns, hints, through)
}

// NameSpeakers is nameSpeakers with rules p.
func NameSpeakers(p NameParams, turns []session.DiarizeSegment, hints []Hint, through float64) map[int]string {
	var all []string
	for _, h := range hints {
		all = append(all, h.Name)
		all = append(all, h.Candidates...)
	}
	canon := canonicalNames(all)
	reads := map[int]map[string]map[int]bool{} // speaker -> name -> buckets
	vote := func(spk int, name string, t float64) {
		if reads[spk] == nil {
			reads[spk] = map[string]map[int]bool{}
		}
		if reads[spk][name] == nil {
			reads[spk][name] = map[int]bool{}
		}
		reads[spk][name][int(math.Floor(t/p.Bucket))] = true
	}
	speakerAt := func(h Hint) (int, float64, bool) {
		t := h.T - p.Lag
		if t > through {
			return 0, t, false
		}
		active := activeAt(turns, t)
		if len(active) != 1 {
			return 0, t, false
		}
		return active[0], t, true
	}
	for _, h := range hints {
		if name := canon[h.Name]; name != "" {
			if spk, t, ok := speakerAt(h); ok {
				vote(spk, name, t)
			}
		}
	}
	// Several lit tiles: the speaker heard then is one of them. A speaker
	// whose name is among them is confirmed; otherwise names other
	// speakers already have are ruled out, and a single name left votes.
	names := decideNames(reads, p)
	for _, h := range hints {
		if len(h.Candidates) < 2 {
			continue
		}
		spk, t, ok := speakerAt(h)
		if !ok {
			continue
		}
		var left []string
		confirmed := false
		for _, c := range h.Candidates {
			name := canon[c]
			if name == "" {
				left = nil // an unread tile could be the speaker
				break
			}
			if names[spk] == name {
				confirmed = true
			}
			taken := false
			for other, n := range names {
				if other != spk && n == name {
					taken = true
				}
			}
			if !taken {
				left = append(left, name)
			}
		}
		switch {
		case confirmed:
			vote(spk, names[spk], t)
		case len(left) == 1:
			vote(spk, left[0], t)
		}
	}
	return decideNames(reads, p)
}

// decideNames gives each speaker its top name if it has at least
// p.MinReads independent reads and p.MinShare of the speaker's reads.
func decideNames(reads map[int]map[string]map[int]bool, p NameParams) map[int]string {
	out := map[int]string{}
	for spk, byName := range reads {
		best, bestN, total := "", 0, 0
		for name, buckets := range byName {
			n := len(buckets)
			total += n
			if n > bestN || (n == bestN && name < best) {
				best, bestN = name, n
			}
		}
		if bestN >= p.MinReads && float64(bestN) >= p.MinShare*float64(total) {
			out[spk] = best
		}
	}
	return out
}
