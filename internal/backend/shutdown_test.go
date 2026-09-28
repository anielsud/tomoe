package backend

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Shutdown must not close the save queue while a StopSession that already
// claimed the session (from the tray, hotkey or meeting detector) has yet
// to enqueue its save: that send would panic on the closed channel.
func TestShutdownWaitsForInFlightStopSession(t *testing.T) {
	a := NewApp()
	a.saveWG.Add(1)
	go a.saveWorker()

	a.stopWG.Add(1) // a StopSession between claiming the session and enqueueing
	done := make(chan struct{})
	go func() {
		a.Shutdown(context.Background())
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("Shutdown returned while a StopSession was still in flight")
	case <-time.After(200 * time.Millisecond):
	}

	a.stopWG.Done()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown did not finish after the StopSession completed")
	}
}

func TestStartSessionRefusedDuringShutdown(t *testing.T) {
	a := NewApp()
	a.shuttingDown = true
	err := a.StartSession("default", "", "en", "")
	if err == nil || !strings.Contains(err.Error(), "shutting down") {
		t.Errorf("StartSession() error = %v, want a shutting-down error", err)
	}
}
