# Speaker pipeline: a simpler design

Status: first version built, opt-in (`diarize_during_meeting`, off by
default; GUI app only, not yet the CLI daemon). It keeps the live
`speaker.Tracker` for the instant label and video-hint names, and replaces
the post-meeting pass. Proposal for the rest. Simulated on one reviewed meeting it matches the
post-meeting pass's accuracy (97.9%) with final labels ready when the
meeting ends, and labels as first shown (96.8–97.5%) beat every live
approach measured. Nothing here ships until it holds on more than one
reviewed meeting and is measured on a business-class machine. Measurements are in
[speaker-attribution-research.md](speaker-attribution-research.md).

## Why change it

Today a meeting's speakers are worked out twice, by two unrelated
algorithms:

1. **Live**: each utterance the speech detector closes gets one speaker
   fingerprint, which `speaker.Tracker` matches against running speaker
   averages. A threshold, a sticky-speaker rule and a short-segment rule
   patch over how unreliable one fingerprint per utterance is.
2. **After the meeting**: sherpa-onnx diarization runs over the whole
   recording (10 s windows every 1 s, a fingerprint per speaker per
   window, clustering), then a similar-speaker merge, then the transcript
   lines are relabeled and optionally split at speaker changes.

The two disagree, are tuned separately, and the second is where the
accuracy is (95.8–97.7% of words right, against 84–96% live) and where
the cost is (about 6 minutes per meeting-hour here, an estimated 20+ on a
business laptop, all after the meeting ends).

Most of what's been added to improve speakers (the live rules, utterance
length tuning, prefix labels, windows within utterances, line splitting)
works around one weakness: the live pass labels a whole utterance from
one fingerprint. Measured directly: labeling words from 2–3 s windows
inside utterances lifts the live guess from 95.9% to 97.2%, close to the
post-meeting pass, without any of the live rules doing the work.

## The design

One timeline of who spoke when, built during the meeting by one
algorithm; transcription independent of it.

1. **Transcription cuts audio for text alone.** The speech detector's
   utterances (0.5 s pause, up to 30 s, or longer) are sized for
   Parakeet's context. Words come with timestamps. Speaker labeling no
   longer constrains where audio is cut, so the trade-off between text
   accuracy and speaker accuracy measured in the utterance-length grid
   goes away.
2. **Segmentation runs continuously.** pyannote segmentation over 10 s
   windows every 1 s, as the post-meeting pass does now, but as audio
   arrives. It finds speaker changes with no pause and overlapping
   speech.
3. **Fingerprints per window-speaker**, every nth window to fit the CPU
   budget. Windows without a fingerprint still count how many people are
   talking.
4. **One clustering, rerun every 10–30 s** over all fingerprints so far,
   with cluster numbers matched to the previous run so a speaker keeps
   their number. The end of the meeting is the same step once more; there
   is no separate post-meeting pass.
5. **Each word takes the timeline's speaker at its time**, and a word in
   a gap where the timeline has nobody speaking takes its line's speaker
   (what line splitting does today). Without that rule about 4.5% of
   words land in gaps and go unlabeled. Speaker changes mid-line split the
   line as a consequence, not as a step. A
   word too recent for the timeline (about 10 s, plus the recluster
   interval) shows a provisional label from the latest speaker averages
   and is relabeled when the timeline catches up.
6. **Teams video hints name clusters**, as they do now.

### What it removes

- `speaker.Tracker`'s threshold, sticky-speaker and short-segment rules,
  and the per-utterance fingerprint.
- The post-meeting subprocess, sherpa-onnx diarization, the
  similar-speaker merge and line splitting as a separate step.
- Utterance length as a speaker setting.
- Live and final labels that disagree, and tuning the two separately.

### Settings left

The clustering threshold and merge similarity (per speaker model), the
fingerprint stride (CPU budget) and the recluster interval.

## What the first version does

- `diarize.Stream` takes the monitor audio as it's captured
  (`live.Config.MonitorAudio`), segments each 10 s window once it's
  complete, fingerprints every `diarize_stride`th one, and reclusters every
  `diarize_recluster` seconds of audio, keeping speaker numbers stable
  (`diarize.StableLabels`). Work runs on its own goroutine and catches up
  if it falls behind.
- `backend.meetingDiarizer` relabels the session's lines from each
  timeline and sends the changed lines to the transcript. A new line is
  labeled from the timeline if it already covers it, else from whichever
  timeline speaker its live label has mapped to so far.
- `Segment.LiveSpeaker` keeps the live label a line was relabeled from;
  timeline speakers are named from the video-hint names in those labels,
  as the post-meeting pass names its clusters.
- When the meeting stops the stream finishes its backlog, reclusters once
  more, applies the result (splitting lines if `split_on_speaker_change`
  is on), and saves the fingerprints as `diarization.gob` next to the
  session. If anything fails, the post-meeting pass runs as before.
- Settings come from the speaker model's profile (`StreamThreshold`,
  `StreamMerge` in `models.SpeakerModels`).

Not yet: removing the live tracker's rules, cutting utterances for text
alone, the CLI daemon, a cap for very long meetings, lower thread
priority.

## Costs and risks

- **The CPU moves into the meeting.** The same work as the post-meeting
  pass, spread over the meeting: about 15% of one machine here at full
  depth, perhaps 40–60% on a business laptop, alongside a video call.
  Mitigations: a larger fingerprint stride, a low-priority thread that
  may fall behind and catch up in pauses (only the end result has to be
  complete), slowing down on battery.
- **Confirmed labels arrive about 10 s late**, plus the recluster
  interval. The provisional label covers the gap.
- **Relabels.** A word's label can change after it's shown. Measured as
  the share of words relabeled; stable numbering keeps the rest steady.
- **Long meetings.** Clustering compares every fingerprint with every
  other: by the end of an hour (about 4,800 at full depth) one recluster
  takes 2.4 s here, 0.7 s at every 2nd window, and a three-hour meeting's
  comparison table is about 450 MB.
  Periodic reclusters need a cap (cluster recent windows, fold older ones
  into speaker averages) or a larger stride.
- **It depends on Tomoe's own diarizer** (`internal/diarize`), which
  scores better than sherpa-onnx on the reviewed meeting but doesn't
  reproduce sherpa's clusters exactly and hasn't shipped. It would start
  as an opt-in setting with today's pipeline the default.
- **Persist the fingerprints** with the session (a few MB per hour), so
  the final clustering can be rerun with other settings in seconds and a
  crash mid-meeting loses nothing.

## Open questions to measure

1. ~~Accuracy when first shown and at the end, relabel rate, delay, by
   recluster interval~~ and ~~fingerprint stride~~ and ~~clustering
   time~~: measured, see the research doc. Every 2nd window with a 10–30 s
   recluster looks like the starting point.
2. Why every 3rd window relabels so much more than every 2nd or 5th.
3. The provisional label for the newest ~10 s (from the latest speaker
   averages) and how often it's wrong.
4. CPU load on a business-class machine with a call running.
5. All of it on a second reviewed meeting.
