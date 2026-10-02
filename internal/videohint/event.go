package videohint

import "time"

// EventStage is the result of one look (see Look): how far the capture
// and detection got, so the hint timeline can show why a look did or
// didn't produce a name.
type EventStage string

const (
	StageWindowNotFound EventStage = "window_not_found"
	StageCaptureFailed  EventStage = "capture_failed"
	StageFrameCaptured  EventStage = "frame_captured"
	StageNotACall       EventStage = "not_a_call"
	StageBlankCapture   EventStage = "blank_capture"
	StageUIFrozen       EventStage = "ui_frozen"
	StageOneOnOne       EventStage = "one_on_one"
	StageNoRule         EventStage = "no_rule"
	StageRingMatched    EventStage = "ring_matched"
	StageNoRingMatch    EventStage = "no_ring_match"
	StageAmbiguousRing  EventStage = "ambiguous_ring"
	StageSpeakerView    EventStage = "speaker_view"
	StageNoLabelRegion  EventStage = "no_label_region"
	StageOCRHit         EventStage = "ocr_hit"
	StageOCRMiss        EventStage = "ocr_miss"
)

// HintAttachMaxAge is the maxAge for speaker.Tracker.SetHintForRecent
// when the previous pipeline (diarize_during_meeting off) attaches a
// name to whoever the live pass heard most recently.
const HintAttachMaxAge = 30 * time.Second
