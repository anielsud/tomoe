import { useEffect, useRef } from 'react';
import { Segment } from '../types';

interface Props {
  segments: Segment[];
  isRecording?: boolean;
}

function formatTime(seconds: number): string {
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  const s = Math.floor(seconds % 60);
  return `${h.toString().padStart(2, '0')}:${m.toString().padStart(2, '0')}:${s.toString().padStart(2, '0')}`;
}

function speakerClass(speaker: string): string {
  // startsWith, not ===: once a video hint attaches a name, the label
  // becomes "Person 1 (Nazanin Rame...)" — still the same speaker, and
  // should keep the same color.
  if (speaker === 'You') return 'you';
  if (speaker.startsWith('Person 1')) return 'person-1';
  if (speaker.startsWith('Person 2')) return 'person-2';
  if (speaker.startsWith('Person 3')) return 'person-3';
  return 'other';
}

export default function TranscriptPane({ segments, isRecording }: Props) {
  const endRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    endRef.current?.scrollIntoView({ behavior: 'smooth' });
  }, [segments]);

  if (segments.length === 0) {
    return (
      <div className="transcript-pane">
        <div className="empty-state">
          {isRecording
            ? 'Listening for speech...'
            : 'Select audio sources and click Start to begin transcription'}
        </div>
      </div>
    );
  }

  return (
    <div className="transcript-pane">
      {segments.map((seg) => (
        <div key={seg.id} className={`segment ${seg.status ? 'segment-pending' : ''}`}>
          <span className="timestamp">[{formatTime(seg.start_time)}]</span>
          <span className={`speaker ${speakerClass(seg.speaker)}`}>
            {seg.speaker}:
          </span>
          {seg.language && seg.language !== 'en' && (
            <span className="lang-badge">{seg.language.toUpperCase()}</span>
          )}
          <span className="text">{seg.text}</span>
          {seg.status === 'live' && (
            <span className="refining-indicator" title="Still speaking — this line will keep growing">
              listening…
            </span>
          )}
          {seg.status === 'pending' && (
            <span className="refining-indicator" title="Still refining this line for accuracy">
              refining…
            </span>
          )}
        </div>
      ))}
      <div ref={endRef} />
    </div>
  );
}
