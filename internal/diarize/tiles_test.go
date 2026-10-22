package diarize

import "testing"

func unit(v ...float32) []float32 {
	var n float32
	for _, x := range v {
		n += x * x
	}
	n = float32(sqrt64(float64(n)))
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = x / n
	}
	return out
}

func sqrt64(x float64) float64 {
	z := x
	for i := 0; i < 40; i++ {
		z -= (z*z - x) / (2 * z)
	}
	return z
}

// Voices that sound alike stay apart when heard under different tiles; an
// embedding with no clear tile still clusters by voice.
func TestClusterKeepsTilesApart(t *testing.T) {
	embs := [][]float32{unit(1, 0), unit(1, 0.01), unit(1, 0.02), unit(0, 1)}
	free := clusterCompleteTiles(embs, 0.5, 0, nil)
	if free[0] != free[1] || free[1] != free[2] {
		t.Fatalf("without tiles the alike voices should share a cluster: %v", free)
	}
	tiles := []string{"room - new york", "ilse novak", "", ""}
	got := clusterCompleteTiles(embs, 0.5, 0, tiles)
	if got[0] == got[1] {
		t.Errorf("a room's audio and a person's tile share a cluster: %v", got)
	}
	if got[2] != got[0] && got[2] != got[1] {
		t.Errorf("an untiled window should still join its voice: %v", got)
	}
	if got[3] == got[0] || got[3] == got[1] {
		t.Errorf("a different voice joined: %v", got)
	}
}

func TestMergeAndAbsorbKeepTilesApart(t *testing.T) {
	embs := [][]float32{unit(1, 0), unit(1, 0.01), unit(0, 1)}
	clusters := []int{0, 1, 2}
	if got := mergeByCentroidTiles(embs, clusters, 0.9, nil); got[0] != got[1] {
		t.Fatalf("without tiles alike clusters merge: %v", got)
	}
	tiles := []string{"room - new york", "ilse novak", ""}
	if got := mergeByCentroidTiles(embs, clusters, 0.9, tiles); got[0] == got[1] {
		t.Errorf("clusters under different tiles merged: %v", got)
	}
	// Window 1 belongs to a small cluster; the only alike large one was
	// heard under another tile, so it goes to the other large one.
	spoke := []float64{10, 1, 10}
	if got := absorbByVoiceTiles(embs, []int{0, 1, 2}, spoke, 2, tiles); got[1] == 0 {
		t.Errorf("a window was absorbed into another tile's cluster: %v", got)
	}
	if got := absorbByVoiceTiles(embs, []int{0, 1, 2}, spoke, 2, nil); got[1] != 0 {
		t.Errorf("without tiles it goes to the alike voice: %v", got)
	}
}

func TestTileTimeline(t *testing.T) {
	at := TileTimeline([]Hint{
		{T: 10.5, Name: "Room - New York - Prospect Park"},
		{T: 12.5, Name: "Room - New Yor…"}, // the same tile, cut short
		{T: 20.5, Candidates: []string{"Ilse Novak", "Wayde Tyrrel"}},
		{T: 30.5, Name: "Ilse Novak"},
	}, 0.5)
	room := at(10.2)
	if room == "" || at(12.3) != room {
		t.Errorf("a cut-short room name should be the same tile: %q %q", room, at(12.3))
	}
	if !IsRoomTile(room) {
		t.Errorf("%q is a room", room)
	}
	if at(9) != "" || at(16) != "" {
		t.Errorf("no read before the first, or past the gap: %q %q", at(9), at(16))
	}
	if at(20.5) != "" {
		t.Errorf("several lit tiles are no clear read: %q", at(20.5))
	}
	if at(31) != "ilse novak" {
		t.Errorf("person tile: %q", at(31))
	}
}
