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
	// from before this field existed implicitly means) or "pending":
	// Text is a fast, lower-fidelity realtime pass and a higher-quality
	// re-decode is still in flight. A later update carrying the same ID
	// and Status "" supersedes it. See internal/live's two-pass pipeline.
	Status string `json:"status,omitempty"`
}
