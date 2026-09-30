import { useEffect, useState, ReactNode } from 'react';
import { ApplyResult, Config, DeviceInfo } from '../types';

type Section = keyof Config;

// When a setting takes effect after Apply (see backend.App.ApplySettings).
type Applies = 'now' | 'next' | 'reload';
const APPLIES_LABEL: Record<Applies, string> = {
  now: 'Applies immediately',
  next: 'Used from the next recording',
  reload: 'Reloads the engines (a few seconds)',
};

const LANGUAGES: { code: string; name: string }[] = [
  { code: 'en', name: 'English' },
  { code: 'bn', name: 'Bengali' },
];

function clone(c: Config): Config {
  return JSON.parse(JSON.stringify(c));
}

function Row({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <div className="setting-row setting-row-edit">
      <div className="setting-label">
        <label>{label}</label>
        {hint && <span className="setting-hint">{hint}</span>}
      </div>
      <div className="setting-control">{children}</div>
    </div>
  );
}

function Group({ title, applies, action, children }: { title: string; applies: Applies; action?: ReactNode; children: ReactNode }) {
  return (
    <section className="settings-group">
      <div className="settings-group-header">
        <h3>{title}</h3>
        <span className={`applies-tag applies-${applies}`}>{APPLIES_LABEL[applies]}</span>
        {action}
      </div>
      {children}
    </section>
  );
}

// SettingsPanel edits config.toml. Apply saves it and applies it to the
// running app without a restart (see backend.App.ApplySettings); each group
// says when its settings take effect.
export default function SettingsPanel() {
  const [saved, setSaved] = useState<Config | null>(null);
  const [draft, setDraft] = useState<Config | null>(null);
  const [mode, setMode] = useState<string>('manual');
  const [inputs, setInputs] = useState<DeviceInfo[]>([]);
  const [monitors, setMonitors] = useState<DeviceInfo[]>([]);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [applying, setApplying] = useState(false);
  const [result, setResult] = useState<ApplyResult | null>(null);
  const [applyError, setApplyError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  async function load() {
    try {
      const app = window.go.backend.App;
      const cfg = (await app.GetConfig()) as Config;
      setSaved(clone(cfg));
      const d = clone(cfg);
      if (d.Multilingual.DefaultLang !== 'en') {
        // The default language always runs on the English model, so a
        // non-English default never worked; pick languages per recording.
        d.Multilingual.DefaultLang = 'en';
        if (!(d.Multilingual.Languages ?? []).includes('en')) {
          d.Multilingual.Languages = ['en', ...(d.Multilingual.Languages ?? [])];
        }
        setNotice(`Your default language was "${cfg.Multilingual.DefaultLang}". It's been switched to English here, because the default language always uses the English model; choose other languages per recording from the toolbar or tray. Apply to save.`);
      }
      setDraft(d);
      const m = await app.SystemAudioMode();
      setMode(m);
      setInputs(((await app.ListAudioDevices()) as DeviceInfo[]).filter(d => d.DeviceType === 0));
      if (m === 'manual') {
        setMonitors((await app.ListMonitorSources()) as DeviceInfo[]);
      }
      setLoadError(null);
    } catch (e) {
      setLoadError(String(e));
    }
  }

  useEffect(() => {
    load();
  }, []);

  if (loadError) {
    return (
      <div style={{ flex: 1, overflow: 'auto' }}>
        <div className="panel-header"><h2>Settings</h2></div>
        <div className="settings-panel">
          <p className="tool-result tool-result-error">Failed to load settings: {loadError}</p>
        </div>
      </div>
    );
  }
  if (!draft || !saved) {
    return <div style={{ flex: 1 }}><div className="panel-header"><h2>Settings</h2></div></div>;
  }

  const dirty = JSON.stringify(draft) !== JSON.stringify(saved);
  const mac = mode === 'auto';

  function set<S extends Section, K extends keyof Config[S]>(section: S, key: K, value: Config[S][K]) {
    setDraft(d => {
      if (!d) return d;
      const next = clone(d);
      next[section][key] = value;
      return next;
    });
    setResult(null);
    setApplyError(null);
  }

  function num<S extends Section, K extends keyof Config[S]>(section: S, key: K, step: number, min = 0) {
    const value = draft![section][key] as unknown as number;
    return (
      <input
        type="number"
        className="setting-input setting-input-num"
        step={step}
        min={min}
        value={Number.isFinite(value) ? value : ''}
        onChange={e => set(section, key, parseFloat(e.target.value) as Config[S][K])}
      />
    );
  }

  function text<S extends Section, K extends keyof Config[S]>(section: S, key: K, placeholder?: string) {
    return (
      <input
        type="text"
        className="setting-input"
        placeholder={placeholder}
        value={draft![section][key] as unknown as string}
        onChange={e => set(section, key, e.target.value as Config[S][K])}
      />
    );
  }

  function toggle<S extends Section, K extends keyof Config[S]>(section: S, key: K) {
    const value = draft![section][key] as unknown as boolean;
    return (
      <label className="setting-toggle">
        <input type="checkbox" checked={value} onChange={e => set(section, key, e.target.checked as Config[S][K])} />
        <span>{value ? 'On' : 'Off'}</span>
      </label>
    );
  }

  function toggleLanguage(code: string, on: boolean) {
    const langs = draft!.Multilingual.Languages ?? [];
    const next = on ? [...langs.filter(l => l !== code), code] : langs.filter(l => l !== code);
    // Keep the order stable (English first) so an unchanged set compares equal.
    set('Multilingual', 'Languages', LANGUAGES.map(l => l.code).filter(c => next.includes(c)));
  }

  // The speaker settings as they were before the macOS port (see the README's
  // "Restoring the pre-macOS-port pipeline").
  function usePrePortSpeakers() {
    setDraft(d => {
      if (!d) return d;
      const next = clone(d);
      next.Transcription.TwoPass = false;
      next.Meeting.SpeakerThreshold = 0.65;
      next.Meeting.StickyThresholdMargin = 0;
      next.Meeting.MinAssignDuration = 0;
      return next;
    });
    setResult(null);
    setApplyError(null);
  }

  async function apply() {
    setApplying(true);
    setApplyError(null);
    setResult(null);
    try {
      const r = (await window.go.backend.App.ApplySettings(draft)) as ApplyResult;
      setResult(r);
      const cfg = (await window.go.backend.App.GetConfig()) as Config;
      setSaved(clone(cfg));
      setDraft(clone(cfg));
    } catch (e) {
      setApplyError(String(e));
    } finally {
      setApplying(false);
    }
  }

  const monitorOptions = mac
    ? [
        { value: 'none', label: 'Mic only' },
        { value: 'everything', label: 'Mic + all system audio' },
      ]
    : [
        { value: '', label: 'Mic + default system audio' },
        { value: 'none', label: 'Mic only' },
        ...monitors.map(m => ({ value: m.Name, label: `Mic + ${m.Name}` })),
      ];
  const monitorValue = mac && draft.Meeting.MonitorDevice === '' ? 'none' : draft.Meeting.MonitorDevice;
  if (!monitorOptions.some(o => o.value === monitorValue)) {
    monitorOptions.push({ value: monitorValue, label: `${monitorValue} (not currently available)` });
  }
  const micOptions = [{ value: 'default', label: 'System default' }, ...inputs.map(d => ({ value: d.Name, label: d.Name }))];
  if (!micOptions.some(o => o.value === draft.Audio.Device)) {
    micOptions.push({ value: draft.Audio.Device, label: `${draft.Audio.Device} (not currently connected)` });
  }

  return (
    <div className="settings-view">
      <div className="panel-header">
        <h2>Settings</h2>
      </div>
      <div className="settings-panel settings-scroll">
        {notice && <p className="settings-notice">{notice}</p>}
        <Group title="Hotkeys" applies="now">
          <Row label="Dictation" hint="e.g. Super+Shift+S">{text('Hotkey', 'Binding')}</Row>
          <Row label="Meeting recording" hint="e.g. Super+Shift+X">{text('Hotkey', 'MeetingBinding')}</Row>
        </Group>

        <Group title="Audio" applies="next">
          <Row label="Microphone">
            <select className="setting-input" value={draft.Audio.Device} onChange={e => set('Audio', 'Device', e.target.value)}>
              {micOptions.map(o => <option key={o.value} value={o.value}>{o.label}</option>)}
            </select>
          </Row>
          <Row label="Hotkey meetings record" hint="Meetings started from the hotkey or tray">
            <select className="setting-input" value={monitorValue} onChange={e => set('Meeting', 'MonitorDevice', e.target.value)}>
              {monitorOptions.map(o => <option key={o.value} value={o.value}>{o.label}</option>)}
            </select>
          </Row>
        </Group>

        <Group title="Transcription" applies="reload">
          <Row label="Two-pass (English meetings)" hint="Live text while people speak, refined per utterance">{toggle('Transcription', 'TwoPass')}</Row>
          {!mac && <Row label="Use NVIDIA GPU" hint="Needs CUDA libraries (see Tools)">{toggle('Transcription', 'GPUEnabled')}</Row>}
          <Row label="Decoding method" hint="Beam search is needed for hotwords">
            <select className="setting-input" value={draft.Transcription.DecodingMethod} onChange={e => set('Transcription', 'DecodingMethod', e.target.value)}>
              <option value="greedy_search">Greedy (fastest)</option>
              <option value="modified_beam_search">Beam search</option>
            </select>
          </Row>
          {draft.Transcription.DecodingMethod === 'modified_beam_search' && (
            <>
              <Row label="Beam width">{num('Transcription', 'MaxActivePaths', 1, 1)}</Row>
              <Row label="Hotwords file" hint="One word or phrase per line; blank for none">{text('Transcription', 'HotwordsFile', '/path/to/hotwords.txt')}</Row>
              <Row label="Hotwords boost">{num('Transcription', 'HotwordsScore', 0.1)}</Row>
            </>
          )}
          <Row label="Model folder">{text('Transcription', 'ModelPath')}</Row>
        </Group>

        <Group title="Languages" applies="reload">
          <Row label="Multiple languages" hint="Offer per-language start options in the tray">{toggle('Multilingual', 'Enabled')}</Row>
          {draft.Multilingual.Enabled && (
            <Row label="Languages" hint="English is always the default">
              <div className="setting-checks">
                {LANGUAGES.map(l => (
                  <label key={l.code} className="setting-toggle">
                    <input
                      type="checkbox"
                      checked={(draft.Multilingual.Languages ?? []).includes(l.code)}
                      disabled={l.code === 'en'}
                      onChange={e => toggleLanguage(l.code, e.target.checked)}
                    />
                    <span>{l.name}</span>
                  </label>
                ))}
              </div>
            </Row>
          )}
        </Group>

        <Group title="Dictation output" applies="next">
          <Row label="Auto-paste into the focused app">{toggle('Output', 'AutoPaste')}</Row>
          <Row label="Copy to clipboard">{toggle('Output', 'Clipboard')}</Row>
          <Row label="Stop after silence (seconds)" hint="0 uses the 5 s default">{num('Output', 'SilenceTimeout', 0.5)}</Row>
        </Group>

        <Group
          title="Meeting speakers"
          applies="now"
          action={<button className="btn btn-secondary btn-sm" onClick={usePrePortSpeakers} title="Two-pass off, threshold 0.65, sticky and short-segment rules off">Use pre-port behavior</button>}
        >
          {!mac && <Row label="Auto-detect meetings" hint="Start recording when a call starts">{toggle('Meeting', 'AutoDetect')}</Row>}
          <Row label="Speaker match threshold" hint="Higher splits voices into more speakers (0–1)">{num('Meeting', 'SpeakerThreshold', 0.01)}</Row>
          <Row label="Sticky-speaker margin" hint="Near-misses joining the last speaker; 0 = off">{num('Meeting', 'StickyThresholdMargin', 0.01)}</Row>
          <Row label="Sticky-speaker window (seconds)">{num('Meeting', 'StickyGraceWindow', 0.5)}</Row>
          <Row label="Short-segment length (seconds)" hint="Shorter replies join the last speaker; 0 = off">{num('Meeting', 'MinAssignDuration', 0.1)}</Row>
          <Row label="Short-segment window (seconds)">{num('Meeting', 'ShortSegmentGraceWindow', 1)}</Row>
        </Group>

        {mac && (
          <Group title="Teams video hints" applies="next">
            <Row label="Check interval (seconds)">{num('Meeting', 'VideoHintPollInterval', 0.5, 0.5)}</Row>
            <Row label="Minimum gap between checks (seconds)">{num('Meeting', 'VideoHintTriggerDebounce', 0.1)}</Row>
          </Group>
        )}
      </div>

      <div className="settings-footer">
        <div className="settings-footer-status">
          {applyError && <span className="tool-result-error">{applyError}</span>}
          {result && !applyError && (
            <span>
              {result.applied?.length ? `Applied: ${result.applied.join(', ')}. ` : 'Saved. '}
              {result.later?.length ? `Next recording: ${result.later.join(', ')}. ` : ''}
              {result.warnings?.map(w => <span key={w} className="tool-result-error"> {w}</span>)}
            </span>
          )}
          {!result && !applyError && dirty && <span className="setting-hint">Unsaved changes</span>}
        </div>
        <button className="btn btn-secondary" disabled={!dirty || applying} onClick={() => { setDraft(clone(saved)); setApplyError(null); }}>
          Revert
        </button>
        <button className="btn btn-primary" disabled={!dirty || applying} onClick={apply}>
          {applying ? 'Applying…' : 'Apply'}
        </button>
      </div>
    </div>
  );
}
