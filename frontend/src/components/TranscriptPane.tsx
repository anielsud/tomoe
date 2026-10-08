import { useEffect, useRef, useState } from 'react';
import { Segment } from '../types';

interface Props {
  segments: Segment[];
  isRecording?: boolean;
  // Renames the speaker labeled label (click a speaker to rename).
  onRename?: (label: string, name: string) => void;
}

function formatTime(seconds: number): string {
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  const s = Math.floor(seconds % 60);
  return `${h.toString().padStart(2, '0')}:${m.toString().padStart(2, '0')}:${s.toString().padStart(2, '0')}`;
}

function speakerClass(speaker: string): string {
  // startsWith, not ===: once a video hint attaches a name, the label
  // becomes "Person 1 (Natalia Rami...)" — still the same speaker, and
  // should keep the same color.
  if (speaker === 'You') return 'you';
  if (speaker.startsWith('Person 1')) return 'person-1';
  if (speaker.startsWith('Person 2')) return 'person-2';
  if (speaker.startsWith('Person 3')) return 'person-3';
  return 'other';
}

// Consecutive lines by one speaker shown as one paragraph: the speech
// detector ends a line at every short pause, so one turn is often several
// lines. Mirrors session.Paragraphs on the Go side.
export function groupParagraphs(segments: Segment[]): Segment[][] {
  const groups: Segment[][] = [];
  for (const seg of segments) {
    const last = groups[groups.length - 1];
    if (last && last[0].speaker === seg.speaker) last.push(seg);
    else groups.push([seg]);
  }
  return groups;
}

export default function TranscriptPane({ segments, isRecording, onRename }: Props) {
  const endRef = useRef<HTMLDivElement>(null);
  const [renaming, setRenaming] = useState<{ id: string; label: string; name: string } | null>(null);

  function finishRename() {
    if (renaming && onRename && renaming.name.trim() && renaming.name.trim() !== renaming.label) {
      onRename(renaming.label, renaming.name.trim());
    }
    setRenaming(null);
  }

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
      {groupParagraphs(segments).map((group) => {
        const seg = group[0];
        return (
        <div key={seg.id} className="segment">
          <span className="timestamp">[{formatTime(seg.start_time)}]</span>
          {renaming?.id === seg.id ? (
            <input
              className="speaker-rename"
              value={renaming.name}
              autoFocus
              onChange={e => setRenaming({ ...renaming, name: e.target.value })}
              onKeyDown={e => {
                if (e.key === 'Enter') finishRename();
                if (e.key === 'Escape') setRenaming(null);
              }}
              onBlur={finishRename}
            />
          ) : (
            <span
              className={`speaker ${speakerClass(seg.speaker)} ${onRename && seg.speaker !== 'You' ? 'speaker-renamable' : ''}`}
              title={onRename && seg.speaker !== 'You' ? 'Click to name this speaker' : undefined}
              onClick={() => {
                if (onRename && seg.speaker !== 'You') {
                  setRenaming({ id: seg.id, label: seg.speaker, name: '' });
                }
              }}
            >
              {seg.speaker}:
            </span>
          )}
          {seg.language && seg.language !== 'en' && (
            <span className="lang-badge">{seg.language.toUpperCase()}</span>
          )}
          {group.map((line) => (
            <span key={line.id} className={line.status ? 'segment-pending' : undefined}>
              <span className="text">{line.text}</span>
              {line.status === 'live' && (
                <span className="refining-indicator" title="Still speaking — this line will keep growing">
                  listening…
                </span>
              )}
              {line.status === 'pending' && (
                <span className="refining-indicator" title="Still refining this line for accuracy">
                  refining…
                </span>
              )}{' '}
            </span>
          ))}
        </div>
        );
      })}
      <div ref={endRef} />
    </div>
  );
}
