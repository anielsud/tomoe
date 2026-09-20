package meeting

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Platform identifies the meeting application.
type Platform string

const (
	PlatformTeams   Platform = "Teams"
	PlatformMeet    Platform = "Meet"
	PlatformZoom    Platform = "Zoom"
	PlatformWebex   Platform = "Webex"
	PlatformSlack   Platform = "Slack"
	PlatformUnknown Platform = "Unknown"
)

// EventType distinguishes start from stop events.
type EventType int

const (
	MeetingStarted EventType = iota
	MeetingStopped
)

// MeetingEvent represents a detected meeting start or stop.
type MeetingEvent struct {
	Type     EventType
	Platform Platform
	// WindowTitle is a best-effort snapshot of the active window's title at
	// the moment MeetingStarted fires. Populated on X11 via xdotool; empty
	// on Wayland or when xdotool is unavailable. Used by calendar match
	// enrichment to extract meeting URLs. Not set on MeetingStopped.
	WindowTitle string
}

// streamInfo holds PulseAudio stream metadata.
type streamInfo struct {
	Index   uint32
	AppName string
	PID     int
}

// trackedMeeting represents an active meeting being monitored.
type trackedMeeting struct {
	pid      int
	platform Platform
}

// Detector monitors PulseAudio streams to detect meeting applications.
// It watches for simultaneous source-output (mic) and sink-input (speaker)
// from the same process, which reliably indicates an active meeting call.
type Detector struct {
	events chan MeetingEvent

	mu      sync.Mutex
	active  *trackedMeeting // currently tracked meeting (nil if none)
	stopped bool

	// pendingPID is set when a potential meeting is detected but
	// awaiting debounce confirmation.
	pendingPID  int
	pendingTime time.Time

	cancel context.CancelFunc
}

// NewDetector creates a new meeting detector.
func NewDetector() *Detector {
	return &Detector{
		events: make(chan MeetingEvent, 4),
	}
}

// Events returns a channel that emits MeetingStarted and MeetingStopped events.
func (d *Detector) Events() <-chan MeetingEvent {
	return d.events
}

// Start begins monitoring PulseAudio streams. Blocks until ctx is cancelled
// or Stop() is called. Safe to call from a goroutine.
func (d *Detector) Start(ctx context.Context) error {
	d.mu.Lock()
	ctx, d.cancel = context.WithCancel(ctx)
	d.mu.Unlock()

	if err := pulseInit(); err != nil {
		return fmt.Errorf("PulseAudio init failed: %w", err)
	}

	// Set this detector as the active instance for callbacks
	setActiveDetector(d)

	if err := pulseSubscribe(); err != nil {
		pulseCleanup()
		return fmt.Errorf("PulseAudio subscribe failed: %w", err)
	}

	// Start event loop in a goroutine
	go func() {
		pulseEventLoop(ctx)
		pulseCleanup()
	}()

	// Start periodic health check for tracked PIDs
	go d.healthCheckLoop(ctx)

	fmt.Println("meeting: detector started")
	return nil
}

// Stop stops the detector and cleans up resources.
func (d *Detector) Stop() {
	// Clear active detector synchronously to prevent a stale goroutine
	// from overwriting a future Start() call's setActiveDetector(d).
	setActiveDetector(nil)

	d.mu.Lock()
	d.stopped = true
	cancel := d.cancel
	d.mu.Unlock()

	// Wake the blocked pa_mainloop_iterate so the event loop goroutine
	// can check ctx.Done() and exit promptly.
	pulseQuit()

	if cancel != nil {
		cancel()
	}
}

// debounceDelay is the time to wait after detecting a potential meeting
// before confirming it. Apps may create/remove streams rapidly during init.
const debounceDelay = 2 * time.Second

// healthCheckInterval is how often we verify tracked PIDs still exist.
const healthCheckInterval = 30 * time.Second

// onSubscribeEvent is called from the PulseAudio subscribe callback.
// It runs on the PulseAudio event loop thread.
func (d *Detector) onSubscribeEvent(facility, eventType int, _ uint32) {
	d.mu.Lock()
	if d.stopped {
		d.mu.Unlock()
		return
	}
	d.mu.Unlock()

	switch {
	case isSourceOutputNew(facility, eventType):
		// New mic stream — check for meeting
		go d.checkForMeeting()
	case isSinkInputNew(facility, eventType):
		// New speaker stream — check for meeting (mic may have appeared first)
		go d.checkForMeeting()
	case isSourceOutputRemove(facility, eventType):
		// Mic stream removed — check if our tracked meeting ended
		go d.checkForMeetingEnd()
	case isSinkInputRemove(facility, eventType):
		// Speaker stream removed — also check for meeting end
		go d.checkForMeetingEnd()
	}
}

// checkForMeeting queries PulseAudio for source-outputs and sink-inputs,
// looking for a process that has both (indicating an active meeting).
func (d *Detector) checkForMeeting() {
	d.mu.Lock()
	if d.active != nil || d.stopped {
		d.mu.Unlock()
		return
	}
	d.mu.Unlock()

	sourceOutputs := pulseListSourceOutputs()
	sinkInputs := pulseListSinkInputs()

	// Build PID set from sink-inputs
	sinkPIDs := make(map[int]string) // PID -> appName
	for _, si := range sinkInputs {
		if si.PID > 0 {
			sinkPIDs[si.PID] = si.AppName
		}
	}

	// Find source-outputs that share a PID with a sink-input
	for _, so := range sourceOutputs {
		if so.PID <= 0 {
			continue
		}
		if _, hasSink := sinkPIDs[so.PID]; hasSink {
			// Found a process with both mic and speaker — potential meeting
			d.mu.Lock()
			if d.active != nil {
				d.mu.Unlock()
				return
			}

			// Debounce: if this is the same PID as a pending detection, check timing
			now := time.Now()
			if d.pendingPID == so.PID && now.Sub(d.pendingTime) >= debounceDelay {
				// Debounce passed — confirm meeting
				platform := identifyPlatform(so.AppName, so.PID)
				// Capture the window title while the meeting is still live —
				// used by calendar match enrichment to extract a meeting URL.
				// Best-effort; empty on Wayland or when xdotool is missing.
				windowTitle := getWindowTitleByPID(so.PID)
				d.active = &trackedMeeting{pid: so.PID, platform: platform}
				d.pendingPID = 0
				d.mu.Unlock()

				fmt.Printf("meeting: detected %s meeting (PID %d)\n", platform, so.PID)
				select {
				case d.events <- MeetingEvent{Type: MeetingStarted, Platform: platform, WindowTitle: windowTitle}:
				default:
					fmt.Println("meeting: event channel full, dropping start event")
				}
				return
			}

			if d.pendingPID == 0 || d.pendingPID != so.PID {
				// New potential meeting — start debounce.
				// Only schedule a goroutine if no pending PID exists.
				// If a different PID appears, update the pending state
				// but let the existing goroutine handle the recheck.
				needSchedule := d.pendingPID == 0
				d.pendingPID = so.PID
				d.pendingTime = now
				d.mu.Unlock()

				if needSchedule {
					go func() {
						time.Sleep(debounceDelay)
						d.checkForMeeting()
					}()
				}
				return
			}

			// Same PID, still within debounce window
			d.mu.Unlock()
			return
		}
	}
}

// checkForMeetingEnd verifies whether the tracked meeting is still active.
func (d *Detector) checkForMeetingEnd() {
	d.mu.Lock()
	if d.active == nil || d.stopped {
		d.mu.Unlock()
		return
	}
	trackedPID := d.active.pid
	platform := d.active.platform
	d.mu.Unlock()

	sourceOutputs := pulseListSourceOutputs()

	// Check if any source-output still belongs to the tracked PID
	for _, so := range sourceOutputs {
		if so.PID == trackedPID {
			return // still active
		}
	}

	// Meeting ended — no more mic streams from this PID
	d.mu.Lock()
	if d.active == nil || d.active.pid != trackedPID {
		d.mu.Unlock()
		return
	}
	d.active = nil
	d.mu.Unlock()

	fmt.Printf("meeting: %s meeting ended (PID %d)\n", platform, trackedPID)
	select {
	case d.events <- MeetingEvent{Type: MeetingStopped, Platform: platform}:
	default:
		fmt.Println("meeting: event channel full, dropping stop event")
	}
}

// healthCheckLoop periodically verifies the tracked meeting PID still exists.
func (d *Detector) healthCheckLoop(ctx context.Context) {
	ticker := time.NewTicker(healthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.mu.Lock()
			if d.active == nil {
				d.mu.Unlock()
				continue
			}
			pid := d.active.pid
			d.mu.Unlock()

			if !processExists(pid) {
				fmt.Printf("meeting: tracked PID %d no longer exists\n", pid)
				d.checkForMeetingEnd()
			}
		}
	}
}
