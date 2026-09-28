package videohint

import (
	"testing"

	"github.com/sosuke-ai/tomoe-pc/internal/meeting"
)

func TestRuleForTeamsIsFullyCalibrated(t *testing.T) {
	r, ok := ruleFor(meeting.PlatformTeams)
	if !ok {
		t.Fatal("expected a Teams rule")
	}
	if !r.Chrome.configured() || !r.Ring.configured() || !r.Label.configured() {
		t.Errorf("Teams rule should configure chrome, ring and label; got %+v", r)
	}
	// The chrome tests exercise DetectCallChrome against their own copy of
	// the Teams calibration; keep the two from drifting apart.
	if r.Chrome != teamsChromeMarker() {
		t.Errorf("Teams chrome marker = %+v, want %+v (update chrome_test.go too)", r.Chrome, teamsChromeMarker())
	}
}

func TestRuleForUncalibratedPlatform(t *testing.T) {
	if _, ok := ruleFor(meeting.PlatformZoom); ok {
		t.Error("expected no rule for Zoom until it has calibration data")
	}
	if (LabelRegion{}).configured() {
		t.Error("zero-value LabelRegion should report unconfigured")
	}
}
