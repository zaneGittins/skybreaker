package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/zaneGittins/skybreaker/internal/control/app"
	"github.com/zaneGittins/skybreaker/internal/control/config"
	"github.com/zaneGittins/skybreaker/internal/control/preview"
	"github.com/zaneGittins/skybreaker/internal/control/store"
)

func (b *fakeBackend) Listening(_ context.Context, p string) ([]preview.Listening, error) {
	return []preview.Listening{{Port: 3000, Public: false}, {Port: 5173, Public: true}}, nil
}

func withPreviews(reg *preview.Registry) func(*Deps) { return func(d *Deps) { d.Previews = reg } }

func TestPreview(t *testing.T) {
	reg := preview.NewRegistry()
	reg.SetOrigin("orbit", "https://skybreaker.example.ts.net:8401")
	f := newFixture(t, you, withPreviews(reg))

	code, body := f.do(t, "GET", "/api/projects/orbit/preview", "", nil)
	var got struct {
		URL       string              `json:"url"`
		Port      int                 `json:"port"`
		Listening []preview.Listening `json:"listening"`
	}
	json.Unmarshal([]byte(body), &got)
	if code != 200 || got.URL != "https://skybreaker.example.ts.net:8401" || got.Port != 0 || len(got.Listening) != 2 || !got.Listening[1].Public {
		t.Fatalf("get: %d %s", code, body)
	}
	if code, body := f.do(t, "POST", "/api/projects/orbit/preview", `{"port": 5173}`, nil); code != 200 {
		t.Fatalf("set: %d %s", code, body)
	}
	if p, ok := reg.Target("orbit"); !ok || p != 5173 {
		t.Fatalf("target %d %v", p, ok)
	}
	for _, bad := range []string{`{"port": 0}`, `{"port": 70000}`, `{"port": "5173"}`} {
		if code, _ := f.do(t, "POST", "/api/projects/orbit/preview", bad, nil); code != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, code)
		}
	}
	if code, _ := f.do(t, "GET", "/api/projects/ghost/preview", "", nil); code != http.StatusNotFound {
		t.Errorf("unknown project: %d", code)
	}
	off := newFixture(t, you)
	if code, _ := off.do(t, "GET", "/api/projects/orbit/preview", "", nil); code != http.StatusNotFound {
		t.Errorf("previews off: %d", code)
	}
}

// A preview page shares the app's host (another port) and your tailnet
// identity. It must not be able to change anything in the app, nor open a
// terminal.
func TestPreviewPagesCantDriveTheApp(t *testing.T) {
	reg := preview.NewRegistry()
	reg.SetOrigin("orbit", "https://skybreaker.example.ts.net:8401")
	f := newFixture(t, you, withPreviews(reg))
	u, _ := url.Parse(f.ts.URL)
	previewOrigin := "http://" + u.Hostname() + ":1"
	for _, h := range []map[string]string{
		{"Sec-Fetch-Site": "same-site", "Origin": previewOrigin},
		{"Origin": previewOrigin},
	} {
		if code, _ := f.do(t, "POST", "/api/projects/orbit/preview", `{"port": 22}`, h); code != http.StatusForbidden {
			t.Errorf("POST from a preview %v: %d", h, code)
		}
	}
	if _, ok := reg.Target("orbit"); ok {
		t.Fatal("a preview page changed the preview")
	}
	if code, _ := f.do(t, "GET", "/api/sessions/s1/attach", "", map[string]string{"Origin": previewOrigin, "Connection": "Upgrade", "Upgrade": "websocket"}); code != http.StatusForbidden {
		t.Errorf("terminal from a preview: %d", code)
	}
}

func (b *fakeBackend) EditSession(id string, e app.SessionEdit) (store.Session, error) {
	if err := app.CheckEdit(e); err != nil {
		return store.Session{}, err
	}
	s := store.Session{ID: id, Project: "orbit"}
	if e.Title != nil {
		s.Title = *e.Title
	}
	if e.Color != nil {
		s.Color = *e.Color
	}
	return s, nil
}

func (b *fakeBackend) ChatStyle(project string) (config.ChatStyle, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.styles[project], nil
}

func (b *fakeBackend) SetChatStyle(project string, s config.ChatStyle) (config.ChatStyle, error) {
	if err := config.CheckChatStyle(s); err != nil {
		return config.ChatStyle{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.styles == nil {
		b.styles = map[string]config.ChatStyle{}
	}
	b.styles[project] = s
	return s, nil
}
