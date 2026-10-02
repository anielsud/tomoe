import { useEffect, useMemo, useRef, useState } from 'react';
import { EventsOn } from '../../wailsjs/runtime/runtime';
import { LookView } from '../types';

// The hint timeline shows every look at the meeting window: the frame,
// the ring found (outlined), and the name read or why none was. Failures
// included, so what the hint layer does in a call can be watched live and
// odd frames saved for analysis.

const MAX_ROWS = 400;

const STAGE_LABEL: Record<string, string> = {
  window_not_found: 'no window',
  capture_failed: 'capture failed',
  frame_captured: 'captured',
  not_a_call: 'not a call',
  blank_capture: 'window captured black',
  ui_frozen: 'window not repainting',
  no_rule: 'no rule',
  ring_matched: 'ring',
  no_ring_match: 'no ring',
  ambiguous_ring: 'several tiles lit',
  speaker_view: 'speaker view',
  no_label_region: 'no label region',
  ocr_hit: 'name',
  ocr_miss: 'no name read',
};

function stageClass(l: LookView): string {
  if (l.usable) return 'hint-ok';
  if (l.stage === 'ambiguous_ring' || l.stage === 'no_ring_match' || l.stage === 'ocr_miss' || l.stage === 'blank_capture' || l.stage === 'ui_frozen') return 'hint-fail';
  return 'hint-idle';
}

function clock(seconds: number): string {
  const s = Math.max(0, Math.floor(seconds));
  return `${Math.floor(s / 60).toString().padStart(2, '0')}:${(s % 60).toString().padStart(2, '0')}`;
}

function api() {
  return window.go?.backend?.App;
}

// Thumb shows a look's thumbnail with its ring(s) outlined, loading the
// thumbnail from the session when the live event didn't carry it.
function Thumb({ look, sessionId, cache }: { look: LookView; sessionId: string; cache: Map<number, string> }) {
  const id = look.thumbOf || look.id;
  const [src, setSrc] = useState<string | undefined>(cache.get(id));
  useEffect(() => {
    if (src || !api()) return;
    api()!.GetLookThumb(sessionId, id).then(uri => {
      cache.set(id, uri);
      setSrc(uri);
    }).catch(() => {});
  }, [id, sessionId, src, cache]);
  if (!look.width) return <div className="hint-thumb hint-thumb-empty">{STAGE_LABEL[look.stage] || look.stage}</div>;
  const rings = look.ring ? [look.ring] : look.rings || [];
  return (
    <div className="hint-thumb">
      {src && <img src={src} alt="" />}
      <svg viewBox={`0 0 ${look.width} ${look.height}`} preserveAspectRatio="none">
        {rings.map((r, i) => (
          <rect key={i} x={r.X} y={r.Y} width={r.Width} height={r.Height}
            className={rings.length > 1 ? 'hint-ring-ambiguous' : 'hint-ring'} />
        ))}
      </svg>
    </div>
  );
}

interface WindowChoice {
  app: string;
  title: string;
  known: boolean;
}

// WindowPicker chooses which window video hints watch: the Teams meeting
// window (automatic), off, or any app's window. An app without a rule yet
// is still captured, so its frames can be saved for analysis.
export function WindowPicker() {
  const [value, setValue] = useState('');
  const [choices, setChoices] = useState<WindowChoice[]>([]);
  const [error, setError] = useState('');
  function refresh() {
    api()?.ListHintWindows().then(ws => setChoices(ws || [])).catch(() => {});
  }
  useEffect(() => {
    api()?.GetConfig().then((c: any) => setValue(c?.Meeting?.VideoHintWindow || '')).catch(() => {});
    refresh();
  }, []);
  const known = new Set(choices.map(c => c.app));
  return (
    <label className="hint-window" title="Which window to read the active speaker from">
      Watch
      <select
        className="setting-input"
        value={value}
        onFocus={refresh}
        onChange={e => {
          const v = e.target.value;
          setValue(v);
          setError('');
          api()?.SetHintWindow(v).catch(err => setError(String(err)));
        }}
      >
        <option value="">Teams meeting (automatic)</option>
        <option value="none">Off</option>
        {value && value !== 'none' && !known.has(value) && <option value={value}>{value} (not on screen)</option>}
        {choices.map(c => (
          <option key={c.app} value={c.app}>
            {c.app}{c.title ? ` — ${c.title.slice(0, 40)}` : ''}{c.known ? '' : ' (no rule yet: frames kept for analysis)'}
          </option>
        ))}
      </select>
      {error && <span className="hint-fail">{error}</span>}
    </label>
  );
}

interface Props {
  // The saved session to show; undefined for the current recording.
  sessionId?: string;
}

export default function HintTimeline({ sessionId }: Props) {
  const [looks, setLooks] = useState<LookView[]>([]);
  const [changesOnly, setChangesOnly] = useState(true);
  const [saved, setSaved] = useState<Record<number, string>>({});
  const cache = useRef(new Map<number, string>()).current;
  const sid = sessionId || '';

  useEffect(() => {
    setLooks([]);
    cache.clear();
    api()?.GetVideoHintLooks(sid).then(ls => setLooks(ls || [])).catch(() => {});
    if (sessionId) return;
    const cancel = EventsOn('videohint:look', (l: LookView) => {
      if (l.thumb) cache.set(l.id, l.thumb);
      setLooks(prev => [...prev, l]);
    });
    return () => cancel();
  }, [sid]);

  const summary = useMemo(() => {
    const count = (f: (l: LookView) => boolean) => looks.filter(f).length;
    return {
      total: looks.length,
      names: count(l => l.usable),
      noRing: count(l => l.stage === 'no_ring_match'),
      ambiguous: count(l => l.stage === 'ambiguous_ring'),
      noName: count(l => l.stage === 'ocr_miss'),
    };
  }, [looks]);

  const rows = useMemo(() => {
    const shown = changesOnly ? looks.filter(l => !l.thumbOf) : looks;
    return shown.slice(-MAX_ROWS).reverse();
  }, [looks, changesOnly]);

  async function save(l: LookView) {
    try {
      const path = await api()!.SaveLookForAnalysis(sid, l.id);
      setSaved(s => ({ ...s, [l.id]: path }));
    } catch (e) {
      setSaved(s => ({ ...s, [l.id]: `couldn't save: ${e}` }));
    }
  }

  return (
    <div className="hint-timeline">
      <div className="hint-summary">
        <span>{summary.total} looks</span>
        <span className="hint-ok">{summary.names} with a name</span>
        <span className="hint-fail">{summary.noRing} no ring</span>
        <span className="hint-fail">{summary.ambiguous} several tiles lit</span>
        <span className="hint-fail">{summary.noName} no name read</span>
        {!sessionId && <WindowPicker />}
        <label className="hint-filter">
          <input type="checkbox" checked={changesOnly} onChange={e => setChangesOnly(e.target.checked)} />
          changes only
        </label>
      </div>
      {rows.length === 0 && (
        <div className="empty-state">
          {sessionId ? 'No looks at the meeting window were recorded in this session.' : 'Looks at the meeting window appear here while recording.'}
        </div>
      )}
      <div className="hint-rows">
        {rows.map(l => (
          <div key={l.id} className={`hint-row ${stageClass(l)}`}>
            <Thumb look={l} sessionId={sid} cache={cache} />
            <div className="hint-info">
              <div className="hint-head">
                <span className="hint-time">{clock(l.sessionTime)}</span>
                <span className={`hint-stage ${stageClass(l)}`}>{STAGE_LABEL[l.stage] || l.stage}</span>
                {l.name && <span className="hint-name">{l.name}{l.fromCache ? ' (known tile)' : ''}</span>}
                {l.candidates && <span className="hint-name">{l.candidates.map(c => c || '?').join(' / ')}</span>}
                {l.window && <span className="hint-time">{l.window}</span>}
              </div>
              <div className="hint-detail">{l.detail}</div>
              {l.width > 0 && (
                <div className="hint-actions">
                  <button className="btn btn-secondary btn-sm" onClick={() => save(l)} disabled={!!saved[l.id]}>
                    Save for analysis
                  </button>
                  {saved[l.id] && <span className="hint-saved">{saved[l.id]}</span>}
                </div>
              )}
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}

// HintTicker is a one-line view of the latest look, for the live
// transcript; clicking it opens the timeline.
export function HintTicker({ onOpen }: { onOpen: () => void }) {
  const [latest, setLatest] = useState<LookView | null>(null);
  useEffect(() => {
    const cancel = EventsOn('videohint:look', (l: LookView) => setLatest(l));
    return () => cancel();
  }, []);
  if (!latest) return null;
  return (
    <button className={`hint-ticker ${stageClass(latest)}`} onClick={onOpen} title="Open the hint timeline">
      <span className="hint-time">{clock(latest.sessionTime)}</span>
      <span className="hint-stage">{STAGE_LABEL[latest.stage] || latest.stage}</span>
      <span className="hint-detail">{latest.name || latest.detail}</span>
    </button>
  );
}
