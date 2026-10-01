package meetingaudio

// NoSource is the second-audio-source selection meaning "no system
// audio": record the mic only. It is what the GUI's "No System Audio"
// option sends, and config's meeting.monitor_device may be set to it.
// It is distinct from "", which on Linux means the default monitor
// source (see NewMonitorSource), so an explicit opt-out never gets
// replaced by that fallback.
const NoSource = "none"

// AutoSource is the selection that captures the meeting app (Teams, Zoom,
// Webex...) when one is making sound, and the whole system's audio
// otherwise, moving to the app once it starts: the macOS default, so
// notification sounds and music stay out of meeting transcripts.
const AutoSource = "auto"
