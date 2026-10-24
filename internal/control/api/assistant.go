package api

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/zaneGittins/skybreaker/internal/assistant"
	"github.com/zaneGittins/skybreaker/internal/control/browser"
	"github.com/zaneGittins/skybreaker/internal/control/config"
	"github.com/zaneGittins/skybreaker/internal/control/store"
	policy "github.com/zaneGittins/skybreaker/internal/proxy/config"
	"github.com/zaneGittins/skybreaker/internal/tsauth"
	"github.com/zaneGittins/skybreaker/internal/webutil"
)

func (s *server) assistantRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /api/assistants/{a}", s.assistant)
	m.HandleFunc("PUT /api/assistants/{a}/look", s.setLook)
	m.HandleFunc("PUT /api/assistants/{a}/personality", s.setPersonality)
	m.HandleFunc("POST /api/assistants/{a}/personality/restore", s.restorePersonality)
	m.HandleFunc("POST /api/assistants/{a}/memories", s.addMemory)
	m.HandleFunc("PUT /api/assistants/{a}/memories/{id}", s.editMemory)
	m.HandleFunc("DELETE /api/assistants/{a}/memories/{id}", s.forgetMemory)
	m.HandleFunc("POST /api/assistants/{a}/proposals/{id}/remember", s.rememberProposal)
	m.HandleFunc("POST /api/assistants/{a}/proposals/{id}/dismiss", s.dismissProposal)
	m.HandleFunc("POST /api/assistants/{a}/proposals/{id}/approve", s.approveProposal)
	m.HandleFunc("GET /api/assistants/{a}/today", s.today)
	m.HandleFunc("GET /api/assistants/{a}/browser/live", s.browserLive)
	m.HandleFunc("GET /api/assistants/{a}/routines", s.routines)
	m.HandleFunc("POST /api/assistants/{a}/routines", s.addRoutine)
	m.HandleFunc("PUT /api/assistants/{a}/routines/{name}", s.editRoutine)
	m.HandleFunc("DELETE /api/assistants/{a}/routines/{name}", s.deleteRoutine)
}

// assistantView is an assistant for its pages, every list present.
type assistantView struct {
	Name        string               `json:"name"`
	Look        assistant.Look       `json:"look"`
	Personality []assistant.Version  `json:"personality"`
	Memories    []assistant.Memory   `json:"memories"`
	Proposals   []assistant.Proposal `json:"proposals"`
	Updates     []assistant.Update   `json:"updates"`
	Shapes      []string             `json:"shapes"`
	Colors      []string             `json:"colors"`
	Tempers     []string             `json:"tempers"`
}

// named is the assistant a request names, or a 404 for anything else.
func (s *server) named(w http.ResponseWriter, r *http.Request) (string, bool) {
	name := r.PathValue("a")
	if s.Assistants == nil || !s.Config.Assistant(name) {
		webutil.Error(w, http.StatusNotFound, "no assistant "+name)
		return "", false
	}
	return name, true
}

// assistantFail answers an assistant store error: what's missing is a 404,
// anything else was the request's fault.
func assistantFail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, assistant.ErrNotFound):
		webutil.Error(w, http.StatusNotFound, err.Error())
	case errors.Is(err, assistant.ErrTooMany):
		webutil.Error(w, http.StatusTooManyRequests, err.Error())
	default:
		webutil.Error(w, http.StatusBadRequest, err.Error())
	}
}

func login(r *http.Request) string {
	p, _ := tsauth.FromContext(r.Context())
	return p.Login
}

func (s *server) assistant(w http.ResponseWriter, r *http.Request) {
	name, ok := s.named(w, r)
	if !ok {
		return
	}
	a := s.Assistants.Get(name)
	webutil.WriteJSON(w, http.StatusOK, assistantView{Name: name, Look: a.Look,
		Personality: nonNil(a.Personality), Memories: nonNil(a.Memories), Proposals: nonNil(a.Proposals), Updates: nonNil(a.Updates),
		Shapes: assistant.Shapes, Colors: assistant.Colors, Tempers: assistant.Tempers})
}

// done answers a change with the assistant as it is now.
func (s *server) done(w http.ResponseWriter, r *http.Request, err error) {
	if err != nil {
		assistantFail(w, err)
		return
	}
	s.assistant(w, r)
}

func (s *server) setLook(w http.ResponseWriter, r *http.Request) {
	name, ok := s.named(w, r)
	var l assistant.Look
	if !ok || !decodeStrict(w, r, &l) {
		return
	}
	s.done(w, r, s.Assistants.SetLook(name, l))
}

// setPersonality changes what the assistant is told in every chat: an
// admin's, like every agent's instructions.
func (s *server) setPersonality(w http.ResponseWriter, r *http.Request) {
	name, ok := s.named(w, r)
	if ok {
		_, ok = s.admin(w, r)
	}
	var in struct {
		Text string `json:"text"`
	}
	if !ok || !decodeStrict(w, r, &in) {
		return
	}
	s.done(w, r, s.Assistants.SetPersonality(name, login(r), in.Text))
}

func (s *server) restorePersonality(w http.ResponseWriter, r *http.Request) {
	name, ok := s.named(w, r)
	if ok {
		_, ok = s.admin(w, r)
	}
	var in struct {
		Version int `json:"version"`
	}
	if !ok || !decodeStrict(w, r, &in) {
		return
	}
	s.done(w, r, s.Assistants.Restore(name, login(r), in.Version))
}

type memoryIn struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

func (s *server) addMemory(w http.ResponseWriter, r *http.Request) {
	name, ok := s.named(w, r)
	var in memoryIn
	if !ok || !decodeStrict(w, r, &in) {
		return
	}
	m, err := s.Assistants.AddMemory(name, login(r), in.Title, in.Body)
	if err != nil {
		assistantFail(w, err)
		return
	}
	webutil.WriteJSON(w, http.StatusOK, m)
}

func (s *server) editMemory(w http.ResponseWriter, r *http.Request) {
	name, ok := s.named(w, r)
	var in memoryIn
	if !ok || !decodeStrict(w, r, &in) {
		return
	}
	s.done(w, r, s.Assistants.EditMemory(name, r.PathValue("id"), in.Title, in.Body))
}

func (s *server) forgetMemory(w http.ResponseWriter, r *http.Request) {
	if name, ok := s.named(w, r); ok {
		s.done(w, r, s.Assistants.Forget(name, r.PathValue("id")))
	}
}

// rememberProposal keeps a proposed memory, with the user's edits if any.
func (s *server) rememberProposal(w http.ResponseWriter, r *http.Request) {
	name, ok := s.named(w, r)
	var in memoryIn
	if !ok || !decodeStrict(w, r, &in) {
		return
	}
	_, err := s.Assistants.Remember(name, r.PathValue("id"), in.Title, in.Body)
	s.done(w, r, err)
}

func (s *server) dismissProposal(w http.ResponseWriter, r *http.Request) {
	if name, ok := s.named(w, r); ok {
		s.done(w, r, s.Assistants.Dismiss(name, r.PathValue("id")))
	}
}

// --- routines ---------------------------------------------------------------

type routineIn struct {
	Name     string `json:"name"`
	Schedule string `json:"schedule"`
	Prompt   string `json:"prompt"`
	Harness  string `json:"harness,omitempty"`
}

type routineView struct {
	Name     string      `json:"name"`
	Schedule string      `json:"schedule"`
	Prompt   string      `json:"prompt"`
	Harness  string      `json:"harness"`
	App      bool        `json:"app"` // made in the app, so it can be changed here
	Runs     []store.Run `json:"runs"`
}

// routinesShown is how many recent runs each routine shows.
const routinesShown = 5

// routines lists an assistant's scheduled work as saved, so a change shows
// at once even while it waits for a restart to run.
func (s *server) routines(w http.ResponseWriter, r *http.Request) {
	name, ok := s.named(w, r)
	if !ok {
		return
	}
	cfg, pending := s.Config, false
	if s.Settings != nil {
		if saved, _, err := s.Settings.Saved(); err == nil {
			cfg = saved
		}
		pending = s.Settings.Pending()
	}
	runs := s.Store.ListRuns(name, 0)
	out := []routineView{}
	for _, j := range cfg.Jobs {
		if j.Project != name {
			continue
		}
		v := routineView{Name: j.Name, Schedule: j.Schedule, Prompt: j.Prompt, Harness: j.Harness, App: cfg.AppJob(j.Name), Runs: []store.Run{}}
		for _, run := range runs {
			if run.Job == j.Name && len(v.Runs) < routinesShown {
				v.Runs = append(v.Runs, run)
			}
		}
		out = append(out, v)
	}
	p, _ := tsauth.FromContext(r.Context())
	webutil.WriteJSON(w, http.StatusOK, map[string]any{"routines": out, "pending": pending,
		"editable": s.Settings != nil && s.Config.Admin(p.Login) && s.Config.SettingsInApp()})
}

// changeRoutines saves a change to the app's routines, as an admin.
func (s *server) changeRoutines(w http.ResponseWriter, r *http.Request, what string, change func(*config.AppFiles) error) {
	by, ok := s.editor(w, r)
	if !ok {
		return
	}
	out, err := s.Settings.Apply(r.Context(), by, what, change, false)
	switch {
	case errors.Is(err, assistant.ErrNotFound):
		webutil.Error(w, http.StatusNotFound, err.Error())
	case err != nil:
		webutil.Error(w, http.StatusBadRequest, err.Error())
	default:
		webutil.WriteJSON(w, http.StatusOK, map[string]string{"restart": out.Restart})
	}
}

func (in routineIn) job(project string) *config.Job {
	return &config.Job{Name: in.Name, Project: project, Schedule: in.Schedule, Prompt: in.Prompt, Harness: in.Harness, Kind: config.JobPrompt}
}

// appRoutine is the index of an assistant's own app-made routine.
func appRoutine(f *config.AppFiles, project, name string) (int, error) {
	for i, j := range f.Routines {
		if j != nil && j.Name == name && j.Project == project {
			return i, nil
		}
	}
	return 0, fmt.Errorf("routine %s: %w", name, assistant.ErrNotFound)
}

func (s *server) addRoutine(w http.ResponseWriter, r *http.Request) {
	name, ok := s.named(w, r)
	var in routineIn
	if !ok || !decodeStrict(w, r, &in) {
		return
	}
	s.changeRoutines(w, r, "added routine "+in.Name+" for assistant "+name, func(f *config.AppFiles) error {
		f.Routines = append(f.Routines, in.job(name))
		return nil
	})
}

func (s *server) editRoutine(w http.ResponseWriter, r *http.Request) {
	name, ok := s.named(w, r)
	var in routineIn
	if !ok || !decodeStrict(w, r, &in) {
		return
	}
	old := r.PathValue("name")
	s.changeRoutines(w, r, "changed routine "+old+" for assistant "+name, func(f *config.AppFiles) error {
		i, err := appRoutine(f, name, old)
		if err == nil {
			f.Routines[i] = in.job(name)
		}
		return err
	})
}

func (s *server) deleteRoutine(w http.ResponseWriter, r *http.Request) {
	name, ok := s.named(w, r)
	if !ok {
		return
	}
	old := r.PathValue("name")
	s.changeRoutines(w, r, "removed routine "+old+" from assistant "+name, func(f *config.AppFiles) error {
		i, err := appRoutine(f, name, old)
		if err == nil {
			f.Routines = slices.Delete(f.Routines, i, i+1)
		}
		return err
	})
}

// approveProposal makes a routine the assistant proposed, as an admin.
func (s *server) approveProposal(w http.ResponseWriter, r *http.Request) {
	name, ok := s.named(w, r)
	if !ok {
		return
	}
	p, err := s.Assistants.Proposal(name, r.PathValue("id"))
	if err != nil {
		assistantFail(w, err)
		return
	}
	if p.Kind != assistant.ProposeRoutine || p.Routine == nil {
		webutil.Error(w, http.StatusBadRequest, "only a routine is approved; remember or dismiss a memory")
		return
	}
	in := routineIn{Name: p.Routine.Name, Schedule: p.Routine.Schedule, Prompt: p.Routine.Prompt, Harness: p.Routine.Harness}
	rec := &statusRecorder{ResponseWriter: w}
	s.changeRoutines(rec, r, "approved routine "+in.Name+" for assistant "+name, func(f *config.AppFiles) error {
		f.Routines = append(f.Routines, in.job(name))
		return nil
	})
	if rec.status == http.StatusOK {
		s.Assistants.Dismiss(name, p.ID)
	}
}

// statusRecorder notes the status a handler wrote.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// --- today ------------------------------------------------------------------

// todayWindow is how far back the brief looks for runs and updates.
const todayWindow = 24 * time.Hour

// today is an assistant's brief: what waits on the user, what it did
// lately, and everything its sandbox may reach.
func (s *server) today(w http.ResponseWriter, r *http.Request) {
	name, ok := s.named(w, r)
	if !ok {
		return
	}
	a := s.Assistants.Get(name)
	since := time.Now().Add(-todayWindow)
	updates := []assistant.Update{}
	for _, u := range slices.Backward(a.Updates) {
		if u.At.After(since) {
			updates = append(updates, u)
		}
	}
	runs := []store.Run{}
	for _, run := range s.Store.ListRuns(name, 10) {
		if run.Status == store.RunRunning || run.Started.After(since) {
			runs = append(runs, run)
		}
	}
	reaches := []policy.Reach{}
	if pp, ok := s.Config.Policy.Project(name); ok {
		reaches = nonNil(pp.Reaches())
	}
	var bv *browserView
	if s.Browsers != nil {
		if st, ok := s.Browsers.BrowserStatus(r.Context(), name); ok {
			bv = &browserView{Taken: st.TakenBy != ""}
			if u, err := url.Parse(st.URL); err == nil && (u.Scheme == "https" || u.Scheme == "http") {
				bv.Site = u.Hostname()
			}
		}
	}
	webutil.WriteJSON(w, http.StatusOK, map[string]any{
		"name": name, "look": a.Look, "browser": bv,
		"approvals": nonNil(s.Approvals.Pending(name)), "requests": nonNil(s.pendingRequests(name)),
		"proposals": nonNil(a.Proposals), "updates": updates, "runs": runs, "reaches": reaches,
	})
}

// --- browser ----------------------------------------------------------------

// Browsers starts and finds assistants' browsers.
type Browsers interface {
	Browser(ctx context.Context, project string) (*browser.Browser, error)
	// BrowserStatus is where a running browser is; false when none runs.
	BrowserStatus(ctx context.Context, project string) (browser.Status, bool)
}

// browserView is a browser as the brief shows it: the site, never the
// page's address, which can carry what's on it.
type browserView struct {
	Site  string `json:"site"`
	Taken bool   `json:"taken"`
}

// browserLive hands an assistant's browser to you over a WebSocket, until
// you're done; any take-over it asked for is then answered.
func (s *server) browserLive(w http.ResponseWriter, r *http.Request) {
	if !tsauth.SameOrigin(r) {
		webutil.Error(w, http.StatusForbidden, "cross-origin browser refused")
		return
	}
	name, ok := s.named(w, r)
	if !ok {
		return
	}
	if s.Browsers == nil {
		webutil.Error(w, http.StatusNotFound, "browsers are off here")
		return
	}
	b, err := s.Browsers.Browser(r.Context(), name)
	if err != nil {
		webutil.Error(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	conn, err := s.Upgrade(w, r)
	if err != nil {
		return // the upgrader has answered
	}
	defer conn.Close(1000, "")
	p, _ := tsauth.FromContext(r.Context())
	by := cmp.Or(p.Name(), "you")
	if err := browser.Live(r.Context(), conn, b, by); err != nil {
		m, _ := json.Marshal(map[string]string{"t": "error", "error": err.Error()})
		conn.Write(r.Context(), false, m)
		return
	}
	for _, p := range s.Assistants.Get(name).Proposals {
		if p.Kind == assistant.ProposeTakeover {
			s.Assistants.Dismiss(name, p.ID)
		}
	}
}
