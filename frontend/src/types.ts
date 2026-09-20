export interface Segment {
  id: string;
  speaker: string;
  text: string;
  start_time: number;
  end_time: number;
  source: string;
  language?: string;
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
  // Optional serialization-time enrichment from the local calendar cache
  // (~/.local/state/tomoe/calendar/). Never part of session.json on disk.
  calendar_event?: CalendarEvent;
}

export interface CalendarParticipant {
  name?: string;
  email?: string;
  response_status?: string; // "accepted" | "declined" | "tentative" | "needsAction"
  is_organizer?: boolean;
}

export interface CalendarEvent {
  source: string;
  provider: string;
  event_id?: string;
  title: string;
  organizer?: CalendarParticipant;
  participants?: CalendarParticipant[];
  participant_count?: number;
  start_time: string;
  end_time: string;
  meeting_url?: string;
  matched_at: string;
  match_score?: number;
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
