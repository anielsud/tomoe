package eval

import "testing"

func TestDiffTextKinds(t *testing.T) {
	ref := Words("We need SOC 2 by the end of the quarter, and the team agreed.")
	hyp := Words("We need sock two by end of the quarters, and a team agreed.")
	kinds := map[string]int{}
	for _, h := range DiffText(ref, hyp) {
		kinds[h.Kind]++
	}
	if kinds[KindMeaningful] != 1 {
		t.Errorf("want one meaningful hunk (SOC 2), got %v", kinds)
	}
	if kinds[KindWordForm] != 1 || kinds[KindSmallWords] < 1 {
		t.Errorf("want quarter/quarters as word form and the/a as small words, got %v", kinds)
	}
}

func TestDiffTextLongRun(t *testing.T) {
	ref := Words("hello there one two three four five six seven eight nine and goodbye")
	hyp := Words("hello there and goodbye")
	hs := DiffText(ref, hyp)
	if len(hs) != 1 || hs[0].Kind != KindLongRun {
		t.Fatalf("want one long run, got %+v", hs)
	}
}

func TestScoreSpotsAndTerms(t *testing.T) {
	ref := Words("I'll talk to Imran about SOC 2 tomorrow. SOC 2 is due.")
	spots := []TextSpot{{ID: "a", Category: "person-name", Before: "I'll talk to", Words: "Imran", After: "about", Pos: 0}}
	wrong := Words("I'll talk to him run about sock two tomorrow. SOC 2 is due.")
	right := Words("I'll talk to Imran about sock two tomorrow. SOC 2 is due.")
	if r := ScoreSpots(ref, wrong, spots); !r[0].Found || r[0].Fixed {
		t.Errorf("wrong transcript: %+v", r[0])
	}
	if r := ScoreSpots(ref, right, spots); !r[0].Fixed {
		t.Errorf("right transcript: %+v", r[0])
	}
	if h := TermHits(ref, right, []string{"SOC 2"}); h["SOC 2"] != [2]int{2, 1} {
		t.Errorf("SOC 2 hits = %v, want said 2, right 1", h["SOC 2"])
	}
}
