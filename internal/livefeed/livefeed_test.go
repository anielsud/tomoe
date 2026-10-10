package livefeed

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sosuke-ai/tomoe-pc/internal/session"
)

// socketPath is a socket path in a fresh short temp dir: macOS limits
// Unix socket paths to about 104 bytes, which t.TempDir() can exceed.
func socketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "lf")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "live.sock")
}

func listen(t *testing.T, opts Options) *Server {
	t.Helper()
	s, err := Listen(socketPath(t), opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

type reader struct {
	t    *testing.T
	conn net.Conn
	sc   *bufio.Scanner
}

func dial(t *testing.T, s *Server) *reader {
	t.Helper()
	conn, err := net.Dial("unix", s.Path())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	return &reader{t: t, conn: conn, sc: sc}
}

// next is the next message, decoded loosely.
func (r *reader) next() map[string]any {
	r.t.Helper()
	r.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if !r.sc.Scan() {
		r.t.Fatalf("no message: %v", r.sc.Err())
	}
	var m map[string]any
	if err := json.Unmarshal(r.sc.Bytes(), &m); err != nil {
		r.t.Fatalf("bad message %q: %v", r.sc.Text(), err)
	}
	if m["v"] != float64(1) {
		r.t.Errorf("message %q lacks v=1", r.sc.Text())
	}
	return m
}

// describe is a message as "type[/state][:segment id:text]" for compact
// comparisons.
func describe(m map[string]any) string {
	d := m["type"].(string)
	if st, ok := m["state"].(string); ok {
		d += "/" + st
	}
	if seg, ok := m["segment"].(map[string]any); ok {
		d += ":" + seg["id"].(string) + ":" + seg["text"].(string)
	}
	return d
}

func waitClients(t *testing.T, s *Server, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for s.Clients() != n {
		if time.Now().After(deadline) {
			t.Fatalf("clients = %d, want %d", s.Clients(), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestHelloFirst(t *testing.T) {
	s := listen(t, Options{Version: "v-test"})
	m := dial(t, s).next()
	if m["type"] != "hello" || m["app"] != "tomoe" || m["version"] != "v-test" {
		t.Errorf("first message = %v, want hello from tomoe v-test", m)
	}
}

func TestSocketIsPrivate(t *testing.T) {
	s := listen(t, Options{})
	info, err := os.Stat(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("socket mode = %o, want 600", perm)
	}
}

// A client connecting mid-meeting gets hello, the session's start and
// its lines in order, then live messages; one connecting with nothing
// recording gets hello and then live messages only.
func TestSnapshotThenLive(t *testing.T) {
	var mu sync.Mutex // stands in for the app's lock
	var current *SessionInfo
	var lines []session.Segment
	s := listen(t, Options{Snapshot: func(attach Attach) {
		mu.Lock()
		defer mu.Unlock()
		attach(current, lines)
	}})

	idle := dial(t, s)
	if got := describe(idle.next()); got != "hello" {
		t.Fatalf("idle client first message = %s", got)
	}
	waitClients(t, s, 1)

	info := SessionInfo{ID: "s1", Title: "Test meeting", Platform: "Teams", CreatedAt: time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC)}
	mu.Lock()
	current = &info
	lines = []session.Segment{{ID: "seg-1", Text: "first line"}, {ID: "seg-2", Text: "second line", Status: "pending"}}
	s.Session(StateStarted, info)
	mu.Unlock()

	mid := dial(t, s)
	want := []string{"hello", "session/started", "segment:seg-1:first line", "segment:seg-2:second line"}
	for _, w := range want {
		if got := describe(mid.next()); got != w {
			t.Errorf("mid-meeting client got %s, want %s", got, w)
		}
	}
	waitClients(t, s, 2)

	s.Segment("s1", session.Segment{ID: "seg-3", Text: "third line"})
	if got := describe(mid.next()); got != "segment:seg-3:third line" {
		t.Errorf("mid-meeting client got %s after the snapshot", got)
	}
	for _, w := range []string{"session/started", "segment:seg-3:third line"} {
		if got := describe(idle.next()); got != w {
			t.Errorf("idle client got %s, want %s", got, w)
		}
	}
}

func TestSessionAndRevisions(t *testing.T) {
	s := listen(t, Options{})
	r := dial(t, s)
	r.next() // hello
	waitClients(t, s, 1)

	created := time.Date(2026, 1, 2, 9, 0, 0, 0, time.FixedZone("", -4*3600))
	info := SessionInfo{ID: "s1", Title: "Test meeting", Platform: "Zoom", CreatedAt: created}
	s.Session(StateStarted, info)
	s.Segment("s1", session.Segment{ID: "seg-1", Speaker: "Person 1", Text: "draft words", Status: "live"})
	s.Segment("s1", session.Segment{ID: "seg-1", Speaker: "Person 1", Text: "Draft words.", Status: "pending"})
	s.Segment("s1", session.Segment{ID: "seg-1", Speaker: "Person 1", Text: "Final words."})
	s.Segment("s1", session.Segment{ID: "seg-1", Speaker: "Alex", Text: "Final words."})
	s.Session(StateStopped, info)
	s.Session(StateSaved, info)

	m := r.next()
	sess := m["session"].(map[string]any)
	if describe(m) != "session/started" || sess["id"] != "s1" || sess["title"] != "Test meeting" || sess["platform"] != "Zoom" || sess["created_at"] != "2026-01-02T09:00:00-04:00" {
		t.Errorf("start message = %v", m)
	}
	wantStatus := []string{"live", "pending", "", ""}
	wantSpeaker := []string{"Person 1", "Person 1", "Person 1", "Alex"}
	for i := range wantStatus {
		m := r.next()
		seg := m["segment"].(map[string]any)
		status, _ := seg["status"].(string)
		if m["type"] != "segment" || m["session_id"] != "s1" || seg["id"] != "seg-1" || status != wantStatus[i] || seg["speaker"] != wantSpeaker[i] {
			t.Errorf("revision %d = %v, want status %q speaker %q", i, m, wantStatus[i], wantSpeaker[i])
		}
	}
	for _, w := range []string{"session/stopped", "session/saved"} {
		if got := describe(r.next()); got != w {
			t.Errorf("got %s, want %s", got, w)
		}
	}
}

// A client that stops reading must be disconnected once its queue fills,
// without the publisher ever waiting on it; the feed keeps serving
// clients that connect afterwards (as the dropped one would, to get a
// fresh snapshot).
func TestSlowClientDisconnectedWithoutBlocking(t *testing.T) {
	s := listen(t, Options{Buffer: 8})
	stuck := dial(t, s) // never reads past hello
	stuck.next()
	waitClients(t, s, 1)

	// Far more than the kernel's socket buffer plus the queue can hold.
	text := strings.Repeat("invented words ", 200)
	start := time.Now()
	for i := 0; i < 5000; i++ {
		s.Segment("s1", session.Segment{ID: "seg", Text: text})
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("publishing took %v; a stuck client must not slow the publisher", took)
	}
	waitClients(t, s, 0)

	// The stuck client sees its connection end after what was buffered.
	stuck.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for stuck.sc.Scan() {
	}
	if err := stuck.sc.Err(); err != nil {
		t.Errorf("stuck client was not disconnected: %v", err)
	}

	again := dial(t, s)
	if got := describe(again.next()); got != "hello" {
		t.Fatalf("reconnecting client got %s first", got)
	}
	waitClients(t, s, 1)
	s.Segment("s1", session.Segment{ID: "seg-9", Text: "after reconnect"})
	if got := describe(again.next()); got != "segment:seg-9:after reconnect" {
		t.Errorf("reconnecting client got %s", got)
	}
}

func TestStaleSocketReplacedAndRemovedOnClose(t *testing.T) {
	path := socketPath(t)
	// A socket file nothing serves, as a crash leaves behind.
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stale socket not left behind: %v", err)
	}

	s, err := Listen(path, Options{})
	if err != nil {
		t.Fatalf("stale socket not replaced: %v", err)
	}
	if _, err := Listen(path, Options{}); err == nil {
		t.Error("a second server took over a socket still being served")
	}
	if err := s.Close(); err != nil {
		t.Error(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("socket still there after Close: %v", err)
	}
}

func TestRefusesNonSocket(t *testing.T) {
	path := socketPath(t)
	if err := os.WriteFile(path, []byte("not a socket"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Listen(path, Options{}); err == nil {
		t.Error("Listen replaced a regular file")
	}
}

func TestNilServerIsOff(t *testing.T) {
	var s *Server
	s.Session(StateStarted, SessionInfo{ID: "s1"})
	s.Segment("s1", session.Segment{ID: "seg-1"})
	if s.Clients() != 0 || s.Close() != nil {
		t.Error("nil server should do nothing")
	}
}
