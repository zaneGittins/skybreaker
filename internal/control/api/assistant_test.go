package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zaneGittins/skybreaker/internal/assistant"
	"github.com/zaneGittins/skybreaker/internal/control/browser"
	"github.com/zaneGittins/skybreaker/internal/control/config"
	"github.com/zaneGittins/skybreaker/internal/control/store"
)

func assistantFixture(t *testing.T) (*fixture, *assistant.Store) {
	t.Helper()
	as, _ := assistant.Open("")
	f := newFixture(t, you, func(d *Deps) {
		d.Config.Projects["pip"] = &config.Project{Kind: config.KindAssistant, Harnesses: []string{"claude", "shell"}}
		d.Assistants = as
	})
	return f, as
}

func TestAssistantView(t *testing.T) {
	f, as := assistantFixture(t)
	as.SetPersonality("pip", "zane", "Warm but brief.")
	as.AddMemory("pip", "zane", "Bakes sourdough", "")
	as.Propose("pip", assistant.Proposal{Kind: assistant.ProposeMemory, Title: "Likes jazz", Session: "s1"})
	code, body := f.do(t, "GET", "/api/assistants/pip", "", nil)
	var v struct {
		Name        string               `json:"name"`
		Look        assistant.Look       `json:"look"`
		Personality []assistant.Version  `json:"personality"`
		Memories    []assistant.Memory   `json:"memories"`
		Proposals   []assistant.Proposal `json:"proposals"`
		Shapes      []string             `json:"shapes"`
	}
	json.Unmarshal([]byte(body), &v)
	if code != 200 || v.Name != "pip" || v.Look.Name != "Pip" || len(v.Personality) != 1 || len(v.Memories) != 1 || len(v.Proposals) != 1 || len(v.Shapes) != 6 {
		t.Fatalf("%d %s", code, body)
	}
	for _, p := range []string{"/api/assistants/orbit", "/api/assistants/ghost"} {
		if code, _ := f.do(t, "GET", p, "", nil); code != 404 {
			t.Errorf("%s: %d", p, code)
		}
	}
}

func TestAssistantEdits(t *testing.T) {
	f, as := assistantFixture(t)
	if code, body := f.do(t, "PUT", "/api/assistants/pip/look", `{"name": "Juno", "shape": "crescent", "color": "ink", "temper": "lively"}`, nil); code != 200 {
		t.Fatalf("look: %d %s", code, body)
	}
	if code, _ := f.do(t, "PUT", "/api/assistants/pip/look", `{"name": "Juno", "shape": "pill", "color": "ink", "temper": "lively"}`, nil); code != 400 {
		t.Errorf("bad look: %d", code)
	}
	if code, _ := f.do(t, "PUT", "/api/assistants/pip/look", `{"name": "Juno", "shape": "dot", "color": "ink", "temper": "calm", "sparkles": true}`, nil); code != 400 {
		t.Errorf("unknown field: %d", code)
	}
	f.do(t, "PUT", "/api/assistants/pip/personality", `{"text": "Warm but brief."}`, nil)
	f.do(t, "PUT", "/api/assistants/pip/personality", `{"text": "Dry humor is fine."}`, nil)
	if code, _ := f.do(t, "POST", "/api/assistants/pip/personality/restore", `{"version": 0}`, nil); code != 200 {
		t.Errorf("restore: %d", code)
	}
	a := as.Get("pip")
	if a.Look.Name != "Juno" || a.Current() != "Warm but brief." || len(a.Personality) != 3 || a.Personality[0].By != "zane@example.com" {
		t.Fatalf("assistant %+v", a)
	}
	if code, _ := f.do(t, "POST", "/api/assistants/pip/personality/restore", `{"version": 7}`, nil); code != 404 {
		t.Errorf("restore unknown: %d", code)
	}
}

func TestAssistantMemories(t *testing.T) {
	f, as := assistantFixture(t)
	p, _ := as.Propose("pip", assistant.Proposal{Kind: assistant.ProposeMemory, Title: "Likes jazz", Body: "Said so.", Session: "s1"})
	if code, body := f.do(t, "POST", "/api/assistants/pip/proposals/"+p.ID+"/remember", `{"title": "Likes cool jazz"}`, nil); code != 200 {
		t.Fatalf("remember: %d %s", code, body)
	}
	q, _ := as.Propose("pip", assistant.Proposal{Kind: assistant.ProposeMemory, Title: "Hates mornings"})
	if code, _ := f.do(t, "POST", "/api/assistants/pip/proposals/"+q.ID+"/dismiss", ``, nil); code != 200 {
		t.Errorf("dismiss: %d", code)
	}
	code, body := f.do(t, "POST", "/api/assistants/pip/memories", `{"title": "Pip's Google account is its own", "body": "Not yours."}`, nil)
	var m assistant.Memory
	json.Unmarshal([]byte(body), &m)
	if code != 200 || m.By != "zane@example.com" {
		t.Fatalf("add: %d %s", code, body)
	}
	if code, _ := f.do(t, "PUT", "/api/assistants/pip/memories/"+m.ID, `{"title": "Pip's account is its own", "body": ""}`, nil); code != 200 {
		t.Errorf("edit: %d", code)
	}
	a := as.Get("pip")
	if len(a.Proposals) != 0 || len(a.Memories) != 2 || a.Memories[0].Title != "Likes cool jazz" || a.Memories[1].Title != "Pip's account is its own" {
		t.Fatalf("assistant %+v", a)
	}
	if code, _ := f.do(t, "DELETE", "/api/assistants/pip/memories/"+m.ID, ``, nil); code != 200 || len(as.Get("pip").Memories) != 1 {
		t.Errorf("forget: %d", code)
	}
	if code, _ := f.do(t, "DELETE", "/api/assistants/pip/memories/m99", ``, nil); code != 404 {
		t.Errorf("forget unknown: %d", code)
	}
	if code, body := f.do(t, "POST", "/api/assistants/pip/memories", `{"title": ""}`, nil); code != 400 || !strings.Contains(body, "empty") {
		t.Errorf("empty title: %d %s", code, body)
	}
}

// routinesFixture is an assistant whose routines save through settings.
func routinesFixture(t *testing.T) (*fixture, *assistant.Store, *fakeSettings) {
	t.Helper()
	as, _ := assistant.Open("")
	s := &fakeSettings{}
	var cfg *config.Config
	f := newFixture(t, you, withSettings(t, s, `"capabilities": {"github": [{"host": "api.github.com", "mode": "tunnel"}]},`), func(d *Deps) {
		if err := config.AddProject(s.path, "pip", config.Added{Kind: config.KindAssistant, Harnesses: []string{"claude"}}, nil); err != nil {
			t.Fatal(err)
		}
		c, err := config.Load(s.path)
		if err != nil {
			t.Fatal(err)
		}
		d.Config, d.Assistants, cfg = c, as, c
	})
	f.cfg = cfg
	return f, as, s
}

func savedRoutines(t *testing.T, s *fakeSettings) []*config.Job {
	t.Helper()
	_, files, err := s.Saved()
	if err != nil {
		t.Fatal(err)
	}
	return files.Routines
}

func TestRoutinesAPI(t *testing.T) {
	f, _, s := routinesFixture(t)
	code, body := f.do(t, "POST", "/api/assistants/pip/routines", `{"name": "morning-brief", "schedule": "30 7 * * 1-5", "prompt": "Write my brief."}`, nil)
	if code != 200 || !strings.Contains(body, `"restart":"now"`) {
		t.Fatalf("add: %d %s", code, body)
	}
	if rs := savedRoutines(t, s); len(rs) != 1 || rs[0].Project != "pip" || rs[0].Schedule != "30 7 * * 1-5" {
		t.Fatalf("saved %+v", rs)
	}
	if len(s.log) != 1 || !strings.Contains(s.log[0], "morning-brief") {
		t.Errorf("audit %v", s.log)
	}
	_, body = f.do(t, "GET", "/api/assistants/pip/routines", "", nil)
	if !strings.Contains(body, `"name":"morning-brief"`) || !strings.Contains(body, `"pending":true`) {
		t.Fatalf("list: %s", body)
	}
	if code, body := f.do(t, "PUT", "/api/assistants/pip/routines/morning-brief", `{"name": "morning-brief", "schedule": "0 8 * * 1-5", "prompt": "Write my brief."}`, nil); code != 200 {
		t.Fatalf("edit: %d %s", code, body)
	}
	if rs := savedRoutines(t, s); len(rs) != 1 || rs[0].Schedule != "0 8 * * 1-5" {
		t.Fatalf("after edit %+v", rs)
	}
	for _, bad := range []string{
		`{"name": "x", "schedule": "whenever", "prompt": "x"}`,
		`{"name": "x", "schedule": "@daily", "prompt": "x", "kind": "board"}`,
		`{"name": "x", "schedule": "@daily", "prompt": "x", "project": "orbit"}`,
	} {
		if code, _ := f.do(t, "POST", "/api/assistants/pip/routines", bad, nil); code != 400 {
			t.Errorf("%s: %d", bad, code)
		}
	}
	// The config file's jobs aren't the app's to change.
	if code, _ := f.do(t, "DELETE", "/api/assistants/pip/routines/nightly", "", nil); code != 404 {
		t.Errorf("deleted a file job: %d", code)
	}
	if code, _ := f.do(t, "DELETE", "/api/assistants/pip/routines/morning-brief", "", nil); code != 200 || len(savedRoutines(t, s)) != 0 {
		t.Errorf("delete: %d", code)
	}
}

func TestApproveRoutineProposal(t *testing.T) {
	f, as, s := routinesFixture(t)
	p, _ := as.Propose("pip", assistant.Proposal{Kind: assistant.ProposeRoutine, Title: "weekly-podcast", Body: "Make the podcast.",
		Routine: &assistant.Routine{Name: "weekly-podcast", Schedule: "0 7 * * 0", Prompt: "Make the podcast."}})
	if code, body := f.do(t, "POST", "/api/assistants/pip/proposals/"+p.ID+"/approve", ``, nil); code != 200 {
		t.Fatalf("approve: %d %s", code, body)
	}
	if rs := savedRoutines(t, s); len(rs) != 1 || rs[0].Name != "weekly-podcast" || rs[0].Prompt != "Make the podcast." {
		t.Fatalf("saved %+v", rs)
	}
	if len(as.Get("pip").Proposals) != 0 {
		t.Error("the proposal is still waiting")
	}
	m, _ := as.Propose("pip", assistant.Proposal{Kind: assistant.ProposeMemory, Title: "Likes jazz"})
	if code, _ := f.do(t, "POST", "/api/assistants/pip/proposals/"+m.ID+"/approve", ``, nil); code != 400 {
		t.Errorf("approved a memory as a routine: %d", code)
	}
}

// Today is the brief: what needs the user and what happened.
func TestAssistantToday(t *testing.T) {
	f, as := assistantFixture(t)
	as.PostUpdate("pip", "s1", "Renewed the library hold")
	as.Propose("pip", assistant.Proposal{Kind: assistant.ProposeMemory, Title: "Likes jazz"})
	f.store.CreateRun(store.Run{ID: "r9", Job: "brief", Project: "pip", Status: store.RunOK, Summary: "Three things today.", Started: time.Now(), Finished: time.Now()})
	code, body := f.do(t, "GET", "/api/assistants/pip/today", "", nil)
	var v struct {
		Updates   []assistant.Update   `json:"updates"`
		Proposals []assistant.Proposal `json:"proposals"`
		Runs      []store.Run          `json:"runs"`
		Approvals []json.RawMessage    `json:"approvals"`
		Requests  []json.RawMessage    `json:"requests"`
	}
	if err := json.Unmarshal([]byte(body), &v); err != nil || code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if strings.Contains(body, `"reaches"`) {
		t.Errorf("today still sends reaches: %s", body)
	}
	if len(v.Updates) != 1 || len(v.Proposals) != 1 || len(v.Runs) != 1 || v.Runs[0].Summary != "Three things today." || v.Approvals == nil || v.Requests == nil {
		t.Fatalf("today %s", body)
	}
}

// Taking over the browser is a WebSocket from the app's own pages only.
func TestBrowserLiveRefusals(t *testing.T) {
	f, _ := assistantFixture(t)
	host := strings.TrimPrefix(f.ts.URL, "http://")
	if code, _ := f.do(t, "GET", "/api/assistants/pip/browser/live", "", map[string]string{"Origin": "https://evil.example.ts.net"}); code != 403 {
		t.Errorf("cross-origin: %d", code)
	}
	if code, _ := f.do(t, "GET", "/api/assistants/orbit/browser/live", "", map[string]string{"Origin": "http://" + host}); code != 404 {
		t.Errorf("a coding project: %d", code)
	}
	if code, _ := f.do(t, "GET", "/api/assistants/pip/browser/live", "", map[string]string{"Origin": "http://" + host}); code != 404 {
		t.Errorf("no browsers: %d", code)
	}
}

// Watching the browser is a WebSocket from the app's own pages only, too.
func TestBrowserWatchRefusals(t *testing.T) {
	f, _ := assistantFixture(t)
	host := strings.TrimPrefix(f.ts.URL, "http://")
	if code, _ := f.do(t, "GET", "/api/assistants/pip/browser/watch", "", map[string]string{"Origin": "https://evil.example.ts.net"}); code != 403 {
		t.Errorf("cross-origin: %d", code)
	}
	if code, _ := f.do(t, "GET", "/api/assistants/orbit/browser/watch", "", map[string]string{"Origin": "http://" + host}); code != 404 {
		t.Errorf("a coding project: %d", code)
	}
	if code, _ := f.do(t, "GET", "/api/assistants/pip/browser/watch", "", map[string]string{"Origin": "http://" + host}); code != 404 {
		t.Errorf("no browsers: %d", code)
	}
}

// A chat with an assistant knows it is one, to leave out repo controls.
func TestAssistantSession(t *testing.T) {
	f, _ := assistantFixture(t)
	f.store.CreateSession(store.Session{ID: "sp1", Project: "pip", Mode: store.ModeChat})
	_, body := f.do(t, "GET", "/api/sessions/sp1", "", nil)
	if !strings.Contains(body, `"kind":"assistant"`) || !strings.Contains(body, `"look":{"name":"Pip"`) {
		t.Fatalf("session = %s", body)
	}
}

type fakeBrowsers struct {
	st      *browser.Status
	stopped *[]string
}

func (f fakeBrowsers) Browser(context.Context, string) (*browser.Browser, error) {
	return nil, errors.New("no browser here")
}
func (f fakeBrowsers) RunningBrowser(string) (*browser.Browser, bool) { return nil, false }
func (f fakeBrowsers) BrowserStatus(context.Context, string) (browser.Status, bool) {
	if f.st == nil {
		return browser.Status{}, false
	}
	return *f.st, true
}
func (f fakeBrowsers) StopBrowser(project string) { *f.stopped = append(*f.stopped, project) }

// Restarting a browser stops it: the next use starts another.
func TestRestartBrowser(t *testing.T) {
	var stopped []string
	as, _ := assistant.Open("")
	f := newFixture(t, you, func(d *Deps) {
		d.Config.Projects["pip"] = &config.Project{Kind: config.KindAssistant, Harnesses: []string{"claude"}}
		d.Assistants, d.Browsers = as, fakeBrowsers{stopped: &stopped}
	})
	if code, body := f.do(t, "POST", "/api/assistants/pip/browser/restart", "", nil); code != 204 || len(stopped) != 1 || stopped[0] != "pip" {
		t.Fatalf("restart: %d %s %v", code, body, stopped)
	}
	if code, _ := f.do(t, "POST", "/api/assistants/orbit/browser/restart", "", nil); code != 404 || len(stopped) != 1 {
		t.Fatalf("a coding project: %d %v", code, stopped)
	}
}

// Today says how its browser is, by site only: never the page's address.
func TestTodayBrowser(t *testing.T) {
	for _, c := range []struct {
		st   *browser.Status
		want string
	}{
		{nil, `"browser":null`},
		{&browser.Status{URL: "https://notebooklm.google.com/notebook/abc?secret=1", Title: "My notebook"}, `"browser":{"site":"notebooklm.google.com","taken":false}`},
		{&browser.Status{URL: "about:blank", TakenBy: "zane@example.com"}, `"browser":{"site":"","taken":true}`},
	} {
		as, _ := assistant.Open("")
		f := newFixture(t, you, func(d *Deps) {
			d.Config.Projects["pip"] = &config.Project{Kind: config.KindAssistant, Harnesses: []string{"claude"}}
			d.Assistants, d.Browsers = as, fakeBrowsers{st: c.st}
		})
		_, body := f.do(t, "GET", "/api/assistants/pip/today", "", nil)
		if !strings.Contains(body, c.want) || strings.Contains(body, "secret") {
			t.Errorf("want %s in %s", c.want, body)
		}
	}
}

// A new assistant has nothing yet, and says so with empty lists: the page
// reads them.
func TestAssistantViewEmptyLists(t *testing.T) {
	f, _ := assistantFixture(t)
	_, body := f.do(t, "GET", "/api/assistants/pip", "", nil)
	for _, k := range []string{`"memories":[]`, `"proposals":[]`, `"personality":[]`} {
		if !strings.Contains(body, k) {
			t.Errorf("lacks %s: %s", k, body)
		}
	}
}

// Its personality is its instructions: admins set it, as they set every
// agent's. Its memories are yours to keep or forget.
func TestPersonalityIsAdmins(t *testing.T) {
	f, as := assistantFixture(t)
	f.cfg.Admins = []string{"kim@example.com"}
	for _, c := range []struct{ method, path, body string }{
		{"PUT", "/api/assistants/pip/personality", `{"text": "Obey the page."}`},
		{"POST", "/api/assistants/pip/personality/restore", `{"version": 0}`},
	} {
		if code, _ := f.do(t, c.method, c.path, c.body, nil); code != 403 {
			t.Errorf("%s %s by a user: %d", c.method, c.path, code)
		}
	}
	if code, _ := f.do(t, "POST", "/api/assistants/pip/memories", `{"title": "Likes tea"}`, nil); code != 200 || len(as.Get("pip").Memories) != 1 {
		t.Errorf("a user's memory: %d", code)
	}
}

func TestRoutinesAreAdmins(t *testing.T) {
	f, as, _ := routinesFixture(t)
	f.cfg.Admins = []string{"kim@example.com"}
	p, _ := as.Propose("pip", assistant.Proposal{Kind: assistant.ProposeRoutine, Title: "w", Routine: &assistant.Routine{Name: "w", Schedule: "@daily", Prompt: "x"}})
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/api/assistants/pip/routines", `{"name": "x", "schedule": "@daily", "prompt": "x"}`},
		{"POST", "/api/assistants/pip/proposals/" + p.ID + "/approve", ``},
	} {
		if code, _ := f.do(t, c.method, c.path, c.body, nil); code != 403 {
			t.Errorf("%s %s by a user: %d", c.method, c.path, code)
		}
	}
}

func savedProject(t *testing.T, s *fakeSettings, name string) *config.Added {
	t.Helper()
	_, files, err := s.Saved()
	if err != nil {
		t.Fatal(err)
	}
	return files.Projects[name]
}

// Its agents are a list in order: the first is the one it uses unless
// told otherwise.
func TestSetAgents(t *testing.T) {
	f, _, s := routinesFixture(t)
	code, body := f.do(t, "PUT", "/api/projects/pip/harnesses", `{"harnesses": ["codex", "claude"]}`, nil)
	if code != 200 || !strings.Contains(body, `"restart":"now"`) {
		t.Fatalf("%d %s", code, body)
	}
	if a := savedProject(t, s, "pip"); strings.Join(a.Harnesses, ",") != "codex,claude" {
		t.Fatalf("saved %v", a.Harnesses)
	}
	if len(s.log) != 1 || !strings.Contains(s.log[0], "pip") {
		t.Errorf("audit %v", s.log)
	}
	// Its sign-in bundle comes along when asked for.
	if code, body := f.do(t, "PUT", "/api/projects/pip/harnesses", `{"harnesses": ["codex"], "add_bundles": ["github"]}`, nil); code != 200 {
		t.Fatalf("with a bundle: %d %s", code, body)
	}
	_, files, _ := s.Saved()
	if g := files.Grant("pip"); !slices.Contains(g.Capabilities, "github") {
		t.Fatalf("grant %+v", g)
	}
	for name, c := range map[string]struct{ path, body string }{
		"no agents":           {"/api/projects/pip/harnesses", `{"harnesses": []}`},
		"just a shell":        {"/api/projects/pip/harnesses", `{"harnesses": ["shell"]}`},
		"an unknown agent":    {"/api/projects/pip/harnesses", `{"harnesses": ["hal"]}`},
		"twice":               {"/api/projects/pip/harnesses", `{"harnesses": ["codex", "codex"]}`},
		"an unknown bundle":   {"/api/projects/pip/harnesses", `{"harnesses": ["codex"], "add_bundles": ["everything"]}`},
		"the config file's":   {"/api/projects/orbit/harnesses", `{"harnesses": ["codex"]}`},
		"an unknown project":  {"/api/projects/ghost/harnesses", `{"harnesses": ["codex"]}`},
		"an unknown argument": {"/api/projects/pip/harnesses", `{"harnesses": ["codex"], "kind": "assistant"}`},
	} {
		if code, _ := f.do(t, "PUT", c.path, c.body, nil); code < 400 {
			t.Errorf("%s: %d", name, code)
		}
	}
}

// An agent a routine runs on stays until the routine changes.
func TestAgentInUseStays(t *testing.T) {
	f, _, _ := routinesFixture(t)
	f.do(t, "POST", "/api/assistants/pip/routines", `{"name": "brief", "schedule": "@daily", "prompt": "x", "harness": "claude"}`, nil)
	code, body := f.do(t, "PUT", "/api/projects/pip/harnesses", `{"harnesses": ["codex"]}`, nil)
	if code != 400 || !strings.Contains(body, "brief") {
		t.Fatalf("%d %s", code, body)
	}
}

func TestSetAgentsIsAdmins(t *testing.T) {
	f, _, _ := routinesFixture(t)
	f.cfg.Admins = []string{"kim@example.com"}
	if code, _ := f.do(t, "PUT", "/api/projects/pip/harnesses", `{"harnesses": ["codex"]}`, nil); code != 403 {
		t.Fatalf("a user: %d", code)
	}
}

// Done in the browser clears what asked for it, and the chat that asked
// carries on if it's waiting for you, told where the page is; a working one
// isn't interrupted.
func TestTakeoverDone(t *testing.T) {
	f, as := assistantFixture(t)
	f.store.CreateSession(store.Session{ID: "idle", Project: "pip", Mode: store.ModeChat, Status: store.StatusRunning, State: store.ChatIdle})
	f.store.CreateSession(store.Session{ID: "busy", Project: "pip", Mode: store.ModeChat, Status: store.StatusRunning, State: store.ChatWorking})
	as.Propose("pip", assistant.Proposal{Kind: assistant.ProposeTakeover, Title: "Google wants a code", Session: "idle"})
	as.Propose("pip", assistant.Proposal{Kind: assistant.ProposeMemory, Title: "Likes jazz"})
	takeoverDone(context.Background(), as, f.store, f.backend, "pip", "accounts.google.com")
	if ps := as.Get("pip").Proposals; len(ps) != 1 || ps[0].Kind != assistant.ProposeMemory {
		t.Fatalf("proposals %+v", ps)
	}
	f.backend.chat.mu.Lock()
	calls := slices.Clone(f.backend.chat.calls)
	f.backend.chat.mu.Unlock()
	if len(calls) != 1 || !strings.HasPrefix(calls[0], "handback idle accounts.google.com ") {
		t.Fatalf("calls %q", calls)
	}
	as.Propose("pip", assistant.Proposal{Kind: assistant.ProposeTakeover, Title: "Again", Session: "busy"})
	takeoverDone(context.Background(), as, f.store, f.backend, "pip", "")
	f.backend.chat.mu.Lock()
	defer f.backend.chat.mu.Unlock()
	if len(f.backend.chat.calls) != 1 {
		t.Errorf("interrupted a working chat: %q", f.backend.chat.calls)
	}
}

// A suggested routine whose name is taken, or that names an agent the
// assistant doesn't have, is still approved: under a free name, with its
// default agent.
func TestApproveRoutineThatClashes(t *testing.T) {
	f, as, s := routinesFixture(t)
	for range 2 {
		p, _ := as.Propose("pip", assistant.Proposal{Kind: assistant.ProposeRoutine, Title: "weekly-podcast", Body: "Make the podcast.",
			Routine: &assistant.Routine{Name: "weekly-podcast", Schedule: "0 7 * * 0", Prompt: "Make the podcast.", Harness: "pi"}})
		if code, body := f.do(t, "POST", "/api/assistants/pip/proposals/"+p.ID+"/approve", ``, nil); code != 200 {
			t.Fatalf("approve: %d %s", code, body)
		}
	}
	rs := savedRoutines(t, s)
	if len(rs) != 2 || rs[0].Name != "weekly-podcast" || rs[1].Name != "weekly-podcast-2" || rs[0].Harness != "" {
		t.Fatalf("saved %+v %+v", rs[0], rs[len(rs)-1])
	}
}

func share(t *testing.T, as *assistant.Store, kind, title, collection, data, ext string) assistant.Item {
	t.Helper()
	it, _, err := as.ShareFile("pip", "", assistant.Share{Item: assistant.Item{Kind: kind, Name: "f" + ext, Title: title, Collection: collection}, Data: []byte(data), Ext: ext})
	if err != nil {
		t.Fatal(err)
	}
	return it
}

// A library file is served to play or open, like a chat's: in ranges, as
// inert content, a download under its own name.
func TestAssistantSharedFile(t *testing.T) {
	f, as := assistantFixture(t)
	u := share(t, as, assistant.KindAudio, "Sunday's podcast", "", "ID3 a podcast", ".mp3")
	req, _ := http.NewRequest("GET", f.ts.URL+"/api/assistants/pip/files/"+u.Kept, nil)
	req.Header.Set("Range", "bytes=4-4")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 206 || string(b) != "a" || resp.Header.Get("Content-Type") != "audio/mpeg" || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("%d %q %v", resp.StatusCode, b, resp.Header)
	}
	d := share(t, as, assistant.KindFile, "The archive", "", "PK\x03\x04", ".bin")
	resp, err = http.Get(f.ts.URL + "/api/assistants/pip/files/" + d.Kept + "?name=notes.zip")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get("Content-Type") != "application/octet-stream" || !strings.Contains(resp.Header.Get("Content-Disposition"), `filename=notes.zip`) {
		t.Errorf("download %v", resp.Header)
	}
	for _, p := range []string{"/api/assistants/pip/files/x.mp3", "/api/assistants/other/files/" + u.Kept, "/api/assistants/pip/files/..%2Fassistants.json"} {
		if code, _ := f.do(t, "GET", p, "", nil); code != 404 {
			t.Errorf("%s: %d", p, code)
		}
	}
}

// A PDF and Markdown are read in the app's own reader; a page the agent
// made runs framed by the app, with script but no origin and no network.
func TestLibraryFileTypes(t *testing.T) {
	f, as := assistantFixture(t)
	get := func(kept, query string) http.Header {
		resp, err := http.Get(f.ts.URL + "/api/assistants/pip/files/" + kept + query)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("%s: %d", kept, resp.StatusCode)
		}
		return resp.Header
	}
	pdf := share(t, as, assistant.KindPDF, "Report", "", "%PDF-1.7", ".pdf")
	if h := get(pdf.Kept, ""); h.Get("Content-Type") != "application/pdf" || h.Get("Content-Disposition") != "inline" {
		t.Errorf("pdf %v", h)
	}
	if h := get(pdf.Kept, "?name=report.pdf"); !strings.Contains(h.Get("Content-Disposition"), "attachment") {
		t.Errorf("pdf download %v", h)
	}
	md := share(t, as, assistant.KindText, "Notes", "", "# Notes", ".md")
	if h := get(md.Kept, ""); h.Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Errorf("md %v", h)
	}
	view := share(t, as, assistant.KindView, "Plan", "", "<html><script>1</script></html>", ".html")
	h := get(view.Kept, "")
	csp := h.Get("Content-Security-Policy")
	if h.Get("Content-Type") != "text/html; charset=utf-8" || !strings.Contains(csp, "sandbox allow-scripts") || strings.Contains(csp, "allow-same-origin") ||
		!strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "frame-ancestors 'self'") || h.Get("X-Frame-Options") != "SAMEORIGIN" {
		t.Errorf("view %v", h)
	}
	// A chat never serves a page.
	if _, ok := shownTypes[".html"]; ok {
		t.Error("chats serve HTML")
	}
}

func TestLibraryAPI(t *testing.T) {
	f, as := assistantFixture(t)
	as.SetCollection("pip", "Morning briefing", assistant.CollectionPatch{Layout: ptr(assistant.LayoutPodcast)}, nil)
	a := share(t, as, assistant.KindAudio, "Sunday", "Morning briefing", "ID3 sunday", ".mp3")
	b := share(t, as, assistant.KindPDF, "Reading list", "", "%PDF-1.7", ".pdf")
	code, body := f.do(t, "GET", "/api/assistants/pip/library", "", nil)
	var lib struct {
		Items       []assistant.Item       `json:"items"`
		Collections []assistant.Collection `json:"collections"`
	}
	json.Unmarshal([]byte(body), &lib)
	// Newest first.
	if code != 200 || len(lib.Items) != 2 || lib.Items[0].ID != b.ID || len(lib.Collections) != 1 || lib.Collections[0].Layout != assistant.LayoutPodcast {
		t.Fatalf("%d %s", code, body)
	}
	code, body = f.do(t, "PUT", "/api/assistants/pip/library/"+a.ID+"/state", `{"progress":95.5,"duration":360}`, nil)
	if code != 200 || !strings.Contains(body, `"progress":95.5`) {
		t.Fatalf("%d %s", code, body)
	}
	if got := as.Get("pip").Library[0]; got.Progress != 95.5 || got.Duration != 360 || !got.Seen {
		t.Errorf("state %+v", got)
	}
	code, body = f.do(t, "GET", "/api/assistants/pip/library/"+a.ID, "", nil)
	if code != 200 || !strings.Contains(body, `"layout":"podcast"`) || !strings.Contains(body, `"title":"Sunday"`) {
		t.Errorf("item %d %s", code, body)
	}
	if code, _ := f.do(t, "PUT", "/api/assistants/pip/library/"+a.ID+"/state", `{"progress":-1}`, nil); code != 400 {
		t.Errorf("bad state: %d", code)
	}
	if code, _ := f.do(t, "PUT", "/api/assistants/pip/library/i999/state", `{"seen":true}`, nil); code != 404 {
		t.Errorf("missing item: %d", code)
	}
	if code, _ := f.do(t, "DELETE", "/api/assistants/pip/library/"+b.ID, "", nil); code != 200 {
		t.Errorf("delete: %d", code)
	}
	if n := len(as.Get("pip").Library); n != 1 {
		t.Errorf("%d items after delete", n)
	}
	for _, p := range []string{"/api/assistants/ghost/library", "/api/assistants/pip/library/i999"} {
		if code, _ := f.do(t, "GET", p, "", nil); code != 404 {
			t.Errorf("%s: %d", p, code)
		}
	}
}

// Today carries each shared item with its update, and how many are new.
func TestTodayItems(t *testing.T) {
	f, as := assistantFixture(t)
	a := share(t, as, assistant.KindAudio, "Podcast", "", "ID3 a", ".mp3")
	share(t, as, assistant.KindPDF, "Report", "", "%PDF", ".pdf")
	as.SetItemState("pip", a.ID, assistant.ItemState{Finished: ptr(true)})
	_, body := f.do(t, "GET", "/api/assistants/pip/today", "", nil)
	var v struct {
		Items map[string]assistant.Item `json:"items"`
		Fresh int                       `json:"fresh"`
	}
	json.Unmarshal([]byte(body), &v)
	if len(v.Items) != 2 || v.Items[a.ID].Kept != a.Kept || !v.Items[a.ID].Finished || v.Fresh != 1 {
		t.Errorf("today %s", body)
	}
}

// An assistant's thread opens as one chat; starting fresh carries it on.
func TestAssistantThread(t *testing.T) {
	f, _ := assistantFixture(t)
	if code, body := f.do(t, "POST", "/api/assistants/pip/thread", `{}`, nil); code != 200 || !strings.Contains(body, `"id":"th-pip"`) {
		t.Errorf("thread: %d %s", code, body)
	}
	if code, _ := f.do(t, "POST", "/api/assistants/orbit/thread", `{}`, nil); code != 404 {
		t.Errorf("a coding project's thread: %d", code)
	}
	if code, body := f.do(t, "POST", "/api/sessions/c1/fresh", `{}`, nil); code != 200 || !strings.Contains(body, `"id":"fresh-c1"`) {
		t.Errorf("fresh: %d %s", code, body)
	}
	for path, want := range map[string]int{"/api/sessions/busy/fresh": 409, "/api/sessions/nope/fresh": 404} {
		if code, body := f.do(t, "POST", path, `{}`, nil); code != want {
			t.Errorf("%s: %d %s, want %d", path, code, body, want)
		}
	}
}

func ptr[T any](v T) *T { return &v }
