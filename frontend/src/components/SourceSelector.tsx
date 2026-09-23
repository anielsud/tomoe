import { DeviceInfo } from '../types';

interface Props {
  devices: DeviceInfo[];
  monitors: DeviceInfo[];
  micDevice: string;
  monitorDevice: string;
  onMicChange: (device: string) => void;
  onMonitorChange: (device: string) => void;
  disabled: boolean;
  // "manual" (Linux: pick a PulseAudio monitor source below) or "auto"
  // (macOS: the second audio source is always auto-detected — the
  // active meeting window's audio via ScreenCaptureKit — so there's
  // nothing to pick; showing the manual picker's empty "No System
  // Audio" state here would misleadingly suggest nothing is captured).
  systemAudioMode: 'manual' | 'auto';
}

export default function SourceSelector({
  devices, monitors, micDevice, monitorDevice,
  onMicChange, onMonitorChange, disabled, systemAudioMode,
}: Props) {
  return (
    <>
      <select
        value={micDevice}
        onChange={(e) => onMicChange(e.target.value)}
        disabled={disabled}
        title="Microphone"
      >
        <option value="">No Mic</option>
        <option value="default">Default Mic</option>
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
        <span className="system-audio-auto" title="Automatically captures the active meeting window's audio (e.g. Microsoft Teams) via ScreenCaptureKit — no selection needed">
          System Audio: Auto-detect
        </span>
      ) : (
        <select
          value={monitorDevice}
          onChange={(e) => onMonitorChange(e.target.value)}
          disabled={disabled}
          title="System Audio"
        >
          <option value="">No System Audio</option>
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
