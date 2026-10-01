package session

import "time"

// Session represents a meeting transcription session.
type Session struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Platform  string    `json:"platform,omitempty"` // "Teams", "Meet", "Zoom", etc.
	Language  string    `json:"language,omitempty"` // ISO 639-1 code: "en", "bn", etc.
	CreatedAt time.Time `json:"created_at"`
	EndedAt   time.Time `json:"ended_at,omitempty"`
	Duration  float64   `json:"duration"` // seconds
	Sources   []string  `json:"sources"`  // e.g., ["mic", "monitor"]
	Segments  []Segment `json:"segments"`
	AudioPath string    `json:"audio_path,omitempty"`
}

// Segment is a single transcribed utterance within a session.
type Segment struct {
	ID        string  `json:"id"`
	Speaker   string  `json:"speaker"`
	Text      string  `json:"text"`
	StartTime float64 `json:"start_time"`         // seconds from session start
	EndTime   float64 `json:"end_time"`           // seconds from session start
	Source    string  `json:"source"`             // "mic" or "monitor"
	Language  string  `json:"language,omitempty"` // ISO 639-1 code: "en", "bn", etc.
	// Status is "" (default, meaning final -- also what every segment
	// from before this field existed implicitly means), "live" (the
	// person is still talking; Text is pass 1's partial hypothesis and
	// will keep growing under the same ID), or "pending" (the utterance
	// is done, Text is pass 1's finished-but-unrefined text, and a
	// higher-quality re-decode is in flight). A later update carrying
	// the same ID and Status "" supersedes either. See internal/live's
	// two-pass pipeline.
	Status string `json:"status,omitempty"`
	// Decision is which rule inside speaker.Tracker.Assign produced
	// Speaker for this segment ("confident", "sticky", "short-segment",
	// "new-speaker" — see speaker.AssignDecision), or "" for the mic
	// source (always "You", never audio-clustered) or when no
	// clustering ran at all. Plain string rather than importing
	// speaker.AssignDecision here, since internal/session has no other
	// reason to depend on internal/speaker. Diagnostic only, purely
	// informational for a diagnostics view (see
	// docs/macos-video-hints.md) -- never read back to change
	// behavior, and safe for older sessions on disk to simply lack it.
	Decision string `json:"decision,omitempty"`
	// Words are the text's words with their timings (session seconds),
	// from the recognizer's token timestamps. Used to split a line where
	// diarization says the speaker changed (see SplitByDiarization).
	// Absent for pass-1 text and for sessions recorded before it existed.
	Words []Word `json:"words,omitempty"`
	// LiveSpeaker is the label the live pass gave the segment when a
	// later pass (diarizing during the meeting) has since relabeled
	// Speaker: kept because it can carry a video-hint name that the
	// relabeling votes on. "" means Speaker is still the live label.
	LiveSpeaker string `json:"live_speaker,omitempty"`
}

// LiveLabel is the label the live pass gave seg (see LiveSpeaker).
func (seg Segment) LiveLabel() string {
	if seg.LiveSpeaker != "" {
		return seg.LiveSpeaker
	}
	return seg.Speaker
}

// Word is one transcribed word and when it was said. Short JSON keys, since
// a long meeting has thousands.
type Word struct {
	Text  string  `json:"t"`
	Start float64 `json:"s"`
	End   float64 `json:"e"`
}

// statusRank orders Segment.Status values by how settled the text is:
// "live" < "pending" < "" (final).
func statusRank(status string) int {
	switch status {
	case "live":
		return 0
	case "pending":
		return 1
	default:
		return 2
	}
}

// UpsertSegment applies a segment or a revision of one (same ID) to the
// session. Live transcription delivers new segments and revisions on
// separate channels, so a revision can arrive before its segment, or a
// segment's first emission can be dropped altogether; either way the
// segment is inserted, in StartTime order. A revision never regresses a
// segment to a less settled status (a stale "live" partial arriving
// after the final text is ignored).
func (s *Session) UpsertSegment(seg Segment) {
	for i := range s.Segments {
		if s.Segments[i].ID != seg.ID {
			continue
		}
		if statusRank(s.Segments[i].Status) <= statusRank(seg.Status) {
			s.Segments[i] = seg
		}
		return
	}
	at := len(s.Segments)
	for at > 0 && s.Segments[at-1].StartTime > seg.StartTime {
		at--
	}
	s.Segments = append(s.Segments, Segment{})
	copy(s.Segments[at+1:], s.Segments[at:])
	s.Segments[at] = seg
}
