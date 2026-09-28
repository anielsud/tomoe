package meetingaudio

import "testing"

func TestNewMonitorSourceNoSourceIsMicOnly(t *testing.T) {
	sc, err := NewMonitorSource(NoSource)
	if err != nil || sc != nil {
		t.Errorf("NewMonitorSource(%q) = (%v, %v), want (nil, nil)", NoSource, sc, err)
	}
}
