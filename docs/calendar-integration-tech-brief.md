# Tomoe PC — Calendar Integration & Config Expansion Tech Brief

**Repository:** `sosuke-ai/tomoe-pc`
**Status:** Draft
**Author:** Imran M Yousuf
**Related:** [`speech-to-text-tech-brief.md`](./speech-to-text-tech-brief.md)

## 1. Overview

This brief covers two related but independent workstreams:

**A. Config expansion.** Adopt `${VAR}` and `$(command)` interpolation in
`~/.config/tomoe/config.toml` so paths, secrets, and hooks can reference
the environment or shell — without switching config loaders.

**B. Calendar integration.** Enrich each recorded meeting session with a
linked calendar event (title, participants, meeting URL). The linkage
happens **after** the session is saved, so a calendar failure never blocks
a recording.

**v1 scope for calendar integration is ICS URL subscriptions only.**
OAuth-based providers (Google Calendar, Microsoft 365 / Outlook) are
deferred to a follow-up release once the ICS path proves the design.
Users get calendar enrichment by pasting a private ICS URL (available
from Google, Outlook, Fastmail, Apple iCloud, etc.) into `~/.config/
tomoe/config.toml` — no OAuth, no token refresh, no keyring, no client
registration. Optional LLM-based topic adjudication (via TypeSafe Jev)
strengthens matches when heuristic signals alone are ambiguous.

The public `calendar.Enricher` interface (§6.3.1) means external
projects that embed Tomoe can plug in their own calendar resolver
without depending on Tomoe's internal implementations.

Both workstreams are additive. Tomoe today is stable and this brief is
written to keep it that way — every design decision below is filtered
through a regression-risk lens.

## 2. Background & correction on "Jev"

The user's request referenced adopting "Jev" configuration and `${}`/`$()`
expansion "from CodeEagle". Research on the CodeEagle `feature/meetings`
branch at `/media/files/projects/gopath/src/github.com/imyousuf/CodeEagle`
found two separate things sharing the name:

1. **TypeSafe Jev** (`github.com/imyousuf/CodeEagle/pkg/jev`) — a Go SDK for
   the TypeSafe Jev *decision/evaluation* API (Noul/Choice/Score primitives
   over `POST https://api.typesafe.ai/v1/systemone`). CodeEagle uses it for
   LLM-judge speaker adjudication in transcripts. It is **not** a config
   library.
2. **CodeEagle's own config loader** — `internal/config/config.go` uses
   **Viper** (YAML) for parsing, plus a bespoke `internal/config/expand.go`
   (237 LOC, stdlib only) that walks the unmarshaled struct with reflection
   and rewrites string fields with `${VAR}` / `$(cmd)` semantics. This is the
   piece worth adopting.

**Implication for Tomoe:**

- Do **not** switch config loaders. Migrating from `pelletier/go-toml/v2` to
  Viper would pull ~15 transitive deps, change case-folding semantics, and
  invite regressions across every existing `[section]`. There is no
  cross-format need — Tomoe has one config file in one format.
- Do **not** pull in `pkg/jev` for configuration. It is unrelated.
- **Do** port CodeEagle's `expand.go` (with `mapstructure` → `toml` tag
  rewiring) as a post-unmarshal pass in `internal/config/`. Zero new deps.

## 3. Non-goals

- Real-time speaker → attendee identity mapping. Tomoe's speaker separation
  is voice-cluster-based (3D-Speaker embeddings), not identity-based. The
  best we can do today is display participants alongside the transcript and
  offer manual assignment later.
- A generic multi-provider abstraction across every possible calendar
  system. The public seam is a single `Enricher` interface (see §6.3.1) —
  external projects that embed Tomoe supply their own implementation if
  they want a different provider or a different matching policy. Inside
  Tomoe we ship one default enricher that composes the built-in ICS
  provider (v1); OAuth providers are additive later without breaking the
  interface. We do not build a full provider registry, plugin loader, or
  IoC container.
- OAuth-based providers (Google Calendar, Microsoft Graph) — **descoped
  for v1** per project owner. Design is preserved for a future phase (§8
  Phase 4); the public interface will accommodate them without changes.
- Background calendar sync. Fetch on-demand at enrichment time with a
  short TTL cache; every background loop is a regression cliff.
- Pre-start enrichment (blocking `StartSession` on a calendar fetch).
  Auto meeting detection is a hot path and must not gain network I/O.

## 4. Regression posture

Tomoe's stability boundary is the live pipeline: audio capture →
StreamCapturer → VAD → transcribe → segments → session save. Nothing in
either workstream may touch that path.

Approved change sites, ranked by risk (all Low unless flagged):

| Site | File(s) | Risk |
|---|---|---|
| Config post-unmarshal expansion pass (Phase 0) | new `internal/config/expand.go`; ~5 lines in `config.go` | Low |
| New **public** `calendar/` package (interfaces + shared types only) (Phase 1) | new package at repo root | Low |
| `Session` gains optional `CalendarEvent` pointer typed as `*calendar.Event` (Phase 1) | `internal/session/session.go` | Low |
| Post-diarize enrichment hook in `persistSession` (Phase 1) | `internal/backend/app.go` (between diarize and `session:saved`) | Low |
| New concrete `internal/calendar/` package (default enricher + ICS provider + matcher) (Phase 1) | new package | Low |
| `App.SetCalendar(calendar.Enricher)` wire-up seam (Phase 1) | `internal/backend/app.go` | Low |
| `[calendar]` config section (Phase 1) | `internal/config/config.go` | Low |
| Frontend chip in `SessionList.tsx` (Phase 1) | `frontend/src/components/SessionList.tsx` | Low |
| Widen `MeetingEvent` with `WindowTitle` (Phase 1) | `internal/meeting/detect.go` + two consumers | Low |
| Optional Jev adjudicator (`internal/calendar/jev.go`) (Phase 2) | new file, opt-in via `[calendar.jev].enabled` | Low |
| Wayland window-title backends (Phase 3) | new package under `internal/meeting/windowtitle/` | Low–Med (compositor variety) |
| Pre-start enrichment | (deferred, not in scope) | **High — do not do** |
| OAuth providers, keyring store, settings pane (Phase 4) | (deferred, not in this workstream) | — |

## 5. Part A — Config expansion

### 5.1 Semantics

Adopted verbatim from CodeEagle `internal/config/expand.go`:

- `${NAME}` — resolves against `os.LookupEnv`; empty string if unset.
- `${NAME:-fallback}` — literal fallback if the var is unset or empty. The
  fallback is not itself re-expanded.
- `$(command)` — runs via `sh -c`, 30-second timeout, trimmed stdout,
  `stderr` discarded (so accidental credential leakage doesn't reach logs).
- `$$` — literal `$`.
- Non-nested — the regex explicitly forbids `)` inside `$(...)`.

### 5.2 Where it runs

In `internal/config/config.go`, after `toml.Unmarshal(data, cfg)` and before
`Load` returns, call `expandConfig(cfg)`. The walker uses reflection to visit
every exported `string` field on the config tree; slices and maps of strings
are handled the same way. Unexported fields are skipped.

**Tag rewiring:** CodeEagle keys its walker off `mapstructure` tags; Tomoe's
config uses `toml`. This is a single-line change when porting.

### 5.3 Fields that benefit immediately

| Config key | Current form | With expansion |
|---|---|---|
| `transcription.model_path` | `'~/.local/share/tomoe/models'` (hand-expanded in `ModelDir()`) | `'${HOME}/.local/share/tomoe/models'` |
| `transcription.hotwords_file` | absolute path | `'${XDG_CONFIG_HOME:-${HOME}/.config}/tomoe/hotwords.txt'` |
| `meeting.monitor_device` | fixed name | `'$(pactl get-default-sink).monitor'` |
| `calendar.jev.api_key` (Phase 2) | fixed string | `'$(pass show tomoe/jev-api-key)'` |
| Future post-meeting webhook URL / CLI hook | — | `'$(pass show tomoe/webhook)'` |

Replace the ad-hoc `~/` expansion currently in `ModelDir()` with a
`${HOME}` default in `DefaultConfig()`; the new pass then handles it
uniformly.

### 5.4 Security & attribution

- `$(...)` runs arbitrary shell as the user. This is fine for a
  user-owned config at `~/.config/tomoe/config.toml`. Do not enable
  expansion on any config path that could be attacker-influenced
  (currently none; noted for future guarding if that changes).
- Never re-serialize expanded values. If Tomoe grows a settings-editor
  path that writes the config back to disk, port CodeEagle's
  `unexpandForWrite` mechanism (`expand.go:138-159`) at the same time to
  preserve the `$(...)` source in the file.
- **Attribution.** The port carries a header comment satisfying
  Apache-2.0 §4(a):

  ```go
  // expand.go implements $-expansion for TOML config values.
  //
  // Adapted from CodeEagle's internal/config/expand.go
  // (github.com/imyousuf/CodeEagle, Apache-2.0).
  // Rewired for pelletier/go-toml `toml` struct tags in place of
  // mapstructure tags. Original behavior preserved.

  package config
  ```

  No separate NOTICE file — the header comment is the whole
  attribution record. GPLv3 (Tomoe) accepts Apache-2.0 code
  incorporation.

### 5.5 Compatibility

- Users who never type `$` in their config see no behavioral change.
- Users who had literal `${` in a value will observe the new expansion.
  The `$$` escape handles this; call it out in the changelog.
- Zero new dependencies; ~230 LOC lifted from CodeEagle with tag rename;
  unit tests ported alongside.

## 6. Part B — Calendar integration

### 6.1 Data model

Public event and participant types live in the new top-level `calendar/`
package (`calendar/event.go`) so external embedders can construct them
directly from their own implementations:

```go
package calendar

type Event struct {
    Source           string        // "google" | "outlook" | "ical" | custom
    Provider         string        // e.g. "Google Calendar", "Fastmail ICS"
    EventID          string        // provider-specific
    Title            string
    Organizer        Participant
    Participants     []Participant // may be truncated for ICS
    ParticipantCount int           // authoritative even when Participants is truncated
    StartTime        time.Time
    EndTime          time.Time
    MeetingURL       string        // Meet/Teams/Zoom join link if known
    MatchedAt        time.Time     // when enrichment ran
    MatchScore       int           // 0–100, for debugging
}

type Participant struct {
    Name           string
    Email          string
    ResponseStatus string  // "accepted" | "declined" | "tentative" | "needsAction" | ""
    IsOrganizer    bool
}
```

`Session` (`internal/session/session.go`) gains one optional pointer field
referencing the public type:

```go
import "github.com/sosuke-ai/tomoe-pc/calendar"

type Session struct {
    // ... existing fields ...
    CalendarEvent *calendar.Event `json:"calendar_event,omitempty"`
}
```

This is the only place an `internal/` package imports the public
`calendar/` package. The dependency direction is one-way — the public
package has no reverse dependency on Tomoe internals — so no import cycle
risk.

**Backward-compatibility:** `store.Load` uses default `json.Unmarshal`, so
older `session.json` files without `calendar_event` unmarshal cleanly, and
older readers ignore the new key.

### 6.2 Config surface

New TOML section under `[calendar]`. **v1 is ICS-only** — OAuth-based
providers (Google, Microsoft) are deferred to a future release (see §8).

```toml
[calendar]
enabled = false
providers = []                       # v1 only supports ["ical"];
                                     # if empty AND [[calendar.ical]] entries exist,
                                     # "ical" is auto-included (§6.2.1)
match_start_window_minutes = 15      # tight window for session-start ↔ event-start
match_end_window_bound = false       # false = unbounded (meetings run long); true = cap at event.EndTime + 15m
match_score_threshold = 40           # events below this score are "no match"
cache_ttl_seconds = 300

[calendar.jev]
enabled = false                      # opt-in; when on, boosts match confidence via topic adjudication
api_key = ''                         # e.g. '$(pass show tomoe/jev-api-key)' via §5 config expansion
model = ''                           # TypeSafe Jev model id
topic_weight = 40                    # points contributed to the match score
transcript_context_seconds = 120     # how much of the transcript's first minutes Jev sees

[[calendar.ical]]
name = 'Personal'
url = ''                             # webcal:// or https:// ICS feed
# users may add multiple [[calendar.ical]] blocks (work, personal, shared team calendar, etc.)
```

Defaults in `DefaultConfig()` set `enabled=false` — if the user does
nothing, nothing changes.

### 6.2.1 ICS auto-enable

When `calendar.enabled=true`, `calendar.providers=[]`, and one or more
`[[calendar.ical]]` blocks are present, the loader treats providers as
`["ical"]`. Users who only want ICS don't have to fill in a redundant
second knob. Future OAuth providers (Google, Microsoft) will require an
explicit entry in `providers` — no auto-enable for flows that need setup.

### 6.3 Package layout

```
calendar/                          # PUBLIC — importable by external projects
├── calendar.go                    # Enricher interface, MatchInput, small helpers
├── event.go                       # Event, Participant (see §6.1)
└── doc.go                         # Package docs + embedding example

internal/calendar/                 # concrete, non-exported implementations
├── enricher.go                    # DefaultEnricher — composes providers + matcher
├── provider.go                    # internal Provider interface (list events over a window)
├── match.go                       # Scoring heuristic
├── jev.go                         # Optional Jev topic adjudicator (behind [calendar.jev].enabled)
├── cache.go                       # In-memory TTL cache
└── ical/                          # ICS provider (arran4/golang-ical) — v1's only provider
```

OAuth providers (`internal/calendar/google/`, `internal/calendar/microsoft/`)
and the associated `internal/calendar/tokens/` keyring store are **out of
scope for v1** and will land in a later release. The public `Enricher`
interface is unchanged when they do.

The public `calendar/` package (repo root) is a deliberate exception to
the project convention documented in `CLAUDE.md` — "Use `internal/` for all
non-main packages — nothing is exported outside the module." That
convention gets amended in the Phase 1 PR to note the exception. The
public surface is intentionally small: interface + two data types + docs.

**Internal `Provider` interface** — used by `DefaultEnricher` to compose
the three built-in providers; not exported:

```go
package calendar // internal/calendar

type Provider interface {
    Name() string
    ListEvents(ctx context.Context, from, to time.Time) ([]calendar.Event, error)
}
```

### 6.3.1 Public API for embedders

The public seam is a single interface in the top-level `calendar/`
package:

```go
package calendar

// Enricher resolves a Tomoe session to a calendar event, or returns nil
// when no match is found. Implementations are expected to be safe for
// concurrent use.
type Enricher interface {
    Enrich(ctx context.Context, in MatchInput) (*Event, error)
}

// MatchInput is the minimal, stable set of facts about a just-recorded
// session that Tomoe hands to the enricher. Additional fields may be
// added over time; existing fields will not change semantics.
type MatchInput struct {
    SessionID     string
    StartTime     time.Time
    EndTime       time.Time
    Platform      string   // "teams", "meet", "zoom", "webex", "slack", ""
    MeetingURL    string   // extracted from window title when available; may be ""
    WindowTitle   string   // raw, when captured; may be ""
}
```

The Wails backend accepts an `Enricher` at construction time:

```go
// internal/backend/app.go
func (a *App) SetCalendar(e calendar.Enricher) { a.calendar = e }
```

`NewApp(...)` continues to work with no calendar — the field is nil-safe.
The `tomoe` and `tomoe-gui` binaries wire in the default enricher
(`internal/calendar.NewDefaultEnricher(cfg)`) when `calendar.enabled` is
true. An external project that vendors or forks Tomoe replaces this one
line with its own `Enricher` and inherits everything else — matching
policy, storage schema, frontend chip, session lifecycle — unchanged.

**Stability contract** for the public package (documented in `doc.go`):

- Field additions to `Event`, `Participant`, and `MatchInput` are
  non-breaking; renames and removals are breaking and require a major
  version.
- The `Enricher.Enrich` signature is frozen at v1.
- `Source` is a free-form string; embedders may define their own values
  (e.g., `"internal-directory"`, `"exchange-on-prem"`) beyond the three
  built-ins Tomoe ships.
- Errors from `Enrich` are logged and treated as "no match" — never
  fatal to session save. Embedders can assume best-effort semantics.

An example embedder (in `doc.go`):

```go
type staticEnricher struct{ events []*calendar.Event }

func (s *staticEnricher) Enrich(ctx context.Context, in calendar.MatchInput) (*calendar.Event, error) {
    for _, e := range s.events {
        if !in.StartTime.Before(e.StartTime) && !in.StartTime.After(e.EndTime) {
            return e, nil
        }
    }
    return nil, nil
}
```

### 6.4 Providers — v1 scope

**v1 ships ICS URL subscription only.** Zero OAuth, no token refresh, no
revocation endpoint, no keyring dependency. One new provider dependency:
`github.com/arran4/golang-ical` (pure Go, MIT). Works with Google,
Microsoft 365, Apple iCloud, Fastmail — every provider that exposes a
private ICS URL. The user pastes their private ICS URL(s) into
`[[calendar.ical]]` blocks and enrichment starts working.

- Fields available per event: `SUMMARY`, `DTSTART`/`DTEND`,
  `DESCRIPTION` (usually contains meeting URL), `LOCATION` (Meet/Teams
  URLs often live here), `ORGANIZER`, `ATTENDEE` lines with `CN=`,
  `mailto:`, `PARTSTAT=`, `ROLE=`.
- Caveats: Google's ICS feed truncates attendee lists on large events;
  some providers strip `ATTENDEE` lines entirely for privacy. Handle by
  falling back to `ParticipantCount=len(known)` and noting truncation.
- Freshness: ICS is publisher-cached; 5–60 min lag on updates.
  Acceptable for post-meeting enrichment.
- Multi-source: multiple `[[calendar.ical]]` blocks (e.g., personal +
  work) are fetched in parallel; events from all sources are pooled and
  passed through the same matcher.

**Deferred to a future release:** Google Calendar OAuth (loopback +
PKCE), Microsoft Graph OAuth (loopback + PKCE), keyring-based token
storage, multi-account per OAuth provider. The public `Enricher`
interface and matcher stay unchanged when they land; only new
implementations of the internal `Provider` interface are added.

### 6.5 Matching heuristic

Inputs at enrichment time: `Session.CreatedAt` (start), `Session.EndedAt`
(end), `Session.Platform`, optionally `MeetingURL` and `WindowTitle`
extracted from the compositor (see §6.7), and optionally the transcript's
first `transcript_context_seconds` for Jev adjudication.

**Candidate window** (asymmetric):
- **Lower bound:** `event.StartTime - match_start_window_minutes` — the
  session must have started around when the event was meant to start.
- **Upper bound:** unbounded when `match_end_window_bound = false`
  (default). Meetings frequently run 30–90+ minutes long; a strict end
  bound throws away real matches. When `true`, `event.EndTime + 15m`
  is used as a safety cap.
- Only events whose **start time** falls within
  `[session.CreatedAt - match_start_window_minutes,
    session.CreatedAt + match_start_window_minutes]` are candidates.
  The upper time-window setting only constrains events that started long
  before the session (a 9 AM meeting shouldn't match a session started
  at 3 PM even if the 9 AM never had an explicit end).

**Scoring** (0–100):

| Signal | Weight | Rule |
|---|---|---|
| Start-time alignment | 30 | Full 30 if `\|session.CreatedAt - event.StartTime\| ≤ 2m`; linear falloff to 0 at `match_start_window_minutes`. |
| Meeting URL match | 20 | Exact match of URL extracted from window title against a URL substring in ICS `DESCRIPTION` or `LOCATION`. |
| Platform hint | 10 | ICS `LOCATION`/`DESCRIPTION` contains a URL host matching Tomoe's detected `Platform` (`meet.google.com` → meet, `teams.microsoft.com` → teams, etc.). |
| Jev topic adjudication | 40 (only when `calendar.jev.enabled=true`) | See §6.5.1. |

**With Jev disabled (default)**, max attainable score is 60. To
compensate, the effective threshold when Jev is off is `min(threshold,
match_score_threshold)` — i.e., the same threshold applies, but 40+ is
still reachable via strong time alignment (30) + platform hint (10) or
URL (20).

**With Jev enabled**, the topic-adjudication signal is the dominant
correctness lever. A session with weak URL/platform signals (e.g., a
Wayland session where the compositor path failed, or a physical
in-person meeting captured by Tomoe) can still confidently attach when
Jev says the transcript topic matches the event title/description.

Score ≥ `match_score_threshold` → attach the event. Below → leave
`CalendarEvent == nil`. On ties: (a) higher Jev score wins if Jev ran;
(b) otherwise, the shortest scheduled event wins (30-min 1:1 beats 8-
hour focus block).

### 6.5.1 Jev topic adjudication

Optional signal added when `[calendar.jev].enabled = true`. Uses the
TypeSafe Jev SDK at `github.com/imyousuf/CodeEagle/pkg/jev` — the same
SDK CodeEagle uses for LLM-judge decisions in transcripts.

**When it runs:**
- Only after the transcript is available (post-diarize, in
  `persistSession`).
- Only when the pre-Jev score is `≥ 20` but `< match_score_threshold`,
  or when multiple candidates score within 10 points of each other.
  Skips Jev when a candidate is already an obvious winner — saves cost
  and latency.

**Input to Jev:**
- Event title + description + attendee display names.
- First `transcript_context_seconds` of the transcript, flattened to
  plain text with speaker prefixes.

**Prompt:** a Choice primitive — "Does this transcript match this
calendar event?" with `{yes, no, ambiguous}`. Yes → +topic_weight
(default 40). Ambiguous → +topic_weight / 2. No → 0.

**Failure handling:** Jev API errors, timeouts, or missing config are
non-fatal — matcher proceeds with the pre-Jev score. Logged for
diagnostics.

**Cost/latency notes:**
- One Jev call per session (or per pair of ambiguous candidates).
- Latency budget: <2 s typical for a Choice primitive.
- Runs post-save on the serial saveWorker, off the UI path.
- Users who don't want the LLM call in the loop leave
  `[calendar.jev].enabled = false` and get pure heuristic matching.

**Dependency:** adds `github.com/imyousuf/CodeEagle/pkg/jev` to
`go.mod`. Pinned to `v0.1.0` per CodeEagle's `feature/meetings` branch.
Standing-rules exception: this dep is explicitly approved by the
project owner for calendar match adjudication (see §9).

### 6.6 Enrichment integration point

Injection site: `internal/backend/app.go`, inside `persistSession`, after
`session.RunDiarizeWithRetry(...)` completes and before
`events.Emit("session:saved", ...)`. This runs on the serial `saveWorker`
goroutine, off the UI/audio path.

Pseudocode:

```go
// after diarize
if a.calendar != nil {  // nil-safe: no calendar wired ⇒ skip enrichment
    in := calendar.MatchInput{
        SessionID:   sess.ID,
        StartTime:   sess.CreatedAt,
        EndTime:     sess.EndedAt,
        Platform:    sess.Platform,
        MeetingURL:  sess.MeetingURL,   // added in phase 4 via WindowTitle widening
        WindowTitle: sess.WindowTitle,
    }
    if ev, err := a.calendar.Enrich(ctx, in); err != nil {
        log.Printf("calendar enrichment failed: %v", err)  // warn-and-continue
    } else if ev != nil {
        sess.CalendarEvent = ev
        if err := a.store.Save(sess); err != nil {
            log.Printf("re-save with calendar event failed: %v", err)
        }
    }
}
events.Emit("session:saved", sess)
```

`a.calendar` is a `calendar.Enricher` (public interface), not a concrete
type. This is the seam. Failure is always non-fatal — mirrors the
diarize pipeline's warn-and-continue discipline. Frontend already refetches
on `session:saved`, so the new field surfaces without additional wiring.

### 6.7 Window-title capture (X11 and Wayland)

To enable URL-based matching for browser meetings (Meet, Teams web,
Webex web), widen `MeetingEvent` in `internal/meeting/detect.go`:

```go
type MeetingEvent struct {
    Type        MeetingEventType
    Platform    meeting.Platform
    WindowTitle string  // NEW — best-effort; may be empty on unsupported compositors
}
```

Populate from the existing `getWindowTitleByPID(pid)` on X11, and — new
for this workstream — from compositor-specific paths on Wayland. Two
consumers (`hotkey.go:100`, `daemon.go:242`) accept the widened struct
trivially. The URL is then extracted in the calendar package with a
simple regex against known join-URL patterns (Meet, Zoom, Teams,
Webex).

**Wayland compositor paths** — implemented behind a small
`windowtitle` interface with per-compositor backends:

| Compositor | Mechanism | Detection |
|---|---|---|
| wlroots (Sway, Hyprland, river, etc.) | `wlr-foreign-toplevel-management-unstable-v1` protocol via a Wayland client, or `swaymsg -t get_tree` / `hyprctl activewindow` shell-outs | Presence of `SWAYSOCK` / `HYPRLAND_INSTANCE_SIGNATURE` env vars |
| GNOME (Mutter) | D-Bus: `org.gnome.Shell` (via a small extension) or `org.gnome.Shell.Introspect` where enabled | `GNOME_DESKTOP_SESSION_ID` or `XDG_CURRENT_DESKTOP=GNOME` |
| KDE (KWin) | KWin scripting via `qdbus org.kde.KWin /KWin activeWindow` | `KDE_FULL_SESSION` or `XDG_CURRENT_DESKTOP=KDE` |
| Unknown / other | Return "", downstream falls back to time+platform matching | fallback |

**Sequencing:**
- Ship X11 capture in the same PR as ICS provider (§8 Phase 1) — the
  code path already exists and is one field wider.
- Ship Wayland backends in a dedicated PR (§8 Phase 3), one commit per
  compositor family. Each backend is small, isolated, and testable in
  a VM without touching the audio pipeline.
- No backend is required for calendar to work — the matcher degrades
  gracefully to time + platform hint + Jev when `WindowTitle == ""`.
- No new hard dependencies — wlroots/GNOME/KDE paths shell out to the
  compositor's own CLI when available, and there's a tiny in-tree
  Wayland client fallback for wlroots (net/wayland-go or direct
  golang.org/x/sys/unix — TBD in Phase 3 design).

### 6.8 Sync cadence & cache

On-demand fetch at enrichment time with a 5-minute in-memory TTL cache
keyed by `(provider, from, to)` window. Warm cache serves subsequent
sessions in the same window without a network round-trip. No background
polling.

Rate-limit awareness:

- Google Calendar v3: 1,000,000 quota units/day, ~10 units per
  `events.list`. Effectively unbounded for a personal app.
- Microsoft Graph: 10,000 requests per 10 minutes per app per tenant.
- ICS: no rate limit; publisher-side CDN caches responses.

### 6.9 Secrets & security (v1)

v1 is ICS-only, so there is no OAuth token to store.

- **ICS URLs** live in `~/.config/tomoe/config.toml` under
  `[[calendar.ical]]`. The URL itself is the credential; treat the
  config file as sensitive (it already inherits ordinary user-file
  permissions). Users may inject via `$(pass show ...)` if the §5
  config expansion is enabled.
- **Jev API key** (when `[calendar.jev].enabled = true`) lives in
  `[calendar.jev].api_key`. Recommend
  `$(pass show tomoe/jev-api-key)` or `${TOMOE_JEV_API_KEY}` expansion
  so the key does not sit in the config file in plain text.
- No keyring dependency in v1. When OAuth providers are added later,
  `github.com/zalando/go-keyring` (Secret Service) will be the primary
  token store with a 0600 file fallback.

### 6.10 Frontend surface

- `frontend/src/types.ts`: add optional `calendar_event?: CalendarEvent`
  to the `Session` interface. Purely additive; no impact on existing
  consumers.
- `frontend/src/components/SessionList.tsx`: add a small chip next to
  the platform badge — `"📅 <title>"` when `calendar_event` is present —
  at the `session-title` row. Click opens a tooltip with organizer +
  attendee list (up to 5, then "+N more") and match score for
  diagnostics.
- `TranscriptPane.tsx`: no change in v1. Deferred: an optional header
  card showing the event summary.
- Wails events: no new events. Enrichment piggybacks on `session:saved`.
- Settings pane: not required for v1 — ICS URLs are edited in
  `~/.config/tomoe/config.toml` directly. Deferred to when OAuth
  providers land (they need connect/disconnect buttons; ICS URL list
  editor can piggyback then).

## 7. Testing plan

### 7.1 Config expansion

- Port CodeEagle's `expand_test.go` verbatim; adjust for `toml` tags.
- Unit tests for: `${VAR}` set/unset, `${VAR:-fallback}`, `$(cmd)` success
  and 30-second-timeout paths, `$$` escape, nested `${` inside `$(...)`,
  slice/map traversal.
- Integration test: load `testdata/config-with-expansion.toml`, assert
  post-load values.
- Round-trip: unmarshal → expand → verify unexpanded fields remain
  unchanged (nothing to do here unless write-back is added).

### 7.2 Calendar

- ICS: fixture-based tests against sample `.ics` files from each major
  provider. Cover truncated attendee lists and missing `ATTENDEE` lines.
- Matching: table-driven tests over (session-time, event-window, URL,
  platform) → expected score and match decision.
- **Public interface contract**: a mock `calendar.Enricher` implementation
  is wired via `App.SetCalendar(...)`, driven through a fake
  `persistSession` invocation, and asserted to observe (a) the right
  `MatchInput`, (b) its returned `*Event` landing on the session, and (c)
  errors being logged rather than propagated. This is the primary guard
  against future changes silently breaking embedders.
- Google/Microsoft providers: unit-test the request builder and response
  parser with recorded fixtures; live smoke test gated behind an
  environment variable, not run in CI.
- Token store: swap keyring for a mock; test file-fallback path when
  keyring unavailable.
- End-to-end: a manual QA script — record a real meeting, verify the
  chip appears with correct title + participants.

### 7.3 Regression coverage

- `make test` must pass unchanged on every PR.
- Add a boot-time smoke check: with `calendar.enabled=false`, verify
  no calendar code executes. This is the primary "did we break anything"
  guard.
- Session round-trip: save and reload a session both with and without a
  `CalendarEvent`; verify all fields survive.

## 8. Rollout & phasing

1. **Phase 0 — Config expansion.** Ship first, alone. Port CodeEagle's
   `expand.go` with `mapstructure`→`toml` tag rewire + Apache-2.0
   attribution header. Zero new deps, zero user-visible change unless a
   `$` appears in a value. One PR. **No calendar changes.**

2. **Phase 1 — Public `calendar/` package + data model + config surface +
   ICS provider + backend wire-up seam.** The public `Enricher`
   interface, `Event`, `Participant`, and `MatchInput` land here —
   freezing the embedder contract before any concrete provider ships.
   Also adds `App.SetCalendar(...)`, `Session.CalendarEvent`,
   `[calendar]` config section, the ICS provider (via
   `arran4/golang-ical`), the matcher (heuristic-only — no Jev yet),
   the X11 window-title path already available today, `MeetingEvent`
   widening with `WindowTitle`, and the session-list chip in the
   frontend. Ships behind `calendar.enabled=false` by default. One PR.
   Also amends the `CLAUDE.md` "internal/ for all non-main packages"
   convention to note the `calendar/` exception.

3. **Phase 2 — Jev topic adjudication.** Adds
   `github.com/imyousuf/CodeEagle/pkg/jev` dependency, `[calendar.jev]`
   config section, and the topic-adjudication signal in the matcher.
   Behind `[calendar.jev].enabled = false` by default. One PR.
   Independently revertible: turn the flag off and matching returns to
   pure heuristic.

4. **Phase 3 — Wayland compositor paths for window-title capture.** One
   commit per compositor family (wlroots, GNOME, KDE) under a single PR
   or split across small PRs. Each backend is isolated behind the
   `windowtitle` interface; failure to detect a compositor returns "" and
   the matcher degrades gracefully.

5. **Phase 4 (deferred) — OAuth providers.** Google Calendar (loopback +
   PKCE, direct HTTP, `x/oauth2`), Microsoft Graph (direct HTTP,
   `x/oauth2`), multi-account per provider (`[[calendar.google.accounts]]`
   / `[[calendar.microsoft.accounts]]` config blocks), keyring-based
   token storage (`zalando/go-keyring` with 0600 fallback), settings-pane
   UI for connect/disconnect. Not in scope for the current work; the
   public interface is designed so this drops in without changing the
   embedder contract.

Each phase is independently revertible. `calendar.enabled=false` is the
kill switch across all of them.

## 9. Decisions (resolved)

Recorded from the interactive review with the project owner:

| # | Question | Decision |
|---|---|---|
| 1 | Push workflow | Feature branch + PR per phase (no direct pushes to `main`). |
| 2 | Commit trailer | Drop `Co-Authored-By: Claude Opus 4.7` for this project; commits do not mention Claude/AI anywhere. |
| 3 | ICS auto-enable | Auto-enable ICS when `[[calendar.ical]]` entries exist and `providers=[]`. |
| 4 | Multi-account (OAuth) | Deferred to Phase 4; OAuth is out of scope for the initial workstream. |
| 5 | Wayland URL extraction | Build compositor-specific paths (wlroots, GNOME, KDE) in Phase 3. |
| 6 | Re-transcribe re-runs enrichment | Yes — `tomoe session re-transcribe <id>` also refreshes `CalendarEvent` when it is empty. |
| 7 | `expand.go` attribution | Header comment in the ported file (Apache-2.0 §4(a)); no separate NOTICE. |
| 8 | Google client_id delivery | Deferred with OAuth; when OAuth lands, delivered via Makefile `-ldflags -X`. |
| 9 | Attendee display cap | Up to 5 attendees, then "+N more". |
| 10 | Match window | Asymmetric — `±match_start_window_minutes` on start alignment, unbounded upper end by default (`match_end_window_bound = false`). Meetings can run 90+ minutes over. |
| 11 | Match score threshold | 40 (permissive; attaches when there is any reasonable signal). |
| 12 | Jev topic adjudication | Adopted as an optional signal (weight 40) when `[calendar.jev].enabled = true`; explicit standing-rules exception for the `github.com/imyousuf/CodeEagle/pkg/jev` dependency. |
| 13 | Standing rules | All four apply: no new deps beyond the approved list (now `arran4/golang-ical` + `imyousuf/CodeEagle/pkg/jev`); no files outside §4; no live-audio-pipeline changes; no new CLI subcommands without asking. |
| 14 | OAuth scope | **Descoped for v1** — Google and Microsoft OAuth providers move to Phase 4. |

## 10. Open questions

None blocking. Anything that arises during Phase 0/1/2/3 implementation
gets raised before touching the corresponding code.

## 11. References

- CodeEagle `internal/config/expand.go` (source of the expansion pass) —
  `/media/files/projects/gopath/src/github.com/imyousuf/CodeEagle` on
  `feature/meetings`. Apache-2.0.
- CodeEagle `pkg/jev/` (TypeSafe Jev SDK, used for topic adjudication in
  Phase 2) — same repo, its own module at `v0.1.0`.
- RFC 5545 — iCalendar object specification.
- `github.com/arran4/golang-ical` — Go ICS parser (MIT).
- Wayland foreign-toplevel-management protocol (`wlr-foreign-toplevel-
  management-unstable-v1`) — used for compositor-side window titles on
  wlroots-based compositors in Phase 3.
- Deferred (Phase 4): Google OAuth 2.0 for Mobile & Desktop Apps —
  loopback + PKCE; Microsoft identity platform — auth code flow with
  PKCE; `github.com/zalando/go-keyring` for Secret Service token
  storage.
- Tomoe touch points cited in this brief:
  - `internal/backend/app.go` — `StartSession`, `persistSession`,
    `saveWorker`.
  - `internal/session/session.go`, `store.go` — session record + I/O.
  - `internal/meeting/detect.go`, `platform_linux.go` — meeting
    detection + platform ID.
  - `internal/config/config.go` — TOML load path.
  - `frontend/src/components/SessionList.tsx`, `frontend/src/types.ts`
    — UI surface.
