package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sosuke-ai/tomoe-pc/internal/config"
	"github.com/sosuke-ai/tomoe-pc/internal/models"
	"github.com/sosuke-ai/tomoe-pc/internal/session"
)

// diarizeSessionCmd is the internal subcommand spawned by the daemon and GUI
// to run sherpa-onnx speaker diarization in an isolated subprocess. Sherpa-onnx
// Process() can SIGSEGV inside its C++/CUDA stack; running it out-of-process
// keeps the parent (daemon or tomoe-gui) alive across such crashes.
//
// Hidden from --help to keep the user-facing CLI surface small. Users who want
// to re-run diarization on a saved session should use `tomoe session
// re-transcribe`, which also re-runs ASR.
var diarizeSessionCmd = &cobra.Command{
	Use:    "diarize-session <session-id>",
	Short:  "(internal) Re-run speaker diarization on a saved session in an isolated subprocess",
	Hidden: true,
	Args:   cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		useCPU, _ := cmd.Flags().GetBool("cpu")
		return runDiarizeSession(args[0], useCPU)
	},
}

func init() {
	diarizeSessionCmd.Flags().Bool("cpu", false, "Force CPU execution (overrides config GPU setting)")
	rootCmd.AddCommand(diarizeSessionCmd)
}

func runDiarizeSession(sessID string, forceCPU bool) error {
	cfg, err := config.Load(config.Path())
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	mgr := models.NewManager(cfg.Transcription.ModelPath)
	status := mgr.Check()
	if !status.DiarizationReady() {
		return fmt.Errorf("diarization models not available")
	}

	store := session.NewStore(config.SessionDir())
	sess, err := store.Load(sessID)
	if err != nil {
		return fmt.Errorf("loading session %s: %w", sessID, err)
	}

	useGPU := cfg.Transcription.GPUEnabled && !forceCPU
	sm, smPath, _ := status.SpeakerModelFor(cfg.Meeting.SpeakerModel, sess.Language)
	fmt.Printf("diarize-session %s: GPU=%v speaker model=%s\n", sessID, useGPU, sm.ID)

	count, err := session.ReidentifyByDiarization(sess, session.DiarizeConfig{
		SegmentationModelPath: status.SpeakerSegmentationPath,
		EmbeddingModelPath:    smPath,
		Threshold:             float32(sm.DiarizeThreshold),
		MergeThreshold:        sm.DiarizeMerge,
		UseGPU:                useGPU,
		SplitOnSpeakerChange:  cfg.Meeting.SplitOnSpeakerChange,
	})
	if err != nil {
		return fmt.Errorf("diarization: %w", err)
	}

	if count == 0 {
		fmt.Printf("diarize-session %s: no segments to refine\n", sessID)
		return nil
	}

	if err := store.Save(sess); err != nil {
		return fmt.Errorf("saving refined session: %w", err)
	}
	fmt.Printf("diarize-session %s: refined %d segments\n", sessID, count)
	return nil
}
