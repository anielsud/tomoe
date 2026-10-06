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

## Vocabulary (hotwords): what's needed

sherpa-onnx can boost hotwords in Parakeet with `modified_beam_search`,
but only with the tokenizer settings Tomoe doesn't pass yet: without
`modeling_unit = "bpe"` and a `bpe_vocab`, every hotword fails to encode
and is skipped ("Some hotwords failed to encode"). A vocab generated from
the model's `tokens.txt` (each token with its negated ID as score) works:
on a clip where v3 heard a colleague's name as two common words, the
hotword fixed it at the default score of 1.5 and left the rest of the
sentence unchanged. Not yet measured across meetings.

## Reproducing

```bash
# Score a saved session or a replay against Teams' transcript
tomoe textdiff <session-id> --ref meeting.txt
tomoe session replay <session-id> --only-current --progress --asr-model <parakeet-dir> --out replay-v2
tomoe textdiff replay-v2/current.json --ref meeting.txt
```
