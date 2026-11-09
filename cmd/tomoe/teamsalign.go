package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sosuke-ai/tomoe-pc/internal/config"
	"github.com/sosuke-ai/tomoe-pc/internal/diarize"
	"github.com/sosuke-ai/tomoe-pc/internal/session"
)

// sessionTeamsAlignCmd names a saved meeting's speakers from the Teams
// transcript of the same meeting (teams.json, written into the session's
// folder by the companion app). A report by default; --apply saves,
// keeping a backup.
var sessionTeamsAlignCmd = &cobra.Command{
	Use:   "teams-align <session-id> | --all",
	Short: "Name a saved meeting's speakers from its Teams transcript (teams.json); a report unless --apply",
	Long: `Lines a saved meeting up with the Teams transcript of the same meeting,
finds the clock offset between them from what was said, and proposes a
Teams name for each speaker label that one Teams name clearly covers (at
least 30 s of overlap, 60% of the label's overlapped speech, twice the
runner-up's). When the words don't line up (another meeting's transcript),
nothing is proposed.

Nothing changes without --apply. --apply renames only labels Tomoe named
("Person N" and "Person N (Name)"), as "Person N (Teams name)", backing up
session.json first; "You" and names you gave are never changed.

--all reports every saved meeting with a Teams transcript (never saves).`,
	Args: cobra.MaximumNArgs(1),
	RunE: runTeamsAlign,
}

func init() {
	sessionTeamsAlignCmd.Flags().Bool("apply", false, "Save the proposed names (backing up session.json)")
	sessionTeamsAlignCmd.Flags().Bool("all", false, "Report every saved meeting with a Teams transcript (never saves)")
	sessionTeamsAlignCmd.Flags().Bool("json", false, "Print the result as JSON")
	sessionTeamsAlignCmd.Flags().Float64("max-shift", session.DefaultTeamsAlignParams().MaxShift, "Largest clock offset to search, in seconds")
	sessionCmd.AddCommand(sessionTeamsAlignCmd)
}

// Label kinds: what a label is before Teams names it.
const (
	kindMic         = "mic"         // "You": the microphone, never renamed
	kindGiven       = "given"       // a name the user gave, never renamed
	kindNamed       = "named"       // "Person N (Name)": named by the window or a voice
	kindRoom        = "room"        // a bare meeting room label, never renamed
	kindPlaceholder = "placeholder" // "Person N", "New speaker", "Name?"
)

// Outcomes: what --apply does with a label's proposal.
const (
	outNone      = "none"       // no proposal
	outAgree     = "agree"      // already that name
	outFill      = "fill"       // placeholder gets the name
	outSpelling  = "spelling"   // Tomoe's name is the same person, spelt otherwise (a cut-off tile)
	outReplace   = "replace"    // Tomoe's name is someone else's
	outKeptMic   = "kept-mic"   // "You" is never renamed
	outKeptGiven = "kept-given" // a name the user gave that Teams disagrees with
	outKeptRoom  = "kept-room"  // a room label, or Teams names a room over a person
)

// changes reports whether --apply renames a label with this outcome.
func changes(outcome string) bool {
	return outcome == outFill || outcome == outSpelling || outcome == outReplace
}

type teamsLabelPlan struct {
	session.TeamsLabel
	Kind     string `json:"kind"`
	Current  string `json:"current,omitempty"` // the name the label has now
	Outcome  string `json:"outcome"`
	NewLabel string `json:"new_label,omitempty"`
}

type teamsPlan struct {
	ID              string                `json:"id"`
	Created         time.Time             `json:"created"`
	Shift           float64               `json:"shift"`
	AtEdge          bool                  `json:"at_edge,omitempty"`
	ShiftFrom       string                `json:"shift_from"`
	Anchors         int                   `json:"anchors"`
	Agreement       float64               `json:"agreement"`
	AgreementAtZero float64               `json:"agreement_at_zero"`
	Overlapped      float64               `json:"overlapped"`
	Labels          []teamsLabelPlan      `json:"labels"`
	UnusedNames     []session.NameOverlap `json:"unused_names,omitempty"`
	Saved           bool                  `json:"saved,omitempty"`
	Backup          string                `json:"backup,omitempty"`
}

var personLabel = regexp.MustCompile(`^Person (\d+)(?: \((.*)\))?$`)

// roomHeadcount is the number of people Teams appends to a meeting room's
// name ("Room - Floor 7 (8)").
var roomHeadcount = regexp.MustCompile(`\s*\(\d+\)$`)

// labelName is a Teams name as a label carries it: a room's name without
// the headcount Teams appends to it.
func labelName(teamsName string) string {
	if diarize.IsRoomTile(teamsName) {
		return roomHeadcount.ReplaceAllString(teamsName, "")
	}
	return teamsName
}

// classifyLabel says what kind of label this is and the name it carries.
func classifyLabel(label string) (kind, name string) {
	l := strings.TrimSpace(label)
	switch {
	case l == "You":
		return kindMic, ""
	case diarize.GivenName(l) != "":
		return kindGiven, l
	}
	if m := personLabel.FindStringSubmatch(l); m != nil {
		if n := strings.TrimSpace(m[2]); n != "" && !strings.HasSuffix(n, "?") {
			return kindNamed, n
		}
		return kindPlaceholder, ""
	}
	if diarize.IsRoomTile(l) {
		return kindRoom, l
	}
	return kindPlaceholder, "" // "New speaker", "Name?"
}

// sameNameLoose reports whether two names are one person's: equal but for
// case, spelt within an edit or two or cut off (diarize.SameName), or one
// is just the other's first name (a tile that shows first names).
func sameNameLoose(a, b string) bool {
	if strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b)) || diarize.SameName(a, b) {
		return true
	}
	fa, fb := strings.Fields(strings.ToLower(a)), strings.Fields(strings.ToLower(b))
	return len(fa) > 0 && len(fb) > 0 && (len(fa) == 1 || len(fb) == 1) && fa[0] == fb[0]
}

// planTeams aligns sess with its Teams transcript and decides, per label,
// what --apply would do.
func planTeams(sess *session.Session, tr *session.TeamsTranscript, p session.TeamsAlignParams) teamsPlan {
	a := session.AlignTeams(sess.Segments, tr.Spans(sess.CreatedAt), p)
	plan := teamsPlan{
		ID: sess.ID, Created: sess.CreatedAt, Shift: a.Shift, AtEdge: a.AtEdge, ShiftFrom: a.ShiftFrom, Anchors: a.Anchors,
		Agreement: a.Agreement, AgreementAtZero: a.AgreementAtZero, Overlapped: a.Overlapped,
		UnusedNames: a.UnusedNames,
	}
	taken := map[string]bool{}
	maxN := 0
	for _, seg := range sess.Segments {
		taken[seg.Speaker] = true
		if m := personLabel.FindStringSubmatch(seg.Speaker); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil && n > maxN {
				maxN = n
			}
		}
	}
	for _, tl := range a.Labels {
		lp := teamsLabelPlan{TeamsLabel: tl, Outcome: outNone}
		lp.Kind, lp.Current = classifyLabel(tl.Label)
		if tl.Proposed != "" {
			lp.Outcome = outcomeFor(lp.Kind, lp.Current, labelName(tl.Proposed))
		}
		if changes(lp.Outcome) {
			n := 0
			if m := personLabel.FindStringSubmatch(tl.Label); m != nil {
				n, _ = strconv.Atoi(m[1])
			} else {
				maxN++
				n = maxN
			}
			lp.NewLabel = fmt.Sprintf("Person %d (%s)", n, labelName(tl.Proposed))
			if lp.NewLabel == tl.Label {
				lp.Outcome, lp.NewLabel = outAgree, ""
			} else if taken[lp.NewLabel] {
				// Another label already reads exactly so: renaming would
				// merge two speakers. Leave it to the user.
				lp.Outcome, lp.NewLabel = outNone, ""
				lp.Reason = "new label already in use"
			} else {
				taken[lp.NewLabel] = true
			}
		}
		plan.Labels = append(plan.Labels, lp)
	}
	return plan
}

// outcomeFor is what --apply does with a label of this kind, carrying
// current, that Teams says is proposed.
func outcomeFor(kind, current, proposed string) string {
	switch kind {
	case kindMic:
		return outKeptMic
	case kindRoom:
		return outKeptRoom
	case kindGiven:
		if sameNameLoose(current, proposed) {
			return outAgree
		}
		return outKeptGiven
	case kindNamed:
		switch {
		case strings.EqualFold(current, proposed):
			return outAgree
		case sameNameLoose(current, proposed):
			return outSpelling
		case diarize.IsRoomTile(proposed) && !diarize.IsRoomTile(current):
			// Teams credits all of a room's speech to the room's tile:
			// Tomoe telling the room's people apart is better than that.
			return outKeptRoom
		}
		return outReplace
	}
	return outFill
}

// applyTeams renames the labels plan changes in sess, the way renaming a
// speaker by hand does (keeping the live label, dropping the voice match),
// and drops voice reviews of the old labels.
func applyTeams(sess *session.Session, plan teamsPlan) int {
	rename := map[string]string{}
	for _, lp := range plan.Labels {
		if changes(lp.Outcome) && lp.NewLabel != "" {
			rename[lp.Label] = lp.NewLabel
		}
	}
	if len(rename) == 0 {
		return 0
	}
	for i := range sess.Segments {
		seg := &sess.Segments[i]
		if to, ok := rename[seg.Speaker]; ok {
			seg.LiveSpeaker, seg.Speaker = seg.LiveLabel(), to
			seg.VoiceMatch = nil
		}
	}
	names := sess.VoiceNames[:0]
	for _, v := range sess.VoiceNames {
		if _, ok := rename[v.Label]; !ok {
			names = append(names, v)
		}
	}
	sess.VoiceNames = names
	doubts := sess.VoiceDoubts[:0]
	for _, v := range sess.VoiceDoubts {
		if _, ok := rename[v.Label]; !ok {
			doubts = append(doubts, v)
		}
	}
	sess.VoiceDoubts = doubts
	return len(rename)
}

// backupSessionFile copies dir's session.json to
// session.json.bak-teams-<time>, never overwriting a file.
func backupSessionFile(dir string, now time.Time) (string, error) {
	orig, err := os.ReadFile(filepath.Join(dir, "session.json"))
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "session.json.bak-teams-"+now.Format("20060102-150405"))
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", err
	}
	if _, err := f.Write(orig); err != nil {
		f.Close()
		return "", err
	}
	return path, f.Close()
}

func runTeamsAlign(cmd *cobra.Command, args []string) error {
	apply, _ := cmd.Flags().GetBool("apply")
	all, _ := cmd.Flags().GetBool("all")
	asJSON, _ := cmd.Flags().GetBool("json")
	p := session.DefaultTeamsAlignParams()
	p.MaxShift, _ = cmd.Flags().GetFloat64("max-shift")
	if p.MaxShift < 0 || p.MaxShift > 3600 {
		return errors.New("--max-shift must be between 0 and 3600 seconds")
	}
	store := session.NewStore(config.SessionDir())
	switch {
	case all && apply:
		return errors.New("--all only reports: apply one session at a time")
	case all && len(args) > 0:
		return errors.New("give a session ID or --all, not both")
	case all:
		return teamsAlignAll(store, p, asJSON)
	case len(args) != 1:
		return errors.New("give a session ID, or --all")
	}

	sess, err := store.Load(args[0])
	if err != nil {
		return err
	}
	tr := store.TeamsTranscript(sess.ID)
	if tr == nil {
		return fmt.Errorf("session %s has no Teams transcript (teams.json)", sess.ID)
	}
	plan := planTeams(sess, tr, p)
	if apply {
		if n := applyTeams(sess, plan); n > 0 {
			dir := filepath.Join(config.SessionDir(), sess.ID)
			if plan.Backup, err = backupSessionFile(dir, time.Now()); err != nil {
				return fmt.Errorf("backing up session.json: %w", err)
			}
			if err := store.Save(sess); err != nil {
				return err
			}
			plan.Saved = true
		}
	}
	if asJSON {
		return printJSON(plan)
	}
	printTeamsPlan(plan, apply)
	return nil
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func printTeamsPlan(plan teamsPlan, apply bool) {
	fmt.Printf("%s %s\n", plan.Created.Local().Format("2006-01-02 15:04"), plan.ID)
	edge := ""
	if plan.AtEdge {
		edge = " (at the search's limit: try a larger --max-shift)"
	}
	fmt.Printf("Clock shift: %+.1f s, from the %s (%d word sequences agree)%s.\n", plan.Shift, plan.ShiftFrom, plan.Anchors, edge)
	fmt.Printf("Speech agreeing with its label's name: %.0f s of %.0f s overlapped (%.0f s unshifted).\n\n",
		plan.Agreement, plan.Overlapped, plan.AgreementAtZero)
	for _, lp := range plan.Labels {
		top := "-"
		if len(lp.Names) > 0 {
			top = fmt.Sprintf("%s %.0f s", lp.Names[0].Name, lp.Names[0].Seconds)
		}
		fmt.Printf("%-28.28s %6.0f s speech, %6.0f s overlapped; best %-30.30s %3.0f%%  ",
			lp.Label, lp.Speech, lp.Overlapped, top, 100*lp.Share)
		switch {
		case lp.Outcome == outNone && lp.TakenBy != "":
			fmt.Printf("none: %s by %s\n", lp.Reason, lp.TakenBy)
		case lp.Outcome == outNone:
			fmt.Printf("none: %s\n", lp.Reason)
		case changes(lp.Outcome):
			fmt.Printf("%s -> %s\n", lp.Outcome, lp.NewLabel)
		default:
			fmt.Printf("%s (%s)\n", lp.Outcome, lp.Proposed)
		}
	}
	if len(plan.UnusedNames) > 0 {
		var parts []string
		for _, n := range plan.UnusedNames {
			parts = append(parts, fmt.Sprintf("%s %.0f s", n.Name, n.Seconds))
		}
		fmt.Printf("\nTeams names no label got: %s\n", strings.Join(parts, ", "))
	}
	switch {
	case plan.Saved:
		fmt.Printf("\nSaved; the previous session.json is %s\n", plan.Backup)
	case apply:
		fmt.Println("\nNothing to change.")
	default:
		fmt.Println("\nNothing saved: --apply saves.")
	}
}

// teamsTotals are --all's counts over every session.
type teamsTotals struct {
	Sessions       int            `json:"sessions"`
	Labels         int            `json:"labels"`
	Proposed       int            `json:"proposed"`
	ByKind         map[string]int `json:"labels_by_kind"`
	ByOutcome      map[string]int `json:"by_outcome"`
	ByReason       map[string]int `json:"unproposed_by_reason"`
	ShareBuckets   map[string]int `json:"proposed_share"`
	ProposedSpeech float64        `json:"proposed_speech_seconds"`
	Speech         float64        `json:"speech_seconds"`
	Shifts         []float64      `json:"shifts"`
	ShiftsFromText int            `json:"shifts_from_text"`
	ShiftsAtEdge   int            `json:"shifts_at_edge"`
	UnusedNames    int            `json:"unused_names"`
}

func shareBucket(s float64) string {
	switch {
	case s >= 0.95:
		return "95-100%"
	case s >= 0.9:
		return "90-95%"
	case s >= 0.8:
		return "80-90%"
	case s >= 0.7:
		return "70-80%"
	default:
		return "60-70%"
	}
}

func teamsAlignAll(store *session.Store, p session.TeamsAlignParams, asJSON bool) error {
	list, err := store.List()
	if err != nil {
		return err
	}
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt.Before(list[j].CreatedAt) })
	t := teamsTotals{ByKind: map[string]int{}, ByOutcome: map[string]int{}, ByReason: map[string]int{}, ShareBuckets: map[string]int{}}
	var plans []teamsPlan
	for _, sess := range list {
		tr := store.TeamsTranscript(sess.ID)
		if tr == nil {
			continue
		}
		plan := planTeams(sess, tr, p)
		plans = append(plans, plan)
		t.Sessions++
		t.Shifts = append(t.Shifts, plan.Shift)
		if plan.ShiftFrom == session.ShiftFromText {
			t.ShiftsFromText++
		}
		if plan.AtEdge {
			t.ShiftsAtEdge++
		}
		t.UnusedNames += len(plan.UnusedNames)
		count := map[string]int{}
		proposed := 0
		for _, lp := range plan.Labels {
			t.Labels++
			t.ByKind[lp.Kind]++
			t.Speech += lp.Speech
			count[lp.Outcome]++
			t.ByOutcome[lp.Outcome]++
			if lp.Proposed != "" {
				proposed++
				t.ShareBuckets[shareBucket(lp.Share)]++
				t.ProposedSpeech += lp.Speech
			} else {
				t.ByReason[lp.Reason]++
			}
		}
		t.Proposed += proposed
		if !asJSON {
			edge := "t" // from the text
			if plan.ShiftFrom != session.ShiftFromText {
				edge = "s" // from the speakers
			}
			if plan.AtEdge {
				edge += "!"
			}
			fmt.Printf("%s %s  shift %+4.0f%-2s labels %3d  proposed %3d  agree %2d  fill %2d  spelling %2d  replace %2d  kept: given %d mic %d room %d\n",
				plan.Created.Local().Format("2006-01-02 15:04"), plan.ID[:8], plan.Shift, edge, len(plan.Labels), proposed,
				count[outAgree], count[outFill], count[outSpelling], count[outReplace], count[outKeptGiven], count[outKeptMic], count[outKeptRoom])
		}
	}
	if asJSON {
		return printJSON(struct {
			Totals   teamsTotals `json:"totals"`
			Sessions []teamsPlan `json:"sessions"`
		}{t, plans})
	}
	fmt.Printf("\n%d sessions, %d labels, %d proposed (%.0f%% of %.1f h speech). agree %d, fill %d, spelling %d, replace %d; kept: given %d, mic %d, room %d. Shifts from the text %d of %d; %d at the search's limit. Nothing saved.\n",
		t.Sessions, t.Labels, t.Proposed, share(t.ProposedSpeech, t.Speech), t.Speech/3600,
		t.ByOutcome[outAgree], t.ByOutcome[outFill], t.ByOutcome[outSpelling], t.ByOutcome[outReplace],
		t.ByOutcome[outKeptGiven], t.ByOutcome[outKeptMic], t.ByOutcome[outKeptRoom], t.ShiftsFromText, t.Sessions, t.ShiftsAtEdge)
	return nil
}
