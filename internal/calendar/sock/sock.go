// Package sock implements a calendar.ParticipantResolver that speaks a small
// JSON protocol over HTTP — either a TCP endpoint or a Unix domain socket.
// It exists so anyone can write their own resolver in any language: the
// only thing that matters is the wire format described below. A likely
// first consumer is a small Optimizely-Mark shim that turns Mark's A2A
// calls into this protocol, but nothing in this package assumes Mark.
//
// # Wire protocol
//
// Request — HTTP POST to Path (default "/resolve") with
// Content-Type: application/json and body:
//
//	{
//	  "event": {
//	    "source":      "ical",
//	    "provider":    "Personal Google",
//	    "event_id":    "…",
//	    "title":       "Q3 review with Aniel",
//	    "organizer":   { "name": "Aniel", "email": "aniel@example.com", ... },
//	    "participants":[ { "name": "…", "email": "…", ... }, … ],
//	    "start_time":  "2026-09-20T10:00:00Z",
//	    "end_time":    "2026-09-20T10:30:00Z",
//	    "meeting_url": "https://meet.google.com/…"
//	  }
//	}
//
// Successful response — HTTP 2xx with Content-Type: application/json and
// body:
//
//	{
//	  "organizer":    { … } | null,   // optional; nil = keep existing
//	  "participants": [ … ]  | null    // optional; nil = keep existing;
//	                                    // empty array = "no participants"
//	}
//
// Any 2xx JSON body is accepted; unknown fields are ignored. Missing
// organizer / participants means "leave what the enricher gave us alone".
//
// Error response — any non-2xx status, or a JSON parse error, is treated as
// a resolver failure. The event is returned unchanged; the failure is
// logged upstream.
package sock

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/sosuke-ai/tomoe-pc/calendar"
	"github.com/sosuke-ai/tomoe-pc/internal/config"
)

// defaultTimeout applies when the config's TimeoutSeconds is unset.
const defaultTimeout = 10 * time.Second

// Resolver POSTs an event to a user-configured HTTP endpoint (TCP or
// Unix-socket) and applies the returned participant list to the event.
type Resolver struct {
	client *http.Client
	// requestURL is the URL passed to http.NewRequestWithContext. For a
	// Unix-socket transport the host is a placeholder ("tomoe-resolver")
	// consumed by the custom Transport; only the path matters there.
	requestURL string
	auth       string
}

// New builds a Resolver from ParticipantResolverConfig. Returns (nil, nil)
// when the config is disabled so callers can inline the result. Returns an
// error only for config that would fail unambiguously — e.g. both
// SocketPath and URL set, or neither set.
func New(cfg config.ParticipantResolverConfig) (*Resolver, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	path := cfg.Path
	if path == "" {
		path = "/resolve"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = defaultTimeout
	}

	hasSocket := cfg.SocketPath != ""
	hasURL := cfg.URL != ""
	if hasSocket == hasURL {
		if hasSocket {
			return nil, errors.New("sock: both socket_path and url set; pick one")
		}
		return nil, errors.New("sock: neither socket_path nor url set")
	}

	var (
		transport  http.RoundTripper
		requestURL string
	)
	if hasSocket {
		socketPath := cfg.SocketPath
		transport = &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
			},
		}
		// The host is a placeholder — the Transport dials the socket
		// regardless. Use a stable value so logs stay readable.
		requestURL = "http://tomoe-resolver" + path
	} else {
		base := strings.TrimRight(cfg.URL, "/")
		if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
			return nil, fmt.Errorf("sock: url must start with http:// or https://, got %q", cfg.URL)
		}
		transport = http.DefaultTransport
		requestURL = base + path
	}

	return &Resolver{
		client: &http.Client{
			Transport: transport,
			Timeout:   timeout,
		},
		requestURL: requestURL,
		auth:       cfg.AuthHeader,
	}, nil
}

// resolveRequest and resolveResponse are the wire types. Kept private —
// external servers only need to conform to the JSON shape documented at
// the top of this file.
type resolveRequest struct {
	Event *calendar.Event `json:"event"`
}

type resolveResponse struct {
	Organizer    *calendar.Participant  `json:"organizer,omitempty"`
	Participants *[]calendar.Participant `json:"participants,omitempty"`
}

// ResolveParticipants implements calendar.ParticipantResolver.
//
// A nil or already-nil-Event input is a no-op. Networking, HTTP, and JSON
// errors are returned to the caller (Tomoe's backend logs them and keeps
// the event unchanged); mutations to ev are only applied on a clean 2xx
// with a valid JSON body.
func (r *Resolver) ResolveParticipants(ctx context.Context, ev *calendar.Event) error {
	if r == nil || ev == nil {
		return nil
	}
	body, err := json.Marshal(resolveRequest{Event: ev})
	if err != nil {
		return fmt.Errorf("sock: marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.requestURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("sock: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if r.auth != "" {
		req.Header.Set("Authorization", r.auth)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("sock: POST: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Read a small tail of the body for diagnostics without leaking
		// arbitrary bytes into logs. 512 is generous for a status line.
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("sock: POST returned %s: %s", resp.Status, bytes.TrimSpace(snippet))
	}

	var parsed resolveResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		if errors.Is(err, io.EOF) {
			// Empty body is legal — means "no changes".
			return nil
		}
		return fmt.Errorf("sock: parse response: %w", err)
	}

	if parsed.Organizer != nil {
		ev.Organizer = parsed.Organizer
	}
	if parsed.Participants != nil {
		ev.Participants = *parsed.Participants
		ev.ParticipantCount = len(ev.Participants)
	}
	return nil
}

// Compile-time interface satisfaction.
var _ calendar.ParticipantResolver = (*Resolver)(nil)
