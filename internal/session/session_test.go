package session

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestSessionJSONRoundTrip(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	sess := &Session{
		ID:        "test-123",
		Title:     "Team Standup",
		CreatedAt: now,
		EndedAt:   now.Add(30 * time.Minute),
		Duration:  1800.0,
		Sources:   []string{"mic", "monitor"},
		Segments: []Segment{
			{
				ID:        "seg-1",
				Speaker:   "You",
				Text:      "Good morning everyone.",
				StartTime: 0.0,
				EndTime:   2.5,
				Source:    "mic",
			},
			{
				ID:        "seg-2",
				Speaker:   "Person 1",
				Text:      "Hey, good morning.",
				StartTime: 3.0,
				EndTime:   4.5,
				Source:    "monitor",
			},
		},
		AudioPath: "/tmp/test/audio.m4a",
	}

	data, err := json.Marshal(sess)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	var loaded Session
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if loaded.ID != sess.ID {
		t.Errorf("ID = %q, want %q", loaded.ID, sess.ID)
	}
	if loaded.Title != sess.Title {
		t.Errorf("Title = %q, want %q", loaded.Title, sess.Title)
	}
	if !loaded.CreatedAt.Equal(sess.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", loaded.CreatedAt, sess.CreatedAt)
	}
	if loaded.Duration != sess.Duration {
		t.Errorf("Duration = %v, want %v", loaded.Duration, sess.Duration)
	}
	if len(loaded.Sources) != len(sess.Sources) {
		t.Fatalf("Sources len = %d, want %d", len(loaded.Sources), len(sess.Sources))
	}
	if len(loaded.Segments) != 2 {
		t.Fatalf("Segments len = %d, want 2", len(loaded.Segments))
	}
	if loaded.Segments[0].Speaker != "You" {
		t.Errorf("Segments[0].Speaker = %q, want %q", loaded.Segments[0].Speaker, "You")
	}
	if loaded.Segments[1].Speaker != "Person 1" {
		t.Errorf("Segments[1].Speaker = %q, want %q", loaded.Segments[1].Speaker, "Person 1")
	}
	if loaded.AudioPath != sess.AudioPath {
		t.Errorf("AudioPath = %q, want %q", loaded.AudioPath, sess.AudioPath)
	}
}

func TestSessionEmptySegments(t *testing.T) {
	sess := &Session{
		ID:        "empty-1",
		Title:     "Empty Session",
		CreatedAt: time.Now().Truncate(time.Second),
	}

	data, err := json.Marshal(sess)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	var loaded Session
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if loaded.Segments != nil {
		t.Errorf("Segments should be nil for empty session, got %v", loaded.Segments)
	}
}

func TestSessionPlatformRoundTrip(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	sess := &Session{
		ID:        "plat-1",
		Title:     "Teams Meeting 2026-03-13 14:30",
		Platform:  "Teams",
		CreatedAt: now,
		Duration:  600.0,
		Sources:   []string{"mic", "monitor"},
	}

	data, err := json.Marshal(sess)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	var loaded Session
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if loaded.Platform != "Teams" {
		t.Errorf("Platform = %q, want %q", loaded.Platform, "Teams")
	}

	// Verify platform appears in JSON
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("Unmarshal to map error: %v", err)
	}
	if _, ok := m["platform"]; !ok {
		t.Error("missing JSON key \"platform\"")
	}
}

func TestSessionBackwardCompatNoPlatform(t *testing.T) {
	// Old session JSON without platform field
	oldJSON := `{"id":"old-1","title":"Meeting","created_at":"2026-03-13T14:00:00Z","duration":300}`

	var loaded Session
	if err := json.Unmarshal([]byte(oldJSON), &loaded); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if loaded.Platform != "" {
		t.Errorf("Platform should be empty for old session, got %q", loaded.Platform)
	}
	if loaded.ID != "old-1" {
		t.Errorf("ID = %q, want %q", loaded.ID, "old-1")
	}
}

func TestSessionPlatformOmittedWhenEmpty(t *testing.T) {
	sess := &Session{
		ID:        "no-plat",
		Title:     "Meeting",
		CreatedAt: time.Now().Truncate(time.Second),
	}

	data, err := json.Marshal(sess)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("Unmarshal to map error: %v", err)
	}

	if _, ok := m["platform"]; ok {
		t.Error("platform key should be omitted when empty")
	}
}

func TestSegmentJSONFields(t *testing.T) {
	seg := Segment{
		ID:        "s1",
		Speaker:   "You",
		Text:      "Hello world",
		StartTime: 1.5,
		EndTime:   3.75,
		Source:    "mic",
	}

	data, err := json.Marshal(seg)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	// Verify JSON field names
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("Unmarshal to map error: %v", err)
	}

	expectedKeys := []string{"id", "speaker", "text", "start_time", "end_time", "source"}
	for _, key := range expectedKeys {
		if _, ok := m[key]; !ok {
			t.Errorf("missing JSON key %q", key)
		}
	}
}

func TestUpsertSegmentAppendsAndReplaces(t *testing.T) {
	s := &Session{}
	s.UpsertSegment(Segment{ID: "seg-1", Text: "hel", StartTime: 1, Status: "live"})
	s.UpsertSegment(Segment{ID: "seg-1", Text: "hello", StartTime: 1, Status: "pending"})
	s.UpsertSegment(Segment{ID: "seg-1", Text: "Hello.", StartTime: 1})

	if len(s.Segments) != 1 || s.Segments[0].Text != "Hello." || s.Segments[0].Status != "" {
		t.Fatalf("segments = %+v, want one final \"Hello.\"", s.Segments)
	}
}

func TestUpsertSegmentRevisionBeforeSegment(t *testing.T) {
	s := &Session{}
	// The final revision overtakes the segment's first "live" emission.
	s.UpsertSegment(Segment{ID: "seg-1", Text: "Hello.", StartTime: 1})
	s.UpsertSegment(Segment{ID: "seg-1", Text: "hel", StartTime: 1, Status: "live"})

	if len(s.Segments) != 1 || s.Segments[0].Text != "Hello." || s.Segments[0].Status != "" {
		t.Fatalf("segments = %+v, want the final text kept", s.Segments)
	}
}

func TestUpsertSegmentInsertsInStartTimeOrder(t *testing.T) {
	s := &Session{}
	s.UpsertSegment(Segment{ID: "a", StartTime: 1})
	s.UpsertSegment(Segment{ID: "c", StartTime: 5})
	s.UpsertSegment(Segment{ID: "b", StartTime: 3}) // e.g. emitted late, after pass 2
	s.UpsertSegment(Segment{ID: "d", StartTime: 5}) // ties keep arrival order

	var got []string
	for _, seg := range s.Segments {
		got = append(got, seg.ID)
	}
	if strings.Join(got, ",") != "a,b,c,d" {
		t.Errorf("order = %v, want a,b,c,d", got)
	}
}
