/* eslint-disable @typescript-eslint/no-explicit-any */
interface Window {
  runtime: {
    EventsOn(eventName: string, callback: (...data: any[]) => void): () => void;
    EventsEmit(eventName: string, ...data: any[]): void;
    EventsOff(eventName: string, ...additionalEventNames: string[]): void;
    WindowShow(): void;
    WindowHide(): void;
    Quit(): void;
  };
  go: {
    backend: {
      App: {
        InitStatus(): Promise<{ done: boolean; error?: string }>;
        ListAudioDevices(): Promise<any[]>;
        ListMonitorSources(): Promise<any[]>;
        SystemAudioMode(): Promise<string>;
        ListAudioSources(): Promise<any[]>;
        StartSession(mic: string, monitor: string, lang: string, platform: string): Promise<void>;
        GetAvailableLanguages(): Promise<string[]>;
        GetDefaultLanguage(): Promise<string>;
        StopSession(): Promise<any>;
        GetSessionList(): Promise<any[]>;
        LoadSession(id: string): Promise<any>;
        ExportSession(id: string, format: string): Promise<string>;
        UpdateSession(id: string, title: string, platform: string): Promise<void>;
        DeleteSession(id: string): Promise<void>;
        RetranscribeSession(id: string, lang: string): Promise<void>;
        GetConfig(): Promise<any>;
        GetGPUInfo(): Promise<any>;
        GetModelStatus(): Promise<any>;
        ApplySettings(cfg: any): Promise<any>;
        GetTools(): Promise<any[]>;
        FixTool(id: string): Promise<void>;
        CopyToClipboard(text: string): Promise<void>;
        GetVideoHintLooks(sessionId: string): Promise<any[]>;
        ListHintWindows(): Promise<any[]>;
        CurrentAudioSource(): Promise<string>;
        SetHintWindow(app: string): Promise<void>;
        GetLookThumb(sessionId: string, look: number): Promise<string>;
        SaveLookForAnalysis(sessionId: string, look: number): Promise<string>;
        RenameSpeaker(sessionId: string, label: string, name: string): Promise<void>;
        Voices(): Promise<any>;
        PlayVoiceClip(clip: any): Promise<string>;
        RenameVoice(model: string, id: string, name: string): Promise<void>;
        MergeVoices(model: string, into: string, from: string): Promise<void>;
        KeepVoicesApart(model: string, a: string, b: string): Promise<void>;
        ForgetVoice(model: string, id: string): Promise<void>;
        LearnVoicesFromSavedSessions(): Promise<number>;
        NotThem(sessionId: string, label: string): Promise<void>;
        RemoveVoiceMeeting(model: string, id: string, sessionId: string): Promise<void>;
        StartVoiceOver(model: string, id: string): Promise<void>;
        ConfirmVoice(model: string, id: string): Promise<void>;
        GetLookTile(sessionId: string, look: number): Promise<string>;
      };
    };
  };
}
