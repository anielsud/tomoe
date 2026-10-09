# Transcription accuracy: measurements and findings

How well Tomoe's words match what was said in real meetings, what kinds
of mistakes are left, and what moves them. Speaker attribution is covered
in [speaker-attribution-research.md](speaker-attribution-research.md).
Recordings, transcripts and judged spots are private and never committed
(they live in the gitignored `testdata/private/`).

## How it's scored

Four Teams meetings recorded on 2026-10-05/06 (35–58 minutes, 4–8
speakers, about 32,000 words in all) have Teams' own transcript, which is
automatic too, so a disagreement is not always Tomoe's mistake.

`tomoe textdiff <transcript> --ref <teams.txt>` aligns the two word by
word after the normalization `tomoe eval` uses (case, punctuation,
fillers, spoken numbers) and reports:

- **Word disagreement** with Teams overall.
- **Disagreements that can change meaning** per 1,000 reference words:
  everything except function-word swaps (the/a, and/but), plural or
  tense, split or joined words, and one-sided stretches of 8+ words (a
  part one side didn't record), which are counted separately. This is a
  broad, noisy number.
- **Judged spots now right.** Every disagreement in the four meetings'
  saved transcripts was reviewed in context and classified; the 506
  where Tomoe's version changed the meaning and Teams' was right (or it
  couldn't be told) are kept as `<ref>.spots.jsonl`, each anchored by the
  reference words around it. A later transcript fixes a spot when it
  matches Teams on every word of it. This is the precise number.
- **Names and terms right** wherever the meeting says them, for every
  name and domain term the judged spots involve.

Transcripts to score come from a saved session or from
`tomoe session replay <id> --only-current [--asr-model dir] [--decoding ...]`,
which replays the session's mic and meeting audio through the live
pipeline and writes `current.json`.

## What the disagreements are (saved transcripts, Parakeet v3)

About half of all disagreement doesn't change meaning: function words
and repeats Teams tidies away (5.4% of words), number formats, spelling,
split words and word forms (2.0%), and the two transcripts starting at
different times (1.9%).

The rest, as judged: about **16 meaning-changing Tomoe errors per 1,000
words** (12–18 per meeting), roughly one every 25–30 seconds of speech:

| Kind | Share | Notes |
|---|---|---|
| Common words misheard | 39% | A similar-sounding word or phrase |
| Domain terms | 37% | Product names, a compliance standard (heard as everyday words dozens of times in one meeting), tools, acronyms, venue names on slides |
| People's names | 13% | The host's own name most often |
| Speech dropped | 11% | Mostly short interjections during overlapping speech |

Teams was the wrong one in about 60 places (a tool name turned into a
person's name, speech it dropped as filler).

Other findings from the same review:

- **A late start.** One meeting's recording began about 45 s after
  speaking started: the Teams window still showed the pre-join screen
  ("not a live call"), so automatic detection waited.
- **Pass-1 text left as final.** 1–15 lines per meeting keep the live
  streaming model's draft (upper case, no punctuation) because Parakeet
  found no speech in the line's audio and the draft stands
  (`live.Coordinator.refine`). They're short (0.4–3.4 s), often slivers
  cut from a neighbouring utterance mid-word; re-decoding with half a
  second of context either side recovers some, not reliably.

## Parakeet v2 (English) against v3 (multilingual)

Tomoe uses Parakeet TDT 0.6B **v3** (25 languages, int8) for every
language. Its English-only predecessor **v2** (int8, same size) was
replayed on the same four meetings with the user's pipeline otherwise
unchanged, against a v3 replay as the baseline (replay noise: the v3
replay fixes 6% of judged spots against the saved transcripts).

| Meeting | Word disagreement v3 → v2 | Meaningful per 1,000 v3 → v2 | Judged spots right v3 → v2 | Names/terms right v3 → v2 |
|---|---|---|---|---|
| A, 58 min | 17.8 → 15.5% | 48.4 → 44.4 | 7 → 34% | 34 → 45% |
| B, 35 min | 20.4 → 17.5% | 42.5 → 35.1 | 4 → 24% | 50 → 66% |
| C, 50 min | 17.7 → 15.0% | 43.0 → 34.9 | 6 → 28% | 42 → 60% |
| D, 52 min | 9.4 → 6.5% | 24.5 → 17.2 | 6 → 45% | 40 → 52% |

By kind of judged error (all four, 506 spots):

| Kind | v3 replay right | v2 right |
|---|---|---|
| Common words misheard (197) | 9% | 42% |
| Domain terms (186) | 4% | 33% |
| People's names (65) | 3% | 17% |
| Speech dropped (58) | 5% | 16% |

- **v2 fixes about a third of the meaningful errors** and lowers word
  disagreement by 2.3–3.0 points in every meeting.
- It helps most with ordinary words, which vocabulary can't touch. Names
  barely improve: they need vocabulary on top of v2.
- Not yet measured: decode speed, and whether v2 gets wrong anything v3
  got right (overall disagreement falls everywhere, which suggests not).
- v2 is English-only; other languages (Bengali) keep v3.

## Bake-off: eleven models (2026-10-07)

Every model sherpa-onnx 1.13.8 can run that handles English, replayed
through the same pipeline on the same four meetings, single-pass (only
the model under test writes text), scored as above. Decode time is each
finalist alone with 4 threads on an Apple Silicon desktop, on one
35-minute meeting.

| Model | Judged errors fixed | Misheard words | Terms | Names | Names/terms right everywhere | Decode time | Word timestamps |
|---|---|---|---|---|---|---|---|
| Cohere Transcribe int8 (14 languages) | **41%** | 44% | **51%** | **28%** | 69% | 7.9% of meeting time | no |
| Whisper large-v3 | 40% | 43% | 48% | 26% | **70%** | far slower (not timed alone) | no |
| Whisper turbo | 38% | 41% | 47% | 26% | 68% | 24.7% | no |
| Whisper distil-large-v3.5 | 38% | 44% | 43% | 23% | 66% | 21.6% | no |
| Parakeet TDT 0.6B v2 fp16 | 33% | 42% | 34% | 17% | 60% | — | yes |
| Parakeet TDT 0.6B v2 int8 | 33% | 42% | 35% | 15% | 59% | 2.4% | yes |
| Qwen3-ASR 0.6B int8 | 28% | 35% | 28% | 23% | 50% | — | no |
| Qwen3-ASR + vocabulary | 24% | 26% | 26% | 23% | 46% | — | no |
| Canary 180M flash int8 | 23% | 31% | 24% | 11% | 43% | — | no |
| FunASR-Nano int8 | 15% | 24% | 10% | 8% | 34% | — | yes |
| Moonshine base (2026) | 8% | 11% | 8% | 0% | 18% | — | no |

- **Cohere Transcribe is the most accurate and the cheapest of the
  accurate tier**: about a quarter more judged errors fixed than
  Parakeet v2, mostly on terms and names, at about 3x its decode time.
  Apache-2.0. It's now the default for English.
- The top tier (Cohere, Whisper) differs from Parakeet mostly in
  vocabulary: misheard ordinary words are close for all of them
  (41–44%). Names stay hard everywhere (28% at best).
- int8 Parakeet loses nothing against fp16.
- **Vocabulary through the model failed both ways tried.** Qwen3-ASR
  takes hotwords as context, and on short or unclear audio sometimes
  printed the whole list as the transcript (9 lines in one meeting); it
  didn't improve names (23% with or without). FunASR-Nano's hotwords
  changed nothing. Parakeet's hotwords need beam search, whose TDT
  implementation can loop (below).
- Running these replays twelve at a time thrashed a 48 GB machine; the
  replays' memory doesn't show in RSS (compressed). Six to seven at once
  was the ceiling.

### Word timings for a model without them

Cohere returns text only; Tomoe uses word timings only to split a line
where diarization hears a different speaker mid-line. Estimating them
(each word a share of the line in proportion to its length,
`session.SpreadWords`) against real timings, `tomoe tune` on the four
meetings:

| Text and timings | Speakers right | 1–3 word turns | 4–15 word turns |
|---|---|---|---|
| Parakeet v2, real timings | 98.0% | 33.9% | 92.2% |
| Parakeet v2, estimated | 97.5% | 34.8% | 91.8% |
| Cohere, estimated | 97.6% | 36.4% | 92.1% |

Estimated timings cost about half a point of speaker accuracy on
average (1.4 points in an eight-person meeting of quick exchanges, 0–0.3
in the others); short turns don't move. Accepted for Cohere; running
Parakeet alongside for real timings would recover it for about 2.4% more
of meeting time.

## Live model (pass 1) bake-off (2026-10-08)

The live text, shown while people speak before the turn's final text
replaces it, came from a streaming Zipformer trained on LibriSpeech (read
audiobooks). It was all capitals, with no punctuation, and weak on
conversation. The live text's job is real-time enrichment (pulling up
relevant information mid-conversation), so words and names matter;
formatting doesn't.

**Method.** About 8 minutes of speech: the first 6 minutes of the fast
group meeting and 4 minutes of a 1:1. Each candidate streamed the same
utterances the app produced, in 100 ms chunks with 2 threads, one process
at a time. Text was compared with the final text (Cohere) for the same
stretch, ignoring case and punctuation. Cohere has its own errors, so
lower is better but this isn't an error rate against the truth. Memory is
the model's own, without the test audio.

| Live candidate | Differs from final: group / 1:1 | Punctuation, capitals | CPU, % of audio | Memory |
|---|---|---|---|---|
| Streaming Zipformer, LibriSpeech (previous) | 41% / 52% | no (all capitals) | 8% | ~225 MB |
| **NeMo streaming FastConformer, 480 ms** | **25% / 38%** | no (lowercase) | **7.6%** | ~500 MB |
| Nemotron Speech Streaming 0.6B, 560 ms | 18% / 31% | yes | 22% | ~1.9 GB |
| Nemotron 3.5 ASR Streaming 0.6B, 560 ms | 19% / 33% | yes | 22% | ~2.0 GB |
| Parakeet unified 0.6B, streaming 560 ms | 28% / 44% | yes | 320% | ~1.9 GB |
| Parakeet v2, re-decoding the utterance every 1 s | 15% / 26% | yes | 38% | ~1.9 GB |

**What this shows:**
- **FastConformer** removes about a third of the previous model's
  differences for the same CPU, with a smaller download (105 MB vs
  310 MB).
- **Nemotron** is closer still, but costs about 1.9 GB and three times the
  CPU on top of the final-text model.
- **Parakeet unified** isn't usable for streaming on CPU.

**In the app:** a 22-minute 1:1 replayed end to end, as the app runs it,
gives drafts that differ from the final text on 27% of words (previous
model: 42%), with no empty drafts. `live_model = "auto"` is now the
FastConformer; `"zipformer-2023"` restores the previous model, and
`"nemotron-560"` uses Nemotron.

## What the new defaults cost (2026-10-09)

Two recorded meetings, replayed through the live pipeline (`session
replay --only-current`) with the previous defaults and with today's. The
previous defaults: Parakeet v3, no turn mode, no padding, no noise gate,
the base speaker model. Today's: Cohere for English, turn mode,
interjections, padding, noise gate, English speaker model. Same audio, an
Apple Silicon desktop, one run at a time, measured with `/usr/bin/time
-l`. Diarization isn't included.

| | Previous defaults | Today's defaults | Change |
|---|---|---|---|
| CPU per meeting minute, 22-min 1:1 | 5.6 s (9.3% of one core) | 11.8 s (19.7%) | 2.1× |
| CPU per meeting minute, 37-min group meeting | 6.3 s (10.5%) | 14.9 s (24.9%) | 2.4× |
| Transcription alone, % of audio length | 1.7–2.0% | 4.6–5.8% | about 2.8× |
| Peak memory | 2.9–3.4 GB | 6.0–6.2 GB | about +3 GB |

Almost all of the extra memory and most of the extra CPU is Cohere (a
~2.9 GB int8 model). Peak memory in both includes the replay holding the
whole recording. `model = "parakeet-v2-en"` keeps Parakeet's cost and
still fixes 33% of judged errors, against Cohere's 41% (bake-off above).

## Vocabulary (hotwords) with Parakeet: blocked upstream

sherpa-onnx can boost hotwords in Parakeet with `modified_beam_search`,
but only with the tokenizer settings Tomoe doesn't pass yet: without
`modeling_unit = "bpe"` and a `bpe_vocab`, every hotword fails to encode
and is skipped ("Some hotwords failed to encode"). A vocab generated from
the model's `tokens.txt` (each token with its negated ID as score) works:
on a clip where v3 heard a colleague's name as two common words, the
hotword fixed it at the default score of 1.5 and left the rest of the
sentence unchanged. Across meetings it couldn't be used: beam search for
TDT models in sherpa-onnx lacks the greedy decoder's guard for a blank
with zero duration (issue #3267, fix #3657 unmerged as of 1.13.8), and
with hotwords a replay spun for two hours on one utterance; beam search
alone was slightly worse than greedy (more dropped words, more
hallucinated "Yeah.").

## Reproducing

```bash
# Score a saved session or a replay against Teams' transcript
tomoe textdiff <session-id> --ref meeting.txt
tomoe session replay <session-id> --only-current --progress --asr-model <parakeet-dir> --out replay-v2
tomoe textdiff replay-v2/current.json --ref meeting.txt

# Bake-off: another model family (whisper, canary, moonshine, qwen3,
# funasr-nano, cohere, transducer); --single-pass so only it writes text
tomoe session replay <session-id> --only-current --single-pass --asr-kind whisper --asr-model <model-dir> --threads 4 --out replay-whisper

# Speaker accuracy with estimated word timings (models without timestamps)
tomoe tune <session-id> --ref meeting.txt --segments replay-whisper/current.json --spread-words

# Utterance cuts and turn mode; --turn-signals rebuilds the speaker-change
# signals the app had (diarizer, teams, both, none) from the saved session
tomoe session replay <session-id> --only-current --min-silence 1.0 --max-speech 10
tomoe session replay <session-id> --only-current --turn-mode=false
tomoe session replay <session-id> --only-current --turn-gap 3 --turn-signals none
```
