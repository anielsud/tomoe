import { useEffect, useState } from 'react';
import { EventsOn } from '../../wailsjs/runtime/runtime';
import { DeviceInfo, AudioSourceView } from '../types';

interface Props {
  devices: DeviceInfo[];
  monitors: DeviceInfo[];
  micDevice: string;
  monitorDevice: string;
  onMicChange: (device: string) => void;
  onMonitorChange: (device: string) => void;
  disabled: boolean;
  // "manual" (Linux: pick a PulseAudio monitor source, below) or
  // "auto" (macOS: pick from audioSources instead — a live list of
  // apps currently producing audio, plus an always-present
  // "Everything"; see ListAudioSources).
  systemAudioMode: 'manual' | 'auto';
  // macOS only (empty on Linux, where systemAudioMode is "manual" and
  // this isn't used) — see App.tsx's periodic refresh while this
  // picker is visible and not recording.
  audioSources: AudioSourceView[];
}

export default function SourceSelector({
  devices, monitors, micDevice, monitorDevice,
  onMicChange, onMonitorChange, disabled, systemAudioMode, audioSources,
}: Props) {
  // What "Meeting app (automatic)" is capturing in this recording:
  // everything until a meeting app makes sound, then just that app.
  const [capturing, setCapturing] = useState('');
  useEffect(() => {
    window.go?.backend?.App?.CurrentAudioSource().then(setCapturing).catch(() => {});
    const offSource = EventsOn('audio:source', (name: string) => setCapturing(name));
    const offStop = EventsOn('session:stopped', () => setCapturing(''));
    return () => { offSource(); offStop(); };
  }, []);
  return (
    <>
      <select
        value={micDevice}
        onChange={(e) => onMicChange(e.target.value)}
        disabled={disabled}
        title="Microphone"
      >
        <option value="">No Mic</option>
        <option value="default">
          Default Mic{(() => {
            const d = devices.find(d => d.DeviceType === 0 && d.IsDefault);
            return d ? ` (${d.Name})` : '';
          })()}
        </option>
        {devices
          .filter(d => d.DeviceType === 0)
          .map(d => (
            <option key={d.ID} value={d.Name}>
              {d.Name}{d.IsDefault ? ' *' : ''}
            </option>
          ))
        }
      </select>

      {systemAudioMode === 'auto' ? (
        <select
          value={monitorDevice}
          onChange={(e) => onMonitorChange(e.target.value)}
          disabled={disabled}
          title="System Audio — 'Meeting app (automatic)' (the default) captures the meeting (Teams, Zoom, Webex..., or Meet in a browser) once it makes sound, and the whole system until then, so notification sounds stay out of the transcript"
        >
          {/* "none", not "": the backend treats "" as "use the default source". */}
          <option value="none">No System Audio</option>
          {audioSources.map(s => (
            <option key={s.id} value={s.id}>
              {s.id === 'auto' && capturing ? `${s.name}: ${capturing}` : s.name}
            </option>
          ))}
        </select>
      ) : (
        <select
          value={monitorDevice}
          onChange={(e) => onMonitorChange(e.target.value)}
          disabled={disabled}
          title="System Audio"
        >
          <option value="none">No System Audio</option>
          {monitors.map(d => (
            <option key={d.ID} value={d.Name}>
              {d.Name}{d.IsDefault ? ' *' : ''}
            </option>
          ))}
        </select>
      )}
    </>
  );
}
