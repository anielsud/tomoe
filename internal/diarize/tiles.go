package diarize

import (
	"sort"
	"strings"
)

// Tiles: the meeting window says who is talking (the tile with the ring),
// and that keeps voices apart that sound alike. Audio heard under a
// meeting room's tile belongs to someone in that room and to none of the
// people on their own tiles; two people's own tiles are two people. The
// clustering keeps embeddings heard under different tiles in different
// clusters (Prepared.WithTiles). Only a clear read counts: one tile lit,
// recent, and most of an embedding's speech under it.

// roomTileShare is how much of an embedding's speech must be under one
// tile for the embedding to count as heard under it.
const roomTileShare = 0.6

// tileMaxGap is how long a read of the ring holds when no newer read
// replaces it (seconds): the watcher looks about once a second while
// someone talks.
const tileMaxGap = 2.0

// TileTimeline returns which tile was lit at session time t, from the
// meeting window's reads: the latest single-name read within tileMaxGap
// before t (shifted back by lag, as the ring follows the voice), else "".
// A read showing several lit tiles is no clear read. Names are folded
// together as NameSpeakers folds them (a tile's name cut short is the same
// tile).
func TileTimeline(hints []Hint, lag float64) func(t float64) string {
	var all []string
	for _, h := range hints {
		all = append(all, h.Name)
		all = append(all, h.Candidates...)
	}
	canon := canonicalNames(all)
	type read struct {
		t   float64
		key string
	}
	var reads []read
	for _, h := range hints {
		key := ""
		if h.Name != "" && len(h.Candidates) < 2 {
			key = normalizeName(canon[h.Name])
		}
		reads = append(reads, read{h.T - lag, key})
	}
	sort.SliceStable(reads, func(i, j int) bool { return reads[i].t < reads[j].t })
	return func(t float64) string {
		i := sort.Search(len(reads), func(i int) bool { return reads[i].t > t }) - 1
		if i < 0 || t-reads[i].t > tileMaxGap {
			return ""
		}
		return reads[i].key
	}
}

// IsRoomTile reports whether a tile's name is a meeting room's (a room
// device joins as one participant for everyone in the room).
func IsRoomTile(key string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(key)), "room")
}

// RoomOnly folds every person's own tile into one: the only constraint
// left is that a room's audio is no one else's. The ring is a noisy guide
// to which of two people is talking (it lags, lingers, and sits on the
// wrong one when they overlap); keeping people apart by it split one
// person into several on meetings with references, while a room's tile is
// lit for anyone in the room.
func RoomOnly(tileAt func(t float64) string) func(t float64) string {
	return func(t float64) string {
		k := tileAt(t)
		if k == "" || IsRoomTile(k) {
			return k
		}
		return "remote"
	}
}

// PairTiles is the tile each embedding (window, local speaker) was heard
// under: the tile lit for at least minShare of the frames that speaker is
// active in that window, else "". offset converts the audio's time to
// session time (StreamInfo.Offset).
func (p *Prepared) PairTiles(tileAt func(t float64) string, offset, minShare float64) []string {
	m := p.Meta
	out := make([]string, len(p.Pairs))
	if m.ReceptiveFieldShift == 0 || m.SampleRate == 0 {
		return out
	}
	for i, pair := range p.Pairs {
		if pair.Chunk >= len(p.Labels) {
			continue
		}
		start := int(float32(pair.Chunk)*float32(m.WindowShift)/float32(m.ReceptiveFieldShift) + 0.5)
		count := map[string]int{}
		active := 0
		for f, row := range p.Labels[pair.Chunk] {
			// Every 4th frame (about 70 ms) is plenty to tell which tile
			// was lit, at a quarter of the lookups.
			if f%4 != 0 || pair.Speaker >= len(row) || row[pair.Speaker] == 0 {
				continue
			}
			active++
			t := float64(start+f)*float64(m.ReceptiveFieldShift)/float64(m.SampleRate) + offset
			if k := tileAt(t); k != "" {
				count[k]++
			}
		}
		best, n := "", 0
		for k, c := range count {
			if c > n || (c == n && k < best) {
				best, n = k, c
			}
		}
		if active > 0 && float64(n) >= minShare*float64(active) {
			out[i] = best
		}
	}
	return out
}
