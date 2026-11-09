package main

import (
	"bufio"
	"errors"
	"io"
	"time"
)

// stampFormat leads every log line. The date is part of it because the app
// runs for days and the log spans weeks.
const stampFormat = "2006-01-02 15:04:05.000 "

// stampLines copies r to w, each line led by the time it arrived, until r
// ends. Write errors are ignored and reading goes on: the app's stdout and
// stderr feed r, and a reader that stopped would block every print.
func stampLines(r io.Reader, w io.Writer, now func() time.Time) {
	br := bufio.NewReaderSize(r, 64<<10)
	atStart := true
	for {
		chunk, err := br.ReadSlice('\n')
		if len(chunk) > 0 {
			if atStart {
				_, _ = io.WriteString(w, now().Format(stampFormat))
			}
			_, _ = w.Write(chunk)
			atStart = chunk[len(chunk)-1] == '\n'
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue // a line longer than the buffer: the rest follows unstamped
		}
		if err != nil {
			return
		}
	}
}
