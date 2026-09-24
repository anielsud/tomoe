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
        ListAudioDevices(): Promise<any[]>;
        ListMonitorSources(): Promise<any[]>;
        SystemAudioMode(): Promise<string>;
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
        ListPendingVideoHints(): Promise<any[]>;
        GetVideoHintImage(id: string): Promise<string>;
        ApproveVideoHint(id: string): Promise<void>;
        DiscardVideoHint(id: string): Promise<void>;
      };
    };
  };
}
