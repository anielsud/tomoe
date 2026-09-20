package sock

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sosuke-ai/tomoe-pc/calendar"
	"github.com/sosuke-ai/tomoe-pc/internal/config"
)

// -----------------------------------------------------------------------
// Constructor edge cases
// -----------------------------------------------------------------------

func TestNewDisabledReturnsNil(t *testing.T) {
	r, err := New(config.ParticipantResolverConfig{Enabled: false})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r != nil {
		t.Errorf("expected nil resolver when disabled, got %+v", r)
	}
}

func TestNewRequiresExactlyOneTransport(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.ParticipantResolverConfig
	}{
		{"neither", config.ParticipantResolverConfig{Enabled: true}},
		{"both", config.ParticipantResolverConfig{Enabled: true, SocketPath: "/x", URL: "http://y"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := New(c.cfg); err == nil {
				t.Errorf("expected error for %+v", c.cfg)
			}
		})
	}
}

func TestNewRejectsUnschemedURL(t *testing.T) {
	if _, err := New(config.ParticipantResolverConfig{Enabled: true, URL: "example.com/foo"}); err == nil {
		t.Error("expected error for URL without scheme")
	}
}

func TestNewDefaultsPathAndTimeout(t *testing.T) {
	r, err := New(config.ParticipantResolverConfig{Enabled: true, URL: "http://localhost:1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(r.requestURL, "/resolve") {
		t.Errorf("default path missing: %q", r.requestURL)
	}
	if r.client.Timeout != defaultTimeout {
		t.Errorf("timeout = %s, want %s", r.client.Timeout, defaultTimeout)
	}
}

// -----------------------------------------------------------------------
// TCP transport — happy paths and error paths
// -----------------------------------------------------------------------

func newTCPResolver(t *testing.T, handler http.HandlerFunc) (*Resolver, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	r, err := New(config.ParticipantResolverConfig{
		Enabled: true, URL: srv.URL, Path: "/resolve", TimeoutSeconds: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r, srv
}

func TestResolveReplacesParticipants(t *testing.T) {
	r, _ := newTCPResolver(t, func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", req.Method)
		}
		if req.URL.Path != "/resolve" {
			t.Errorf("path = %s, want /resolve", req.URL.Path)
		}
		if ct := req.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		// Echo the request payload back with rewritten participants.
		var in resolveRequest
		if err := json.NewDecoder(req.Body).Decode(&in); err != nil {
			t.Fatal(err)
		}
		if in.Event == nil || in.Event.Title != "Q3 review" {
			t.Errorf("unexpected inbound event: %+v", in.Event)
		}
		out := resolveResponse{
			Organizer: &calendar.Participant{Name: "Aniel Sharma", Email: "aniel@optimizely.com", IsOrganizer: true},
			Participants: &[]calendar.Participant{
				{Name: "Aniel Sharma", Email: "aniel@optimizely.com", IsOrganizer: true, ResponseStatus: "accepted"},
				{Name: "Imran Yousuf", Email: "imran.yousuf@optimizely.com", ResponseStatus: "accepted"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})

	ev := &calendar.Event{
		Title:     "Q3 review",
		Organizer: &calendar.Participant{Name: "Aniel"},
		Participants: []calendar.Participant{
			{Name: "Aniel"}, {Name: "Imran"},
		},
		ParticipantCount: 2,
	}
	if err := r.ResolveParticipants(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	if ev.Organizer == nil || ev.Organizer.Name != "Aniel Sharma" {
		t.Errorf("organizer not replaced: %+v", ev.Organizer)
	}
	if len(ev.Participants) != 2 || ev.Participants[1].Email != "imran.yousuf@optimizely.com" {
		t.Errorf("participants not replaced: %+v", ev.Participants)
	}
	if ev.ParticipantCount != 2 {
		t.Errorf("ParticipantCount = %d, want 2", ev.ParticipantCount)
	}
}

func TestResolveOmittedFieldsLeaveEventAlone(t *testing.T) {
	r, _ := newTCPResolver(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`)) // empty response
	})
	ev := &calendar.Event{
		Title:     "T",
		Organizer: &calendar.Participant{Name: "Aniel"},
		Participants: []calendar.Participant{
			{Name: "Aniel"}, {Name: "Imran"},
		},
		ParticipantCount: 2,
	}
	if err := r.ResolveParticipants(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	if ev.Organizer == nil || ev.Organizer.Name != "Aniel" {
		t.Errorf("empty response should leave organizer alone, got %+v", ev.Organizer)
	}
	if len(ev.Participants) != 2 {
		t.Errorf("empty response should leave participants alone, got %+v", ev.Participants)
	}
}

func TestResolveEmptyParticipantsArrayClears(t *testing.T) {
	// Empty array is distinct from omitted — treated as "no participants".
	r, _ := newTCPResolver(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"participants":[]}`))
	})
	ev := &calendar.Event{
		Participants:     []calendar.Participant{{Name: "A"}, {Name: "B"}},
		ParticipantCount: 2,
	}
	if err := r.ResolveParticipants(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	if len(ev.Participants) != 0 {
		t.Errorf("participants should be cleared, got %+v", ev.Participants)
	}
	if ev.ParticipantCount != 0 {
		t.Errorf("ParticipantCount = %d, want 0", ev.ParticipantCount)
	}
}

func TestResolveSendsAuthHeader(t *testing.T) {
	seen := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		seen = req.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	r, err := New(config.ParticipantResolverConfig{
		Enabled: true, URL: srv.URL, Path: "/x", AuthHeader: "Bearer secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ResolveParticipants(context.Background(), &calendar.Event{}); err != nil {
		t.Fatal(err)
	}
	if seen != "Bearer secret" {
		t.Errorf("Authorization = %q, want %q", seen, "Bearer secret")
	}
}

func TestResolveNon2xxReturnsError(t *testing.T) {
	r, _ := newTCPResolver(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad request from resolver", http.StatusBadRequest)
	})
	err := r.ResolveParticipants(context.Background(), &calendar.Event{})
	if err == nil {
		t.Fatal("expected error for 400")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error should mention status: %v", err)
	}
}

func TestResolveMalformedJSONReturnsError(t *testing.T) {
	r, _ := newTCPResolver(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{not valid`))
	})
	if err := r.ResolveParticipants(context.Background(), &calendar.Event{}); err == nil {
		t.Error("expected parse error")
	}
}

func TestResolveEmptyBodyIsOK(t *testing.T) {
	r, _ := newTCPResolver(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// no body written
	})
	ev := &calendar.Event{Title: "T", Participants: []calendar.Participant{{Name: "A"}}}
	if err := r.ResolveParticipants(context.Background(), ev); err != nil {
		t.Errorf("empty body should be OK, got %v", err)
	}
	if len(ev.Participants) != 1 {
		t.Errorf("participants should be untouched on empty body, got %+v", ev.Participants)
	}
}

func TestResolveTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	r, err := New(config.ParticipantResolverConfig{
		Enabled: true, URL: srv.URL, Path: "/x", TimeoutSeconds: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err = r.ResolveParticipants(context.Background(), &calendar.Event{})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if elapsed > 1500*time.Millisecond {
		t.Errorf("timeout not enforced promptly: %s", elapsed)
	}
}

func TestResolveNilEventNoop(t *testing.T) {
	r, _ := newTCPResolver(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("server should not be reached for nil event")
	})
	if err := r.ResolveParticipants(context.Background(), nil); err != nil {
		t.Errorf("nil event should be a no-op, got %v", err)
	}
}

func TestNilResolverNoop(t *testing.T) {
	var r *Resolver
	if err := r.ResolveParticipants(context.Background(), &calendar.Event{}); err != nil {
		t.Errorf("nil resolver should be a no-op, got %v", err)
	}
}

// -----------------------------------------------------------------------
// Unix domain socket transport
// -----------------------------------------------------------------------

func TestResolveOverUnixSocket(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "resolver.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	mux := http.NewServeMux()
	mux.HandleFunc("/resolve", func(w http.ResponseWriter, req *http.Request) {
		var in resolveRequest
		if err := json.NewDecoder(req.Body).Decode(&in); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		out := resolveResponse{
			Participants: &[]calendar.Participant{{Name: "canonical " + in.Event.Title}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	r, err := New(config.ParticipantResolverConfig{
		Enabled: true, SocketPath: sockPath, Path: "/resolve", TimeoutSeconds: 5,
	})
	if err != nil {
		t.Fatal(err)
	}

	ev := &calendar.Event{Title: "Q3", Participants: []calendar.Participant{{Name: "old"}}}
	if err := r.ResolveParticipants(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	if len(ev.Participants) != 1 || ev.Participants[0].Name != "canonical Q3" {
		t.Errorf("unix-socket resolve produced unexpected participants: %+v", ev.Participants)
	}
}

func TestResolveOverMissingUnixSocket(t *testing.T) {
	// Socket path exists as a directory placeholder but nothing is listening.
	sockPath := filepath.Join(t.TempDir(), "nope.sock")
	r, err := New(config.ParticipantResolverConfig{
		Enabled: true, SocketPath: sockPath, Path: "/resolve", TimeoutSeconds: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ResolveParticipants(context.Background(), &calendar.Event{}); err == nil {
		t.Error("expected connection error for missing socket")
	}
}

