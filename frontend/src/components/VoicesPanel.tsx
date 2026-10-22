import { useEffect, useRef, useState } from 'react';
import { NamedByVoice, UnknownVoice, VoiceClip, VoicePair, VoiceProfile, VoicesView } from '../types';

// A meeting whose voice is less like the profile's other meetings than this
// is flagged: across saved meetings, 95% of one person's meetings agree at
// 0.68 or more.
const AGREE_FLAG = 0.6;

// VoicesPanel manages the voice profiles (see backend.App.Voices): voices
// from recent meetings nobody has named ("Who's this?"), profiles that
// may be one person ("Same person?"), and every person Tomoe knows by
// voice, to play, rename or forget.
export default function VoicesPanel({ onSettings }: { onSettings: () => void }) {
  const [view, setView] = useState<VoicesView | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [note, setNote] = useState<string | null>(null);
  const [playing, setPlaying] = useState<string | null>(null);
  const audio = useRef<HTMLAudioElement | null>(null);

  async function load() {
    try {
      setView(await window.go.backend.App.Voices() as VoicesView);
      setError(null);
    } catch (e) {
      setError(String(e));
    }
  }

  useEffect(() => {
    load();
    return () => audio.current?.pause();
  }, []);

  async function play(key: string, clip: VoiceClip) {
    audio.current?.pause();
    if (playing === key) {
      setPlaying(null);
      return;
    }
    try {
      const url = await window.go.backend.App.PlayVoiceClip(clip);
      const a = new Audio(url);
      audio.current = a;
      setPlaying(key);
      a.onended = () => setPlaying(p => (p === key ? null : p));
      await a.play();
    } catch (e) {
      setPlaying(null);
      setError(`Couldn't play it: ${e}`);
    }
  }

  async function act(key: string, f: () => Promise<unknown>, done?: string) {
    setBusy(key);
    try {
      await f();
      setNote(done ?? null);
      await load();
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(null);
    }
  }

  function PlayButton({ id, clip }: { id: string; clip: VoiceClip }) {
    return (
      <button className="btn btn-secondary btn-sm voice-play" title="Play a few seconds" onClick={() => play(id, clip)}>
        {playing === id ? '■' : '▶'}
      </button>
    );
  }

  const byId: Record<string, VoiceProfile> = {};
  view?.profiles.forEach(p => { byId[`${p.model}/${p.id}`] = p; });

  return (
    <div style={{ flex: 1, overflow: 'auto' }}>
      <div className="panel-header">
        <h2>Voices</h2>
        <button
          className="btn btn-secondary btn-sm"
          disabled={busy !== null}
          onClick={() => act('learn', async () => {
            const n = await window.go.backend.App.LearnVoicesFromSavedSessions();
            return n;
          }, 'Learnt from every saved meeting.')}
        >
          {busy === 'learn' ? 'Learning…' : 'Learn from saved meetings'}
        </button>
        <button className="btn btn-secondary btn-sm" onClick={load}>Refresh</button>
      </div>
      <div className="settings-panel">
        {error && <p className="tool-result tool-result-error">{error}</p>}
        {note && <p className="tool-result">{note}</p>}
        {view && !view.enabled && (
          <p className="tools-summary tools-summary-warn">
            Voice profiles are off: meetings don't teach them and speakers aren't named from them.{' '}
            <button className="btn btn-secondary btn-sm" onClick={onSettings}>Settings</button>
          </p>
        )}
        {!view && !error && <p>Loading…</p>}

        {view && view.namedByVoice.length > 0 && (
          <section className="tool-group">
            <div className="tool-group-header"><h3>Named by voice</h3></div>
            <p className="tool-needed">Speakers in the last two weeks that a profile named, not the meeting window or you. Play them and say if they're right: either answer teaches the profile.</p>
            {view.namedByVoice.map(n => {
              const key = `n/${n.session}/${n.label}`;
              return (
                <NamedRow
                  key={key}
                  n={n}
                  busy={busy === key}
                  play={<PlayButton id={key} clip={n.clip} />}
                  onRight={() => act(key, () => window.go.backend.App.RenameSpeaker(n.session, n.label, n.name), `Confirmed ${n.name} in "${n.title}".`)}
                  onNotThem={() => act(key, () => window.go.backend.App.NotThem(n.session, n.label), `${n.name}'s profile won't name that voice again.`)}
                  onName={name => act(key, () => window.go.backend.App.RenameSpeaker(n.session, n.label, name), `Named them ${name}; ${n.name}'s profile won't name that voice again.`)}
                />
              );
            })}
          </section>
        )}

        {view && view.unknown.length > 0 && (
          <section className="tool-group">
            <div className="tool-group-header"><h3>Who's this?</h3></div>
            <p className="tool-needed">Voices from the last two weeks that nobody has named. Naming one names them in that meeting and teaches their profile.</p>
            {view.unknown.map(u => (
              <UnknownRow
                key={`${u.session}/${u.label}`}
                u={u}
                play={<PlayButton id={`u/${u.session}/${u.label}`} clip={u.clip} />}
                busy={busy === `u/${u.session}/${u.label}`}
                onName={name => act(`u/${u.session}/${u.label}`, () => window.go.backend.App.RenameSpeaker(u.session, u.label, name), `Named ${u.label} in "${u.title}" ${name}.`)}
              />
            ))}
          </section>
        )}

        {view && view.pairs.length > 0 && (
          <section className="tool-group">
            <div className="tool-group-header"><h3>Same person?</h3></div>
            <p className="tool-needed">These profiles sound alike. One person under two names, or a name the meeting window misread?</p>
            {view.pairs.map(pr => {
              const a = byId[`${pr.model}/${pr.a}`];
              const b = byId[`${pr.model}/${pr.b}`];
              if (!a || !b) return null;
              const key = `p/${pr.a}/${pr.b}`;
              return (
                <PairRow
                  key={key}
                  pair={pr}
                  a={a}
                  b={b}
                  busy={busy === key}
                  playA={<PlayButton id={`${key}/a`} clip={a.clip} />}
                  playB={<PlayButton id={`${key}/b`} clip={b.clip} />}
                  onSame={(into, from) => act(key, () => window.go.backend.App.MergeVoices(pr.model, into.id, from.id), `${from.name} is now part of ${into.name}.`)}
                  onApart={() => act(key, () => window.go.backend.App.KeepVoicesApart(pr.model, pr.a, pr.b))}
                />
              );
            })}
          </section>
        )}

        {view && (
          <section className="tool-group">
            <div className="tool-group-header"><h3>People ({view.profiles.length})</h3></div>
            {view.profiles.length === 0 && (
              <p className="tool-needed">
                No profiles yet. They're learnt from meetings where a person's own tile was lit, and from speakers you name.
                Learn from saved meetings to start with the people you've already met.
              </p>
            )}
            {view.profiles.map(p => (
              <ProfileRow
                key={`${p.model}/${p.id}`}
                p={p}
                busy={busy === `r/${p.id}`}
                play={<PlayButton id={`r/${p.id}`} clip={p.clip} />}
                onRename={name => act(`r/${p.id}`, () => window.go.backend.App.RenameVoice(p.model, p.id, name))}
                onStartOver={() => act(`r/${p.id}`, () => window.go.backend.App.StartVoiceOver(p.model, p.id), `${p.name} starts over: learnt from new meetings only.`)}
                onRemoveMeeting={sid => act(`r/${p.id}`, () => window.go.backend.App.RemoveVoiceMeeting(p.model, p.id, sid), `Took that meeting out of ${p.name}.`)}
                playClip={(key, clip) => <PlayButton id={key} clip={clip} />}
                onForget={() => act(`r/${p.id}`, () => window.go.backend.App.ForgetVoice(p.model, p.id), `Forgot ${p.name}.`)}
              />
            ))}
          </section>
        )}
      </div>
    </div>
  );
}

function when(iso: string): string {
  const d = new Date(iso);
  return isNaN(d.getTime()) ? '' : d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
}

function minutes(s: number): string {
  return s >= 60 ? `${Math.round(s / 60)} min` : `${Math.round(s)} s`;
}

function UnknownRow({ u, play, busy, onName }: { u: UnknownVoice; play: JSX.Element; busy: boolean; onName: (name: string) => void }) {
  const [name, setName] = useState('');
  return (
    <div className="tool-row voice-row">
      {play}
      <div className="tool-body">
        <div className="tool-name">{u.label} <span className="voice-meta">in {u.title} · {when(u.at)} · spoke {minutes(u.seconds)}</span></div>
        {u.soundsLike && (
          <div className="tool-detail">
            {u.similarity! >= 0.7 ? 'Sounds like' : 'Sounds a little like'} {u.soundsLike} ({Math.round(u.similarity! * 100)}% alike){' '}
            <button className="btn btn-secondary btn-sm" disabled={busy} onClick={() => onName(u.soundsLike!)}>It's {u.soundsLike}</button>
          </div>
        )}
      </div>
      <form className="voice-name" onSubmit={e => { e.preventDefault(); if (name.trim()) onName(name.trim()); }}>
        <input className="setting-input" value={name} placeholder="Who is it?" onChange={e => setName(e.target.value)} disabled={busy} />
        <button className="btn btn-primary btn-sm" type="submit" disabled={busy || !name.trim()}>Name</button>
      </form>
    </div>
  );
}

function PairRow({ pair, a, b, busy, playA, playB, onSame, onApart }: {
  pair: VoicePair; a: VoiceProfile; b: VoiceProfile; busy: boolean; playA: JSX.Element; playB: JSX.Element;
  onSame: (into: VoiceProfile, from: VoiceProfile) => void; onApart: () => void;
}) {
  return (
    <div className="tool-row voice-row">
      <div className="tool-body">
        <div className="tool-name">
          {playA} {a.name} <span className="voice-meta">({a.meetings} meetings)</span>
          {'  ·  '}
          {playB} {b.name} <span className="voice-meta">({b.meetings} meetings)</span>
        </div>
        <div className="tool-detail">{Math.round(pair.similarity * 100)}% alike</div>
      </div>
      <div className="voice-actions">
        <button className="btn btn-secondary btn-sm" disabled={busy} onClick={() => onSame(a, b)}>Same: {a.name}</button>
        <button className="btn btn-secondary btn-sm" disabled={busy} onClick={() => onSame(b, a)}>Same: {b.name}</button>
        <button className="btn btn-secondary btn-sm" disabled={busy} onClick={onApart}>Different people</button>
      </div>
    </div>
  );
}

function NamedRow({ n, play, busy, onRight, onNotThem, onName }: {
  n: NamedByVoice; play: JSX.Element; busy: boolean; onRight: () => void; onNotThem: () => void; onName: (name: string) => void;
}) {
  const [naming, setNaming] = useState(false);
  const [name, setName] = useState('');
  return (
    <div className="tool-row voice-row">
      {play}
      <div className="tool-body">
        <div className="tool-name">{n.name} <span className="voice-meta">as {n.label.replace(/ \(.*\)$/, '')} in {n.title} · {when(n.at)} · spoke {minutes(n.seconds)}</span></div>
        <div className="tool-detail">{Math.round(n.similarity * 100)}% like {n.name}'s profile (70% needed)</div>
      </div>
      {naming ? (
        <form className="voice-name" onSubmit={e => { e.preventDefault(); if (name.trim()) onName(name.trim()); }}>
          <input className="setting-input" value={name} autoFocus placeholder="Who is it?" onChange={e => setName(e.target.value)} disabled={busy} />
          <button className="btn btn-primary btn-sm" type="submit" disabled={busy || !name.trim()}>Name</button>
          <button className="btn btn-secondary btn-sm" type="button" onClick={() => setNaming(false)}>Cancel</button>
        </form>
      ) : (
        <div className="voice-actions">
          <button className="btn btn-secondary btn-sm" disabled={busy} onClick={onRight}>Right</button>
          <button className="btn btn-secondary btn-sm" disabled={busy} onClick={onNotThem}>Not them</button>
          <button className="btn btn-secondary btn-sm" disabled={busy} onClick={() => setNaming(true)}>It's someone else…</button>
        </div>
      )}
    </div>
  );
}

function ProfileRow({ p, play, busy, onRename, onForget, onStartOver, onRemoveMeeting, playClip }: {
  p: VoiceProfile; play: JSX.Element; busy: boolean; onRename: (name: string) => void; onForget: () => void;
  onStartOver: () => void; onRemoveMeeting: (session: string) => void; playClip: (key: string, clip: VoiceClip) => JSX.Element;
}) {
  const [editing, setEditing] = useState(false);
  // The app's window shows no browser dialogs (confirm() answers no), so
  // confirmations are in the row.
  const [confirming, setConfirming] = useState<'forget' | 'startOver' | null>(null);
  const [removing, setRemoving] = useState<string | null>(null);
  const [open, setOpen] = useState(false);
  const [name, setName] = useState(p.name);
  const from = [p.fromTiles ? `own tile in ${p.fromTiles}` : '', p.fromNamed ? `named by you in ${p.fromNamed}` : ''].filter(Boolean).join(', ');
  const flagged = p.heard.filter(h => p.heard.length > 1 && h.agreement < AGREE_FLAG).length;
  return (
    <div className="voice-profile">
      <div className="tool-row voice-row">
        {play}
        <div className="tool-body">
          {editing ? (
            <form className="voice-name" onSubmit={e => { e.preventDefault(); if (name.trim()) { onRename(name.trim()); setEditing(false); } }}>
              <input className="setting-input" value={name} autoFocus onChange={e => setName(e.target.value)} />
              <button className="btn btn-primary btn-sm" type="submit" disabled={busy || !name.trim()}>Save</button>
              <button className="btn btn-secondary btn-sm" type="button" onClick={() => { setName(p.name); setEditing(false); }}>Cancel</button>
            </form>
          ) : (
            <div className="tool-name">
              {p.name}
              {flagged > 0 && <span className="voice-flag" title="A meeting sounds unlike the rest: open the meetings to check">{flagged} to check</span>}
            </div>
          )}
          <div className="tool-detail">
            {p.startedOver
              ? 'Started over: learns from new meetings'
              : <>{p.meetings} {p.meetings === 1 ? 'meeting' : 'meetings'} ({from}) · last heard {when(p.lastHeard)}</>}
            {p.rejected > 0 && <> · {p.rejected} {p.rejected === 1 ? 'voice' : 'voices'} you said aren't them</>}
            {p.heard.length > 0 && (
              <> · <button className="link-button" onClick={() => setOpen(!open)}>{open ? 'Hide meetings' : 'Meetings'}</button></>
            )}
          </div>
        </div>
        {!editing && !confirming && (
          <div className="voice-actions">
            <button className="btn btn-secondary btn-sm" disabled={busy} onClick={() => setEditing(true)}>Rename</button>
            <button className="btn btn-secondary btn-sm" disabled={busy || p.startedOver} onClick={() => setConfirming('startOver')}>Start over</button>
            <button className="btn btn-danger btn-sm" disabled={busy} onClick={() => setConfirming('forget')}>Forget</button>
          </div>
        )}
        {confirming === 'forget' && (
          <div className="voice-actions">
            <span className="voice-meta">Forget for good? This name won't be learnt again unless you name someone it.</span>
            <button className="btn btn-danger btn-sm" disabled={busy} onClick={() => { setConfirming(null); onForget(); }}>Forget</button>
            <button className="btn btn-secondary btn-sm" onClick={() => setConfirming(null)}>Keep</button>
          </div>
        )}
        {confirming === 'startOver' && (
          <div className="voice-actions">
            <span className="voice-meta">Drop what's learnt so far and learn from new meetings only?</span>
            <button className="btn btn-danger btn-sm" disabled={busy} onClick={() => { setConfirming(null); onStartOver(); }}>Start over</button>
            <button className="btn btn-secondary btn-sm" onClick={() => setConfirming(null)}>Keep</button>
          </div>
        )}
      </div>
      {open && (
        <div className="voice-meetings">
          {p.heard.map(h => {
            const odd = p.heard.length > 1 && h.agreement < AGREE_FLAG;
            return (
              <div key={h.session} className="tool-row voice-row">
                {playClip(`m/${p.id}/${h.session}`, h.clip)}
                <div className="tool-body">
                  <div className="tool-name">{h.title || h.session.slice(0, 8)} <span className="voice-meta">{when(h.at)} · {h.source === 'named' ? 'named by you' : 'own tile'} · {h.fingerprints} fingerprints</span></div>
                  <div className={`tool-detail ${odd ? 'voice-odd' : ''}`}>
                    {p.heard.length > 1
                      ? <>{Math.round(h.agreement * 100)}% like the other meetings{odd ? ': may be someone else' : ''}</>
                      : 'Only meeting'}
                  </div>
                </div>
                {removing === h.session ? (
                  <div className="voice-actions">
                    <span className="voice-meta">Take it out for good?</span>
                    <button className="btn btn-danger btn-sm" disabled={busy} onClick={() => { setRemoving(null); onRemoveMeeting(h.session); }}>Take out</button>
                    <button className="btn btn-secondary btn-sm" onClick={() => setRemoving(null)}>Keep</button>
                  </div>
                ) : (
                  <div className="voice-actions">
                    <button className="btn btn-secondary btn-sm" disabled={busy} onClick={() => setRemoving(h.session)}>Take out</button>
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
