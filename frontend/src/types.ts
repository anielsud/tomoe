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
  status?: 'live' | 'pending' | 'removed' | '';
  // Which speaker.Tracker.Assign rule produced `speaker` for this
  // segment ("confident" | "sticky" | "short-segment" | "new-speaker"),
  // or "" for mic/system-audio (never audio-clustered) or when no
  // clustering ran. Diagnostic only -- see DiagnosticsPane; the normal
  // TranscriptPane ignores this field entirely.
  decision?: string;
}

// One look at the meeting window (backend.LookView).
export interface RingBox {
  X: number;
  Y: number;
  Width: number;
  Height: number;
  Confidence: number;
}

export interface LookView {
  id: number;
  time: string;
  sessionTime: number; // seconds into the session
  stage: string;
  detail: string;
  window?: string; // app (and window title) captured
  name?: string;
  fromCache?: boolean;
  usable: boolean;
  ring?: RingBox;
  rings?: RingBox[];
  candidates?: string[]; // names under each lit tile, when several were lit
  width: number;
  height: number;
  thumb?: string; // data URI, on live looks with their own thumbnail
  thumbOf?: number; // the look whose thumbnail this one shares
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
    // models.ASRModels ID, or "auto".
    Model: string;
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
    // DefaultSources and AutoSave exist in config.toml but nothing reads
    // them, and MaxSpeechDuration/MinSilenceDuration (utterance bounds for
    // meetings) are still being measured, so the settings page doesn't
    // offer them.
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
    VideoHintWindow: string; // "" Teams or Zoom meeting window, "none" off, else an app's name
    VideoHintLearnInterval: number;
    VideoHintCheckInterval: number;
    SplitOnSpeakerChange: boolean;
    // "auto" (English-trained for English, base otherwise) or a
    // models.SpeakerModels ID.
    SpeakerModel: string;
    // Diarize with Tomoe's own diarizer while recording instead of after
    // (see docs/speaker-pipeline-design.md).
    DiarizeDuringMeeting: boolean;
    DiarizeStride: number;
    DiarizeRecluster: number;
    RecordForTuning: boolean;
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
