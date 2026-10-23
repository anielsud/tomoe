package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/sosuke-ai/tomoe-pc/internal/config"
	"github.com/sosuke-ai/tomoe-pc/internal/diarize"
	"github.com/sosuke-ai/tomoe-pc/internal/session"
)

// sessionRediarizeCmd names saved meetings' speakers as a meeting recorded
// today would be (room split, voice profiles), from their saved
// fingerprints. A report by default; --apply saves, keeping a backup.
var sessionRediarizeCmd = &cobra.Command{
	Use:   "rediarize [session-id...]",
	Short: "Name saved meetings' speakers as today's settings would (room split, voice profiles); a report unless --apply",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(config.Path())
		if err != nil {
			return err
		}
		apply, _ := cmd.Flags().GetBool("apply")
		var since time.Time
		if s, _ := cmd.Flags().GetString("since"); s != "" {
			if since, err = time.ParseInLocation("2006-01-02", s, time.Local); err != nil {
				return err
			}
		}
		store := session.NewStore(config.SessionDir())
		ids := args
		if len(ids) == 0 {
			list, err := store.List()
			if err != nil {
				return err
			}
			for _, s := range list {
				ids = append(ids, s.ID)
			}
		}
		var total, named0, named1 float64
		touched := 0
		for _, id := range ids {
			sess, err := store.Load(id)
			if err != nil || sess.CreatedAt.Before(since) {
				continue
			}
			dir := filepath.Join(config.SessionDir(), id)
			if _, err := os.Stat(filepath.Join(dir, "diarization.gob")); err != nil {
				continue
			}
			_, info, err := diarize.LoadSaved(dir)
			if err != nil {
				continue
			}
			o := diarize.RediarizeOptions{RoomSplit: cfg.Meeting.DiarizeRoomSplit, RoomThreshold: cfg.Meeting.RoomSplitThreshold, RoomMerge: cfg.Meeting.RoomSplitMerge}
			if cfg.Meeting.VoiceProfiles && info.Model != "" {
				if o.Voices, err = diarize.VoiceStore(cfg.Meeting, info.Model); err != nil {
					return err
				}
			}
			res, err := diarize.Rediarize(dir, sess, savedHints(dir, sess), o)
			if err != nil {
				fmt.Printf("%s: %v\n", id[:8], err)
				continue
			}
			total += res.Speech
			named0 += res.NamedBefore
			named1 += res.NamedAfter
			if len(res.Changes) == 0 {
				continue
			}
			touched++
			fmt.Printf("%s %s %-36.36s named %3.0f%% -> %3.0f%%  (%d by voice)\n", sess.CreatedAt.Local().Format("2006-01-02 15:04"), id[:8], sess.Title,
				share(res.NamedBefore, res.Speech), share(res.NamedAfter, res.Speech), res.VoiceNamed)
			sort.SliceStable(res.Changes, func(i, j int) bool { return res.Changes[i].Seconds > res.Changes[j].Seconds })
			for i, c := range res.Changes {
				if i == 6 {
					fmt.Printf("    … %d more\n", len(res.Changes)-6)
					break
				}
				fmt.Printf("    %-24s -> %-24s %5.1f min\n", orNone(c.Before), orNone(c.After), c.Seconds/60)
			}
			if out, _ := cmd.Flags().GetString("out"); out != "" {
				b, _ := json.MarshalIndent(sess, "", "  ")
				if err := os.WriteFile(filepath.Join(out, id+".json"), b, 0o644); err != nil {
					return err
				}
			}
			if apply {
				orig, _ := os.ReadFile(filepath.Join(dir, "session.json"))
				_ = os.WriteFile(filepath.Join(dir, "session.json.bak-rediarize"), orig, 0o644)
				if err := store.Save(sess); err != nil {
					return err
				}
			}
		}
		fmt.Printf("\n%d meetings change; speech named %.0f%% -> %.0f%% of %.1f h.", touched, share(named0, total), share(named1, total), total/3600)
		if apply {
			fmt.Println(" Saved (each session.json backed up as session.json.bak-rediarize).")
		} else {
			fmt.Println(" Nothing saved: --apply saves.")
		}
		return nil
	},
}

func share(a, b float64) float64 {
	if b == 0 {
		return 0
	}
	return 100 * a / b
}

func orNone(s string) string {
	if s == "" {
		return "(no name)"
	}
	return s
}

func init() {
	sessionRediarizeCmd.Flags().Bool("apply", false, "Save the new names (backing up each session.json)")
	sessionRediarizeCmd.Flags().String("since", "", "Only meetings since this date (YYYY-MM-DD)")
	sessionRediarizeCmd.Flags().String("out", "", "Also write each rediarized session to this directory, to compare")
	sessionCmd.AddCommand(sessionRediarizeCmd)
}
