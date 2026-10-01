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
