package chat

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// models picks the models out of a driver's info events.
func models(events []Event) []Model {
	var out []Model
	for _, e := range find(events, KindInfo) {
		if e.Info.Models != nil {
			out = e.Info.Models
		}
	}
	return out
}

func modelIDs(ms []Model) []string {
	var ids []string
	for _, m := range ms {
		ids = append(ids, m.ID)
	}
	return ids
}

// The model list is Claude's own, from its initialize response (recorded
// from Claude Code 2.1.303, account details removed).
func TestClaudeModelsFromInitialize(t *testing.T) {
	d := newClaude()
	d.Open(Options{})
	events, _, _ := replay(t, d, "claude-models.jsonl")
	ms := models(events)
	ids := modelIDs(ms)
	if len(ms) < 5 || ids[0] != "default" || !slices.Contains(ids, "sonnet") || !slices.Contains(ids, "fable") {
		t.Fatalf("models = %q", ids)
	}
	if !ms[0].Default || ms[1].Default {
		t.Errorf("only %q is the default: %+v", ms[0].ID, ms[:2])
	}
	if ms[0].Name != "Default (recommended)" || ms[0].Resolved != "claude-opus-5-5" || !strings.Contains(ms[0].Description, "Opus 5.5") {
		t.Errorf("default model = %+v", ms[0])
	}
}

func TestClaudeSetModel(t *testing.T) {
	d := newClaude()
	d.Open(Options{})
	if _, err := d.SetModel("--evil"); err == nil {
		t.Error("a model that looks like a flag was taken")
	}
	lines, err := d.SetModel("sonnet")
	if err != nil || len(lines) != 1 {
		t.Fatalf("SetModel = %q, %v", lines, err)
	}
	m := decodeLine(t, lines[0])
	req := m["request"].(map[string]any)
	if m["type"] != "control_request" || req["subtype"] != "set_model" || req["model"] != "sonnet" || m["request_id"] != "skybreaker-2" {
		t.Errorf("set_model = %s", lines[0])
	}
	d.SetModel("bogus-model-x")
	// The recording answers the initialize, then the two set_model requests.
	events, _, _ := replay(t, d, "claude-models.jsonl")
	var got []string
	for _, e := range find(events, KindInfo) {
		if e.Info.Model != "" {
			got = append(got, e.Info.Model)
		}
	}
	if !slices.Equal(got, []string{"sonnet"}) {
		t.Errorf("model infos = %q, want only the accepted switch", got)
	}
	notes := find(events, KindNotice)
	if len(notes) != 1 || !strings.Contains(notes[0].Text, "bogus-model-x") {
		t.Errorf("refused switch notices = %+v", notes)
	}
}

func TestClaudeArgvModel(t *testing.T) {
	d := newClaude()
	if got := d.Argv(Options{Model: "fable"}); !slices.Contains(got, "--model=fable") {
		t.Errorf("argv %q lacks the chosen model", got)
	}
	if got := strings.Join(d.Argv(Options{}), " "); strings.Contains(got, "--model") {
		t.Errorf("argv names a model nobody chose: %s", got)
	}
}

// The model list is codex's own, from model/list, so it has whatever the
// account has (recorded from codex 0.163.0, which lists Daybreak Blue).
func TestCodexModelsFromModelList(t *testing.T) {
	d := newCodex()
	open := d.Open(Options{})
	out := d.Read(line(map[string]any{"id": decodeLine(t, open[0])["id"], "result": map[string]any{}}))
	var listID any
	for _, w := range out.Write {
		if m := decodeLine(t, w); m["method"] == "model/list" {
			listID = m["id"]
		}
	}
	if listID == nil {
		t.Fatal("no model/list after initialize")
	}
	rec, err := os.ReadFile("testdata/codex-models.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	rec = []byte(strings.Replace(string(rec), `{"id":2,`, `{"id":`+jsonNum(listID)+`,`, 1))
	out = d.Read(rec)
	ms := models(out.Events)
	ids := modelIDs(ms)
	if !slices.Contains(ids, "gpt-daybreak-blue-latest") || !slices.Contains(ids, "gpt-6.1-sol") {
		t.Fatalf("models = %q", ids)
	}
	for _, m := range ms {
		if m.ID == "gpt-6.1-sol" && (!m.Default || m.Name != "GPT-6.1-Sol" || m.Description == "") {
			t.Errorf("gpt-6.1-sol = %+v", m)
		}
		if m.ID == "gpt-daybreak-blue-latest" && (m.Default || m.Name != "Daybreak Blue") {
			t.Errorf("daybreak = %+v", m)
		}
	}
}

func TestCodexModelListPagesAndHidden(t *testing.T) {
	d := newCodex()
	open := d.Open(Options{})
	out := d.Read(line(map[string]any{"id": decodeLine(t, open[0])["id"], "result": map[string]any{}}))
	var listID any
	for _, w := range out.Write {
		if m := decodeLine(t, w); m["method"] == "model/list" {
			listID = m["id"]
		}
	}
	out = d.Read(line(map[string]any{"id": listID, "result": map[string]any{"nextCursor": "c2", "data": []any{
		map[string]any{"id": "a", "model": "a", "displayName": "A", "description": "", "hidden": false, "isDefault": true},
		map[string]any{"id": "h", "model": "h", "displayName": "H", "description": "", "hidden": true, "isDefault": false},
	}}}))
	if len(out.Events) != 0 || len(out.Write) != 1 {
		t.Fatalf("first page: events %+v, writes %q", out.Events, out.Write)
	}
	next := decodeLine(t, out.Write[0])
	if next["method"] != "model/list" || next["params"].(map[string]any)["cursor"] != "c2" {
		t.Fatalf("next page request = %v", next)
	}
	out = d.Read(line(map[string]any{"id": next["id"], "result": map[string]any{"nextCursor": nil, "data": []any{
		map[string]any{"id": "b", "model": "b", "displayName": "B", "description": "", "hidden": false, "isDefault": false},
	}}}))
	if ids := modelIDs(models(out.Events)); !slices.Equal(ids, []string{"a", "b"}) {
		t.Errorf("models = %q, want the visible ones from both pages", ids)
	}
}

func TestCodexSetModel(t *testing.T) {
	d, _ := handshake(t, Options{})
	if _, err := d.SetModel("bad model"); err == nil {
		t.Error("a model with a space was taken")
	}
	lines, err := d.SetModel("gpt-daybreak-blue-latest")
	if err != nil || len(lines) != 0 {
		t.Fatalf("SetModel = %q, %v; codex takes the model with the next turn", lines, err)
	}
	send, _ := d.Send("hi")
	p := decodeLine(t, send[0])["params"].(map[string]any)
	if p["model"] != "gpt-daybreak-blue-latest" {
		t.Errorf("turn/start params = %v", p)
	}
}

func TestCodexChosenModelAtStart(t *testing.T) {
	for _, resume := range []string{"", "thr-0"} {
		d := newCodex()
		open := d.Open(Options{Model: "gpt-6-astra", Resume: resume})
		out := d.Read(line(map[string]any{"id": decodeLine(t, open[0])["id"], "result": map[string]any{}}))
		found := false
		for _, w := range out.Write {
			m := decodeLine(t, w)
			if m["method"] == "thread/start" || m["method"] == "thread/resume" {
				found = true
				if m["params"].(map[string]any)["model"] != "gpt-6-astra" {
					t.Errorf("%s params = %v", m["method"], m["params"])
				}
			}
		}
		if !found {
			t.Fatal("no thread request")
		}
	}
	d, _ := handshake(t, Options{})
	send, _ := d.Send("hi")
	if _, ok := decodeLine(t, send[0])["params"].(map[string]any)["model"]; ok {
		t.Error("a turn named a model nobody chose")
	}
}

func TestValidModel(t *testing.T) {
	for _, ok := range []string{"sonnet", "claude-opus-5-5", "claude-opus-5-5[1m]", "gpt-5.6-sol", "openai/gpt-x", "us.anthropic.claude:1"} {
		if err := validModel(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "-x", "--model", "a b", "a\nb", "a\x00", "a‮b", strings.Repeat("m", 200)} {
		if validModel(bad) == nil {
			t.Errorf("%q was valid", bad)
		}
	}
}

func jsonNum(v any) string {
	return strings.TrimSpace(string(line(v)))
}
