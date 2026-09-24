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
  };
  Output: {
    AutoPaste: boolean;
    Clipboard: boolean;
  };
  Multilingual: {
    Enabled: boolean;
    Languages: string[];
    DefaultLang: string;
  };
  Meeting: {
    DefaultSources: string;
    MonitorDevice: string;
    SpeakerThreshold: number;
    MaxSpeechDuration: number;
    MinSilenceDuration: number;
    AutoSave: boolean;
    AutoDetect: boolean;
  };
}

export interface GPUInfo {
  Available: boolean;
  Sufficient: boolean;
  Name: string;
  VRAMMB: number;
  CUDAVersion: string;
}

export interface ModelStatus {
  ParakeetReady: boolean;
  VADReady: boolean;
  SpeakerEmbeddingReady: boolean;
  LangIDReady: boolean;
  BengaliReady: boolean;
  ModelDir: string;
}
