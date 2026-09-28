package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRelabelByDiarizationAssignsByOverlap(t *testing.T) {
	segs := []Segment{
		{Speaker: "You", Source: "mic", StartTime: 0, EndTime: 2},
		{Speaker: "Person 1", Source: "monitor", StartTime: 0, EndTime: 2},
		{Speaker: "Person 1", Source: "monitor", StartTime: 5, EndTime: 7},
		{Speaker: "Other", Source: "monitor", StartTime: 20, EndTime: 21}, // no overlap: unchanged
	}
	diar := []DiarizeSegment{{Start: 0, End: 3, Speaker: 4}, {Start: 4, End: 8, Speaker: 9}}
	speakerMap := map[int]string{4: "Person 1", 9: "Person 2"}

	if n := relabelByDiarization(segs, diar, speakerMap, false); n != 2 {
		t.Errorf("relabeled %d segments, want 2", n)
	}
	want := []string{"You", "Person 1", "Person 2", "Other"}
	for i, w := range want {
		if segs[i].Speaker != w {
			t.Errorf("segment %d speaker = %q, want %q", i, segs[i].Speaker, w)
		}
	}
}

func TestRelabelByDiarizationKeepsVideoHintNames(t *testing.T) {
	segs := []Segment{
		{Speaker: "Person 3 (Jordan Alva…)", Source: "monitor", StartTime: 0, EndTime: 2},
		{Speaker: "Person 3 (Jordan Alvarez)", Source: "monitor", StartTime: 2, EndTime: 4},
		{Speaker: "Person 5", Source: "monitor", StartTime: 4, EndTime: 5}, // same cluster, no hint yet
		{Speaker: "Person 1", Source: "monitor", StartTime: 10, EndTime: 12},
	}
	diar := []DiarizeSegment{{Start: 0, End: 6, Speaker: 0}, {Start: 9, End: 13, Speaker: 1}}
	speakerMap := map[int]string{0: "Person 1", 1: "Person 2"}

	relabelByDiarization(segs, diar, speakerMap, false)

	// Equal speaking time for both reads: the full name wins over the truncated one.
	for i := 0; i < 3; i++ {
		if segs[i].Speaker != "Person 1 (Jordan Alvarez)" {
			t.Errorf("segment %d speaker = %q, want %q", i, segs[i].Speaker, "Person 1 (Jordan Alvarez)")
		}
	}
	if segs[3].Speaker != "Person 2" {
		t.Errorf("segment 3 speaker = %q, want %q (no name to carry)", segs[3].Speaker, "Person 2")
	}
}

func TestRelabelByDiarizationMostSpeakingTimeWins(t *testing.T) {
	segs := []Segment{
		{Speaker: "Person 1 (Sam)", Source: "monitor", StartTime: 0, EndTime: 1},
		{Speaker: "Person 2 (Alex)", Source: "monitor", StartTime: 1, EndTime: 6},
	}
	diar := []DiarizeSegment{{Start: 0, End: 6, Speaker: 0}}
	relabelByDiarization(segs, diar, map[int]string{0: "Person 1"}, false)
	for i := range segs {
		if segs[i].Speaker != "Person 1 (Alex)" {
			t.Errorf("segment %d speaker = %q, want %q", i, segs[i].Speaker, "Person 1 (Alex)")
		}
	}
}

func TestRelabelByDiarizationLeavesSystemAudioAlone(t *testing.T) {
	segs := []Segment{{Speaker: "System Audio", Source: "monitor", StartTime: 0, EndTime: 3}}
	diar := []DiarizeSegment{{Start: 0, End: 3, Speaker: 0}}
	if n := relabelByDiarization(segs, diar, map[int]string{0: "Person 1"}, false); n != 0 || segs[0].Speaker != "System Audio" {
		t.Errorf("relabeled %d, speaker %q; want System Audio left alone", n, segs[0].Speaker)
	}
}

func TestReidentifyByDiarizationSkipsSessionsWithNothingToDiarize(t *testing.T) {
	audio := filepath.Join(t.TempDir(), "audio.m4a")
	if err := os.WriteFile(audio, []byte("placeholder"), 0o644); err != nil {
		t.Fatal(err)
	}
	sess := &Session{
		AudioPath: audio,
		Sources:   []string{"mic", "monitor"},
		Segments: []Segment{
			{Speaker: "You", Source: "mic", StartTime: 0, EndTime: 1},
			{Speaker: "System Audio", Source: "monitor", StartTime: 1, EndTime: 2},
		},
	}
	// Returns before touching the (placeholder) audio or any model.
	n, err := ReidentifyByDiarization(sess, DiarizeConfig{})
	if err != nil || n != 0 {
		t.Errorf("ReidentifyByDiarization() = (%d, %v), want (0, nil)", n, err)
	}
}
