// Package livefeed serves the meeting transcript, as it is written, to
// other programs on the same machine (turned on by [meeting] live_feed).
//
// Protocol v1: a Unix domain socket at ~/.local/share/tomoe/live.sock,
// mode 0600 so only the user can connect, carrying newline-delimited JSON
// from server to client only. Every message has "v": 1 and a "type":
//
//	{"v":1,"type":"hello","app":"tomoe","version":"..."}
//	{"v":1,"type":"session","state":"started","session":{"id":"...","title":"...","platform":"Teams","created_at":"..."}}
//	{"v":1,"type":"segment","session_id":"...","segment":{...session.Segment...}}
//
// hello is first on every connection. A session's state is "started",
// "stopped" (recording ended; final decoding may still revise lines) or
// "saved" (the session file is complete). A segment message is sent for
// every new line and every revision of one, with the same segment id
// (revisions replace earlier versions); segment.status says how final it
// is: "live" (pass-1 draft while the person is still speaking), "pending"
// (finished, waiting for the final decode), "" (final) or "removed"
// (withdrawn). Speaker relabels arrive as revisions too. A client that
// connects while a session is recording gets hello, that session's
// "started" and every current line in order before anything live.
//
// The feed must never slow transcription down, so nothing here blocks the
// publisher: each client has a bounded queue, a publish only ever tries
// to add to it, and a client whose queue is full is disconnected (it
// reconnects and gets a fresh snapshot) rather than waited for. With no
// client connected a publish returns before even encoding the message.
package livefeed

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/sosuke-ai/tomoe-pc/internal/config"
	"github.com/sosuke-ai/tomoe-pc/internal/session"
)

// ProtocolVersion is every message's "v".
const ProtocolVersion = 1

// DefaultBuffer is how many messages a client may fall behind by before
// it's disconnected. A meeting produces a few messages a second (more
// with two-pass drafts and relabels), so this is minutes of slack for a
// client that is merely busy, while bounding what a stuck one can cost.
const DefaultBuffer = 1024

// Session states (the session message's "state").
const (
	// StateStarted: recording began.
	StateStarted = "started"
	// StateStopped: recording ended; final decoding may still revise
	// lines.
	StateStopped = "stopped"
	// StateSaved: the session file is complete.
	StateSaved = "saved"
)

// SocketPath is where the feed listens: live.sock in Tomoe's data
// directory (~/.local/share/tomoe/live.sock).
func SocketPath() string {
	return filepath.Join(config.DataDir(), "live.sock")
}

// SessionInfo identifies a session in session messages.
type SessionInfo struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Platform  string    `json:"platform"`
	CreatedAt time.Time `json:"created_at"`
}

// InfoFor is sess's SessionInfo.
func InfoFor(sess *session.Session) SessionInfo {
	return SessionInfo{ID: sess.ID, Title: sess.Title, Platform: sess.Platform, CreatedAt: sess.CreatedAt}
}

type helloMsg struct {
	V       int    `json:"v"`
	Type    string `json:"type"`
	App     string `json:"app"`
	Version string `json:"version"`
}

type sessionMsg struct {
	V       int         `json:"v"`
	Type    string      `json:"type"`
	State   string      `json:"state"`
	Session SessionInfo `json:"session"`
}

type segmentMsg struct {
	V         int             `json:"v"`
	Type      string          `json:"type"`
	SessionID string          `json:"session_id"`
	Segment   session.Segment `json:"segment"`
}

// Attach registers a newly connected client: sess is the session
// recording now (nil if none) and segments its lines in order. See
// SnapshotFunc.
type Attach func(sess *SessionInfo, segments []session.Segment)

// SnapshotFunc gives a new client the current state. It must call attach
// exactly once, while holding the same lock its owner holds when it
// publishes segments and session starts, so that every change lands
// either in the snapshot or after it in the client's queue -- never
// lost between the two, and never both. attach doesn't block (it encodes
// the snapshot and queues the client), and never calls back out.
type SnapshotFunc func(attach Attach)

// Options configure Listen.
type Options struct {
	// Version is the hello message's "version" (Tomoe's build version).
	Version string
	// Buffer is each client's queue length; DefaultBuffer if 0.
	Buffer int
	// Snapshot gives each new client the current state; nil sends only
	// hello.
	Snapshot SnapshotFunc
}

// Server is a running feed. Its publishing methods are safe to call on a
// nil *Server (they do nothing), so callers needn't check whether the
// feed is turned on.
type Server struct {
	ln   net.Listener
	path string
	opts Options

	mu      sync.Mutex
	clients map[*client]struct{}
	closed  bool

	wg sync.WaitGroup
}

type client struct {
	conn net.Conn
	// snapshot is written before anything queued in ch.
	snapshot []byte
	ch       chan []byte
}

// Listen starts serving at path (see SocketPath) with permissions 0600.
// A socket file left behind by a Tomoe that didn't shut down cleanly is
// replaced; one that is still being served (another Tomoe running) is an
// error, as is anything at path that isn't a socket.
func Listen(path string, opts Options) (*Server, error) {
	if opts.Buffer <= 0 {
		opts.Buffer = DefaultBuffer
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("live feed: %w", err)
	}
	if err := removeStale(path); err != nil {
		return nil, err
	}
	ln, err := listenPrivate(path)
	if err != nil {
		return nil, err
	}
	s := &Server{ln: ln, path: path, opts: opts, clients: map[*client]struct{}{}}
	s.wg.Add(1)
	go s.acceptLoop()
	return s, nil
}

// listenPrivate creates the socket in a fresh 0700 directory beside path,
// narrows it to 0600 there, and only then renames it into place, so it is
// never connectable by anyone else, even for an instant. (net.Listen takes
// no mode and creates the socket with the process umask; changing the umask
// would be process-wide and race every other goroutine creating files.)
func listenPrivate(path string) (net.Listener, error) {
	dir, err := os.MkdirTemp(filepath.Dir(path), ".lf-")
	if err != nil {
		return nil, fmt.Errorf("live feed: %w", err)
	}
	defer os.RemoveAll(dir)
	tmp := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", tmp)
	if err != nil {
		return nil, fmt.Errorf("live feed: %w", err)
	}
	// The listener would unlink its original name on Close; after the
	// rename that name is gone, so Close removes path itself.
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	if err := os.Chmod(tmp, 0o600); err != nil {
		ln.Close()
		return nil, fmt.Errorf("live feed: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		ln.Close()
		return nil, fmt.Errorf("live feed: %w", err)
	}
	return ln, nil
}

// removeStale removes a socket file at path that nothing is serving.
func removeStale(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("live feed: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("live feed: %s exists and isn't a socket", path)
	}
	if conn, err := net.DialTimeout("unix", path, 200*time.Millisecond); err == nil {
		conn.Close()
		return fmt.Errorf("live feed: %s is already being served (is another Tomoe running?)", path)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("live feed: removing stale socket: %w", err)
	}
	return nil
}

// Path is the socket's path.
func (s *Server) Path() string { return s.path }

func (s *Server) acceptLoop() {
	defer s.wg.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed || errors.Is(err, net.ErrClosed) {
				return
			}
			fmt.Printf("live feed: accept: %v\n", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		// The snapshot takes the owner's lock, which may be held for a
		// while (starting a recording holds it throughout), so it runs off
		// the accept loop.
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.attachClient(conn)
		}()
	}
}

// attachClient sends conn hello and the snapshot, then queues it for
// live messages.
func (s *Server) attachClient(conn net.Conn) {
	var c *client
	attach := func(sess *SessionInfo, segments []session.Segment) {
		if c != nil {
			return // attach called twice; the first one counts
		}
		buf := encode(helloMsg{V: ProtocolVersion, Type: "hello", App: "tomoe", Version: s.opts.Version})
		if sess != nil {
			buf = append(buf, encode(sessionMsg{V: ProtocolVersion, Type: "session", State: StateStarted, Session: *sess})...)
			for _, seg := range segments {
				buf = append(buf, encode(segmentMsg{V: ProtocolVersion, Type: "segment", SessionID: sess.ID, Segment: seg})...)
			}
		}
		c = &client{conn: conn, snapshot: buf, ch: make(chan []byte, s.opts.Buffer)}
		s.mu.Lock()
		if s.closed {
			c = nil
		} else {
			s.clients[c] = struct{}{}
		}
		s.mu.Unlock()
	}
	if s.opts.Snapshot != nil {
		s.opts.Snapshot(attach)
	}
	if c == nil && s.opts.Snapshot == nil {
		attach(nil, nil)
	}
	if c == nil { // closed meanwhile, or the snapshot never attached
		conn.Close()
		return
	}
	s.writeLoop(c)
}

// writeLoop writes c's snapshot and then its queue until the queue is
// closed (dropped or server closed) or a write fails (client gone).
func (s *Server) writeLoop(c *client) {
	defer c.conn.Close()
	if _, err := c.conn.Write(c.snapshot); err != nil {
		s.drop(c)
		return
	}
	c.snapshot = nil
	for b := range c.ch {
		if _, err := c.conn.Write(b); err != nil {
			s.drop(c)
			return
		}
	}
}

// drop removes c, closing its queue and connection (which also unblocks
// a write in progress). Safe to call more than once.
func (s *Server) drop(c *client) {
	s.mu.Lock()
	s.dropLocked(c)
	s.mu.Unlock()
}

func (s *Server) dropLocked(c *client) {
	if _, ok := s.clients[c]; !ok {
		return
	}
	delete(s.clients, c)
	close(c.ch)
	c.conn.Close()
}

// Session sends a session message (state is StateStarted, StateStopped
// or StateSaved).
func (s *Server) Session(state string, info SessionInfo) {
	if s == nil {
		return
	}
	s.publish(func() any {
		return sessionMsg{V: ProtocolVersion, Type: "session", State: state, Session: info}
	})
}

// Segment sends a new line or a revision of one.
func (s *Server) Segment(sessionID string, seg session.Segment) {
	if s == nil {
		return
	}
	s.publish(func() any {
		return segmentMsg{V: ProtocolVersion, Type: "segment", SessionID: sessionID, Segment: seg}
	})
}

// publish queues msg() for every client without blocking, disconnecting
// any client whose queue is full. msg is only built (and encoded) when
// someone is listening.
func (s *Server) publish(msg func() any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.clients) == 0 {
		return
	}
	b := encode(msg())
	if b == nil {
		return
	}
	for c := range s.clients {
		select {
		case c.ch <- b:
		default:
			fmt.Printf("live feed: a client fell %d messages behind; disconnecting it\n", cap(c.ch))
			s.dropLocked(c)
		}
	}
}

// Clients is the number of connected clients.
func (s *Server) Clients() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.clients)
}

// Close stops serving, disconnects every client and removes the socket
// file. It must not be called while holding the lock SnapshotFunc takes,
// since it waits for clients still being attached.
func (s *Server) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	for c := range s.clients {
		s.dropLocked(c)
	}
	s.mu.Unlock()
	err := s.ln.Close()
	s.wg.Wait()
	if rerr := os.Remove(s.path); rerr != nil && !errors.Is(rerr, os.ErrNotExist) && err == nil {
		err = rerr
	}
	return err
}

// encode is msg as one line of JSON, or nil if it can't be encoded (a
// NaN or infinite time in a segment is the only way): that message is
// skipped and logged rather than taking the app down from inside the
// transcript path.
func encode(msg any) []byte {
	b, err := json.Marshal(msg)
	if err != nil {
		fmt.Printf("live feed: skipping a message: %v\n", err)
		return nil
	}
	return append(b, '\n')
}
