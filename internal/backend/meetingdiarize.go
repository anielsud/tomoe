package backend

import (
	"fmt"
	"path/filepath"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/sosuke-ai/tomoe-pc/internal/config"
	"github.com/sosuke-ai/tomoe-pc/internal/diarize"
	"github.com/sosuke-ai/tomoe-pc/internal/models"
	"github.com/sosuke-ai/tomoe-pc/internal/session"
)

// newMeetingDiarizer starts diarizing the session about to record while it
// records (see diarize.SessionDiarizer), sending relabeled lines to the
// transcript, or returns nil (logging why, if it was asked for) so the
// post-meeting pass runs as before.
func newMeetingDiarizer(a *App, cfg *config.Config, status *models.Status, lang string) *diarize.SessionDiarizer {
	var d *diarize.SessionDiarizer
	d, err := diarize.NewSessionDiarizer(cfg.Meeting, status, lang, &a.mu, func(changed []session.Segment) {
		a.mu.Lock()
		visible := a.meetingDiar == d
		a.mu.Unlock()
		if visible {
			for _, seg := range changed {
				wailsRuntime.EventsEmit(a.ctx, "transcript:segment:update", seg)
			}
		}
	})
	if err != nil {
		fmt.Printf("diarize during meeting: %v; diarizing after the meeting instead\n", err)
		return nil
	}
	if d != nil {
		fmt.Printf("diarizing during the meeting (%s)\n", d.Describe())
	}
	return d
}

// finishMeetingDiarizer applies d's final labels to sess, reporting
// whether that worked (if not, the post-meeting pass should run).
func finishMeetingDiarizer(d *diarize.SessionDiarizer, sess *session.Session) bool {
	if err := d.Finish(filepath.Join(config.SessionDir(), sess.ID)); err != nil {
		fmt.Printf("session %s: diarizing during the meeting failed: %v\n", sess.ID, err)
		return false
	}
	return true
}
