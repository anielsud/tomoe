# macOS video-hint speaker labeling

Covers `internal/videohint`: reading a meeting app's on-screen
active-speaker UI (a colored ring around whoever's talking, plus their
name label) and turning it into a naming hint for
`internal/speaker`'s audio-only clustering. See
[`macos-support.md`](macos-support.md) for overall status/roadmap and
[`macos-audio-capture.md`](macos-audio-capture.md) for the window
discovery/capture this builds on.

## Concept

Teams already knows who's talking: it rings their tile. That's a
second, independent signal next to the voice-based speaker pipeline.
`videohint.Watcher` captures the meeting window, finds the lit tile and
reads the name under it, and records every look with a timestamp. The
names then do two jobs:

- **Name speakers by vote.** Each read votes for whoever the diarization
  timeline has talking at that moment, so the diarization lag doesn't
  matter. A speaker is named only when the evidence agrees.
- **Correct the clustering.** Two stretches of speech confidently read
  as different people can't be one speaker; clusters whose reads agree
  on a name merge at a looser voice-similarity threshold.

A speaker with no agreeing reads stays "Person N". A name the user gives
in the transcript (click a speaker) beats any read.

```text
speech starts / voice change / ring moves ─┐
                                           ▼
Teams window → capture → call-chrome gate → rings → name(s) → Look (timestamped)
                                                                  │
                    session's looks.jsonl + thumbnail ◄───────────┤
                    hint timeline (live)              ◄───────────┤
                                                                  ▼
                         diarize.SessionDiarizer: votes by time → names
```

With `diarize_during_meeting = false` (the previous pipeline) a name
read goes to `speaker.Tracker.SetHintForRecent`, attaching to whichever
live speaker was heard most recently, as before.

## Ring detection (`ring.go`)

Pure Go, no cgo. Every pixel within `ColorTolerance` of the ring color
is marked, connected marked pixels form candidates, and a candidate is a
ring when it:

- covers `MinAreaFraction`–`MaxAreaFraction` of the frame;
- is mostly hollow (at least 30% of its bounding box empty);
- **is shaped like a border**: at least 90% of its pixels lie within a
  few pixels of its bounding box's edges, and it covers at least 70% of
  each of the four edges (rounded corners leave the ends empty). A
  ring-colored virtual background makes hollow-ish blobs that fail
  this. On the saved frames it removed every background fragment
  (galleries went from 8–13 candidates to the 1–3 real lit tiles).

**Teams calibration** (from a real, live, multi-participant call,
explicit authorization obtained first): active-speaker ring is
RGB(129,136,243), a hollow rounded-square border. `ColorTolerance: 25`
is deliberately generous to survive lighting/monitor variation.

**Several lit tiles are real.** Teams rings every tile making sound,
for example two conference rooms, or three people at once. Each lit
tile's name is read into `Look.Candidates`, and attribution resolves the
speaker by elimination (see Attribution).

**Speaker view has no ring.** In speaker view (and 1:1 calls) Teams
draws no ring: the main video *is* the active speaker, named at its
bottom left. When no ring is found and the stage's left margin isn't
Teams' flat dark gallery background (brightness 29), the watcher reads
that label (`speaker_view` looks). This covered every "no ring" frame
saved so far.

`frames_test.go` runs all of this over a folder of saved frames
(`TOMOE_HINT_FRAMES=<dir> go test ./internal/videohint -run
TestSavedFrames -v`); the frames show real meetings, so they stay local.

## Label geometry (`label.go`, `rule.go`'s `LabelRegion`)

**Model the label's position/size in absolute pixels relative to the
ring's bottom edge, not as a fraction of the ring's own size.** This
was the second attempt, not the first — worth keeping the story
because the wrong mental model is a natural one to reach for:

The original calibration modeled the label as a fraction of the ring's
bounding box (bottom 27% of ring height, full ring width), eyeballed
against one gallery-tile screenshot. It happened to look right for
that one tile size and was wrong everywhere else. Measuring two real
frames at very different scales — a full-screen 1-on-1 tile
(~1794x1026px ring) and a ~440x245px gallery tile — showed label
position/size differing by more than 3x in ring-relative *fraction*
terms, but matching within a few pixels in *absolute* offset from the
ring's bottom edge and absolute label height. That's exactly what
fixed-size font rendering predicts (Teams renders the name label at a
constant on-screen font size regardless of tile size) and a
fraction-of-ring model structurally can't express. Current values:
`BottomOffset: 52`, `Height: 40`, `MaxWidth: 300` (clamped to the
ring's own width if narrower, so a small tile's crop doesn't spill into
a neighboring tile). Re-running OCR against every real saved frame
that had a ring match: 0/7 succeeded under the fractional model, 7/7
succeed under the fixed-pixel model.

**Porting lesson:** when calibrating any "where does app X draw UI
element Y" rule from a single screenshot, get at least two real
examples at very different absolute scales before trusting a
ring/tile-*relative* model — fixed-font-size UI chrome won't scale
proportionally, and one example can't distinguish "scales with the
container" from "constant size, just measured at one particular
container size."

## OCR (`internal/ocr`, `videohint/ocr.go`)

Names are read with open models, the same on every platform: PaddleOCR
PP-OCRv5 text detection and English text recognition (Apache-2.0, in ONNX
form from RapidOCR v3.9.2, run through the ONNX Runtime Tomoe already
ships, `internal/onnxrt`). They replace Apple's Vision framework
(`VNRecognizeTextRequest`), so no part of name reading depends on macOS.
The models (4.8 MB and 7.9 MB) download with the others when video hints
are on (Tools, `tomoe init`, `tomoe model download`); each is checked
against its pinned SHA256 before it's kept.

Detection matters as much as recognition. A recognizer reads one tight
line of text; a label crop is a tile's bottom strip with video above it
and icons beside the name. Fed the crop directly the open recognizer
read 15% of names right, and tuned crop heuristics reached 95% on the
meetings they were tuned on but 27% on others (a speaker-view label sits
elsewhere). Detecting the text lines first and reading the widest one
(`recognizeName`) is layout-independent. Replayed on five recorded
sessions (gallery, speaker view, screen share, 1:1 audio and video
calls), it reads 327 of 329 labels right (99.4%; both misses were other
text, a "Voice isolation" caption and an icon) at 22-29 ms a label, where
Vision read 90.7% exactly on the same gallery labels at 17 ms. The 1:1
toolbar check reads the toolbar's button area (about 145 ms, at most
every 10 s while no ring is found) and identified every 1:1 call frame
with no meeting misread as one. Names with letters outside the English
model's set (accents) would need the Latin model, which read this
meeting's names slightly worse (it took an icon for an "l").

The earlier Vision bridge's two bugs (autoreleased objects in a `__block`
variable; a Go pointer held across the cgo boundary) went with it.

**Noise stripping.** Observed live: a real name OCR'd as "Devin
Dobrowolski Priv" — "Priv" (a truncated "Privacy") came from Teams'
background-blur/privacy indicator overlapping the label crop, not from
the name. `cleanOCRName` (`label.go`) strips a single trailing word
from the OCR result if it matches (or is a partial-word prefix of) a
small known-noise list (`privacy`, `muted`, `mute`, `recording`,
`live`) — matched only as the *last* word, since a real name is never
expected to end with one of these, and only ever strips one word, so
a genuinely two-word name is never touched. This is a text-level
patch, not a geometry recalibration — the actual overlay's on-screen
position hasn't been measured, so if a *different* trailing artifact
shows up it won't yet be caught; extend the list rather than assume
this is exhaustive.

## Call-chrome gate (`chrome.go`)

**`FindMeetingWindow`'s title heuristic isn't enough to confirm a
window is actually showing a live call.** Two real, distinct
wrong-window captures made it through live: a 1:1 chat conversation,
and the post-meeting recording/playback page — both Teams-owned, both
non-trivially titled, neither an actual call. One of them, worse than
just wasting a capture, produced a *plausible-looking wrong OCR read*:
The OCR happily read the page's own window-title text off screen and
returned it as if it were a recognized speaker's name.

Fixed with `DetectCallChrome`: look for the "Leave call" hang-up
icon's small red glyph within a fixed-position search window
(**absolute pixels from the frame's top-right corner, not a fraction
of frame size** — same reasoning as label geometry: this is native
toolbar chrome, and Teams renders it at a constant pixel size/position
regardless of window size). Calibrated against real saved frames:
exactly 110 matching pixels in every one of 15 real call frames checked
(across four different window sizes), 0 in all 7 non-call frames. The
watcher checks it before Ring/Label: a rejected frame is a
`not_a_call` look, shown in the timeline but never attributed.

**Matched by hue+saturation, not exact RGB** — a deliberate bet that a
semantic "danger/leave" accent color keeps its hue across light/dark
theme even if its brightness shifts, since theme changes mostly affect
background/surface luminance rather than accent hues in most native UI
toolkits. Untested against an actual light-mode capture — none exists
in this library yet.

**Calibration gotcha found live:** a first pass searched too tall a Y
window (20-95px from the top) and picked up a second, unrelated
reddish element (a notification-badge-shaped area at y~20-37) inside
the *same* chat-window false positive at a pixel count close enough to
the real icon's (120 vs. 110) to nearly defeat the gate. Tightening the
search window to y∈[44,63] (matching the real icon's actual measured
position) separated the two completely. Porting lesson: a
"pixel count within a search box" gate is only as good as how tightly
the box excludes *other* things that share the target color — measure
the false positive's exact location before assuming a wider net is
safer.

## Watching (`watcher.go`)

**Which window** (`video_hint_window`, also switchable live from the hint
timeline): by default the Teams meeting window, found by title as before;
`none` turns hints off; or any app's name, whose largest window is
captured. Video is independent of the audio source. An app without a
rule (only Teams has one) is still captured and every look recorded,
marked `no_rule`, so pointing the watcher at, say, Zoom collects frames
to write Zoom's rule from ("Save for analysis"); nothing is read from
them until then.

The watcher looks at the window on two clocks:

- **Learning**, every `video_hint_learn_interval` (default 0.35 s): while
  anyone who spoke in the last minute has no name
  (`SessionDiarizer.NeedsNames`), and for 3 s after any sign of a
  speaker change.
- **Checking**, every `video_hint_check_interval` (default 1 s)
  otherwise.

Speaker-change signals, fastest first: speech starting after at least
0.25 s of quiet (`live.Config.OnMonitorSpeechStart`), the ring moving to
another tile, and a new voice in the newest part of a diarization window
(`StreamConfig.OnSpeakerChange`, about 1 s, before any clustering). The
live pass's "this speaker has no name" signal also starts a burst.

A tile's name is remembered by its position. A ring on a known tile
reuses the name (`from_cache`) and is only read again after 1 s while
learning, or 5 s while checking. Measured per look on an Apple Silicon
desktop: ring detection 4 ms, thumbnail 2 ms, a full-resolution frame
21 ms (only when something changed), reading a name 16 ms (only when
needed), plus the capture.

## Hint timeline (`look.go`, frontend `HintTimeline.tsx`)

Every look is recorded, failures included: `looks.jsonl` plus a
320-pixel thumbnail per distinct look (`looks/<id>.jpg`) in the
session's folder. Looks that found the same thing within 10 s share a
thumbnail. It stays on this computer and goes with the session.

The timeline (camera button; "Hints" on a saved session) shows each look
with its ring outlined, the name read or why there wasn't one, and a
**Save for analysis** button. That copies the full-resolution frame
(kept in memory for the last ~90 s) or the thumbnail, plus the look's
details, to `~/.local/share/tomoe/hint-analysis/`: the folder
`frames_test.go` reads. There's no review or approval step any more.

The call-chrome gate (Leave button) still decides whether anything read
counts as a name; a look that fails it is shown but never attributed.

## Attribution (`internal/diarize/hints.go`)

Each read is a `Hint` (session time, name). `nameSpeakers`:

1. shifts each hint back 0.5 s (the ring lights after speech starts and
   lingers after it stops);
2. gives its vote to the one speaker the timeline has talking then.
   Hints during silence or overlapping speech don't vote;
3. counts reads of a name within 5 s as one, and names a speaker only
   with at least 2 independent reads and 60% of that speaker's reads;
4. folds spellings together: truncations ("Natalia Rami…") and one- or
   two-letter OCR misreads ("Ortlz") become the most-read spelling;
5. resolves several lit tiles by elimination: a speaker whose name is
   among them is confirmed, otherwise names other speakers already have
   are ruled out and a single remaining name votes. If any lit tile's
   name wasn't read, the hint doesn't vote.

Labels read "Person N (Name)"; a user rename shows the name alone. A
line not yet covered by the timeline shows the latest read during it,
provisionally ("Ana?").

Names only ever reach the diarizer's clusters through this vote. An
earlier version also let name reads split and merge the clusters; replayed
against Teams' own transcript it made speaker labels worse (stale reads
from a window Teams wasn't repainting split correct clusters, and with
clean reads it still gained nothing), so it was removed. The measurements
are in speaker-attribution-research.md.

## Tuning the numbers (`tomoe tune`)

The vote rules, the ring lag, the look rate and the fingerprint stride
are tuned from one recorded meeting:

1. Turn on **Record for tuning** (`record_for_tuning = true`) and record a
   real meeting. Hints look at the learning rate throughout and save every
   distinct full frame; diarization fingerprints every window. It costs
   more CPU and about 250 MB an hour. Turn it off afterwards.
2. Export the meeting's Teams transcript and review it, as for `tomoe eval`.
3. Run `tomoe tune <session-id> --ref <reviewed.txt>`.

Everything replays from what the session saved: its transcript with word
timings, its fingerprints (`diarization.gob`, with `diarization.json`
holding the start offset and settings) and its looks (`looks.jsonl`).
Sparser look rates and strides are simulated by thinning the recording,
which is why it's recorded at full rate. The reference's clock (Teams
counts from the meeting's start) is matched to the recording's
automatically from the text (`--ref-offset` overrides).

Without `--ref`, `tomoe tune` reports what can be measured without an
answer key: looks by result, per-look cost and CPU, the names read, the
ring's lag behind the voice (ring moves matched to timeline speaker
changes), how consistent each speaker's reads are, and how much a slower
look rate or higher stride changes the labels against every look at
stride 1. A raw Teams transcript export works as `--ref`: Teams labels
speakers from each person's own audio, so its speaker names are reliable.

With `--ref`, each setting is scored on speaker accuracy and on names: the share of
words labeled with the right person's name, a wrong name, or none, ranked
by right minus twice wrong. Each look records its cost (capture, rings,
name reading, encoding), so the hint layer's CPU share is reported for
each look rate. Every look also records the shape measurements of every
ring-colored region near the detection thresholds, and the saved full
frames feed `frames_test.go`, for tuning detection itself. Only final
labels are scored; how labels look while the meeting is still going is
`tomoe eval --online`'s job.

### Window inventory (Record for tuning)

The window rule in automatic mode is `teamsvideo.PickMeetingWindow`: the
frontmost Teams window whose title isn't empty, "Window" or a "Chat |"
panel. It never checks that the window holds a call, so a Calendar window
in front of the meeting would be picked (seen live: with no call going,
it picked "Calendar | … | Microsoft Teams"). With `record_for_tuning` on,
each session records enough to see which cases happen in real calls
(pop-out and compact windows, screen-share toolbars, calling from a chat):

- every look says which window it captured (`window_id`, the title in
  `window`) and `pick`: what the rule picked and what it passed over.
- `looks.jsonl` gets the full window list (`windows`: owner, title, pid,
  size, layer, front-to-back order) when the set of windows changes (at
  most every 5 s) and every 30 s.
- the watched window's full frame is kept for every look that looks
  different from the last one kept (see below), whatever was found in it.
- `windows/<look>-<window>.jpg` is a full-size picture of each other
  Teams window, looked at every second and kept when it's the first of
  that window, its size changed, it looks different, or 30 s have passed;
  `windows/<look>-<window>-thumb.jpg` is a 320 px thumbnail of up to 12
  other apps' windows, taken when the Teams windows change and every 30 s.
- "Looks different" means the mean brightness change per cell on a 48x27
  grid exceeds `keepDiff` (12, on 0-255), at most one picture a second.
  Calibrated on 181 frames from a real call: consecutive frames differ by
  a median of 6.3, p90 16, p99 26, max 111, so ordinary video motion
  stays mostly under it and a layout change does not. A size change
  always keeps. No dwell time is needed: a new view is kept on the next
  look (about 0.35 s). Pictures are saved whether or not a ring, name or
  call is found.

**First real call (2026-10-02) found:**

- The main call window captures cleanly, even when hidden behind another
  window. Occlusion is not a problem.
- Teams' **"Meeting compact view"**, a floating panel (window layer 19,
  about 690x250), is found at the right size but **captures entirely
  black**. It was labeled "not a call", and the rule kept watching it for
  78 s while the full call window sat behind it, so no names were read
  for that stretch. Cause, confirmed live in a second call: the window
  server lists its sharing state as 0 (the app asked not to be captured),
  while the call window is 1 (readable), so the black capture is Teams'
  choice, not a Tomoe bug, and ScreenCaptureKit would be expected to
  honor it too. The call window behind it still captures and is read.
- A Calendar or Chat window in front of the call is picked by the title
  rule and fails the Leave-button check ("not a call") until the call
  window is in front again; it happened at each click away from the call.

Automatic mode now tries the acceptable Teams windows frontmost first
(`teamsvideo.MeetingCandidates`) and takes the first that captures
something and shows the call controls (Leave button); `pick` says which
were passed over and why. A frame that is all black gets the stage
`blank_capture` instead of "not a call". Checked against that call's
frames: both compact-view frames are blank, the call window shows its
controls, and the Calendar window is readable without them.

**Stale hints from a window Teams isn't repainting (found by replaying
that call against Teams' transcript):** when the call window is hidden or
in the background (behind the compact view or another window), Teams
stops repainting its interface: the call timer, the speaker highlight and
the name labels freeze, while the video tiles keep moving. Two stretches
of the 82-minute call (10:12:58-10:18:54 and 10:26:30-10:30:55, 621 s in
all) had a timer that never changed, and in both the highlight sat on one
person while others spoke (Teams' transcript has three speakers in the
second one; the ring said the same name for every look). Those stale reads
were what the clustering constraints (since removed) split on; see
speaker-attribution-research.md. The watcher now hashes the timer region
at the toolbar's left edge each look; unchanged for 4 s means the
interface isn't repainting, the look is marked `ui_frozen` and no name is
read from it. Replayed over the call's saved frames it flags exactly those
two stretches plus two isolated frames. Keeping the call window visible
(even partly, on any display) avoids the freeze.

The region is in points and scaled by the capture's pixels per point (a
Retina capture is 2x), and a freeze only counts once the region has been
seen changing for that window and size: a region that isn't really the
timer (a layout or scale this rule doesn't know) never changes, and must
not read as a window that stopped repainting, which would silently stop
every name read. A window hidden from the very start of a meeting is
therefore only caught once it has repainted at least once.

**1:1 calls.** Calling someone (from a chat, say) opens a call window
titled with their name ("<name> | Microsoft Teams"), separate from the
chat window, which the window rule already skips ("Chat |"). Its layout
has no active-speaker ring: the other person is a round avatar (camera
off) or a full video, named at the bottom left, and the toolbar has
calling controls a meeting doesn't (Hold, Transfer, Dial pad, Consult).
When a look finds no ring and no speaker view, the toolbar is OCR'd (at
most every 10 s per window) and, if it has those controls, the look names
the other person from the stage label, else from the window title (stage
`one_on_one`). With one other person, every remote voice is theirs. On
the first recorded call from a chat every look had been "no ring" (no name
at all); replayed, all 10 saved frames give the right name, and 25
"no ring" frames from a group meeting are unaffected.

**Checking the hints (`tomoe tune`, hints.txt).** Every `tomoe tune` run
also reports on the hints themselves, apart from what the clustering made
of them: which windows were watched (title and size), stages, the
stretches when the watched window wasn't repainting (from looks marked
`ui_frozen`, or, for sessions recorded before that check, the saved full
frames replayed through it) with the names the ring gave and, with
`--ref`, who the reference has speaking then; and how often the name under
the ring was the reference's speaker at that second, overall, with the
window repainting or not, the worst minutes, and who the reference had
speaking when no ring was found. A reference turn is taken to last as long
as its words take to say (a Teams export has only start times; letting a
turn run to the next start credited a one-word "Yeah" with everything the
previous speaker said after it, which first made rings look missed when
they weren't).

On the 82-minute call: 18% of named looks disagree with the reference
overall, 51% while the window wasn't repainting, 15% while it was. Of the
looks while it was repainting, 85% agree, 10.7% are the host speaking
(Teams doesn't ring your own tile, so the highlight stays on the last
remote speaker; harmless, since such a read only votes for a remote voice
talking at that moment and there is none) and 4.4% are another remote
person speaking (ring lag at speaker changes and the reference's timing).
Looks that found no ring were the host speaking 70% of the time, nobody
17%, and someone else 13% (about a minute and a half of the call), so no
further rule is needed for gallery calls like this one. hints.txt names
people; `tune-*/` and `eval-*/` in the repo root are gitignored.

All of it is local, in the session folder. It includes other apps'
windows, so keep that folder out of any backup that leaves the computer
(e.g. the sessions backup script) unless that's intended, and turn Record
for tuning off afterwards.

## Diagnostics pane (frontend `DiagnosticsPane.tsx`)

A real-time, separate view into *how* each transcript line got its
speaker label — deliberately its own tab, not folded into
`TranscriptPane`, so the normal transcript stays exactly as clean as
it looks today; this is a tuning tool, not something an end user needs
to see. Built on the exact same `transcript:segment`/
`transcript:segment:update` Wails events `TranscriptPane` already
consumes — no separate backend event stream to keep in sync — plus one
new field on `session.Segment`: `Decision` (`speaker.AssignDecision`
as a plain string, kept string-typed there specifically so
`internal/session` doesn't need to depend on `internal/speaker`),
exposed via a new `speaker.Tracker.LastDecision()` getter rather than
changing `Assign`'s own return signature (which many existing
callers/tests already use).

The speaker label itself already carries everything the view needs:
`Tracker.label()` embeds a resolved hint in parens (`"Person 1
(Alex)"`), so `DiagnosticsPane` parses that back out and combines it
with `Decision` to render `"Alex [Person 1, OCR]"` (a hint exists, but
this segment's own decision wasn't a fresh confident match) or `"Alex
[Person 1, OCR+Centroid match]"` (this segment's audio independently
re-confirmed the same identity this instant) — no cross-referencing
the video-hint event stream needed.

With diarizing during the meeting, names come from the timeline's votes
and every covered line is relabeled when they change, earlier lines
included. With the previous pipeline a hint still only labels lines
emitted after it lands.

## Still open

- **Rules for other apps.** Zoom, Meet and Webex windows can be watched
  and their frames collected, but nothing is read from them until each
  has a rule (ring color and shape, label position, call chrome).
- **Tuned once.** The vote thresholds and the ring lag barely matter
  (one 82-minute, 5-speaker meeting; see speaker-attribution-research.md),
  and one look every 1-2 s loses nothing in the saved transcript against
  every 0.35 s.
- **Speaker-view detection** relies on Teams' gallery background
  brightness and the label's position, calibrated on one display.
- **Truncated names.** Teams' tiles often truncate long names; the
  fullest spelling read wins, which may still be truncated.
