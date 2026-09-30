import { useEffect, useState } from 'react';
import { EventsOn } from '../../wailsjs/runtime/runtime';
import { InitProgress } from '../types';

function formatBytes(n: number): string {
  if (n <= 0) return '0 MB';
  return `${(n / (1024 * 1024)).toFixed(0)} MB`;
}

// InitScreen is shown from app launch until the backend's runInit
// finishes (see backend.App.runInit / appinit.EnsureInitialized): the
// same GPU-detect, config-generate, model-download flow the CLI's
// `tomoe`/`tomoe init` runs, driven here by init:progress/init:done/
// init:failed events rather than terminal output. Models are a one-time
// ~690MB download, so this is what a genuine first launch looks like;
// on every later launch these events fire and resolve near-instantly
// (each step's already-present check is a no-op), so this screen is
// only ever visible for a moment.
export default function InitScreen() {
  const [progress, setProgress] = useState<InitProgress | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const cancelProgress = EventsOn('init:progress', (p: InitProgress) => {
      setProgress(p);
    });
    const cancelFailed = EventsOn('init:failed', (message: string) => {
      setError(message);
    });
    // init:failed fires once and never replays, same as init:done (see
    // App's matching InitStatus check): runInit can fail before this
    // mounts (a malformed config.toml, no network), so ask for whatever
    // already happened too.
    window.go?.backend?.App?.InitStatus()
      .then(s => { if (s.error) setError(s.error); })
      .catch(() => {});
    return () => {
      cancelProgress();
      cancelFailed();
    };
  }, []);

  const pct = progress && progress.total > 0
    ? Math.min(100, Math.round((progress.downloaded / progress.total) * 100))
    : null;

  return (
    <div className="init-screen">
      <div className="init-card">
        <h1>Setting up Tomoe</h1>
        {error ? (
          <p className="init-error">
            Setup failed: {error}
            <br />
            Run <code>tomoe init</code> from a terminal to see full output, or check your network connection and restart Tomoe.
          </p>
        ) : (
          <>
            <p className="init-step">{progress ? progress.step : 'Checking models…'}</p>
            <div className="init-progress-track">
              <div
                className={`init-progress-fill ${pct === null ? 'init-progress-indeterminate' : ''}`}
                style={pct !== null ? { width: `${pct}%` } : undefined}
              />
            </div>
            <p className="init-detail">
              {progress && progress.total > 0
                ? `${formatBytes(progress.downloaded)} / ${formatBytes(progress.total)} (${pct}%)`
                : 'This only happens once — downloading the speech models (about 690MB).'}
            </p>
          </>
        )}
      </div>
    </div>
  );
}
