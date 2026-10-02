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

// hintLag and minNameReads are the defaults' values, used where a
// NameParams isn't passed.
const (
	hintLag      = 0.5
	minNameReads = 2
	minNameShare = 0.6
)

// normalizeName folds case, spaces and a trailing ellipsis (truncated
// tiles).
func normalizeName(s string) string {
	return strings.ToLower(strings.TrimSpace(strings.TrimRight(strings.TrimSpace(s), "…")))
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
			display[k] = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(n), "…."))
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
// least minNameReads independent reads and minNameShare of the speaker's
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

// HintedPairs is hintedPairs with lag (seconds) for the ring's delay.
func HintedPairs(p *Prepared, hints []Hint, lag float64) map[int]string {
	return hintedPairsLag(p, hints, lag)
}

// ApplyHintConstraints exposes applyHintConstraints for tuning.
func ApplyHintConstraints(embs [][]float32, clusters []int, named map[int]string, relaxedMerge float64) []int {
	return applyHintConstraints(embs, clusters, named, relaxedMerge)
}

// hintedPairs maps hints (in stream time) to the window-speakers they
// name: for each window covering a hint's time, the local speaker talking
// alone at that frame, if it has an embedding. A window-speaker named
// two different things is left unnamed.
func hintedPairs(p *Prepared, hints []Hint) map[int]string {
	return hintedPairsLag(p, hints, hintLag)
}

func hintedPairsLag(p *Prepared, hints []Hint, lag float64) map[int]string {
	m := p.Meta
	index := make(map[ChunkSpeaker]int, len(p.Pairs))
	for i, pr := range p.Pairs {
		index[pr] = i
	}
	var names []string
	for _, h := range hints {
		names = append(names, h.Name)
		names = append(names, h.Candidates...)
	}
	canon := canonicalNames(names)
	out := map[int]string{}
	conflict := map[int]bool{}
	sr := float64(m.SampleRate)
	for _, h := range hints {
		name := canon[h.Name]
		if name == "" {
			continue
		}
		at := (h.T - lag) * sr // sample
		if at < 0 {
			continue
		}
		last := int(at) / m.WindowShift
		first := max(0, (int(at)-m.WindowSize)/m.WindowShift+1)
		for c := first; c <= last && c < len(p.Labels); c++ {
			chunk := p.Labels[c]
			if len(chunk) == 0 {
				continue
			}
			f := int((at - float64(c*m.WindowShift)) / float64(m.WindowSize) * float64(len(chunk)))
			if f < 0 || f >= len(chunk) {
				continue
			}
			spk, n := -1, 0
			for s, v := range chunk[f] {
				if v != 0 {
					spk, n = s, n+1
				}
			}
			if n != 1 {
				continue
			}
			i, ok := index[ChunkSpeaker{c, spk}]
			if !ok {
				continue
			}
			if prev, seen := out[i]; seen && prev != name {
				conflict[i] = true
			}
			out[i] = name
		}
	}
	for i := range conflict {
		delete(out, i)
	}
	return out
}

// applyHintConstraints corrects clusters with what the meeting window
// said: a cluster holding window-speakers confidently named two different
// things is split between them (each unnamed member going to the nearer
// name's voice), and clusters whose window-speakers are mostly named the
// same are merged when their voices are at least relaxedMerge similar,
// looser than the usual merge, since the names already say they're one
// person. A name needs minNameReads window-speakers to count.
func applyHintConstraints(embs [][]float32, clusters []int, named map[int]string, relaxedMerge float64) []int {
	if len(named) == 0 {
		return clusters
	}
	out := append([]int(nil), clusters...)
	next := 0
	for _, c := range out {
		next = max(next, c+1)
	}

	// Split clusters that hold two or more confident names.
	members := map[int][]int{}
	for i, c := range out {
		members[c] = append(members[c], i)
	}
	ids := make([]int, 0, len(members))
	for c := range members {
		ids = append(ids, c)
	}
	sort.Ints(ids)
	for _, c := range ids {
		count := map[string]int{}
		for _, i := range members[c] {
			if n, ok := named[i]; ok {
				count[n]++
			}
		}
		var strong []string
		for n, k := range count {
			if k >= minNameReads {
				strong = append(strong, n)
			}
		}
		if len(strong) < 2 {
			continue
		}
		sort.Strings(strong)
		cents := make([][]float64, len(strong))
		group := map[string]int{}
		for g, n := range strong {
			group[n] = g
		}
		for _, i := range members[c] {
			if g, ok := group[named[i]]; ok {
				cents[g] = addTo(cents[g], embs[i])
			}
		}
		newID := make([]int, len(strong))
		newID[0] = c
		for g := 1; g < len(strong); g++ {
			newID[g] = next
			next++
		}
		for _, i := range members[c] {
			g, ok := group[named[i]]
			if !ok {
				g = nearest(cents, embs[i])
			}
			out[i] = newID[g]
		}
	}

	// Merge clusters dominated by the same name.
	byCluster := map[int][]int{}
	for i, c := range out {
		byCluster[c] = append(byCluster[c], i)
	}
	dominant := map[int]string{}
	cent := map[int][]float64{}
	for c, ms := range byCluster {
		count := map[string]int{}
		total := 0
		for _, i := range ms {
			cent[c] = addTo(cent[c], embs[i])
			if n, ok := named[i]; ok {
				count[n]++
				total++
			}
		}
		for n, k := range count {
			if k >= minNameReads && float64(k) >= minNameShare*float64(total) {
				dominant[c] = n
			}
		}
	}
	parent := map[int]int{}
	find := func(x int) int {
		for {
			p, ok := parent[x]
			if !ok || p == x {
				return x
			}
			x = p
		}
	}
	cs := make([]int, 0, len(dominant))
	for c := range dominant {
		cs = append(cs, c)
	}
	sort.Ints(cs)
	for a := 0; a < len(cs); a++ {
		for b := a + 1; b < len(cs); b++ {
			ca, cb := cs[a], cs[b]
			if dominant[ca] == dominant[cb] && find(ca) != find(cb) && cosine(cent[ca], cent[cb]) >= relaxedMerge {
				parent[find(cb)] = find(ca)
			}
		}
	}
	for i, c := range out {
		out[i] = find(c)
	}
	return compactLabels(len(out), func(i int) int { return out[i] })
}

func addTo(sum []float64, v []float32) []float64 {
	if sum == nil {
		sum = make([]float64, len(v))
	}
	for i, x := range v {
		sum[i] += float64(x)
	}
	return sum
}

func nearest(cents [][]float64, v []float32) int {
	best, bestSim := 0, math.Inf(-1)
	for g, c := range cents {
		if s := cosine(c, toF64(v)); s > bestSim {
			best, bestSim = g, s
		}
	}
	return best
}

func toF64(v []float32) []float64 {
	out := make([]float64, len(v))
	for i, x := range v {
		out[i] = float64(x)
	}
	return out
}

func cosine(a, b []float64) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return -1
	}
	return dot / math.Sqrt(na*nb)
}
