package chat

import (
	"os"
	"testing"
)

// A routine's run log reads back as the chat it would have been.
func TestReplayClaudeRun(t *testing.T) {
	b, err := os.ReadFile("testdata/claude-run.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	var tool *Tool
	var text string
	for _, e := range Replay("claude", b) {
		switch e.Kind {
		case KindTool:
			tool = e.Tool
		case KindText:
			text = e.Text
		case KindDelta:
			t.Errorf("replay has a delta: %+v", e)
		}
	}
	if tool == nil || tool.Kind != "command" || tool.Status != "done" || tool.Output != "log.jsonl" {
		t.Errorf("tool = %+v", tool)
	}
	if text != "The directory `/tmp/runrec` contains one file, `log.jsonl`." {
		t.Errorf("text = %q", text)
	}
}

func TestReplayWithoutADriverIsEmpty(t *testing.T) {
	if got := Replay("pi", []byte("plain text\n")); len(got) != 0 {
		t.Errorf("pi replay = %+v", got)
	}
	if got := Replay("", []byte("{}\n")); len(got) != 0 {
		t.Errorf("no driver replay = %+v", got)
	}
}
