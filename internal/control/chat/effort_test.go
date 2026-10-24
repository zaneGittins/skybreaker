package chat

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/zaneGittins/skybreaker/internal/control/store"
)

// efforts lists the effort levels a driver's info events reported, in order.
func efforts(events []Event) []string {
	var out []string
	for _, e := range find(events, KindInfo) {
		if e.Info.Effort != "" {
			out = append(out, e.Info.Effort)
		}
	}
	return out
}

func modelByID(ms []Model, id string) *Model {
	for i := range ms {
		if ms[i].ID == id {
			return &ms[i]
		}
	}
	return nil
}

// Claude lists each model's levels in its initialize response; a model
// without effort lists none.
func TestClaudeEffortsFromInitialize(t *testing.T) {
	d := newClaude()
	d.Open(Options{})
	events, _, _ := replay(t, d, "claude-models.jsonl")
	ms := models(events)
	if m := modelByID(ms, "default"); m == nil || !slices.Equal(m.Efforts, []string{"low", "medium", "high", "xhigh", "max"}) {
		t.Errorf("default = %+v", m)
	}
	// Claude's list is whole: a model in it without levels has none.
	if m := modelByID(ms, "claude-haiku-4-5-20251001"); m == nil || m.Efforts != nil || !m.EffortsKnown {
		t.Errorf("haiku 4.5 = %+v", m)
	}
}

func TestClaudeArgvEffort(t *testing.T) {
	d := newClaude()
	if got := d.Argv(Options{Effort: "high"}); !slices.Contains(got, "--effort=high") {
		t.Errorf("argv %q lacks the chosen effort", got)
	}
	if got := strings.Join(d.Argv(Options{}), " "); strings.Contains(got, "--effort") {
		t.Errorf("argv names an effort nobody chose: %s", got)
	}
}

// Claude takes any effort without complaint, so the driver asks what it
// runs at after initialize and after every change (recorded from Claude
// Code 2.1.309 started with --effort=low).
func TestClaudeEffort(t *testing.T) {
	d := newClaude()
	d.Open(Options{})
	out := d.Read([]byte(`{"type":"control_response","response":{"subtype":"success","request_id":"skybreaker-1","response":{"models":[]}}}`))
	if len(out.Write) != 1 || decodeLine(t, out.Write[0])["request"].(map[string]any)["subtype"] != "get_settings" {
		t.Fatalf("after initialize wrote %q, want get_settings", out.Write)
	}
	if _, err := d.SetEffort("--max"); err == nil {
		t.Error("an effort that looks like a flag was taken")
	}
	lines, err := d.SetEffort("max")
	if err != nil || len(lines) != 2 {
		t.Fatalf("SetEffort = %q, %v", lines, err)
	}
	apply := decodeLine(t, lines[0])["request"].(map[string]any)
	if apply["subtype"] != "apply_flag_settings" || apply["settings"].(map[string]any)["effortLevel"] != "max" {
		t.Errorf("apply = %s", lines[0])
	}
	if decodeLine(t, lines[1])["request"].(map[string]any)["subtype"] != "get_settings" {
		t.Errorf("no get_settings after the change: %s", lines[1])
	}
	events, _, _ := replay(t, d, "claude-effort.jsonl")
	if got := efforts(events); !slices.Equal(got, []string{"low", "max"}) {
		t.Errorf("efforts = %q", got)
	}
}

// codex lists each model's levels and its default in model/list (recorded
// from codex 0.163.0).
func TestCodexEffortsFromModelList(t *testing.T) {
	d := newCodex()
	open := d.Open(Options{})
	out := d.Read(line(map[string]any{"id": decodeLine(t, open[0])["id"], "result": map[string]any{}}))
	var listID any
	for _, w := range out.Write {
		if m := decodeLine(t, w); m["method"] == "model/list" {
			listID = m["id"]
		}
	}
	rec, err := os.ReadFile("testdata/codex-models.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	rec = []byte(strings.Replace(string(rec), `{"id":2,`, `{"id":`+jsonNum(listID)+`,`, 1))
	ms := models(d.Read(rec).Events)
	sol := modelByID(ms, "gpt-6.1-sol")
	if sol == nil || sol.DefaultEffort != "low" || !slices.Equal(sol.Efforts, []string{"low", "medium", "high", "xhigh", "max", "ultra"}) {
		t.Errorf("gpt-6.1-sol = %+v", sol)
	}
	if luna := modelByID(ms, "gpt-6-luna"); luna == nil || slices.Contains(luna.Efforts, "ultra") || luna.DefaultEffort != "medium" || !luna.EffortsKnown {
		t.Errorf("gpt-6-luna = %+v", luna)
	}
}

// codex takes effort with each turn and keeps it for later ones, as it
// does the model; the thread reports the effort it was started with.
func TestCodexEffort(t *testing.T) {
	d := newCodex()
	d.Open(Options{Effort: "high"})
	if _, err := d.SetEffort("a b"); err == nil {
		t.Error("bad effort accepted")
	}
	out := d.Read(line(map[string]any{"id": 1, "result": map[string]any{}}))
	start := func() map[string]any {
		for _, w := range out.Write {
			if m := decodeLine(t, w); m["method"] == "thread/start" {
				return m
			}
		}
		return nil
	}()
	if start == nil {
		t.Fatalf("no thread/start in %q", out.Write)
	}
	out = d.Read(line(map[string]any{"id": start["id"], "result": map[string]any{"thread": map[string]any{"id": "th"}, "model": "gpt-6.1-sol", "reasoningEffort": "high"}}))
	if got := efforts(out.Events); !slices.Equal(got, []string{"high"}) {
		t.Errorf("efforts = %q", got)
	}
	turn := func() map[string]any {
		lines, err := d.Send("go")
		if err != nil || len(lines) != 1 {
			t.Fatalf("Send = %q, %v", lines, err)
		}
		return decodeLine(t, lines[0])["params"].(map[string]any)
	}
	if p := turn(); p["effort"] != "high" {
		t.Errorf("first turn = %+v", p)
	}
	d.Read(line(map[string]any{"method": "turn/started", "params": map[string]any{"threadId": "th", "turn": map[string]any{"id": "t1"}}}))
	d.Read(line(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "th", "turn": map[string]any{"id": "t1", "status": "completed"}}}))
	if _, err := d.SetEffort("ultra"); err != nil {
		t.Fatal(err)
	}
	if p := turn(); p["effort"] != "ultra" {
		t.Errorf("turn after the change = %+v", p)
	}
}

func TestPiArgvEffort(t *testing.T) {
	d := newPi()
	if got := d.Argv(Options{Effort: "high"}); !containsSeq(got, []string{"--thinking", "high"}) {
		t.Errorf("argv %q lacks the chosen effort", got)
	}
	if got := strings.Join(d.Argv(Options{}), " "); strings.Contains(got, "--thinking") {
		t.Errorf("argv names an effort nobody chose: %s", got)
	}
}

// pi says which levels only the model it runs has, so the driver asks
// after each switch (and at the start, see TestPiModesAndLogin), and follows pi's reports of the level,
// which it also lowers by itself for a model without reasoning (recorded
// from pi with a mock provider, started with --thinking low).
func TestPiEffort(t *testing.T) {
	d := newPi()
	d.Open(Options{})
	lines, err := d.SetEffort("xhigh")
	if err != nil || len(lines) != 1 {
		t.Fatalf("SetEffort = %q, %v", lines, err)
	}
	if m := decodeLine(t, lines[0]); m["type"] != "set_thinking_level" || m["level"] != "xhigh" {
		t.Errorf("set = %s", lines[0])
	}
	if _, err := d.SetEffort("--x"); err == nil {
		t.Error("bad effort accepted")
	}
	var first, last []Model
	events := replayPi(t, d, "pi-effort.jsonl", nil)
	for _, e := range find(events, KindInfo) {
		if e.Info.Models == nil {
			continue
		}
		if first == nil {
			first = e.Info.Models
		}
		last = e.Info.Models
	}
	// pi says a model's levels only once it runs it.
	if m := modelByID(first, "mock/plain"); m == nil || m.EffortsKnown {
		t.Errorf("plain before pi ran it = %+v", m)
	}
	if got := efforts(events); !slices.Equal(got, []string{"low", "xhigh", "off"}) {
		t.Errorf("efforts = %q", got)
	}
	if m := modelByID(last, "mock/deep"); m == nil || !slices.Equal(m.Efforts, []string{"off", "low", "medium", "high", "xhigh"}) {
		t.Errorf("deep = %+v", m)
	}
	if m := modelByID(last, "mock/plain"); m == nil || !slices.Equal(m.Efforts, []string{"off"}) || !m.EffortsKnown {
		t.Errorf("plain = %+v", m)
	}
}

// After a switch pi is asked for the new model's levels.
func TestPiAsksLevelsAfterSwitch(t *testing.T) {
	d := newPi()
	d.Open(Options{})
	out := d.Read([]byte(`{"type":"response","command":"set_model","success":true,"data":{"id":"plain","name":"Plain","provider":"mock","contextWindow":100000}}`))
	if len(out.Write) != 1 || decodeLine(t, out.Write[0])["type"] != "get_available_thinking_levels" {
		t.Errorf("after a switch wrote %q", out.Write)
	}
}

// fitEffort keeps a level the new model has, else takes the model's
// default, else leaves the level for the harness to fit.
func TestFitEffort(t *testing.T) {
	codex := Model{ID: "luna", Efforts: []string{"low", "medium", "high"}, DefaultEffort: "medium", EffortsKnown: true}
	claude := Model{ID: "opus-4-6", Efforts: []string{"low", "high"}, EffortsKnown: true}
	unknown := Model{ID: "mock/deep"}
	for _, c := range []struct {
		md     Model
		chosen string
		want   string
	}{
		{codex, "high", "high"},
		{codex, "ultra", "medium"},
		{claude, "xhigh", "xhigh"},
		{unknown, "ultra", "ultra"},
		{codex, "", ""},
	} {
		if got := fitEffort(c.md, c.chosen); got != c.want {
			t.Errorf("fitEffort(%s, %q) = %q, want %q", c.md.ID, c.chosen, got, c.want)
		}
	}
}

// efforts sent are the levels a fake Claude was told, in order.
func effortsSent(f *fakeClaude) []string {
	var out []string
	for _, m := range f.received() {
		if r, ok := m["request"].(map[string]any); ok && r["subtype"] == "apply_flag_settings" {
			out = append(out, r["settings"].(map[string]any)["effortLevel"].(string))
		}
	}
	return out
}

func TestManagerEffort(t *testing.T) {
	h := newHarness(t)
	s := h.session("claude")
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := h.m.SetEffort(ctx, s.ID, "a b"); err == nil {
		t.Error("bad effort accepted")
	}
	// Before the harness runs, the choice goes on its command line.
	must(h.m.SetEffort(ctx, s.ID, "medium"))
	must(h.m.SetModel(ctx, s.ID, "sonnet"))
	must(h.m.Send(ctx, s.ID, "x"))
	waitFor(t, "the turn", func() bool { return h.state(s.ID).State == store.ChatIdle && h.state(s.ID).Resume != "" })
	h.mu.Lock()
	f := h.procs[0]
	h.mu.Unlock()
	if !slices.Contains(f.argv, "--effort=medium") {
		t.Errorf("argv = %q", f.argv)
	}
	// Running, only a level the chat's model lists is taken, and at once.
	if err := h.m.SetEffort(ctx, s.ID, "max"); err == nil {
		t.Error("a level sonnet doesn't list was taken")
	}
	must(h.m.SetEffort(ctx, s.ID, "high"))
	waitFor(t, "the effort request", func() bool { return slices.Equal(effortsSent(f), []string{"high"}) })
	if c := h.state(s.ID).ChosenEffort; c != "high" {
		t.Errorf("chosen effort = %q", c)
	}
	// A model without the level keeps it for the harness to fit, and every
	// switch tells the harness again, since some (pi) reset it on a switch.
	must(h.m.SetModel(ctx, s.ID, "fable"))
	must(h.m.SetModel(ctx, s.ID, "default"))
	waitFor(t, "the level after each switch", func() bool { return slices.Equal(effortsSent(f), []string{"high", "high", "high"}) })
	if c := h.state(s.ID).ChosenEffort; c != "high" {
		t.Errorf("chosen effort after switching = %q", c)
	}
	// A model the harness lists without levels takes none.
	must(h.m.SetModel(ctx, s.ID, "plain"))
	if err := h.m.SetEffort(ctx, s.ID, "high"); err == nil {
		t.Error("a level was taken for a model without levels")
	}
}
