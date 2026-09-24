import { useState, useEffect } from 'react';

interface VideoHintSummary {
  id: string;
  platform: string;
  windowTitle: string;
  windowOwner: string;
  width: number;
  height: number;
  timestamp: string;
  reason: string;
}

const platformColors: Record<string, string> = {
  Teams: '#4b53bc',
  Meet: '#00897b',
  Zoom: '#2d8cff',
  Webex: '#07c160',
  Slack: '#611f69',
};

function PlatformBadge({ platform }: { platform?: string }) {
  if (!platform) return null;
  const bg = platformColors[platform] || '#555';
  return (
    <span
      style={{
        display: 'inline-block',
        fontSize: 10,
        fontWeight: 600,
        padding: '1px 6px',
        borderRadius: 3,
        backgroundColor: bg,
        color: '#fff',
        marginLeft: 6,
        verticalAlign: 'middle',
      }}
    >
      {platform}
    </span>
  );
}

export default function VideoHintReview() {
  const [pending, setPending] = useState<VideoHintSummary[]>([]);
  const [expandedId, setExpandedId] = useState<string | null>(null);
  const [images, setImages] = useState<Record<string, string>>({});
  const [busyId, setBusyId] = useState<string | null>(null);

  useEffect(() => {
    load();
  }, []);

  async function load() {
    try {
      if (window.go?.backend?.App) {
        const list = await window.go.backend.App.ListPendingVideoHints();
        setPending(list || []);
      }
    } catch (e) {
      console.error('Failed to load pending video hints:', e);
    }
  }

  async function toggleExpand(id: string) {
    const next = expandedId === id ? null : id;
    setExpandedId(next);
    if (next && !images[next]) {
      try {
        const dataURI = await window.go.backend.App.GetVideoHintImage(next);
        setImages(prev => ({ ...prev, [next]: dataURI }));
      } catch (e) {
        console.error('Failed to load snapshot image:', e);
      }
    }
  }

  async function handleApprove(id: string, e: React.MouseEvent) {
    e.stopPropagation();
    setBusyId(id);
    try {
      await window.go.backend.App.ApproveVideoHint(id);
      setPending(prev => prev.filter(p => p.id !== id));
      if (expandedId === id) setExpandedId(null);
    } catch (e) {
      console.error('Failed to approve video hint:', e);
    }
    setBusyId(null);
  }

  async function handleDiscard(id: string, e: React.MouseEvent) {
    e.stopPropagation();
    setBusyId(id);
    try {
      await window.go.backend.App.DiscardVideoHint(id);
      setPending(prev => prev.filter(p => p.id !== id));
      if (expandedId === id) setExpandedId(null);
    } catch (e) {
      console.error('Failed to discard video hint:', e);
    }
    setBusyId(null);
  }

  return (
    <div style={{ flex: 1, overflow: 'auto' }}>
      <div className="panel-header">
        <h2>Pending Screenshots</h2>
        <span className="session-count">
          {pending.length} awaiting review
        </span>
      </div>

      <div style={{ padding: '8px 16px', fontSize: 12, color: 'var(--text-muted)' }}>
        Captured when meeting mode saw a meeting-app window it doesn't have a
        labeling rule for yet. Nothing here is used automatically — the
        matched window can be the wrong thing entirely (e.g. a chat tab, not
        an actual call). Approve only screenshots you've confirmed are
        actually from a meeting.
      </div>

      {pending.length === 0 ? (
        <div className="empty-state" style={{ height: 200 }}>
          No pending screenshots
        </div>
      ) : (
        <div className="session-list">
          {pending.map(p => {
            const isExpanded = p.id === expandedId;
            const isBusy = busyId === p.id;
            return (
              <div key={p.id} className={`accordion-item ${isExpanded ? 'expanded' : ''}`}>
                <div className="accordion-header" onClick={() => toggleExpand(p.id)}>
                  <div className="accordion-chevron">{isExpanded ? '▼' : '▶'}</div>
                  <div className="accordion-info">
                    <div className="session-title">
                      {p.windowTitle || '(no title captured)'}
                      <PlatformBadge platform={p.platform} />
                    </div>
                    <div className="session-meta">
                      {new Date(p.timestamp).toLocaleString()}
                      {' '}&middot; {p.width}x{p.height}
                      {' '}&middot; {p.reason}
                    </div>
                  </div>
                  <div className="accordion-actions">
                    <button
                      className="btn btn-secondary btn-sm"
                      disabled={isBusy}
                      onClick={(e) => handleApprove(p.id, e)}
                    >
                      Approve
                    </button>
                    <button
                      className="btn btn-secondary btn-sm btn-danger"
                      disabled={isBusy}
                      onClick={(e) => handleDiscard(p.id, e)}
                    >
                      Discard
                    </button>
                  </div>
                </div>

                {isExpanded && (
                  <div className="accordion-body">
                    {images[p.id] ? (
                      <img
                        src={images[p.id]}
                        alt={`Snapshot of ${p.windowOwner || p.platform}`}
                        style={{ maxWidth: '100%', borderRadius: 4, display: 'block' }}
                      />
                    ) : (
                      <div className="empty-state" style={{ height: 80, fontSize: 13 }}>
                        Loading image...
                      </div>
                    )}
                  </div>
                )}
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}
