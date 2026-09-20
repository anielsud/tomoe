package calendar

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sosuke-ai/tomoe-pc/calendar"
)

// CachedMatch is the ephemeral, per-session record persisted under
// $XDG_STATE_HOME/tomoe/calendar/<session-id>.json. It is NOT part of
// session.json — the durable session record is untouched by the calendar
// workstream. Deleting this cache is always safe; enrichment will re-run
// on the next session re-transcribe.
type CachedMatch struct {
	SessionID     string          `json:"session_id"`
	WindowTitle   string          `json:"window_title,omitempty"`
	MeetingURL    string          `json:"meeting_url,omitempty"`
	CalendarEvent *calendar.Event `json:"calendar_event,omitempty"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

// Store is a file-per-session cache of CachedMatch records. Not safe for
// concurrent writes to the same session id, but callers only write for the
// session they just persisted, so a race is not expected in practice.
type Store struct {
	dir string
}

// NewStore returns a Store that reads and writes under dir. dir is created
// on demand at Save time.
func NewStore(dir string) *Store {
	return &Store{dir: dir}
}

// Dir returns the directory the store writes into. Useful for tests and
// logging.
func (s *Store) Dir() string { return s.dir }

// Load returns the cached match for sessionID, or (nil, nil) when no cache
// entry exists. Returns an error only for unexpected I/O or JSON failures —
// a missing file is not an error.
func (s *Store) Load(sessionID string) (*CachedMatch, error) {
	if sessionID == "" {
		return nil, errors.New("calendar store: empty session id")
	}
	path := s.pathFor(sessionID)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("calendar store: read %s: %w", path, err)
	}
	var m CachedMatch
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("calendar store: parse %s: %w", path, err)
	}
	return &m, nil
}

// Save writes m to disk atomically via temp+rename. Creates the store
// directory on demand.
func (s *Store) Save(m *CachedMatch) error {
	if m == nil {
		return errors.New("calendar store: nil match")
	}
	if m.SessionID == "" {
		return errors.New("calendar store: empty session id")
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("calendar store: mkdir %s: %w", s.dir, err)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("calendar store: marshal: %w", err)
	}

	final := s.pathFor(m.SessionID)
	tmp, err := os.CreateTemp(s.dir, ".calendar-*.tmp")
	if err != nil {
		return fmt.Errorf("calendar store: temp file: %w", err)
	}
	tmpPath := tmp.Name()
	// Clean up the temp file if we fail before the rename.
	defer func() { _ = os.Remove(tmpPath) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("calendar store: write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("calendar store: close temp: %w", err)
	}
	if err := os.Rename(tmpPath, final); err != nil {
		return fmt.Errorf("calendar store: rename %s → %s: %w", tmpPath, final, err)
	}
	return nil
}

// Delete removes the cache entry for sessionID. Missing entries are not an
// error — Delete is idempotent.
func (s *Store) Delete(sessionID string) error {
	if sessionID == "" {
		return errors.New("calendar store: empty session id")
	}
	err := os.Remove(s.pathFor(sessionID))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("calendar store: remove: %w", err)
	}
	return nil
}

func (s *Store) pathFor(sessionID string) string {
	return filepath.Join(s.dir, sessionID+".json")
}

// DefaultStoreDir returns the standard cache location under
// $XDG_STATE_HOME/tomoe/calendar (or $HOME/.local/state/tomoe/calendar).
func DefaultStoreDir() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = os.Getenv("HOME")
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "tomoe", "calendar")
}
