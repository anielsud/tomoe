package ical

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestRewriteWindowsTZIDsPropertyForm(t *testing.T) {
	in := []byte("DTSTART;TZID=Eastern Standard Time:20260921T073000\r\n")
	got := rewriteWindowsTZIDs(in)
	want := "DTSTART;TZID=America/New_York:20260921T073000\r\n"
	if string(got) != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestRewriteWindowsTZIDsHeaderFormCRLF(t *testing.T) {
	in := []byte("BEGIN:VTIMEZONE\r\nTZID:Bangladesh Standard Time\r\nEND:VTIMEZONE\r\n")
	got := rewriteWindowsTZIDs(in)
	if !bytes.Contains(got, []byte("TZID:Asia/Dhaka")) {
		t.Errorf("expected Asia/Dhaka substitution, got %q", got)
	}
	if bytes.Contains(got, []byte("Bangladesh Standard Time")) {
		t.Errorf("original name should be gone, got %q", got)
	}
}

func TestRewriteWindowsTZIDsHeaderFormLF(t *testing.T) {
	in := []byte("BEGIN:VTIMEZONE\nTZID:Pacific Standard Time\nEND:VTIMEZONE\n")
	got := rewriteWindowsTZIDs(in)
	if !bytes.Contains(got, []byte("TZID:America/Los_Angeles")) {
		t.Errorf("expected America/Los_Angeles substitution, got %q", got)
	}
}

func TestRewriteWindowsTZIDsIdempotent(t *testing.T) {
	in := []byte("DTSTART;TZID=Eastern Standard Time:20260921T073000\r\n")
	once := rewriteWindowsTZIDs(in)
	twice := rewriteWindowsTZIDs(once)
	if !bytes.Equal(once, twice) {
		t.Errorf("second pass changed the output:\nfirst  = %q\nsecond = %q", once, twice)
	}
}

func TestRewriteWindowsTZIDsUnknownZoneLeftAlone(t *testing.T) {
	in := []byte("DTSTART;TZID=Made Up Zone Name:20260921T073000\r\n")
	got := rewriteWindowsTZIDs(in)
	if !bytes.Equal(got, in) {
		t.Errorf("unknown zone should pass through unchanged\nin = %q\nout = %q", in, got)
	}
}

func TestRewriteWindowsTZIDsNoTZIDFastPath(t *testing.T) {
	in := []byte("just some text\nwith no tzid anywhere\n")
	got := rewriteWindowsTZIDs(in)
	if &got[0] != &in[0] {
		t.Error("fast path should return the same slice header when no TZID token present")
	}
}

func TestRewriteWindowsTZIDsIANANamesPassthrough(t *testing.T) {
	// If a feed already uses IANA names, rewrite must not double-map them.
	in := []byte("DTSTART;TZID=America/New_York:20260921T073000\r\n")
	got := rewriteWindowsTZIDs(in)
	if !bytes.Equal(got, in) {
		t.Errorf("IANA name should not be rewritten:\nin  = %q\nout = %q", in, got)
	}
}

// TestParseICSWithWindowsTZID is the end-to-end test the whole file was
// written for: an Outlook fixture that would parse to zero events with
// vanilla golang-ical, but returns real events after the rewrite.
func TestParseICSWithWindowsTZID(t *testing.T) {
	f := openFixture(t, "outlook-teams.ics")

	// Wide window covering both events.
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	events, err := parseICS("Engineering 1:1", f, from, to)
	if err != nil {
		t.Fatalf("parseICS: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2 (both DTSTARTs should resolve)", len(events))
	}

	// The Eastern Standard Time event is 07:30 New York = 11:30 UTC.
	if !events[0].StartTime.Equal(time.Date(2026, 9, 21, 11, 30, 0, 0, time.UTC)) {
		t.Errorf("event 0 start = %v, want 2026-09-21T11:30:00Z", events[0].StartTime.UTC())
	}
	// URL should be extracted from DESCRIPTION.
	if !strings.Contains(events[0].MeetingURL, "teams.microsoft.com/l/meetup-join/") {
		t.Errorf("event 0 MeetingURL = %q, want a Teams meetup-join URL", events[0].MeetingURL)
	}

	// The Bangladesh Standard Time event is 14:00 Dhaka = 08:00 UTC.
	if !events[1].StartTime.Equal(time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC)) {
		t.Errorf("event 1 start = %v, want 2026-09-21T08:00:00Z", events[1].StartTime.UTC())
	}
}
