package diarize

import "testing"

func TestSnapToInvitee(t *testing.T) {
	invitees := []string{"Robin Vale", "Kai Moreno", "Natalia Ramirez", "Ana Lee", "Jon Smithson", "Jan Smithson"}
	tests := []struct{ read, want string }{
		{"Robin Vale", "Robin Vale"},            // exact
		{"robin vale", "Robin Vale"},            // case
		{"Robln Vale", "Robin Vale"},            // one OCR misread
		{"Kal Moren0", "Kai Moreno"},            // two misreads, 10 letters: allowed
		{"Ana Lea", "Ana Lee"},                  // 7 letters allow one edit
		{"Ana Lxx", "Ana Lxx"},                  // …but not two
		{"Ann", "Ann"},                          // short names: no edits at all
		{"Natalya Ramires", "Natalia Ramirez"},  // two edits on 15 letters
		{"Natalya Ramyres", "Natalya Ramyres"},  // three edits: never
		{"Natalia Rami…", "Natalia Rami…"},      // truncated: left for canonicalNames
		{"Natalia Ramirez…", "Natalia Ramirez"}, // trailing junk
		{"Jon Smithsen", "Jon Smithson"},        // the closer of two similar invitees
		{"Jxn Smithson", "Jxn Smithson"},        // as close to both: unchanged
		{"Pat Quinn", "Pat Quinn"},              // a guest: nobody close
		{"", ""},
	}
	for _, tt := range tests {
		if got := SnapToInvitee(tt.read, invitees); got != tt.want {
			t.Errorf("SnapToInvitee(%q) = %q, want %q", tt.read, got, tt.want)
		}
	}
	if got := SnapToInvitee("Robin Vale", nil); got != "Robin Vale" {
		t.Errorf("no invitees: %q", got)
	}
}

func TestSnapHints(t *testing.T) {
	hints := []Hint{
		{T: 1, Name: "Robln Vale"},
		{T: 2, Candidates: []string{"Kai Moren0", "Pat Quinn"}},
		{T: 3, Name: "Pat Quinn"},
	}
	if n := SnapHints(hints, nil); n != 0 || hints[0].Name != "Robln Vale" {
		t.Fatalf("no invitees changed %d reads", n)
	}
	n := SnapHints(hints, []string{"Robin Vale", "Kai Moreno"})
	if n != 2 || hints[0].Name != "Robin Vale" || hints[1].Candidates[0] != "Kai Moreno" || hints[1].Candidates[1] != "Pat Quinn" || hints[2].Name != "Pat Quinn" {
		t.Errorf("changed %d: %+v", n, hints)
	}
}
