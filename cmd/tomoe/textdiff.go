package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sosuke-ai/tomoe-pc/internal/config"
	"github.com/sosuke-ai/tomoe-pc/internal/eval"
	"github.com/sosuke-ai/tomoe-pc/internal/session"
)

// textDiffCmd scores a transcript's words against a Teams transcript of the
// same meeting: the scorecard for transcription changes (models, decoding,
// vocabulary) on real meetings. Both transcripts are automatic, so a
// disagreement isn't always Tomoe's error; spots judged earlier say which
// are.
var textDiffCmd = &cobra.Command{
	Use:   "textdiff <session-id | session.json | replay .json> --ref <teams.txt>",
	Short: "Score a transcript's words against a Teams transcript: disagreements that matter, judged spots fixed, names and terms",
	Long: `Aligns a transcript's words to a Teams transcript of the same meeting
(numbers, fillers and punctuation normalized as tomoe eval does) and reports:

  - word disagreement with Teams overall;
  - disagreements that can change meaning, per 1,000 reference words
    (leaving out function-word swaps, plural/tense, split/joined words,
    and stretches of 8+ words only one side has, reported separately);
  - if <ref>.spots.jsonl exists next to the reference: how many spots
    judged earlier as Tomoe errors are now right, by category, and how
    often each judged name or term is right wherever the meeting says it.

The transcript can be a saved session (by ID or session.json) or the
.json a session replay writes (tomoe session replay --only-current).`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		refPath, _ := cmd.Flags().GetString("ref")
		out, _ := cmd.Flags().GetString("hunks")
		return runTextDiff(args[0], refPath, out)
	},
}

func init() {
	textDiffCmd.Flags().String("ref", "", "Teams transcript of the same meeting (required)")
	textDiffCmd.Flags().String("hunks", "", "Also write every meaningful disagreement to this .jsonl file, for review")
	_ = textDiffCmd.MarkFlagRequired("ref")
	rootCmd.AddCommand(textDiffCmd)
}

// loadTranscriptSegments reads a saved session (ID or session.json) or a
// replay's segment list.
func loadTranscriptSegments(arg string) ([]session.Segment, error) {
	if strings.HasSuffix(arg, ".json") {
		data, err := os.ReadFile(arg)
		if err != nil {
			return nil, err
		}
		var segs []session.Segment
		if err := json.Unmarshal(data, &segs); err == nil {
			return segs, nil
		}
		var sess session.Session
		if err := json.Unmarshal(data, &sess); err != nil {
			return nil, fmt.Errorf("%s is neither a segment list nor a session: %w", arg, err)
		}
		return sess.Segments, nil
	}
	sess, err := session.NewStore(config.SessionDir()).Load(arg)
	if err != nil {
		return nil, err
	}
	return sess.Segments, nil
}

func runTextDiff(arg, refPath, hunksPath string) error {
	segs, err := loadTranscriptSegments(arg)
	if err != nil {
		return err
	}
	sort.SliceStable(segs, func(i, j int) bool { return segs[i].StartTime < segs[j].StartTime })
	var hypText []string
	for _, s := range segs {
		hypText = append(hypText, s.Text)
	}
	f, err := os.Open(refPath)
	if err != nil {
		return err
	}
	ref, err := eval.ParseTeamsTranscript(f, 1e9)
	f.Close()
	if err != nil {
		return err
	}
	var refText []string
	for _, t := range ref.Turns {
		refText = append(refText, t.Text)
	}
	rw, hw := eval.Words(strings.Join(refText, " ")), eval.Words(strings.Join(hypText, " "))

	counts := eval.Align(rw, hw)
	hunks := eval.DiffText(rw, hw)
	type tally struct{ hunks, words int }
	byKind := map[string]*tally{}
	for _, h := range hunks {
		t := byKind[h.Kind]
		if t == nil {
			t = &tally{}
			byKind[h.Kind] = t
		}
		t.hunks++
		t.words += max(h.RefTo-h.RefFrom, h.HypTo-h.HypFrom)
	}
	per1k := func(n int) float64 { return 1000 * float64(n) / float64(max(1, len(rw))) }

	fmt.Printf("Reference %d words, transcript %d words (normalized)\n", len(rw), len(hw))
	fmt.Printf("Word disagreement with Teams: %.1f%% (%d substituted, %d missing, %d extra)\n",
		100*counts.Rate(), counts.Substitutions, counts.Deletions, counts.Insertions)
	m := byKind[eval.KindMeaningful]
	if m == nil {
		m = &tally{}
	}
	fmt.Printf("Disagreements that can change meaning: %d (%.1f per 1,000 reference words), %d words\n", m.hunks, per1k(m.hunks), m.words)
	for _, k := range []string{eval.KindSmallWords, eval.KindWordForm, eval.KindSplitJoin, eval.KindLongRun} {
		if t := byKind[k]; t != nil {
			fmt.Printf("  not counted: %-12s %4d places, %5d words\n", k, t.hunks, t.words)
		}
	}

	spotsPath := strings.TrimSuffix(refPath, filepath.Ext(refPath)) + ".spots.jsonl"
	if spots, err := readSpots(spotsPath); err == nil && len(spots) > 0 {
		results := eval.ScoreSpots(rw, hw, spots)
		type cat struct{ found, fixed int }
		cats := map[string]*cat{}
		var found, fixed int
		for _, r := range results {
			c := cats[r.Spot.Category]
			if c == nil {
				c = &cat{}
				cats[r.Spot.Category] = c
			}
			if r.Found {
				c.found++
				found++
				if r.Fixed {
					c.fixed++
					fixed++
				}
			}
		}
		fmt.Printf("\nJudged spots (%s): %d of %d located; now right: %d (%.0f%%)\n", filepath.Base(spotsPath), found, len(spots), fixed, 100*float64(fixed)/float64(max(1, found)))
		names := make([]string, 0, len(cats))
		for n := range cats {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			fmt.Printf("  %-18s %3d of %3d right\n", n, cats[n].fixed, cats[n].found)
		}
		var terms []string
		seen := map[string]bool{}
		for _, s := range spots {
			if t := strings.TrimSpace(s.Term); t != "" && !seen[strings.ToLower(t)] {
				seen[strings.ToLower(t)] = true
				terms = append(terms, t)
			}
		}
		hits := eval.TermHits(rw, hw, terms)
		var said, right int
		for _, h := range hits {
			said += h[0]
			right += h[1]
		}
		fmt.Printf("\nNames and terms from the judged spots, everywhere the reference says them: %d of %d right (%.0f%%)\n", right, said, 100*float64(right)/float64(max(1, said)))
		for _, t := range eval.SortedTerms(hits) {
			fmt.Printf("  %-28s %3d of %3d\n", t, hits[t][1], hits[t][0])
		}
	}

	if hunksPath != "" {
		f, err := os.Create(hunksPath)
		if err != nil {
			return err
		}
		defer f.Close()
		w := bufio.NewWriter(f)
		for _, h := range hunks {
			if h.Kind != eval.KindMeaningful {
				continue
			}
			line, _ := json.Marshal(map[string]any{
				"ref": strings.Join(rw[h.RefFrom:h.RefTo], " "), "hyp": strings.Join(hw[h.HypFrom:h.HypTo], " "),
				"before": strings.Join(rw[max(0, h.RefFrom-4):h.RefFrom], " "), "after": strings.Join(rw[h.RefTo:min(len(rw), h.RefTo+4)], " "),
				"pos": float64(h.RefFrom) / float64(max(1, len(rw))),
			})
			fmt.Fprintln(w, string(line))
		}
		if err := w.Flush(); err != nil {
			return err
		}
	}
	return nil
}

func readSpots(path string) ([]eval.TextSpot, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var spots []eval.TextSpot
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var s eval.TextSpot
		if err := json.Unmarshal(sc.Bytes(), &s); err == nil {
			spots = append(spots, s)
		}
	}
	return spots, sc.Err()
}
