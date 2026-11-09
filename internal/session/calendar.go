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
// carries the event's times and join link.
type calendarFile struct {
	Writer    string           `json:"writer"`
	Subject   string           `json:"subject"`
	Organizer calendarPerson   `json:"organizer"`
	Invitees  []calendarPerson `json:"invitees"`
}

// calendarPerson is the event's organizer or an invitee.
type calendarPerson struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// maxCalendarPeople bounds how many invitees are read.
const maxCalendarPeople = 2000

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

// readCalendar reads calendar.json in dir, or nil when there is none, it
// is unreadable or it is from another writer.
func readCalendar(dir string) *calendarFile {
	f, err := os.Open(filepath.Join(dir, calendarFileName))
	if err != nil {
		return nil
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxCalendarFileBytes+1))
	if err != nil || len(data) > maxCalendarFileBytes {
		return nil
	}
	var c calendarFile
	if json.Unmarshal(data, &c) != nil || c.Writer != calendarWriter {
		return nil
	}
	return &c
}

// ReadCalendarInvitees is the names of the people invited to the calendar
// event a session folder's calendar.json matched (the organizer and the
// invitees, each once, as one printable line), or nil when there is no
// such file, it is unreadable or from another writer.
func ReadCalendarInvitees(dir string) []string {
	c := readCalendar(dir)
	if c == nil {
		return nil
	}
	people := append([]calendarPerson{c.Organizer}, c.Invitees...)
	if len(people) > maxCalendarPeople {
		people = people[:maxCalendarPeople]
	}
	var names []string
	seen := map[string]bool{}
	for _, p := range people {
		name := cleanName(p.Name, maxTeamsSpeaker)
		if name == "" || seen[strings.ToLower(name)] {
			continue
		}
		seen[strings.ToLower(name)] = true
		names = append(names, name)
	}
	return names
}

// readCalendarSubject reads calendar.json in dir; see CalendarSubject.
func readCalendarSubject(dir string) string {
	c := readCalendar(dir)
	if c == nil {
		return ""
	}
	subject := strings.Join(strings.Fields(c.Subject), " ") // one line
	if r := []rune(subject); len(r) > maxCalendarSubject {
		subject = strings.TrimSpace(string(r[:maxCalendarSubject]))
	}
	return subject
}
