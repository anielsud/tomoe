package meetingaudio

// NoSource is the second-audio-source selection meaning "no system
// audio": record the mic only. It is what the GUI's "No System Audio"
// option sends, and config's meeting.monitor_device may be set to it.
// It is distinct from "", which on Linux means the default monitor
// source (see NewMonitorSource), so an explicit opt-out never gets
// replaced by that fallback.
const NoSource = "none"
