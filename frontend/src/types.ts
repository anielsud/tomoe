export interface Segment {
  id: string;
  speaker: string;
  text: string;
  start_time: number;
  end_time: number;
  source: string;
  language?: string;
  // "live": the person is still talking, text will keep growing under
  // this same id. "pending": the utterance is done, text is pass 1's
  // unrefined result, and a slower higher-fidelity re-decode is in
  // flight. Absent/"" means final. See internal/live's two-pass
  // pipeline.
  status?: 'live' | 'pending' | '';
  // Which speaker.Tracker.Assign rule produced `speaker` for this
  // segment ("confident" | "sticky" | "short-segment" | "new-speaker"),
  // or "" for mic/system-audio (never audio-clustered) or when no
  // clustering ran. Diagnostic only -- see DiagnosticsPane; the normal
  // TranscriptPane ignores this field entirely.
  decision?: string;
}

export interface VideoHintActivityEntry {
  time: string;
  platform: string;
  stage: string;
  detail: string;
  name?: string;
  thumbnail?: string; // data URI, only set for stage "ocr_hit"
}

export interface Session {
  id: string;
  title: string;
  platform?: string;
  language?: string;
  created_at: string;
  ended_at?: string;
  duration: number;
  sources: string[];
  segments: Segment[];
  audio_path?: string;
}

export interface DeviceInfo {
  ID: string;
  Name: string;
  IsDefault: boolean;
  DeviceType: number; // 0=Input, 1=Monitor
}

// macOS's second-audio-source picker option (see ListAudioSources).
// "everything" is always present; every other id is a decimal PID.
export interface AudioSourceView {
  id: string;
  name: string;
}

// Wails serializes Go structs as JSON using Go field names (PascalCase)
// since Config uses `toml` tags, not `json` tags.
export interface Config {
  Hotkey: {
    Binding: string;
    MeetingBinding: string;
  };
  Audio: {
    Device: string;
  };
  Transcription: {
    GPUEnabled: boolean;
    ModelPath: string;
    HotwordsFile: string;
    HotwordsScore: number;
    DecodingMethod: string;
    MaxActivePaths: number;
    TwoPass: boolean;
  };
  Output: {
    AutoPaste: boolean;
    Clipboard: boolean;
    SilenceTimeout: number;
  };
  Multilingual: {
    Enabled: boolean;
    Languages: string[];
    DefaultLang: string;
  };
  Meeting: {
    // DefaultSources, MaxSpeechDuration, MinSilenceDuration and AutoSave
    // exist in config.toml but nothing reads them, so the settings page
    // doesn't offer them.
    DefaultSources: string;
    MonitorDevice: string;
    SpeakerThreshold: number;
    MaxSpeechDuration: number;
    MinSilenceDuration: number;
    AutoSave: boolean;
    AutoDetect: boolean;
    StickyGraceWindow: number;
    StickyThresholdMargin: number;
    MinAssignDuration: number;
    ShortSegmentGraceWindow: number;
    VideoHintPollInterval: number;
    VideoHintTriggerDebounce: number;
  };
}

// ApplySettings's result (see backend.ApplyResult).
export interface ApplyResult {
  applied: string[] | null;
  later: string[] | null;
  warnings: string[] | null;
}

// One dependency on the Tools page (see backend.ToolStatus).
export interface ToolFix {
  kind: 'action' | 'command';
  label: string;
  command?: string;
}

export interface ToolStatus {
  id: string;
  name: string;
  group: string;
  ok: boolean;
  required: boolean;
  neededFor: string;
  detail: string;
  fix?: ToolFix;
}

// "tools:progress" / "tools:done" event payloads (see backend.ToolProgressEvent/ToolDoneEvent).
export interface ToolProgress {
  id: string;
  message: string;
  downloaded: number;
  total: number;
}

export interface ToolDone {
  id: string;
  error?: string;
  note?: string;
}

export interface GPUInfo {
  Available: boolean;
  Sufficient: boolean;
  Name: string;
  VRAMMB: number;
  CUDAVersion: string;
}

// "init:progress" event payload (see backend.InitProgressEvent) — one
// step of first-run setup (see appinit.EnsureInitialized): generating
// config.toml, then downloading whichever models aren't already
// present. total is 0 until the response with Content-Length arrives.
export interface InitProgress {
  step: string;
  downloaded: number;
  total: number;
}

export interface ModelStatus {
  ParakeetReady: boolean;
  VADReady: boolean;
  SpeakerEmbeddingReady: boolean;
  LangIDReady: boolean;
  BengaliReady: boolean;
  ModelDir: string;
}
