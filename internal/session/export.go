package session

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
)

// ExportMarkdown writes the session as Markdown to w.
func ExportMarkdown(sess *Session, w io.Writer) error {
	_, err := fmt.Fprintf(w, "# %s\n\n", sess.Title)
	if err != nil {
		return err
	}

	_, err = fmt.Fprintf(w, "**Date:** %s  \n", sess.CreatedAt.Format("2006-01-02 15:04:05"))
	if err != nil {
		return err
	}

	_, err = fmt.Fprintf(w, "**Duration:** %s\n", formatDuration(sess.Duration))
	if err != nil {
		return err
	}

	if sess.Platform != "" {
		_, err = fmt.Fprintf(w, "**Platform:** %s\n", sess.Platform)
		if err != nil {
			return err
		}
	}

	_, _ = fmt.Fprint(w, "\n")

	for _, p := range Paragraphs(sess.Segments) {
		_, err = fmt.Fprintf(w, "**[%s] %s:** %s\n\n",
			formatTimestamp(p.StartTime), p.Speaker, p.Text)
		if err != nil {
			return err
		}
	}

	return nil
}

// ExportPlainText writes the session as plain text to w.
func ExportPlainText(sess *Session, w io.Writer) error {
	_, err := fmt.Fprintf(w, "%s\n", sess.Title)
	if err != nil {
		return err
	}

	_, err = fmt.Fprintf(w, "Date: %s\n", sess.CreatedAt.Format("2006-01-02 15:04:05"))
	if err != nil {
		return err
	}

	_, err = fmt.Fprintf(w, "Duration: %s\n", formatDuration(sess.Duration))
	if err != nil {
		return err
	}

	if sess.Platform != "" {
		_, err = fmt.Fprintf(w, "Platform: %s\n", sess.Platform)
		if err != nil {
			return err
		}
	}

	_, _ = fmt.Fprint(w, "\n")

	for _, p := range Paragraphs(sess.Segments) {
		_, err = fmt.Fprintf(w, "[%s] %s: %s\n",
			formatTimestamp(p.StartTime), p.Speaker, p.Text)
		if err != nil {
			return err
		}
	}

	return nil
}

// ExportSRT writes the session as SRT subtitle format to w.
func ExportSRT(sess *Session, w io.Writer) error {
	for i, seg := range sess.Segments {
		_, err := fmt.Fprintf(w, "%d\n", i+1)
		if err != nil {
			return err
		}

		_, err = fmt.Fprintf(w, "%s --> %s\n",
			formatSRTTimestamp(seg.StartTime), formatSRTTimestamp(seg.EndTime))
		if err != nil {
			return err
		}

		_, err = fmt.Fprintf(w, "%s: %s\n\n", seg.Speaker, seg.Text)
		if err != nil {
			return err
		}
	}

	return nil
}

// formatTimestamp formats seconds as HH:MM:SS.
func formatTimestamp(seconds float64) string {
	total := int(math.Round(seconds))
	h := total / 3600
	m := (total % 3600) / 60
	s := total % 60
	return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
}

// formatSRTTimestamp formats seconds as HH:MM:SS,mmm (SRT format).
func formatSRTTimestamp(seconds float64) string {
	total := int(seconds * 1000)
	ms := total % 1000
	total /= 1000
	h := total / 3600
	m := (total % 3600) / 60
	s := total % 60
	return fmt.Sprintf("%02d:%02d:%02d,%03d", h, m, s, ms)
}

// formatDuration formats seconds as a human-readable duration.
func formatDuration(seconds float64) string {
	total := int(math.Round(seconds))
	h := total / 3600
	m := (total % 3600) / 60
	s := total % 60
	if h > 0 {
		return fmt.Sprintf("%dh%02dm%02ds", h, m, s)
	}
	if m > 0 {
		return fmt.Sprintf("%dm%02ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

// A Paragraph is consecutive lines by one speaker, shown and exported as
// one block: the speech detector ends a line at every half-second pause,
// so one turn is often several lines.
type Paragraph struct {
	Speaker            string
	StartTime, EndTime float64
	Text               string
}

// Paragraphs joins consecutive segments (in time order) with the same
// speaker into paragraphs.
func Paragraphs(segs []Segment) []Paragraph {
	sorted := append([]Segment(nil), segs...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].StartTime < sorted[j].StartTime })
	var out []Paragraph
	for _, s := range sorted {
		text := strings.TrimSpace(s.Text)
		if text == "" {
			continue
		}
		if n := len(out); n > 0 && out[n-1].Speaker == s.Speaker {
			out[n-1].Text += " " + text
			out[n-1].EndTime = max(out[n-1].EndTime, s.EndTime)
			continue
		}
		out = append(out, Paragraph{Speaker: s.Speaker, StartTime: s.StartTime, EndTime: s.EndTime, Text: text})
	}
	return out
}
