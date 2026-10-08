package session

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func testSession() *Session {
	return &Session{
		Title:     "Team Standup",
		CreatedAt: time.Date(2026, 3, 10, 9, 0, 0, 0, time.UTC),
		Duration:  125.0,
		Segments: []Segment{
			{Speaker: "You", Text: "Good morning everyone.", StartTime: 0, EndTime: 2.5},
			{Speaker: "Person 1", Text: "Hey, good morning.", StartTime: 3.0, EndTime: 4.5},
			{Speaker: "Person 2", Text: "Can we push to 4?", StartTime: 65.0, EndTime: 67.2},
		},
	}
}

func TestExportMarkdown(t *testing.T) {
	sess := testSession()
	var buf bytes.Buffer

	if err := ExportMarkdown(sess, &buf); err != nil {
		t.Fatalf("ExportMarkdown error: %v", err)
	}

	output := buf.String()

	if !strings.Contains(output, "# Team Standup") {
		t.Error("missing title heading")
	}
	if !strings.Contains(output, "**Date:** 2026-03-10 09:00:00") {
		t.Error("missing date")
	}
	if !strings.Contains(output, "**Duration:** 2m05s") {
		t.Error("missing duration")
	}
	if !strings.Contains(output, "**[00:00:00] You:** Good morning everyone.") {
		t.Error("missing first segment")
	}
	if !strings.Contains(output, "**[00:00:03] Person 1:** Hey, good morning.") {
		t.Error("missing second segment")
	}
	if !strings.Contains(output, "**[00:01:05] Person 2:** Can we push to 4?") {
		t.Error("missing third segment")
	}
}

func TestExportPlainText(t *testing.T) {
	sess := testSession()
	var buf bytes.Buffer

	if err := ExportPlainText(sess, &buf); err != nil {
		t.Fatalf("ExportPlainText error: %v", err)
	}

	output := buf.String()

	if !strings.Contains(output, "Team Standup\n") {
		t.Error("missing title")
	}
	if !strings.Contains(output, "[00:00:00] You: Good morning everyone.") {
		t.Error("missing first segment")
	}
	if !strings.Contains(output, "[00:01:05] Person 2: Can we push to 4?") {
		t.Error("missing third segment")
	}
}

func TestExportSRT(t *testing.T) {
	sess := testSession()
	var buf bytes.Buffer

	if err := ExportSRT(sess, &buf); err != nil {
		t.Fatalf("ExportSRT error: %v", err)
	}

	output := buf.String()

	// SRT format: sequence number, timestamps, text
	if !strings.Contains(output, "1\n00:00:00,000 --> 00:00:02,500") {
		t.Error("missing first SRT entry")
	}
	if !strings.Contains(output, "You: Good morning everyone.") {
		t.Error("missing first segment text")
	}
	if !strings.Contains(output, "2\n00:00:03,000 --> 00:00:04,500") {
		t.Error("missing second SRT entry")
	}
	if !strings.Contains(output, "3\n00:01:05,000 --> 00:01:07,200") {
		t.Error("missing third SRT entry")
	}
}

func TestFormatTimestamp(t *testing.T) {
	tests := []struct {
		seconds float64
		want    string
	}{
		{0, "00:00:00"},
		{65, "00:01:05"},
		{3661, "01:01:01"},
		{0.4, "00:00:00"},
		{0.5, "00:00:01"}, // rounds
	}

	for _, tt := range tests {
		got := formatTimestamp(tt.seconds)
		if got != tt.want {
			t.Errorf("formatTimestamp(%v) = %q, want %q", tt.seconds, got, tt.want)
		}
	}
}

func TestFormatSRTTimestamp(t *testing.T) {
	tests := []struct {
		seconds float64
		want    string
	}{
		{0, "00:00:00,000"},
		{2.5, "00:00:02,500"},
		{67.2, "00:01:07,200"},
		{3661.123, "01:01:01,123"},
	}

	for _, tt := range tests {
		got := formatSRTTimestamp(tt.seconds)
		if got != tt.want {
			t.Errorf("formatSRTTimestamp(%v) = %q, want %q", tt.seconds, got, tt.want)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		seconds float64
		want    string
	}{
		{0, "0s"},
		{30, "30s"},
		{65, "1m05s"},
		{3661, "1h01m01s"},
	}

	for _, tt := range tests {
		got := formatDuration(tt.seconds)
		if got != tt.want {
			t.Errorf("formatDuration(%v) = %q, want %q", tt.seconds, got, tt.want)
		}
	}
}

func TestParagraphs(t *testing.T) {
	segs := []Segment{
		{Speaker: "Person 1 (Ana)", Text: "So the plan", StartTime: 0, EndTime: 2},
		{Speaker: "Person 1 (Ana)", Text: "is to ship it.", StartTime: 2.6, EndTime: 4},
		{Speaker: "You", Text: "Okay.", StartTime: 4.2, EndTime: 4.6},
		{Speaker: "Person 1 (Ana)", Text: "On Friday.", StartTime: 5, EndTime: 6},
		{Speaker: "Person 1 (Ana)", Text: "  ", StartTime: 6.5, EndTime: 7},
	}
	ps := Paragraphs(segs)
	if len(ps) != 3 || ps[0].Text != "So the plan is to ship it." || ps[0].EndTime != 4 || ps[2].Text != "On Friday." {
		t.Errorf("Paragraphs = %+v", ps)
	}
}

func TestUpsertSegmentRemoved(t *testing.T) {
	s := &Session{}
	s.UpsertSegment(Segment{ID: "a", Text: "yeah", Status: "live"})
	s.UpsertSegment(Segment{ID: "b", Text: "hello"})
	s.UpsertSegment(Segment{ID: "a", Status: StatusRemoved})
	s.UpsertSegment(Segment{ID: "c", Status: StatusRemoved})
	if len(s.Segments) != 1 || s.Segments[0].ID != "b" {
		t.Errorf("segments = %+v, want only b", s.Segments)
	}
}
