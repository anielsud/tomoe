# This fork: what it adds, where it stands, what's next

Written 2026-10-21. This fork carries a macOS port of Tomoe PC and work on
transcript quality and live use, offered upstream as pull requests. The
detailed findings, with methods and dates, are in the research documents
listed at the end.

## Why

The goal is a meeting transcript good enough to build on: the right words
(names and terms especially), the right speaker on each line, and a live
text stream fast enough for a companion program to act on during the
meeting. The companion here is Musubi, a local knowledge base that reads
Tomoe's saved sessions and its live feed. Every change is measured on real
meetings before it becomes a default, and every new default has a setting
that restores the old behaviour, so Linux users keep their current results
unless they opt in.

## What the fork adds

**macOS port** (upstream PR #2): the CLI dictation mode, the GUI, meeting
mode with system audio capture, and speaker labelling from the meeting
window's video tiles.

**Transcript quality** (upstream PR #4, on every platform):
- word error rate on the hand-reviewed meeting 10.4% → 6.5%;
- names and terms right 43% → 73%;
- speaker labels right 94.7% → 97.2% (full pipeline);
- stray speakers per meeting 7–40 → 0;
- at a cost of 2.1–2.4× the CPU and about 3 GB more memory (a smaller
  English model is the budget option).

Upstream PR #3 makes merging similar speakers deterministic.

**Live use** (branch `feature/live`, to go upstream stacked on #4):
- an opt-in live feed: a private local socket streaming the session and
  each line as it is drafted and finalised (`[meeting] live_feed`);
- live lines labelled from the meeting window first, then the voice match;
- measured line lengths for single-pass transcription, with 15 s lines
  the choice for live use (`two_pass = false`, `max_speech_duration`,
  `turn_max_seconds`; the code's defaults are unchanged);
- a once-a-minute log of the final pass's backlog, to catch lag;
- a macOS fix: Objective-C entry points called from Go drain their own
  autorelease pools (a thread had spun at full CPU for hours after a
  meeting);
- research: single-pass line length, per-utterance hotwords, speaker
  identity from what is said.

## Branches

| Branch | What it is |
|---|---|
| `main` | Upstream's main, untouched |
| `feature/macos-port` | Upstream PR #2 |
| `fix/deterministic-speaker-merge` | Upstream PR #3 |
| `feature/transcript-quality` | Upstream PR #4 (stacked on #2) |
| `feature/live` | New work since #4; fork PRs merge here |
| `vocabulary`, `exp-vocab-hints` | Parked: vocabulary hints (below) |
| `zoom-chat` | Parked: reading Zoom's chat through Accessibility |

New work goes to `feature/live` as flat pull requests; nothing else is
merged into `feature/transcript-quality`, which PR #4 tracks.

## What's next

1. Upstream review of #2, #3 and #4; then `feature/live` as its own PR
   stacked on #4.
2. Word timings anchored on the live drafts, for finer speaker boundaries.
3. Vocabulary hints (parked): per-utterance hotwords work with the
   Parakeet TDT model once its beam search is fixed upstream in
   sherpa-onnx, and then beat the current model on names; what decides it
   is how well the hint list covers what is said.

## Research documents

| Document | What it covers |
|---|---|
| [transcription-accuracy-research.md](transcription-accuracy-research.md) | Model bake-offs, the live model, the new defaults and their cost, single-pass line length, hotwords |
| [speaker-attribution-research.md](speaker-attribution-research.md) | Speaker models, live settings, video hints, turn mode, overlap, highlight boundaries, identity from content |
| [speaker-pipeline-design.md](speaker-pipeline-design.md) | The speaker pipeline |
| [speech-to-text-tech-brief.md](speech-to-text-tech-brief.md) | Background on the speech models |
| [multilingual-models.md](multilingual-models.md) | Language coverage |
| [macos-support.md](macos-support.md), [macos-build.md](macos-build.md), [macos-audio-capture.md](macos-audio-capture.md), [macos-video-hints.md](macos-video-hints.md) | The macOS port |
| [dev-setup.md](dev-setup.md) | Development setup |
