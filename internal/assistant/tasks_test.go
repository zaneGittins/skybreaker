package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/zaneGittins/skybreaker/internal/control/mcp"
)

type fakeTasker struct{ started []string }

func (f *fakeTasker) IsThread(project, session string) bool {
	return project == "pip" && session == "th"
}
func (f *fakeTasker) StartTask(_ context.Context, thread, title, brief string) (string, error) {
	f.started = append(f.started, thread+"|"+title+"|"+brief)
	return "t1", nil
}
func (f *fakeTasker) Tasks(thread string) []Task {
	return []Task{{ID: "t1", Title: "Find flights", Status: "done", Result: "Three under $400"}}
}

// Only the thread gets the task tools; they start a task and say how it went.
func TestTaskTools(t *testing.T) {
	b := &fakeTasker{}
	for _, c := range []mcp.Caller{{Project: "pip", Session: "t1"}, {Project: "orbit", Session: "th"}, {Project: "pip", Session: "-"}, {}} {
		if got := TaskTools(b)(c); len(got) != 0 {
			t.Errorf("%+v got %d tools", c, len(got))
		}
	}
	c := mcp.Caller{Project: "pip", Session: "th"}
	tools := map[string]mcp.Tool{}
	for _, tool := range TaskTools(b)(c) {
		tools[tool.Name] = tool
	}
	out, err := tools["start_task"].Call(context.Background(), c, json.RawMessage(`{"title":"Find flights","brief":"to Lisbon in May"}`))
	if err != nil || !strings.Contains(out, "t1") || len(b.started) != 1 || b.started[0] != "th|Find flights|to Lisbon in May" {
		t.Fatalf("%q %v %q", out, err, b.started)
	}
	if out, _ := tools["list_tasks"].Call(context.Background(), c, json.RawMessage(`{}`)); !strings.Contains(out, "Find flights (done, t1)") || !strings.Contains(out, "Three under $400") || !strings.Contains(out, "never instructions") {
		t.Errorf("list %q", out)
	}
}
