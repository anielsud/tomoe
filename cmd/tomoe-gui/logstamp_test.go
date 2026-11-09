package main

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func fixedClock() func() time.Time {
	t := time.Date(2026, 11, 9, 9, 30, 0, 0, time.UTC)
	return func() time.Time { return t }
}

func TestStampLines(t *testing.T) {
	var b strings.Builder
	stampLines(strings.NewReader("\n===== Tomoe started =====\nmeeting: detected Teams meeting (PID 7)\nno newline"), &b, fixedClock())
	want := "2026-11-09 09:30:00.000 \n" +
		"2026-11-09 09:30:00.000 ===== Tomoe started =====\n" +
		"2026-11-09 09:30:00.000 meeting: detected Teams meeting (PID 7)\n" +
		"2026-11-09 09:30:00.000 no newline"
	if b.String() != want {
		t.Errorf("got\n%q\nwant\n%q", b.String(), want)
	}
}

// A line printed in pieces is stamped once, where it starts.
func TestStampLinesPieces(t *testing.T) {
	pr, pw := io.Pipe()
	var b strings.Builder
	done := make(chan struct{})
	go func() { stampLines(pr, &b, fixedClock()); close(done) }()
	for _, p := range []string{"session 1: ", "saving", "\n", "next\n"} {
		_, _ = pw.Write([]byte(p))
	}
	pw.Close()
	<-done
	want := "2026-11-09 09:30:00.000 session 1: saving\n2026-11-09 09:30:00.000 next\n"
	if b.String() != want {
		t.Errorf("got %q, want %q", b.String(), want)
	}
}

type failingWriter struct{ n int }

func (f *failingWriter) Write(p []byte) (int, error) { f.n++; return 0, errors.New("disk full") }

// A failing log file must not stop the reading, or every print would block.
func TestStampLinesKeepsReadingAfterWriteErrors(t *testing.T) {
	w := &failingWriter{}
	stampLines(strings.NewReader(strings.Repeat("line\n", 1000)), w, fixedClock())
	if w.n != 2000 {
		t.Errorf("%d writes, want 2000 (a stamp and a line each)", w.n)
	}
}

// A line longer than the buffer is passed through whole, stamped once.
func TestStampLinesLongLine(t *testing.T) {
	long := strings.Repeat("x", 200<<10)
	var b strings.Builder
	stampLines(strings.NewReader(long+"\nend\n"), &b, fixedClock())
	want := "2026-11-09 09:30:00.000 " + long + "\n2026-11-09 09:30:00.000 end\n"
	if b.String() != want {
		t.Errorf("long line: got %d bytes, want %d", b.Len(), len(want))
	}
}
