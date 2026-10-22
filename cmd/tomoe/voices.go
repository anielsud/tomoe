package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/sosuke-ai/tomoe-pc/internal/config"
	"github.com/sosuke-ai/tomoe-pc/internal/diarize"
	"github.com/sosuke-ai/tomoe-pc/internal/models"
	"github.com/sosuke-ai/tomoe-pc/internal/session"
	"github.com/sosuke-ai/tomoe-pc/internal/videohint"
	"github.com/sosuke-ai/tomoe-pc/internal/voices"
)

// voicesCmd manages voice profiles (config.MeetingConfig.VoiceProfiles).
var voicesCmd = &cobra.Command{
	Use:   "voices",
	Short: "Voice profiles: learn them from saved meetings, list them",
}

var voicesLearnCmd = &cobra.Command{
	Use:   "learn [session-id...]",
	Short: "Learn voice profiles from saved meetings (all of them by default)",
	Long: `Learns each person's voice from saved meetings: the fingerprints heard
while their own tile was lit, and the speakers you named. Learning a meeting
again replaces what it taught before. Meetings need their fingerprints
(diarization.gob, saved when diarizing during the meeting).`,
	RunE: runVoicesLearn,
}

var voicesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List voice profiles, and pairs that may be one person",
	RunE:  runVoicesList,
}

func init() {
	voicesLearnCmd.Flags().String("since", "", "Only meetings since this date (YYYY-MM-DD)")
	voicesCmd.AddCommand(voicesLearnCmd, voicesListCmd)
	rootCmd.AddCommand(voicesCmd)
}

func runVoicesLearn(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(config.Path())
	if err != nil {
		return err
	}
	var since time.Time
	if s, _ := cmd.Flags().GetString("since"); s != "" {
		if since, err = time.ParseInLocation("2006-01-02", s, time.Local); err != nil {
			return fmt.Errorf("--since: %w", err)
		}
	}
	ids := args
	if len(ids) == 0 {
		ents, err := os.ReadDir(config.SessionDir())
		if err != nil {
			return err
		}
		for _, e := range ents {
			if e.IsDir() {
				ids = append(ids, e.Name())
			}
		}
	}
	store := session.NewStore(config.SessionDir())
	meetings, people := 0, map[string]bool{}
	for _, id := range ids {
		sess, err := store.Load(id)
		if err != nil || sess.CreatedAt.Before(since) {
			continue
		}
		dir := filepath.Join(config.SessionDir(), id)
		if _, err := os.Stat(filepath.Join(dir, "diarization.gob")); err != nil {
			continue
		}
		names, err := diarize.LearnSaved(cfg.Meeting, dir, sess, savedHints(dir, sess))
		if err != nil {
			fmt.Printf("%s: skipped: %v\n", id, err)
			continue
		}
		if len(names) > 0 {
			meetings++
			for _, n := range names {
				people[n] = true
			}
			fmt.Printf("%s %s: %d voices\n", sess.CreatedAt.Local().Format("2006-01-02 15:04"), id[:8], len(names))
		}
	}
	fmt.Printf("Learnt from %d meetings: %d people.\n", meetings, len(people))
	return nil
}

// savedHints are a saved session's name reads from the meeting window.
func savedHints(dir string, sess *session.Session) []diarize.Hint {
	looks, _ := videohint.ReadLooks(dir)
	var hints []diarize.Hint
	for _, l := range looks {
		t := l.Time.Sub(sess.CreatedAt).Seconds()
		if name, candidates := l.Accepted(); name != "" {
			hints = append(hints, diarize.Hint{T: t, Name: name})
		} else if len(candidates) > 1 {
			hints = append(hints, diarize.Hint{T: t, Candidates: candidates})
		}
	}
	return hints
}

func runVoicesList(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(config.Path())
	if err != nil {
		return err
	}
	for _, m := range models.SpeakerModels {
		if _, err := os.Stat(voices.Path(config.DataDir(), m.ID)); err != nil {
			continue
		}
		vs, err := diarize.VoiceStore(cfg.Meeting, m.ID)
		if err != nil {
			return err
		}
		profiles := vs.Profiles()
		fmt.Printf("%s: %d profiles\n", m.Name, len(profiles))
		byID := map[string]string{}
		for _, p := range profiles {
			byID[p.ID] = p.Name
			v := p.Voice()
			fmt.Printf("  %-30s %3d meetings %5d fingerprints, last heard %s\n", p.Name, len(p.Meetings), v.N, p.LastHeard().Local().Format("2006-01-02"))
		}
		pairs := vs.LookAlike(lookAlike)
		sort.SliceStable(pairs, func(i, j int) bool { return pairs[i].Similarity > pairs[j].Similarity })
		if len(pairs) > 0 {
			fmt.Println("  May be one person:")
			for _, pr := range pairs {
				fmt.Printf("    %s / %s (%.2f alike)\n", byID[pr.A], byID[pr.B], pr.Similarity)
			}
		}
	}
	return nil
}

// lookAlike is how alike two profiles must be to ask whether they're one
// person: the naming bar (see voices.DefaultParams).
const lookAlike = 0.7
