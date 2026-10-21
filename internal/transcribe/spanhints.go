package transcribe

import (
	"encoding/json"
	"os"
	"strings"
)

// Experiment (exp-vocab-hints): per-utterance hotwords. The live
// coordinator sets the span being decoded; a transducer engine asks
// SpanHotwords for that span's phrases. TOMOE_HOTWORD_SPANS names a JSON
// lines file of {"start":s,"end":s,"words":[...]} in session seconds.
var SpanHotwords func(start, end float64) string

var spanStart, spanEnd float64

// SetSpan records the session-time span of the next decode.
func SetSpan(start, end float64) { spanStart, spanEnd = start, end }

func init() {
	path := os.Getenv("TOMOE_HOTWORD_SPANS")
	if path == "" {
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	type span struct {
		Start, End float64
		Words      []string
	}
	var spans []span
	for _, l := range strings.Split(string(b), "\n") {
		var s span
		if json.Unmarshal([]byte(l), &s) == nil {
			spans = append(spans, s)
		}
	}
	SpanHotwords = func(start, end float64) string {
		seen := map[string]bool{}
		var out []string
		for _, s := range spans {
			if s.End < start || s.Start > end {
				continue
			}
			for _, w := range s.Words {
				if !seen[w] {
					seen[w] = true
					out = append(out, w)
				}
			}
		}
		return strings.Join(out, "/")
	}
}
