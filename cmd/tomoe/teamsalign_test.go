package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sosuke-ai/tomoe-pc/internal/session"
)

func TestClassifyLabel(t *testing.T) {
	tests := []struct{ label, kind, name string }{
		{"You", kindMic, ""},
		{"Robin Vale", kindGiven, "Robin Vale"},
		{"Person 3", kindPlaceholder, ""},
		{"Person 3 (Robin Vale)", kindNamed, "Robin Vale"},
		{"Person 3 (Robin?)", kindPlaceholder, ""},
		{"Person 3 (Room - Floor 7)", kindNamed, "Room - Floor 7"},
		{"New speaker", kindPlaceholder, ""},
		{"Robin?", kindPlaceholder, ""},
		{"Room - Floor 7", kindRoom, "Room - Floor 7"},
	}
	for _, tt := range tests {
		if kind, name := classifyLabel(tt.label); kind != tt.kind || name != tt.name {
			t.Errorf("classifyLabel(%q) = %q, %q; want %q, %q", tt.label, kind, name, tt.kind, tt.name)
		}
	}
}

func TestOutcomeFor(t *testing.T) {
	tests := []struct{ kind, current, proposed, want string }{
		{kindMic, "", "Robin Vale", outKeptMic},
		{kindGiven, "Robin Vale", "Robin Vale", outAgree},
		{kindGiven, "Robin", "Robin Vale", outAgree}, // first name only
		{kindGiven, "Robin Vale", "Kai Moreno", outKeptGiven},
		{kindNamed, "Robin Vale", "robin vale", outAgree},
		{kindNamed, "Natalia Rami", "Natalia Ramirez", outSpelling}, // cut-off tile
		{kindNamed, "Robin Vale", "Kai Moreno", outReplace},
		{kindNamed, "Robin Vale", "Room - Floor 7", outKeptRoom},
		{kindNamed, "Room - Fl", "Room - Floor 7", outSpelling},
		{kindNamed, "Room - Floor 7", "Robin Vale", outReplace},
		{kindRoom, "Room - Floor 7", "Robin Vale", outKeptRoom},
		{kindPlaceholder, "", "Robin Vale", outFill},
	}
	for _, tt := range tests {
		if got := outcomeFor(tt.kind, tt.current, tt.proposed); got != tt.want {
			t.Errorf("outcomeFor(%s, %q, %q) = %s, want %s", tt.kind, tt.current, tt.proposed, got, tt.want)
		}
	}
}

func TestLabelName(t *testing.T) {
	if got := labelName("Room - Floor 7 (8)"); got != "Room - Floor 7" {
		t.Errorf("room: %q", got)
	}
	if got := labelName("Robin Vale (2)"); got != "Robin Vale (2)" {
		t.Errorf("person: %q", got)
	}
}

// teamsSession is a synthetic meeting: each label talks in turns, and the
// Teams transcript (clock 10 s behind) names who really spoke, with text
// that lines the two up.
func teamsSession() (*session.Session, *session.TeamsTranscript) {
	created := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	sess := &session.Session{ID: "s1", CreatedAt: created}
	tr := &session.TeamsTranscript{Writer: "musubi", Source: "teams", Start: created.Add(-10 * time.Second)}
	who := []struct{ label, name string }{
		{"You", "Owner Person"},
		{"Person 1", "Robin Vale"},             // placeholder: filled
		{"Person 2 (Kai Moren)", "Kai Moreno"}, // spelling
		{"Pat Quinn", "Lee Hart"},              // the user's name: kept
		{"Person 4 (Lee Hart)", "Ana Ruiz"},    // someone else: replaced
	}
	tm := 2.0
	for i := 0; i < 100; i++ {
		w := who[i%len(who)]
		text := fmt.Sprintf("we talked about topic%d and then item%d before part%d", i, i*3, i*7)
		sess.Segments = append(sess.Segments, session.Segment{ID: fmt.Sprint(i), Speaker: w.label, StartTime: tm, EndTime: tm + 9.5, Text: text,
			VoiceMatch: &session.VoiceMatch{Profile: "p", Similarity: 0.9}})
		tr.Turns = append(tr.Turns, session.TeamsTurn{Speaker: w.name, Offset: tm, Text: text})
		tm += 10
	}
	sess.VoiceNames = []session.VoiceName{{Label: "Person 4 (Lee Hart)", Name: "Lee Hart"}, {Label: "Pat Quinn", Name: "Pat Quinn"}}
	return sess, tr
}

func TestPlanAndApplyTeams(t *testing.T) {
	sess, tr := teamsSession()
	plan := planTeams(sess, tr, session.DefaultTeamsAlignParams())
	if plan.ShiftFrom != session.ShiftFromText || plan.Shift < 9 || plan.Shift > 11 {
		t.Errorf("shift %v from %s, want about +10 from the text", plan.Shift, plan.ShiftFrom)
	}
	want := map[string]struct{ outcome, newLabel string }{
		"You":                  {outKeptMic, ""},
		"Person 1":             {outFill, "Person 1 (Robin Vale)"},
		"Person 2 (Kai Moren)": {outSpelling, "Person 2 (Kai Moreno)"},
		"Pat Quinn":            {outKeptGiven, ""},
		"Person 4 (Lee Hart)":  {outReplace, "Person 4 (Ana Ruiz)"},
	}
	for _, lp := range plan.Labels {
		w := want[lp.Label]
		if lp.Outcome != w.outcome || lp.NewLabel != w.newLabel {
			t.Errorf("%s: %s -> %q (%s), want %s -> %q", lp.Label, lp.Outcome, lp.NewLabel, lp.Reason, w.outcome, w.newLabel)
		}
	}
	if n := applyTeams(sess, plan); n != 3 {
		t.Errorf("renamed %d labels, want 3", n)
	}
	for _, seg := range sess.Segments {
		switch seg.LiveLabel() {
		case "You", "Pat Quinn":
			if seg.Speaker != seg.LiveLabel() || seg.LiveSpeaker != "" || seg.VoiceMatch == nil {
				t.Errorf("kept label changed: %+v", seg)
			}
		default:
			if !strings.HasPrefix(seg.Speaker, "Person ") || seg.VoiceMatch != nil || seg.LiveSpeaker == "" {
				t.Errorf("renamed line: %+v", seg)
			}
		}
	}
	if len(sess.VoiceNames) != 1 || sess.VoiceNames[0].Label != "Pat Quinn" {
		t.Errorf("voice names = %+v, want only the kept label's", sess.VoiceNames)
	}
	// Applying again changes nothing.
	if n := applyTeams(sess, planTeams(sess, tr, session.DefaultTeamsAlignParams())); n != 0 {
		t.Errorf("second apply renamed %d", n)
	}
}

func TestBackupSessionFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "session.json"), []byte(`{"id":"s1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 9, 8, 7, 6, 0, time.Local)
	path, err := backupSessionFile(dir, now)
	if err != nil || filepath.Base(path) != "session.json.bak-teams-20261109-080706" {
		t.Fatalf("backup %q, %v", path, err)
	}
	if b, _ := os.ReadFile(path); string(b) != `{"id":"s1"}` {
		t.Errorf("backup holds %q", b)
	}
	if _, err := backupSessionFile(dir, now); err == nil {
		t.Error("a second backup in the same second overwrote the first")
	}
}
