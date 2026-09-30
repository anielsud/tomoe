import { useEffect, useState } from 'react';
import { EventsOn } from '../../wailsjs/runtime/runtime';
import { ToolDone, ToolProgress, ToolStatus } from '../types';

const GROUP_ORDER = ['Command-line tools', 'Models', 'Permissions', 'GPU'];

// Every model row shares one "download missing models" fix, run as "models".
function fixId(tool: ToolStatus): string {
  return tool.id.startsWith('model-') ? 'models' : tool.id;
}

function formatMB(n: number): string {
  return `${(n / (1024 * 1024)).toFixed(0)} MB`;
}

// ToolsPanel lists every external dependency, model and permission Tomoe
// uses (see backend.App.GetTools), and offers a fix for anything missing:
// an in-app action (download, install, permission prompt) or a terminal
// command to copy when the fix needs sudo or a rebuild.
export default function ToolsPanel() {
  const [tools, setTools] = useState<ToolStatus[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [running, setRunning] = useState<Record<string, ToolProgress | null>>({});
  const [results, setResults] = useState<Record<string, ToolDone>>({});
  const [copied, setCopied] = useState<string | null>(null);

  async function load() {
    try {
      const list = await window.go.backend.App.GetTools();
      setTools(list as ToolStatus[]);
      setError(null);
    } catch (e) {
      setError(String(e));
    }
  }

  useEffect(() => {
    load();
    const offProgress = EventsOn('tools:progress', (p: ToolProgress) => {
      setRunning(r => ({ ...r, [p.id]: p }));
    });
    const offDone = EventsOn('tools:done', (d: ToolDone) => {
      setRunning(r => {
        const next = { ...r };
        delete next[d.id];
        return next;
      });
      setResults(r => ({ ...r, [d.id]: d }));
      load();
    });
    return () => {
      offProgress();
      offDone();
    };
  }, []);

  async function runFix(id: string) {
    setResults(r => {
      const next = { ...r };
      delete next[id];
      return next;
    });
    setRunning(r => ({ ...r, [id]: null }));
    try {
      await window.go.backend.App.FixTool(id);
    } catch (e) {
      setRunning(r => {
        const next = { ...r };
        delete next[id];
        return next;
      });
      setResults(r => ({ ...r, [id]: { id, error: String(e) } }));
    }
  }

  async function copy(command: string) {
    try {
      await window.go.backend.App.CopyToClipboard(command);
      setCopied(command);
      setTimeout(() => setCopied(c => (c === command ? null : c)), 2000);
    } catch (e) {
      setError(String(e));
    }
  }

  const missing = tools?.filter(t => !t.ok) ?? [];
  const missingRequired = missing.filter(t => t.required);
  const groups = tools
    ? GROUP_ORDER.filter(g => tools.some(t => t.group === g))
    : [];

  function renderProgress(id: string) {
    if (!(id in running)) return null;
    const p = running[id];
    const pct = p && p.total > 0 ? Math.min(100, Math.round((p.downloaded / p.total) * 100)) : null;
    return (
      <div className="tool-progress">
        <div className="tool-progress-text">
          {p ? p.message : 'Starting…'}
          {p && p.total > 0 && ` · ${formatMB(p.downloaded)} / ${formatMB(p.total)}`}
        </div>
        <div className="init-progress-track">
          <div
            className={`init-progress-fill ${pct === null ? 'init-progress-indeterminate' : ''}`}
            style={pct !== null ? { width: `${pct}%` } : undefined}
          />
        </div>
      </div>
    );
  }

  function renderResult(id: string) {
    const r = results[id];
    if (!r || (!r.error && !r.note)) return null;
    return <div className={r.error ? 'tool-result tool-result-error' : 'tool-result'}>{r.error ?? r.note}</div>;
  }

  function renderFix(tool: ToolStatus, showActionFix: boolean) {
    if (tool.ok || !tool.fix) return null;
    const id = fixId(tool);
    if (tool.fix.kind === 'command' && tool.fix.command) {
      return (
        <div className="tool-fix">
          <code className="tool-command">{tool.fix.command}</code>
          <button className="btn btn-secondary btn-sm" onClick={() => copy(tool.fix!.command!)}>
            {copied === tool.fix.command ? 'Copied' : tool.fix.label}
          </button>
        </div>
      );
    }
    if (!showActionFix) return null;
    return (
      <div className="tool-fix">
        <button className="btn btn-primary btn-sm" disabled={id in running} onClick={() => runFix(id)}>
          {id in running ? 'Working…' : tool.fix.label}
        </button>
      </div>
    );
  }

  return (
    <div style={{ flex: 1, overflow: 'auto' }}>
      <div className="panel-header">
        <h2>Tools</h2>
        <button className="btn btn-secondary btn-sm" onClick={load}>Refresh</button>
      </div>
      <div className="settings-panel">
        {error && <p className="tool-result tool-result-error">Failed to check tools: {error}</p>}
        {tools && (
          <p className={`tools-summary ${missingRequired.length ? 'tools-summary-bad' : missing.length ? 'tools-summary-warn' : ''}`}>
            {missing.length === 0
              ? 'Everything Tomoe uses is installed.'
              : `${missing.length} missing${missingRequired.length ? `, ${missingRequired.length} required to transcribe at all` : ''}.`}
          </p>
        )}
        {groups.map(group => {
          const rows = tools!.filter(t => t.group === group);
          // Models share one download action: show its button and
          // progress once, for the group.
          const sharedModelFix = group === 'Models' && rows.some(t => !t.ok && t.fix?.kind === 'action');
          return (
            <section key={group} className="tool-group">
              <div className="tool-group-header">
                <h3>{group}</h3>
                {sharedModelFix && (
                  <button className="btn btn-primary btn-sm" disabled={'models' in running} onClick={() => runFix('models')}>
                    {'models' in running ? 'Downloading…' : 'Download missing models'}
                  </button>
                )}
              </div>
              {group === 'Models' && renderProgress('models')}
              {group === 'Models' && renderResult('models')}
              {rows.map(tool => (
                <div key={tool.id} className="tool-row">
                  <span
                    className={`tool-status ${tool.ok ? 'tool-ok' : tool.required ? 'tool-missing-required' : 'tool-missing'}`}
                    title={tool.ok ? 'OK' : tool.required ? 'Missing (required)' : 'Missing'}
                  >
                    {tool.ok ? '✓' : '!'}
                  </span>
                  <div className="tool-body">
                    <div className="tool-name">
                      {tool.name}
                      {tool.required && <span className="tool-badge">required</span>}
                    </div>
                    <div className="tool-needed">{tool.neededFor}</div>
                    <div className="tool-detail">{tool.detail}</div>
                    {renderFix(tool, group !== 'Models')}
                    {group !== 'Models' && renderProgress(tool.id)}
                    {group !== 'Models' && renderResult(tool.id)}
                  </div>
                </div>
              ))}
            </section>
          );
        })}
      </div>
    </div>
  );
}
