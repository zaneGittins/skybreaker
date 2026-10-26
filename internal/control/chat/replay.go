package chat

import "bytes"

// Replay reads a scheduled run's log as the chat events it would have made.
// Only Claude's headless output is its chat protocol; other harnesses give
// none, and their log is read as text.
func Replay(driver string, log []byte) []Event {
	if driver != "claude" {
		return nil
	}
	d := newClaude()
	var out []Event
	for line := range bytes.Lines(log) {
		for _, e := range d.Read(bytes.TrimSpace(line)).Events {
			if e.Kind != KindDelta {
				out = append(out, e)
			}
		}
	}
	return out
}
