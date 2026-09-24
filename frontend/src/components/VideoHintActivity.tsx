import { useEffect, useRef, useState } from 'react';
import { EventsOn } from '../../wailsjs/runtime/runtime';
import { VideoHintActivityEntry } from '../types';

const MAX_ENTRIES = 50;

// stageIcon gives each pipeline stage a one-glyph visual identity, so a
// glance at the collapsed ticker (or a scan down the expanded log) shows
// at least as much as reading the stage name would.
function stageIcon(stage: string): string {
  switch (stage) {
    case 'window_not_found':
      return '…';
    case 'capture_failed':
      return '⚠';
    case 'frame_captured':
      return '📷';
    case 'no_rule':
      return '?';
    case 'ring_matched':
      return '◎';
    case 'no_ring_match':
      return '○';
    case 'no_label_region':
      return '?';
    case 'ocr_hit':
      return '✓';
    case 'ocr_miss':
      return '×';
    case 'escalated':
      return '⇪';
    default:
      return '•';
  }
}

function relativeTime(iso: string): string {
  const ms = Date.now() - new Date(iso).getTime();
  if (ms < 1000) return 'just now';
  if (ms < 60000) return `${Math.floor(ms / 1000)}s ago`;
  return `${Math.floor(ms / 60000)}m ago`;
}

export default function VideoHintActivity() {
  const [entries, setEntries] = useState<VideoHintActivityEntry[]>([]);
  const [expanded, setExpanded] = useState(false);
  const logRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (window.go?.backend?.App) {
      window.go.backend.App.GetVideoHintActivity()
        .then(existing => setEntries((existing || []).slice(-MAX_ENTRIES)))
        .catch(() => {
          // Method may not exist in older builds, or Linux (no video
          // hints there) -- an empty log is the correct fallback.
        });
    }

    const cancel = EventsOn('videohint:activity', (entry: VideoHintActivityEntry) => {
      setEntries(prev => [...prev, entry].slice(-MAX_ENTRIES));
    });
    return () => cancel();
  }, []);

  useEffect(() => {
    if (expanded) {
      logRef.current?.scrollTo({ top: logRef.current.scrollHeight });
    }
  }, [entries, expanded]);

  if (entries.length === 0) {
    return null;
  }

  const latest = entries[entries.length - 1];

  return (
    <div className={`videohint-activity ${expanded ? 'expanded' : ''}`}>
      <button
        className="videohint-activity-ticker"
        onClick={() => setExpanded(e => !e)}
        title="How and when the screen-based speaker-naming hint is operating"
      >
        <span className="videohint-chevron">{expanded ? '▾' : '▸'}</span>
        {latest.thumbnail ? (
          <img className="videohint-thumb videohint-thumb-sm" src={latest.thumbnail} alt="" />
        ) : (
          <span className="videohint-icon">{stageIcon(latest.stage)}</span>
        )}
        <span className="videohint-detail">{latest.detail}</span>
        <span className="videohint-time">{relativeTime(latest.time)}</span>
      </button>
      {expanded && (
        <div className="videohint-activity-log" ref={logRef}>
          {entries.map((entry, i) => (
            <div key={i} className={`videohint-activity-row stage-${entry.stage}`}>
              <span className="videohint-time">{new Date(entry.time).toLocaleTimeString()}</span>
              {entry.thumbnail ? (
                <img className="videohint-thumb" src={entry.thumbnail} alt="" />
              ) : (
                <span className="videohint-icon">{stageIcon(entry.stage)}</span>
              )}
              <span className="videohint-detail">{entry.detail}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
