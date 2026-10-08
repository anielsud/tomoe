# Speaker attribution: measurements and findings

How well Tomoe tells speakers apart in meetings, pass by pass, and what it
costs. Everything here was measured with `tomoe eval` (see
[Reproducing](#reproducing)) and should be re-measured when the pipeline
changes. Recordings and reference transcripts are private and never
committed (they live in the gitignored `testdata/private/`).

## The pipeline being measured

A meeting's speaker labels go through these passes:

1. **Live guess.** Each utterance the speech detector (Silero VAD) closes
   is transcribed (Parakeet), embedded with the speaker model and assigned
   to a speaker by `speaker.Tracker` (cosine similarity against running
   speaker centroids). This is what the transcript shows during the
   meeting.
   - *Default* (as on main): single-pass, threshold 0.65, sticky and
     short-segment rules off.
   - *Experimental*: two-pass (streaming Zipformer text while speaking,
     Parakeet once the utterance ends), threshold 0.55, sticky margin 0.15,
     short-segment 0.7 s.
2. **Post-meeting diarization**, run in a subprocess once the session is
   saved (`tomoe diarize-session`): sherpa-onnx's offline diarization
   (pyannote segmentation 3.0, the speaker model, complete-linkage
   clustering at threshold 1.1), giving the *initial diarization*. Then
   `session.MergeSimilarSpeakers` merges clusters whose voices are more
   similar than 0.55, giving the *refined diarization*.
3. **Final labels.** Each transcript line takes the diarization speaker it
   overlaps most. This **replaces** the live labels; live guesses and
   Teams video hints only vote on names.
4. **Final, split at speaker changes** (`split_on_speaker_change`, off by
   default): a line is split where diarization hears a different voice
   mid-line, using Parakeet's word timings.

## How it's scored

- **Reference**: a Teams transcript corrected by hand for one meeting
  (59 minutes, 8 speakers, 177 turns, 8984 words, English). Six
  interjections where two people talk at once are annotated by giving the
  overlapping turns the same start time.
- **Right speaker by word**: the hypothesis transcript is aligned word by
  word to the reference text, and each aligned word scores whether its
  speaker (after a one-to-one Hungarian mapping of clusters to people) is
  the reference speaker. Reported overall and by the length of the
  reference turn the word is in: 1–3, 4–15, 16–60 and 61+ words, since
  short interjections are where attribution fails and a time-based score
  hides them. Only 38 words fall in 1–3 word turns in this meeting, so
  that bucket moves by about 2.6 points per word.
- Also reported: time-based confusion (excluding 1 s around each speaker
  change), how many people got a cluster of their own, cluster count, and
  text word error after normalizing numbers, fillers and spellings.

One meeting is a small sample. Settings tuned on it are at risk of
overfitting; a second reviewed meeting is planned before defaults change
on accuracy grounds.

## Results with today's speaker model

Speaker model: ERes2Net base, trained on Mandarin (3D-Speaker,
`3dspeaker_speech_eres2net_base_sv_zh-cn_3dspeaker_16k.onnx`).

| Pass | Overall | 1–3 words | 4–15 words | 16–60 | 61+ | People with own cluster |
|---|---|---|---|---|---|---|
| Live guess, default | 83.6% | 0% | 27.8% | 76.8% | 87.9% | 7/8 |
| Live guess, experimental | 91.8% | 21.1% | 37.0% | 89.5% | 94.8% | 7/8 |
| Initial diarization | 91.4% | 31.6% | 58.1% | 91.7% | 92.8% | 6/8 |
| Refined diarization | 92.5% | 34.2% | 66.5% | 92.0% | 93.9% | 5/8 |
| Final labels | 94.8% | 28.9% | 52.9% | 93.6% | 97.0% | 5/8 |
| Final, split at changes | 95.8% | 55.3% | 70.9% | 95.0% | 97.1% | 5/8 |

Text word error (final, Parakeet) is 10.2% against the reviewed
transcript; the streaming pass 1 text shown live in two-pass mode is
35.9%.

Overlapping speech: of the 6 annotated interjections (12 s), live
transcription hears 5 but attributes none correctly; the final split
attributes 1. Neither diarizer hears more than about 1 s of the 12 s as
two speakers at once, at any setting tried. Better overlap handling
needs a different (overlap-aware or separation) model, not tuning.

### Diarization settings sweep (sherpa-onnx, as shipped)

Thresholds 0.8–1.25, shortest turn 0.1–0.3 s, merge 0–0.65. The shipped
settings (1.1, 0.3 s, merge 0.55) score 95.8% final. The best found was
97.2% (threshold 0.8, merge 0.45), but neighboring settings fall off
quickly, so it wasn't adopted on one meeting's evidence.

### Tomoe's own diarizer

`internal/diarize` reimplements the same steps (pyannote segmentation via
onnxruntime, per-window embeddings excluding overlapped frames,
complete-linkage clustering, a centroid merge, reconstruction) so that
segmentation and embeddings are computed once and every clustering
setting is scored in seconds. On synthetic audio it reproduces sherpa's
output exactly; on the real meeting its clusters differ from sherpa's for
an unidentified reason, so it's judged on its scores, not as a drop-in.

Best setting (threshold 0.7, merge 0.6): **97.6%** final, on a plateau
(every neighboring setting scores at least 96.5%). It isn't used by the
app.

## Speaker model comparison

Scored with Tomoe's own diarizer (final, split at changes), each at its
best setting. Prepare time is segmentation plus embeddings for the hour,
7 workers × 2 threads on an Apple Silicon desktop.

| Speaker model | Final | 1–3 words | 4–15 words | Prepare time |
|---|---|---|---|---|
| ERes2Net base, Mandarin-trained (3D-Speaker) | 97.6% | 39.5% | 72.7% | 3:52 |
| ERes2Net, English-trained (VoxCeleb) | 97.9% | 28.9% | 77.5% | 4:18 |
| TitaNet-large (NVIDIA NeMo) | 98.0% | 36.8% | 78.9% | 3:53 |
| WeSpeaker ResNet34 (VoxCeleb) | 80.0% | 15.8% | 58.6% | 4:44 |
| WeSpeaker CAM++ (VoxCeleb) | 52.8% | 52.6% | 37.4% | 1:41 |

- The two ERes2Net models and TitaNet are effectively tied overall; the
  English-trained models gain on 4–15 word turns.
- Both WeSpeaker models are poor at every setting tried. Possibly an input
  mismatch (feature normalization) rather than the models themselves; not
  investigated, and they aren't offered.

### The English-trained model on the shipped pipeline

Same passes as above, with the English-trained ERes2Net in place of the
base model everywhere (live clustering and sherpa diarization), **at the
base model's settings** (live threshold 0.65/0.55, diarization 1.1 and
merge 0.55):

| Pass | Base model | English-trained |
|---|---|---|
| Live guess, default | 83.6% | 90.9% |
| Live guess, experimental | 91.8% | 95.9% |
| Initial diarization | 91.4% | 83.5% |
| Refined diarization | 92.5% | 94.4% |
| Final labels | 94.8% | 96.8% |
| Final, split at changes | 95.8% | 97.7% |
| 4–15 word turns, final split | 70.9% | 76.2% |
| 1–3 word turns, final split | 55.3% | 36.8% |
| People with own cluster (final) | 5/8 | 7/8 |

It's better in every pass that reaches the transcript, and the live guess
improves most. Its initial diarization is worse at threshold 1.1 (it
over-splits, and the merge step recovers), which suggests its own
diarization settings might differ; the sweep below says they needn't. On
short turns it is mixed: better on 4–15 word turns, worse on 1–3 word
turns (a difference of 7 of 38 words).

Because of this, `speaker_model = "auto"` (the default) uses the
English-trained model for English meetings. Other languages, Bengali
included, keep the base model until a model is measured on them.

Sweeping sherpa's settings for the English-trained model (same grid as
above) puts the base model's settings (1.1, 0.3 s, merge 0.55) on a wide
plateau: ten settings around it score 97.6–97.7% final, and the best
anywhere is 97.7%. So it keeps the same settings. It keeps 7 of 8 people
apart at every threshold up to 1.1; the base model keeps 5 or 6.

### The merge step was order-dependent (fixed)

`MergeSimilarSpeakers` merges greedily over speaker clusters taken from a
Go map, whose order is random, so the same meeting could get different
final labels each time it was diarized: two runs of the English-trained
model at the same settings scored 96.9% and 97.7%. It now sorts the
clusters first. With the base model the shipped settings scored 95.8%
both before and after, so its figures above stand.

## What each live setting contributes

The experimental preset changes four things at once. Each was measured
alone (turned on over the defaults) and on top of the others (turned off
from experimental). Live guess, right speaker by word; clusters is how
many speakers the live pass created for the 8 people.

| Run | English-trained | Clusters | Base model | Clusters |
|---|---|---|---|---|
| Default | 90.9% | 119 | 83.6% | 119 |
| + two-pass | 90.9% | 122 | 83.6% | 122 |
| + threshold 0.55 | 95.4% | 81 | 91.6% | 77 |
| + sticky 0.15 | 91.5% | 108 | 83.8% | 110 |
| + short-segment 0.7 s | 90.9% | 90 | 83.6% | 91 |
| Experimental (all four) | 95.9% | 42 | 91.8% | 44 |
| − two-pass | 95.9% | 42 | 91.8% | 44 |
| − threshold (back to 0.65) | 91.8% | 76 | 84.0% | 80 |
| − sticky | 95.4% | 53 | 91.7% | 51 |
| − short-segment | 95.7% | 75 | 91.8% | 68 |

- **The threshold is nearly all of the accuracy gain**: +4.5 points alone
  with the English-trained model, +8 with the base model.
- **Two-pass has no effect on speakers.** It changes when text appears
  and the live text, not who said it.
- **Sticky and short-segment barely move accuracy** (up to +0.5) but cut
  the number of spurious speakers: together they take the English-trained
  model from 81 clusters to 42 at threshold 0.55. Fewer phantom
  "Person N" entries is what a user sees during the meeting, and it's
  what video hints have to name.
- None of this reaches the final transcript, which the post-meeting pass
  relabels.

## Utterance length

Each utterance gets one speaker fingerprint and is transcribed on its
own, so where the speech detector cuts affects both. Two knobs: the pause
that ends an utterance and the longest one before it's cut
(`min_silence_duration`, `max_speech_duration`). English-trained model,
experimental live settings:

| Pause | Max | Live guess | 4–15 words | 16–60 words | Clusters | Text word error |
|---|---|---|---|---|---|---|
| 0.3 s | 10 s | 95.9% | 59.8% | 94.8% | 71 | 11.4% |
| 0.3 s | 30 s | 95.9% | 59.8% | 94.8% | 70 | 11.4% |
| 0.5 s | 10 s | 96.4% | 57.1% | 94.8% | 45 | 10.5% |
| 0.5 s | 30 s (current) | 95.9% | 48.0% | 94.8% | 42 | 10.2% |
| 0.8 s | 10 s | 96.3% | 56.9% | 95.3% | 29 | 10.5% |
| 0.8 s | 30 s | 93.6% | 47.7% | 83.6% | 25 | 9.8% |

- A 10 s cap helps 4–15 word turns most (people talking back to back with
  no 0.5 s gap otherwise end up in one utterance under one label), for
  little text cost.
- A long pause without the cap merges speakers: 16–60 word turns fall
  to 83.6%.
- Shorter utterances cost text accuracy because Parakeet decodes each one
  without the audio around it, and words at the cuts get clipped. This is
  the case for separating the speaker timeline from transcription (see
  [speaker-pipeline-design.md](speaker-pipeline-design.md)).
- The added delay is the pause itself; processing adds about 80–110 ms
  here.

## Labeling within an utterance

### Early labels during a long utterance

How soon a long utterance's speaker could be shown and relabeled as more
audio arrives: the label from just its first N seconds (read-only match,
`Tracker.Peek`) against the label from all of it. Experimental settings,
English-trained model; "words" are those in utterances longer than N.

| After | Utterances | Words | From the start | From all of it | Same label |
|---|---|---|---|---|---|
| 1 s | 421 | 8659 | 34.7% | 96.3% | 44.2% |
| 2 s | 379 | 8449 | 80.4% | 96.7% | 83.4% |
| 3 s | 331 | 8127 | 90.4% | 97.0% | 92.4% |
| 5 s | 226 | 6858 | 94.2% | 96.9% | 96.0% |
| 10 s | 84 | 3724 | 95.4% | 96.0% | 97.6% |

A label is usable after about 3 s and nearly final after 5 s. One second
of audio is too little for a fingerprint.

### Windows within an utterance

Each word labeled from the fingerprint of the window around it (windows
every 0.5 s within each utterance, matched to known speakers only),
against one label per utterance. Experimental settings, English-trained
model. Fingerprint load is all live fingerprinting as a share of meeting
time.

| Window | Overall | 1–3 words | 4–15 words | 16–60 words | Words relabeled (fixed / broken) | Fingerprint load |
|---|---|---|---|---|---|---|
| none (per utterance) | 95.9% | 15.8% | 48.0% | 94.8% | — | 3.2% |
| 1.5 s | 96.8% | 18.4% | 65.6% | 96.6% | 143 (106 / 24) | 10.0% |
| 2 s | 97.2% | 15.8% | 66.1% | 97.2% | 173 (138 / 19) | not measured alone |
| 3 s | 97.2% | 13.2% | 64.8% | 97.4% | 157 (131 / 12) | 13.4% |

Labeling inside utterances brings the live guess to 97.2%, near the
post-meeting pass (97.7%), and lifts 4–15 word turns from 48% to 66%. It
catches speaker changes with no pause between them, which one label per
utterance can't, for three to four times the fingerprint work.

## Diarizing during the meeting (simulated)

The design in [speaker-pipeline-design.md](speaker-pipeline-design.md):
Tomoe's own diarizer run during the meeting, reclustering every interval
on the windows finished so far, with speaker numbers kept stable across
reclusters (`tomoe eval --online`). English-trained model, threshold 0.6,
merge 0.5. Words take the timeline's speaker, and words in a gap their
line's. *First shown* is the label a word got from the first recluster
covering it; *relabeled* is the share of words whose label changed after
that; *delay* is from the end of a word to its first label.

| Fingerprint every | Recluster every | Final | 4–15 words (final) | First shown | Relabeled | Delay median / 90th | Slowest recluster | All reclusters |
|---|---|---|---|---|---|---|---|---|
| window | 10 s | 97.9% | 77.5% | 96.8% | 1.9% | 5 / 9 s | 2.41 s | 242 s |
| window | 30 s | 97.9% | 77.5% | 97.3% | 1.0% | 15 / 27 s | 2.50 s | 85 s |
| 2nd window | 10 s | 97.6% | 75.8% | 96.9% | 2.2% | 5 / 9 s | 0.64 s | 66 s |
| 2nd window | 30 s | 97.6% | 75.8% | 97.5% | 1.1% | 15 / 27 s | 0.70 s | 23 s |
| 3rd window | 10 s | 97.7% | 75.3% | 87.9% | 10.9% | 5 / 9 s | 0.29 s | 29 s |
| 5th window | 10 s | 97.2% | 73.1% | 96.1% | 2.0% | 5 / 9 s | 0.11 s | 11 s |
| 5th window | 30 s | 97.2% | 73.1% | 96.6% | 1.1% | 15 / 27 s | 0.11 s | 4 s |

- **The final result equals the post-meeting run** (97.9% at full
  depth), but it's ready seconds after the meeting ends.
- **Labels as first shown (96–97.5%) beat every live approach measured**
  (best: windows within utterances, 97.2%), and only 1–2% of words
  change afterwards.
- **Every 2nd window costs 0.3 points** for half the fingerprint work;
  every 5th costs 0.7 points for a fifth.
- Every 3rd (and 4th) window looked like an outlier: normal final score,
  but first-shown labels 88% and 11% of words relabeled. **Cause
  (found 2026-10-02):** a frame none of whose covering windows were
  fingerprinted has no votes, and the cluster sort handed it to cluster 0,
  an arbitrary speaker. The newest frames at a recluster are covered by
  only the last window or two, and windows start every 1 s while
  reclusters fall every 10 s, so strides dividing 10 (2, 5) always
  fingerprint the last window and strides 3 and 4 mostly don't. Relabels
  were spread evenly across the meeting, mostly "cluster 0 → the real
  speaker". **Fix:** frames with no votes stay unlabeled until a later
  recluster has a fingerprint for them. Rerun (first shown / relabeled):

  | Stride | 10 s recluster, before | 10 s, after | 30 s, after |
  |---|---|---|---|
  | 2 | 96.9% / 2.2% | 96.9% / 2.2% | 97.5% / 1.1% |
  | 3 | 87.9% / 10.9% | 94.8% / 4.2% | 96.1% / 2.3% |
  | 4 | 87.7% / 11.1% | 94.8% / 4.2% | 96.7% / 1.8% |
  | 5 | 96.1% / 2.0% | 96.2% / 1.8% | 96.6% / 1.1% |

  Final scores did not move at any stride (97.2–97.9%), and stride 1 is
  untouched. Strides 3 and 4 at 10 s are still about 2 points below 2 and
  5 on first-shown: their newest frames are labeled a recluster later.
  The app's default (stride 2, 10 s) was never affected.
- Without the gap rule (timeline only) final scores drop to 94–94.6%.

The built version (`diarize_during_meeting`, `tomoe eval
--stream-diarizer`, running the real `diarize.Stream` through the live
replay) reproduces the simulation: every 2nd window, recluster every 10 s,
**97.6% final**, 7/8 people, 356 reclusters.

## Video hints (macOS)

Teams' active-speaker ring and name label, read from the meeting window,
are a second signal for who's speaking; the design and the attribution
rules are in [macos-video-hints.md](macos-video-hints.md).

**Ring detection on 22 saved frames that had failed:**

| Failure | Frames | Cause | After the fixes |
|---|---|---|---|
| No ring found | 7 | Speaker view: Teams draws no ring | Speaker view recognized in all 7, name read under the main video |
| Several rings, filmstrip | 10 | Real: Teams lights every tile making sound (two conference rooms) | All lit tiles' names read, resolved by elimination |
| Several rings, gallery | 5 | A purple virtual background passing the color test (8–13 candidates) | Border-shape test: 4 of 5 frames now one ring; the fifth has 3 really lit tiles |

Names read correctly on all three frame types once icon scraps after a
name ("… fo") were stripped; one-letter misreads ("Ortlz") are folded
into the most-read spelling. Per look on an Apple Silicon desktop: ring
detection 4 ms, thumbnail 2 ms, full frame 21 ms (when something
changed), reading a name 16 ms (when needed), plus the capture.

**First recording with Record for tuning** (18 minutes, 1:1 Teams call,
one remote speaker), from `tomoe tune` without a reference:

| | |
|---|---|
| Looks | 3,067: 60% ring read, 40% speaker view (no ring; name read under the main video) |
| Cost per look, median | capture 14.8 ms, rings 3.3 ms, name read 13.8 ms (when run), look 29.7 ms (90th: 37 ms) |
| CPU, looks every 0.35 s / 0.7 s / 1 s / 2 s | 7.8% / 3.6% / 2.5% / 1.3% of one core |
| Labels vs every look, stride 1 | every 1 s: 99.6% the same name; every 2 s: 98.2%; stride 5 at 1 s: 99.6% |
| Other side's words named | 98.7% |

So checking every second costs about 2.5% of a core and changes almost
nothing; the learning rate's 7.8% is only paid while someone needs
naming. It also found two bugs, since fixed: an icon read as text after
the name ("Alex Kim •.•") won the spelling, and lines no turn covered kept
provisional labels ("New speaker", "Alex Kim?") in the saved transcript.

**Not measured yet:** how well names are attributed when there's a
choice between people, the ring's lag (it never moved in a 1:1 call), and
the vote thresholds. That needs one meeting
recorded with `record_for_tuning` and a reviewed transcript of it, then
`tomoe tune` (see macos-video-hints.md, "Tuning the numbers").

## Speed and processing load

Measured on the same hour of audio, uncached, one run at a time.
"This Mac" is the performance cores of an Apple Silicon desktop with 4
threads. "Business-class approximation" runs the same thing on its
efficiency cores (`taskpolicy -b`) with 2 threads, a stand-in for a
typical business laptop; treat it as a ballpark.

### Live passes

Delay is from the end of an utterance (after the detector's 0.5 s of
silence) to its final text and speaker label. Load is processing time as
a share of meeting time.

| | This Mac | Business-class approx. |
|---|---|---|
| **Default (single-pass)** | | |
| Parakeet decode per utterance | 137 ms | 634 ms |
| Speaker embedding per utterance | 216 ms | 714 ms |
| Delay, median / 90th / 99th | 273 ms / 0.69 s / 1.45 s | 1.06 s / 2.6 s / 5.8 s |
| Load | 4.8% | 18.4% |
| **Experimental (two-pass)** | | |
| Streaming text per 30 ms window | 0.67 ms | 2.45 ms |
| Delay to final label, median / 90th / 99th | 109 ms / 275 ms / 599 ms | 504 ms / 1.3 s / 2.8 s |
| Load | 7.4% | 28% |

In two-pass mode text appears while the person is still speaking; the
delay above is only for Parakeet's refined text and the speaker label.
Speech detection and speaker clustering are negligible (under 1%).
Speaker embedding is the largest live cost, and in two-pass mode it runs
about twice per utterance (920 embeddings for 471 utterances), which is
worth removing.

### Post-meeting diarization

| | This Mac | Business-class approx. |
|---|---|---|
| sherpa-onnx, as shipped (4 threads) | 6:15 for the hour (10.5% of meeting time) | roughly 20–25 min (stopped after 13 min unfinished) |
| Tomoe's own diarizer (2 workers × 2 threads) | 9:04 (15.3%) | not measured |

Nearly all diarization time is computing embeddings; clustering and
reconstruction take about 5 s. Our diarizer embeds every 10 s window per
local speaker (4841 embeddings for the hour), so it does more of that work
than sherpa. Fewer or shorter embeddings are the lever if it's to ship.

### Diarizing during the meeting: keeping up, and catching up

A 52-minute briefing with a screen share (2026-10-06) got its final
labels 84 s after it ended, where nine other meetings that week took
0.4–5.4 s. The Stream does all its work on one low-priority thread and
falls behind when the machine is busy; Finish then waits for it to work
through the backlog one window at a time. Measured on that briefing's
audio on an idle Apple Silicon desktop (`tomoe eval --diarization-timing
stream`, which now prints real progress):

| | Time | Share of meeting time |
|---|---|---|
| One thread, every window (stride 1, as Record for tuning forces) | 1198 s | 38% |
| One thread, every 2nd window (stride 2, the default), reclusters excluded | 635 s | 20% |
| Parallel catch-up of the whole meeting (new) | 199 s | 6% |

- **Fingerprints are the cost.** Segmenting all 3,112 windows in parallel
  took under 20 s; the rest is speaker fingerprints (about 30 a second in
  parallel). Reclusters were 88 s of the 1198 s (slowest 0.84 s).
- At 38% of a core when idle, stride 1 has little headroom once a call,
  a screen share and the live transcript compete for the efficiency
  cores; the briefing's 84 s wait means it ended roughly four minutes of
  audio behind. Stride 2 halves that load.
- **Catch-up (shipped):** once the meeting has ended the Stream raises its
  thread back to normal priority (macOS; Linux can't without privileges),
  skips the intermediate reclusters nobody will see, and, with 30 or more
  windows left, segments and fingerprints the rest in parallel
  (`Stream.catchUp`, via `Prepare`). The parallel run produced exactly
  the same fingerprints (3,567) and speakers (10) as one window at a time,
  about 6x faster, so an 84 s wait becomes roughly 14 s.
- The log line at the end of each meeting, and `diarization.json`'s
  `stats`, now say how far behind the diarizer was at the end and at most,
  what reclusters cost and what was caught up, so real meetings show
  where it falls behind.
- Not done: thinning fingerprints (stride 2 or more) automatically while
  the Stream is far behind, so it never builds a backlog; it costs about
  0.3 points per stride step, only while behind.

## Video hints, tuned on a real meeting (2026-10-02)

`tomoe tune` on a session recorded with Record for tuning (82 minutes, 5
speakers, Teams window in gallery, speaker and screen-share layouts)
against Teams' own transcript of the same meeting (72 minutes overlap, the
reference shifted +633 s to match). Scored by word on the final labels.
Teams labels each speaker from their own audio, so it's a reliable key; the
host's own words are labeled "You" by Tomoe (23% of the words), which the
scorer counts as unnamed, so the "none" column is almost entirely that and
about 77% is the most "right" can reach.

| Setting | Speakers right | Names right / wrong | Hint CPU |
|---|---|---|---|
| Defaults (clustering constraints on, looks every 0.35 s) | 92.8% | 70.0% / 5.7% | 8.2% |
| Constraints off, same looks | 96.8% | 72.5% / 2.6% | 8.2% |
| Constraints off, one look every 1 s | 96.8% | 73.5% / 2.4% | 2.6% |
| Constraints off, one look every 2 s | 96.8% | 73.5% / 2.4% | 1.4% |
| Constraints off, one look every 5 s | 96.8% | 72.4% / 3.5% | 0.6% |

- **The clustering constraints hurt**: with them on, speaker accuracy was
  92–93.6% against 96.5–96.8% with them off at every stride and look rate
  tried, and wrong names rose from about 2.5% to 5.5–6%. They were
  "reasoned, not tuned" until now. Removed (2026-10-02): names now reach
  clusters only through the vote.
- **Splitting is the harmful half** (same session, default naming rules,
  one half of the constraints switched on at a time):

  | Constraints | Speakers right | Names right / wrong |
  |---|---|---|
  | Neither | 96.8% | 72.5% / 2.6% |
  | Merge only (clusters dominated by one name, voices at least 0.3 alike) | 96.7% | 72.4% / 2.5% |
  | Split only (a cluster with two confident names) | 93.0% | 70.2% / 5.2% |
  | Both, merge threshold raised from 0.3 to 0.5 | 93.0% | 70.2% / 5.2% |
  | Both (the old default) | 92.8% | 70.0% / 5.7% |

  Merging changed nothing here and raising its threshold changed nothing;
  the whole loss comes from splitting. The mechanism isn't shown. A name
  read that is wrong or late (the highlight stays on one person through
  another's short interjection, as seen at 10:17-10:19 in this call) tags
  the wrong person's voice, and a cluster with two such names is split.
  Merge-only scored the same as none, so both halves were removed.
- **Most of the constraints' damage was stale name reads.** Teams stops
  repainting a hidden call window (docs/macos-video-hints.md, "Stale hints"),
  and two stretches of this call (621 s) fed the constraints a highlight
  frozen on one person. Replayed with the looks from those stretches
  ignored, as the `ui_frozen` check now does:

  | | Speakers right | Names right / wrong |
  |---|---|---|
  | Constraints off | 96.8% | 73.5% / 2.4% |
  | Constraints on, all looks (before) | 92.8% | 70.0% / 5.7% |
  | Constraints on, frozen-window looks ignored | 96.3% | 73.1% / 2.7% |

  The gap to "off" falls from 4.0 points to 0.5, but they still didn't
  help in this meeting, so they were removed outright. Both rules take every tag
  at face value: the harmful split came from 300 wrong tags (one person's name)
  against 2622 right ones (another person's) in one cluster, a 10% minority name, which a
  minimum share for the second name would have blocked (not tried). Worth
  re-measuring on a meeting where the diarizer over-splits a speaker.
- **The vote rules barely matter**: lag, reads, share and bucket changes
  moved names right by under 1 point (73.4–73.5% across the top 20).
- **Looks can be much sparser for the saved transcript**: one look every
  1–2 s loses nothing against every 0.35 s and costs 2.6% / 1.4% of a core
  instead of 8.2%. Every 5 s loses about 1 point of names. This is the
  final transcript only; the live ticker and early labels do depend on how
  often it looks, which wasn't measured.
- **Stride** (constraints off): 96.8% at 1, 96.7% at 2, 96.5% at 3, 96.1%
  at 5, as in the earlier replay.
- **Limits**: one meeting; two of the five speakers (Teams: 270 and 178
  words of about 18,000) barely spoke, so their accuracy is noisy; the
  first 10:33 of the recording has no reference and isn't scored; Teams'
  transcript is itself automatic.

## Ten more meetings (2026-10-05/06)

Ten Teams meetings recorded with Record for tuning on the app's defaults
(diarizing during the meeting, English model, threshold 0.6, merge 0.5,
stride 1 because recording for tuning keeps every fingerprint): 7.6 hours
in all, 1:1s up to a large briefing. Four have a Teams transcript to score
against (35–58 minutes each, 4–8 speakers); the other six don't (Teams
didn't transcribe them), but three of those are 1:1s, where every remote
word belongs to the one other person, so mistakes can be counted without
a reference.

**Scores on the saved transcripts**, by word aligned to Teams' transcript.
"Right" counts a name that Teams truncated on the tile (a prefix of the
full name) as right; "You" is scored as the host.

| Meeting | Speakers | Right | 1–3 word turns | 4–15 | Wrong speaker | Tiny unnamed clusters | Wrong name on a right cluster |
|---|---|---|---|---|---|---|---|
| A, 58 min | 4 | 98.6% | 68% | 95.9% | 0.8% | 0.6% | 0 |
| B, 35 min | 8 | 97.1% | 50% | 93.0% | 2.5% | 0.2% | 0 |
| C, 50 min | 7 | 98.2% | 56% | 88.6% | 1.0% | 0.6% | 0 |
| D, 52 min (large briefing) | 6 | 96.9% | (2 words) | 90.9% | 0.0% | 0.1% | 2.9% |

`tomoe tune` on the same four agrees (speakers right 95.7–99.8%, names
wrong 1.4–3.5%).

What's left, by how much it shows in a transcript rather than by its share
of words:

1. **Tiny clusters.** Every meeting ends with 5–21 extra speakers that
   own a few words each: a line split off another person's sentence at a
   brief diarization flicker (0.3–2 s, 1–6 words), or a short backchannel
   while the host is talking. They're 0.1–0.6% of words but each is its
   own "Person N" line, and they appear even in 1:1s (6–8 extra labels in
   each of the three, all wrong by construction). Cluster numbers reach
   the low hundreds because each recluster mints new ones.
   **Simulated fix:** give each line of a cluster with fewer than 15–30
   words in the whole meeting the speaker of the neighbouring line
   (previous if it ended within 2 s, else next):

   | Meeting | Before | Absorb < 15 or < 30 words | < 60 words |
   |---|---|---|---|
   | A | 98.6% | 99.0% | 99.0% |
   | B | 97.1% | 97.1% | 96.4% |
   | C | 98.2% | 98.7% | 98.7% |
   | D | 96.9% | 97.0% | 97.0% |

   Never worse at 15–30 words, and 1–3 word turns improve (A 68→76%,
   C 56→59%). At 60 words it swallows a real person who said 43 words
   (B), so the floor has to stay well under what a quiet participant says.
   The same rule clears the 1:1s. Done properly it belongs in the
   diarizer's clustering (a minimum total duration per cluster, the rest
   reassigned to the nearest centroid) rather than as a relabel.

   **Shipped** as `min_speaker_words = 20` (`session.AbsorbSmallSpeakers`,
   applied to the final labels when diarizing during the meeting; 0 turns
   it off). `tomoe tune` on the four meetings, with the toolbar fix below:

   | Meeting | Speakers right | Names right / wrong / none |
   |---|---|---|
   | A | 97.6 → 98.2% | 80.7 / 1.4 / 17.9 → 81.2 / 1.6 / 17.3% |
   | B | 95.7 → 95.8% | 85.4 / 3.5 / 11.0 → 85.6 / 3.9 / 10.5% |
   | C | 97.4 → 98.0% | 74.3 / 1.5 / 24.2 → 74.8 / 1.7 / 23.6% |
   | D | 99.8 → 99.9% | 97.0 / 2.9 / 0.1 → 97.1 / 0.1 / 2.9% |

   The cost: absorbed lines that were unnamed now carry a neighbour's
   name, right about two times in three overall but mostly wrong in B
   (eight people trading short turns), so wrong names rise 0.2–0.4 points
   in A–C. Absorbing only when both neighbours are the same speaker left
   some stray lines and saved about 0.1 point, so it wasn't worth it.

   **By voice, then by line.** `Prepared.AbsorbSmallClusters` gives every
   window of a cluster heard for under N seconds to the larger cluster
   with the most similar mean voice, in the final timeline only (a new
   voice must be free to start small during the meeting). `tomoe tune`'s
   "Small speakers" sweep, speakers right / 1–3 word turns / 4–15 word
   turns / names wrong / speakers left with under 20 words:

   | Meeting | Neither | Voice 2 s | Lines 20 w | Voice 2 s + lines 20 w |
   |---|---|---|---|---|
   | A | 97.6 / 34 / 92.7 / 1.4 / 23 | 97.8 / 36 / 93.3 / 1.5 / 11 | 98.2 / 41 / 94.7 / 1.6 / 0 | 98.3 / 43 / 94.9 / 1.5 / 0 |
   | B | 95.7 / 41 / 88.7 / 3.5 / 11 | 95.7 / 41 / 88.9 / 3.7 / 5 | 95.8 / 41 / 88.9 / 3.9 / 0 | 95.8 / 41 / 88.9 / 3.9 / 0 |
   | C | 97.4 / 27 / 87.2 / 1.5 / 22 | 97.7 / 33 / 90.7 / 1.8 / 7 | 98.0 / 31 / 89.6 / 1.7 / 0 | 98.0 / 35 / 90.9 / 1.7 / 0 |
   | D | 99.8 / – / 100 / 0.1 / 6 | 99.9 / – / 100 / 0.1 / 2 | 99.9 / – / 100 / 0.1 / 0 | 99.9 / – / 100 / 0.1 / 0 |

   Both together match or beat lines alone on every column in every
   meeting, so both ship (`min_speaker_seconds = 2`,
   `min_speaker_words = 20`). Larger voice floors (5–12 s) helped A
   (4–15 words 96%) but added wrong names in B (4.0%).

   **Do short turns cluster at all?** Barely. Whatever the rule, words in
   1–3 word reference turns get the right speaker 27–43% of the time and
   4–15 word turns 87–93%; a second of speech doesn't make a reliable
   fingerprint. The small clusters are a different thing: in all four
   meetings nearly every one held a single person who already had a
   large cluster (an over-split fragment, not a new voice), which is why
   pointing them at the nearest voice works.
2. **Toolbar text read as a name** (D, 2.9% of words, one speaker's whole
   cluster). A Teams popup ("Limited call features") covered most of the
   lit tile, so the ring was found as a 129×6 px strip. `LabelRect` puts
   the name box at `ring.Y + ring.Height - BottomOffset`, which for a
   6 px ring is above the tile, on the call toolbar, and OCR read "Take
   control | Annotate" as the speaker's name. Fixes: reject (or rebuild
   from same-row tiles) a ring much shorter than a tile, and drop reads
   made of Teams' toolbar words (Take control, Annotate, Pop out, Chat,
   People, Raise, React, View, Notes, Apps, More, Camera, Mic, Share,
   Leave). **Shipped** both (`labelFits`, `plausibleName`; replays
   apply them to recorded looks through `Look.Accepted`): D's wrong names
   fall from 2.9% to 0.1%, and that speaker is left unnamed, since no
   read of their tile was ever clean. Other junk reads seen and correctly
   outvoted: single
   characters ("E", "F", "-"), a shared document's title, a slide's
   hashtag, and a name with its space dropped (292 reads, folded into the
   right spelling).
3. **Truncated names.** Teams cuts long names on the tile ("First Las…");
   in D, 38% of words carry a truncated name. Right person, unfinished
   label. The meeting invite's attendee list would complete them when one
   attendee matches the prefix.
4. **Same person, two labels.** In six meetings one name sits on two to
   four clusters ("Person 1 (X)" and "Person 34 (X)"), all but one of
   them tiny; absorbing tiny clusters removes most. Showing named
   clusters by name alone would hide the rest.
5. **Short turns and turn boundaries** remain the main source of wrong
   speakers (0.8–2.5% of words): 1–3 word turns are right 50–68% of the
   time, 4–15 word turns 89–96%. Errors cluster where two people trade
   short turns. Same open problem as before.
6. Host and remote mixed up: under 0.2% of words.

**Look rate and stride, again.** On all four meetings, one look a second
scores within 0.1 points of every 0.35 s (names right and wrong) for 3%
of a core instead of 8–11%; every 2 s loses at most 0.3 points for 1.3–1.9%.
Stride 2 is within 0.1 points of stride 1 for half the fingerprints. The
first tuned meeting said the same.

Final labels were ready 0.4–5.4 s after each meeting ended, except D:
83.8 s (52 minutes, mostly long monologues; not investigated yet).

## Open questions

- **Short turns.** Words in 1–3 word turns are right 30–55% of the time
  with any model, and 4–15 word turns 53–79%. A person does far better
  from audio alone, and interjections are often interruptions rather
  than call and response, so turn-taking priors won't solve it. Candidates:
  better segmentation around short turns, more frequent Teams video
  hints, text-based correction.
- **Overlap.** Neither diarizer hears two speakers at once; needs an
  overlap-aware model.
- **A second reviewed meeting** before any setting is changed on accuracy
  grounds, to check for overfitting.
- **Live threshold for the English-trained model.** Measured only at the
  base model's 0.65 and 0.55.
- **Bengali and mixed-language meetings** haven't been measured with any
  speaker model.

## Reproducing

```bash
# Every pass, against a reviewed reference (cached transcription after the first run)
tomoe eval meeting.mp4 --ref meeting.reviewed.txt

# A speaker model everywhere: an ID (eres2net-base, eres2net-en) or an .onnx path
tomoe eval meeting.mp4 --ref meeting.reviewed.txt --embedding-model eres2net-en

# Sweep sherpa's diarization settings, or our own diarizer's
tomoe eval meeting.mp4 --ref meeting.reviewed.txt --sweep
tomoe eval meeting.mp4 --ref meeting.reviewed.txt --sweep --own-diarizer

# Timing: live pipeline uncached at app-like threads, and diarization alone
tomoe eval meeting.mp4 --ref meeting.reviewed.txt --no-cache --skip-diarization --threads 4
tomoe eval meeting.mp4 --ref meeting.reviewed.txt --diarization-timing sherpa --threads 4

# Business-class approximation on Apple Silicon: efficiency cores only
taskpolicy -b tomoe eval meeting.mp4 --ref meeting.reviewed.txt --no-cache --skip-diarization --threads 2
```

The reference format is a Teams transcript export (`Name   0:12` header
lines followed by the text). Turns that overlap share a start time.
