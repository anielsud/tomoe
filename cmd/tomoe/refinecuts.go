package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/sosuke-ai/tomoe-pc/internal/config"
	"github.com/sosuke-ai/tomoe-pc/internal/diarize"
	"github.com/sosuke-ai/tomoe-pc/internal/session"
)

// sessionRefineCutsCmd moves speaker-change cuts in a saved session to
// where the voice changes (MeetingConfig.RefineSpeakerCuts does it after
// each meeting).
var sessionRefineCutsCmd = &cobra.Command{
	Use:   "refine-cuts <session-id>",
	Short: "Move each speaker-change cut in a saved session to where the voice changes",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		store := session.NewStore(config.SessionDir())
		sess, err := store.Load(args[0])
		if err != nil {
			return err
		}
		began := time.Now()
		rp := diarize.DefaultRefineParams()
		rp.From, _ = cmd.Flags().GetFloat64("from")
		rp.To, _ = cmd.Flags().GetFloat64("to")
		rp.From, rp.To = rp.From*60, rp.To*60
		if g, _ := cmd.Flags().GetFloat64("min-gain"); g >= 0 {
			rp.MinGain = g
		}
		n, err := diarize.RefineSavedCuts(filepath.Join(config.SessionDir(), sess.ID), sess, rp)
		if err != nil {
			return err
		}
		fmt.Printf("%d cuts moved in %.0fs\n", n, time.Since(began).Seconds())
		if out, _ := cmd.Flags().GetString("out"); out != "" {
			b, err := json.MarshalIndent(sess, "", "  ")
			if err != nil {
				return err
			}
			return os.WriteFile(out, b, 0o644)
		}
		return store.Save(sess)
	},
}

func init() {
	sessionRefineCutsCmd.Flags().String("out", "", "Write the refined session here instead of saving it")
	sessionRefineCutsCmd.Flags().Float64("from", 0, "Only cuts from this minute")
	sessionRefineCutsCmd.Flags().Float64("min-gain", -1, "How much better a new cut must fit the voices to move (default: the measured setting)")
	sessionRefineCutsCmd.Flags().Float64("to", 0, "Only cuts up to this minute (0: to the end)")
	sessionCmd.AddCommand(sessionRefineCutsCmd)
}
