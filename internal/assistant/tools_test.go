package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/zaneGittins/skybreaker/internal/control/mcp"
)

func isPip(p string) bool { return p == "pip" }

func call(t *testing.T, s *Store, c mcp.Caller, name, args string) (string, error) {
	t.Helper()
	for _, tool := range Tools(s, isPip)(c) {
		if tool.Name == name {
			return tool.Call(context.Background(), c, json.RawMessage(args))
		}
	}
	t.Fatalf("%s: no tool %s", c.Project, name)
	return "", nil
}

// Only an assistant's own sandbox gets its tools: a coding project or the
// UI mount has no memories to propose.
func TestToolsAreForAssistants(t *testing.T) {
	s, _ := open(t)
	for _, c := range []mcp.Caller{{Project: "orbit", Session: "s1"}, {User: "zane@example.com"}, {}} {
		if got := Tools(s, isPip)(c); len(got) != 0 {
			t.Errorf("%+v got %d tools", c, len(got))
		}
	}
	var names []string
	for _, tool := range Tools(s, isPip)(mcp.Caller{Project: "pip", Session: "s1"}) {
		names = append(names, tool.Name)
	}
	if strings.Join(names, ",") != "recall,propose_memory,propose_routine,post_update" {
		t.Fatalf("tools %v", names)
	}
}

func TestProposeAndRecall(t *testing.T) {
	s, _ := open(t)
	c := mcp.Caller{Project: "pip", Session: "s1"}
	out, err := call(t, s, c, "propose_memory", `{"title": "Prefers short podcasts", "body": "Under 20 minutes."}`)
	if err != nil || !strings.Contains(out, "user") {
		t.Fatalf("%q %v", out, err)
	}
	if out, _ := call(t, s, c, "recall", `{}`); strings.Contains(out, "podcasts") {
		t.Fatalf("recalled an unapproved memory: %q", out)
	}
	p := s.Get("pip").Proposals[0]
	if p.Session != "s1" {
		t.Errorf("provenance %+v", p)
	}
	s.Remember("pip", p.ID, "", "")
	if out, _ := call(t, s, c, "recall", `{"query": "podcast"}`); !strings.Contains(out, "Prefers short podcasts") || !strings.Contains(out, "Under 20 minutes.") {
		t.Fatalf("recall %q", out)
	}
	if _, err := call(t, s, c, "propose_memory", `{"title": ""}`); err == nil {
		t.Error("proposed an empty memory")
	}
	if _, err := call(t, s, c, "propose_memory", `{"title": "x", "remember_now": true}`); err == nil {
		t.Error("accepted an unknown argument")
	}
	// A terminal or scheduled run has no chat to point back to.
	call(t, s, mcp.Caller{Project: "pip", Session: "-"}, "propose_memory", `{"title": "From a run"}`)
	if p := s.Get("pip").Proposals[0]; p.Session != "" {
		t.Errorf("run provenance %+v", p)
	}
}

func TestProposeRoutine(t *testing.T) {
	s, _ := open(t)
	c := mcp.Caller{Project: "pip", Session: "s1"}
	if _, err := call(t, s, c, "propose_routine", `{"name": "weekly-podcast", "schedule": "0 7 * * 0", "prompt": "Make this week's podcast."}`); err != nil {
		t.Fatal(err)
	}
	p := s.Get("pip").Proposals[0]
	if p.Kind != ProposeRoutine || p.Routine == nil || p.Routine.Schedule != "0 7 * * 0" || p.Title == "" {
		t.Fatalf("proposal %+v", p)
	}
	for _, bad := range []string{
		`{"name": "Weekly Podcast", "schedule": "0 7 * * 0", "prompt": "x"}`,
		`{"name": "weekly", "schedule": "every sunday", "prompt": "x"}`,
		`{"name": "weekly", "schedule": "0 7 * * 0", "prompt": ""}`,
	} {
		if _, err := call(t, s, c, "propose_routine", bad); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestPostUpdateTool(t *testing.T) {
	s, _ := open(t)
	if _, err := call(t, s, mcp.Caller{Project: "pip", Session: "s1"}, "post_update", `{"text": "Renewed the library hold"}`); err != nil {
		t.Fatal(err)
	}
	if u := s.Get("pip").Updates; len(u) != 1 || u[0].Text != "Renewed the library hold" {
		t.Fatalf("updates %+v", u)
	}
}

func TestLongScheduleIsntATitleError(t *testing.T) {
	s, _ := open(t)
	if _, err := call(t, s, mcp.Caller{Project: "pip", Session: "s1"}, "propose_routine", `{"name": "`+strings.Repeat("n", 48)+`", "schedule": "0,5,10,15,20,25,30,35,40,45,50,55 0-23 1-31 1-12 0-6", "prompt": "x"}`); err != nil {
		t.Fatal(err)
	}
}
