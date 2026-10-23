package diarize

import (
	"math"
	"sort"
	"strconv"

	"github.com/sosuke-ai/tomoe-pc/internal/session"
	"github.com/sosuke-ai/tomoe-pc/internal/voices"
)

// Rediarizing a saved meeting names its speakers as a meeting recorded
// today would be: the saved fingerprints clustered again (splitting a
// meeting room's voices when that's on), named from the meeting window's
// reads, then from voice profiles. Lines keep their text and timing (they
// aren't split again); names the user gave stay.

// RediarizeOptions are how a saved meeting is rediarized.
type RediarizeOptions struct {
	RoomSplit                bool
	RoomThreshold, RoomMerge float64
	Voices                   *voices.Store // nil: no voice profiles
}

// RediarizeChange is speech whose speaker's name changed.
type RediarizeChange struct {
	Before, After string // names ("" for none)
	Seconds       float64
}

// RediarizeResult is what rediarizing changed.
type RediarizeResult struct {
	Lines int // lines relabelled
	// NamedBefore and NamedAfter are how much of the meeting's speech (not
	// the mic) has a person's name, in seconds, of Speech.
	NamedBefore, NamedAfter, Speech float64
	Changes                         []RediarizeChange // by name, most speech first
	VoiceNamed                      int               // speakers named from profiles
}

// Rediarize relabels a saved session's lines (sess, in dir) from its
// fingerprints and hints (its name reads). The caller saves sess.
func Rediarize(dir string, sess *session.Session, hints []Hint, o RediarizeOptions) (RediarizeResult, error) {
	var res RediarizeResult
	p, info, err := LoadSaved(dir)
	if err != nil {
		return res, err
	}
	q := p
	if o.RoomSplit && len(hints) > 0 {
		q = p.WithTiles(p.PairTiles(RoomOnly(TileTimeline(hints, hintLag)), info.Offset, roomTileShare)).WithRoomSplit(o.RoomThreshold, o.RoomMerge)
	}
	params := info.Params
	cl := q.Cluster(params.Threshold, params.NumClusters)
	if params.MergeSimilarity > 0 {
		cl = q.MergeClusters(cl, params.MergeSimilarity)
	}
	cl = q.AbsorbSmallClusters(cl, params, info.MinSpeakerSeconds)
	turns := q.ReconstructClusters(cl, params)
	for i := range turns {
		turns[i].Start += info.Offset
		turns[i].End += info.Offset
	}
	names := NameSpeakers(DefaultNameParams(), turns, hints, math.Inf(1))

	// Names already someone's in this meeting: the window's, and the user's.
	given := map[string]bool{}
	for _, s := range sess.Segments {
		if n := GivenName(s.Speaker); n != "" {
			given[n] = true
		}
	}
	var matched map[int]voices.Matched
	if o.Voices != nil {
		vs := map[int]voices.Voice{}
		for i, c := range cl {
			if c >= 0 && (names[c] == "" || IsRoomTile(names[c])) {
				v := vs[c]
				v.Add(p.Embeddings[i])
				vs[c] = v
			}
		}
		matched = o.Voices.Match(vs, func(name string) bool {
			for _, n := range names {
				if !IsRoomTile(n) && SameName(n, name) {
					return true
				}
			}
			for n := range given {
				if SameName(n, name) {
					return true
				}
			}
			return false
		})
	}
	labels := map[int]string{}
	for _, t := range turns {
		k := t.Speaker
		switch {
		case matched[k].Name != "":
			labels[k] = personLabel(k, matched[k].Name)
		case names[k] != "":
			labels[k] = personLabel(k, names[k])
		default:
			labels[k] = personLabel(k, "")
		}
	}
	byLabel := map[string]*session.VoiceMatch{}
	sess.VoiceNames = nil
	for k, m := range matched {
		if m.Name == "" {
			continue
		}
		byLabel[labels[k]] = &session.VoiceMatch{Profile: m.Profile, Similarity: m.Similarity}
		sess.VoiceNames = append(sess.VoiceNames, session.VoiceName{Label: labels[k], Name: m.Name, Profile: m.Profile, Model: info.Model, Similarity: m.Similarity})
		res.VoiceNamed++
	}
	sort.Slice(sess.VoiceNames, func(i, j int) bool { return sess.VoiceNames[i].Label < sess.VoiceNames[j].Label })

	assigned, labs := session.DiarizationLabels(sess.Segments, turns, labels)
	changes := map[[2]string]float64{}
	for i := range sess.Segments {
		seg := &sess.Segments[i]
		if !session.Diarizable(*seg) {
			continue
		}
		d := seg.EndTime - seg.StartTime
		res.Speech += d
		before := speakerName(seg.Speaker)
		if before != "" {
			res.NamedBefore += d
		}
		if assigned[i] < 0 || GivenName(seg.Speaker) != "" {
			if before != "" {
				res.NamedAfter += d
			}
			continue
		}
		label := labs[assigned[i]]
		after := speakerName(label)
		if after != "" {
			res.NamedAfter += d
		}
		if label != seg.Speaker {
			res.Lines++
			seg.LiveSpeaker, seg.Speaker = seg.LiveLabel(), label
		}
		seg.VoiceMatch = byLabel[label]
		if before != after {
			changes[[2]string{before, after}] += d
		}
	}
	for k, s := range changes {
		res.Changes = append(res.Changes, RediarizeChange{Before: k[0], After: k[1], Seconds: s})
	}
	sort.Slice(res.Changes, func(i, j int) bool { return res.Changes[i].Seconds > res.Changes[j].Seconds })
	return res, nil
}

func personLabel(k int, name string) string {
	if name == "" {
		return "Person " + strconv.Itoa(k+1)
	}
	return "Person " + strconv.Itoa(k+1) + " (" + name + ")"
}

// speakerName is the person a line's label names ("" for none, or a room).
func speakerName(label string) string {
	if n := GivenName(label); n != "" {
		return n
	}
	if n := session.HintName(label); n != "" && !IsRoomTile(n) && !isProvisional(label) {
		return n
	}
	return ""
}
