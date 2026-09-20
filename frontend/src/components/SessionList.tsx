import { useState, useEffect } from 'react';
import { EventsOn } from '../../wailsjs/runtime/runtime';
import { CalendarEvent, Session } from '../types';

interface Props {
  onExport: (sessionId: string) => void;
}

function formatDuration(seconds: number): string {
  const m = Math.floor(seconds / 60);
  const s = Math.floor(seconds % 60);
  return `${m}m${s.toString().padStart(2, '0')}s`;
}

const platformColors: Record<string, string> = {
  Teams: '#4b53bc',
  Meet: '#00897b',
  Zoom: '#2d8cff',
  Webex: '#07c160',
  Slack: '#611f69',
};

const platformOptions = ['', 'Teams', 'Meet', 'Zoom', 'Webex', 'Slack', 'Unknown'];

function PlatformBadge({ platform }: { platform?: string }) {
  if (!platform || platform === 'Unknown') return null;
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

function LanguageBadge({ language }: { language?: string }) {
  if (!language) return null;
  return (
    <span
      style={{
        display: 'inline-block',
        fontSize: 10,
        fontWeight: 600,
        padding: '1px 6px',
        borderRadius: 3,
        backgroundColor: '#666',
        color: '#fff',
        marginLeft: 6,
        verticalAlign: 'middle',
      }}
    >
      {language.toUpperCase()}
    </span>
  );
}

// CalendarChip surfaces the linked event on a session row. Rendered only
// when the backend attached a `calendar_event` via the local cache. Tooltip
// shows organizer + up to 5 attendees ("+N more").
function CalendarChip({ event }: { event?: CalendarEvent }) {
  if (!event) return null;
  const shown = (event.participants || []).slice(0, 5);
  const remaining = Math.max(0, (event.participant_count ?? (event.participants?.length ?? 0)) - shown.length);
  const parts: string[] = [];
  if (event.organizer?.name || event.organizer?.email) {
    parts.push(`Organizer: ${event.organizer.name || event.organizer.email}`);
  }
  if (shown.length > 0) {
    const list = shown.map(p => p.name || p.email || '').filter(Boolean).join(', ');
    parts.push(`Attendees: ${list}${remaining > 0 ? ` +${remaining} more` : ''}`);
  } else if ((event.participant_count ?? 0) > 0) {
    parts.push(`Attendees: ${event.participant_count}`);
  }
  parts.push(`Start: ${new Date(event.start_time).toLocaleString()}`);
  if (event.match_score) {
    parts.push(`Match score: ${event.match_score}`);
  }
  const tooltip = parts.join('\n');
  return (
    <span
      title={tooltip}
      style={{
        display: 'inline-block',
        fontSize: 10,
        fontWeight: 600,
        padding: '1px 6px',
        borderRadius: 3,
        backgroundColor: '#4a4a4a',
        color: '#fff',
        marginLeft: 6,
        verticalAlign: 'middle',
        maxWidth: 220,
        overflow: 'hidden',
        textOverflow: 'ellipsis',
        whiteSpace: 'nowrap',
      }}
    >
      📅 {event.title}
    </span>
  );
}

export default function SessionList({ onExport }: Props) {
  const [sessions, setSessions] = useState<Session[]>([]);
  const [expandedId, setExpandedId] = useState<string | null>(null);
  const [editingId, setEditingId] = useState<string | null>(null);
  const [editTitle, setEditTitle] = useState('');
  const [editPlatform, setEditPlatform] = useState('');
  const [languages, setLanguages] = useState<string[]>([]);
  const [retranscribing, setRetranscribing] = useState<string | null>(null);

  useEffect(() => {
    loadSessions();
    loadLanguages();

    // Refresh session list when a new session is saved or retranscribed
    const cancelSaved = EventsOn('session:saved', () => {
      loadSessions();
    });
    const cancelRetranscribed = EventsOn('session:retranscribed', () => {
      setRetranscribing(null);
      loadSessions();
    });
    const cancelError = EventsOn('session:retranscribe:error', () => {
      setRetranscribing(null);
    });
    return () => { cancelSaved(); cancelRetranscribed(); cancelError(); };
  }, []);

  async function loadSessions() {
    try {
      if (window.go?.backend?.App) {
        const list = await window.go.backend.App.GetSessionList();
        setSessions(list || []);
      }
    } catch (e) {
      console.error('Failed to load sessions:', e);
    }
  }

  async function loadLanguages() {
    try {
      if (window.go?.backend?.App) {
        const langs = await window.go.backend.App.GetAvailableLanguages();
        if (langs && langs.length > 1) {
          setLanguages(langs);
        }
      }
    } catch (e) {
      console.error('Failed to load languages:', e);
    }
  }

  async function handleRetranscribe(id: string, lang: string, e: React.MouseEvent) {
    e.stopPropagation();
    setRetranscribing(id);
    try {
      await window.go.backend.App.RetranscribeSession(id, lang);
    } catch (e) {
      console.error('Failed to retranscribe:', e);
      setRetranscribing(null);
    }
  }

  async function handleDelete(id: string, e: React.MouseEvent) {
    e.stopPropagation();
    try {
      await window.go.backend.App.DeleteSession(id);
      setSessions(prev => prev.filter(s => s.id !== id));
      if (expandedId === id) setExpandedId(null);
    } catch (e) {
      console.error('Failed to delete session:', e);
    }
  }

  function startEditing(sess: Session, e: React.MouseEvent) {
    e.stopPropagation();
    setEditingId(sess.id);
    setEditTitle(sess.title);
    setEditPlatform(sess.platform || '');
  }

  async function saveEdit(id: string) {
    try {
      await window.go.backend.App.UpdateSession(id, editTitle, editPlatform);
      setSessions(prev =>
        prev.map(s =>
          s.id === id ? { ...s, title: editTitle, platform: editPlatform || undefined } : s
        )
      );
    } catch (e) {
      console.error('Failed to update session:', e);
    }
    setEditingId(null);
  }

  function cancelEdit() {
    setEditingId(null);
  }

  function toggleExpand(id: string) {
    setExpandedId(expandedId === id ? null : id);
  }

  return (
    <div style={{ flex: 1, overflow: 'auto' }}>
      <div className="panel-header">
        <h2>Past Sessions</h2>
        <span className="session-count">{sessions.length} session{sessions.length !== 1 ? 's' : ''}</span>
      </div>

      {sessions.length === 0 ? (
        <div className="empty-state" style={{ height: 200 }}>
          No saved sessions yet
        </div>
      ) : (
        <div className="session-list">
          {sessions.map(sess => {
            const isExpanded = sess.id === expandedId;
            const isEditing = sess.id === editingId;
            return (
              <div key={sess.id} className={`accordion-item ${isExpanded ? 'expanded' : ''}`}>
                <div
                  className="accordion-header"
                  onClick={() => toggleExpand(sess.id)}
                >
                  <div className="accordion-chevron">{isExpanded ? '\u25BC' : '\u25B6'}</div>
                  <div className="accordion-info">
                    {isEditing ? (
                      <div
                        style={{ display: 'flex', flexDirection: 'column', gap: 4 }}
                        onClick={e => e.stopPropagation()}
                      >
                        <input
                          type="text"
                          value={editTitle}
                          onChange={e => setEditTitle(e.target.value)}
                          onKeyDown={e => {
                            if (e.key === 'Enter') saveEdit(sess.id);
                            if (e.key === 'Escape') cancelEdit();
                          }}
                          autoFocus
                          style={{
                            background: 'var(--bg-secondary)',
                            border: '1px solid var(--border)',
                            color: 'var(--text)',
                            borderRadius: 3,
                            padding: '2px 6px',
                            fontSize: 13,
                          }}
                        />
                        <div style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
                          <select
                            value={editPlatform}
                            onChange={e => setEditPlatform(e.target.value)}
                            style={{
                              background: 'var(--bg-secondary)',
                              border: '1px solid var(--border)',
                              color: 'var(--text)',
                              borderRadius: 3,
                              padding: '2px 4px',
                              fontSize: 11,
                            }}
                          >
                            {platformOptions.map(p => (
                              <option key={p} value={p}>
                                {p || '(none)'}
                              </option>
                            ))}
                          </select>
                          <button
                            className="btn btn-secondary btn-sm"
                            onClick={() => saveEdit(sess.id)}
                            style={{ fontSize: 11, padding: '1px 8px' }}
                          >
                            Save
                          </button>
                          <button
                            className="btn btn-secondary btn-sm"
                            onClick={cancelEdit}
                            style={{ fontSize: 11, padding: '1px 8px' }}
                          >
                            Cancel
                          </button>
                        </div>
                      </div>
                    ) : (
                      <>
                        <div className="session-title">
                          {sess.title}
                          <PlatformBadge platform={sess.platform} />
                          <LanguageBadge language={sess.language} />
                          <CalendarChip event={sess.calendar_event} />
                        </div>
                        <div className="session-meta">
                          {new Date(sess.created_at).toLocaleDateString()}
                          {sess.platform && sess.platform !== 'Unknown' && (
                            <> &middot; {sess.platform}</>
                          )}
                          {' '}&middot; {formatDuration(sess.duration)}
                          {' '}&middot; {sess.segments?.length || 0} segments
                        </div>
                      </>
                    )}
                  </div>
                  <div className="accordion-actions">
                    {!isEditing && (
                      <button
                        className="btn btn-secondary btn-sm"
                        onClick={(e) => startEditing(sess, e)}
                      >
                        Edit
                      </button>
                    )}
                    {languages.length > 0 && sess.audio_path && (
                      retranscribing === sess.id ? (
                        <span style={{ fontSize: 11, color: 'var(--text-muted)' }}>
                          Re-transcribing...
                        </span>
                      ) : (
                        languages.map(lang => (
                          <button
                            key={lang}
                            className="btn btn-secondary btn-sm"
                            onClick={(e) => handleRetranscribe(sess.id, lang, e)}
                            title={`Re-transcribe in ${lang.toUpperCase()}`}
                          >
                            Re-transcribe ({lang.toUpperCase()})
                          </button>
                        ))
                      )
                    )}
                    <button
                      className="btn btn-secondary btn-sm"
                      onClick={(e) => { e.stopPropagation(); onExport(sess.id); }}
                    >
                      Export
                    </button>
                    <button
                      className="btn btn-secondary btn-sm btn-danger"
                      onClick={(e) => handleDelete(sess.id, e)}
                    >
                      Delete
                    </button>
                  </div>
                </div>

                {isExpanded && (
                  <div className="accordion-body">
                    {(!sess.segments || sess.segments.length === 0) ? (
                      <div className="empty-state" style={{ height: 80, fontSize: 13 }}>
                        No audio detected in this session
                      </div>
                    ) : (
                      sess.segments.map(seg => (
                        <div key={seg.id} className="segment">
                          <span className="timestamp">
                            [{Math.floor(seg.start_time / 60)}:{(Math.floor(seg.start_time) % 60).toString().padStart(2, '0')}]
                          </span>
                          <span className={`speaker ${seg.speaker.toLowerCase().replace(/\s+/g, '-')}`}>
                            {seg.speaker}:
                          </span>
                          {seg.language && (
                            <span style={{ fontSize: 10, color: 'var(--text-muted)', marginRight: 4 }}>
                              [{seg.language}]
                            </span>
                          )}
                          <span className="text">{seg.text}</span>
                        </div>
                      ))
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
