package session

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// calendarFileName is the calendar event a companion app matched a
// session to, written into the session's folder. Tomoe only reads it: it
// never writes, changes or deletes it.
const calendarFileName = "calendar.json"

// calendarWriter is the only writer whose calendar.json is trusted.
const calendarWriter = "musubi"

// Bounds on what is read from calendar.json: the file is small, and the
// subject is shown as a one-line title.
const (
	maxCalendarFileBytes = 1 << 20
	maxCalendarSubject   = 300 // runes
)

// calendarFile is the part of calendar.json Tomoe uses. The file also
// carries the event's times, join link, organizer and invitees.
type calendarFile struct {
	Writer  string `json:"writer"`
	Subject string `json:"subject"`
}

// CalendarSubject is the subject of the calendar event the session with
// this id was matched to (its folder's calendar.json, written by the
// companion app), or "" when there is no such file, it is unreadable or
// from another writer, or its subject is empty.
func (s *Store) CalendarSubject(id string) string {
	if id == "" || id != filepath.Base(id) {
		return "" // a session ID is a single folder name
	}
	return readCalendarSubject(filepath.Join(s.baseDir, id))
}

// readCalendarSubject reads calendar.json in dir; see CalendarSubject.
func readCalendarSubject(dir string) string {
	f, err := os.Open(filepath.Join(dir, calendarFileName))
	if err != nil {
		return ""
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxCalendarFileBytes+1))
	if err != nil || len(data) > maxCalendarFileBytes {
		return ""
	}
	var c calendarFile
	if json.Unmarshal(data, &c) != nil || c.Writer != calendarWriter {
		return ""
	}
	subject := strings.Join(strings.Fields(c.Subject), " ") // one line
	if r := []rune(subject); len(r) > maxCalendarSubject {
		subject = strings.TrimSpace(string(r[:maxCalendarSubject]))
	}
	return subject
}
