package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zaneGittins/skybreaker/internal/wsock"
)

// fakeServer answers like Skybreaker's API for one user.
type fakeServer struct {
	*httptest.Server
	mu      sync.Mutex
	got     []string // "METHOD path body" of each request
	frames  []string // what the attach endpoint received, in order
	attachK func(c *wsock.Conn)
}

func newFake(t *testing.T) (*fakeServer, *Client) {
	t.Helper()
	f := &fakeServer{}
	m := http.NewServeMux()
	m.HandleFunc("GET /api/meta", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"version": "v0.1.0", "user": "you@github"})
	})
	m.HandleFunc("GET /api/inbox", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"projects":[{"name":"bifract","harnesses":["claude","codex"],"running":true},{"name":"orbit","harnesses":["codex"]}],
		"sessions":[
		  {"id":"s1","project":"bifract","harness":"claude","title":"Run the tests","status":"running","mode":"chat","state":"idle","updated":"2026-10-19T10:00:00Z"},
		  {"id":"s2","project":"bifract","harness":"codex","title":"Refactor","status":"running","mode":"chat","state":"waiting","updated":"2026-10-19T09:00:00Z"},
		  {"id":"s3","project":"orbit","harness":"shell","title":"","status":"running","created":"2026-10-19T11:00:00Z"},
		  {"id":"s4","project":"orbit","harness":"claude","title":"old","status":"exited"}
		]}`))
	})
	m.HandleFunc("POST /api/projects/{p}/sessions", func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		f.record(r, in["harness"]+"/"+in["mode"])
		if in["harness"] == "nope" {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":"harness \"nope\" is not enabled for bifract"}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"id": "new1", "project": r.PathValue("p"), "harness": in["harness"], "status": "running"})
	})
	m.HandleFunc("POST /api/sessions/{id}/terminal", func(w http.ResponseWriter, r *http.Request) {
		f.record(r, "")
		if r.PathValue("id") == "s2" {
			w.WriteHeader(http.StatusConflict)
			w.Write([]byte(`{"error":"the agent is working; stop it first"}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"id": "t-" + r.PathValue("id"), "status": "running"})
	})
	m.HandleFunc("GET /api/sessions/{id}/attach", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != "https://"+r.Host {
			http.Error(w, "cross-origin terminal refused", http.StatusForbidden)
			return
		}
		c, err := wsock.Accept(w, r)
		if err != nil {
			return
		}
		f.attachK(c)
	})
	f.Server = httptest.NewTLSServer(m)
	t.Cleanup(f.Close)
	pool := x509.NewCertPool()
	pool.AddCert(f.Certificate())
	c, err := New(f.URL)
	if err != nil {
		t.Fatal(err)
	}
	c.TLS = &tls.Config{RootCAs: pool}
	c.HTTP = f.Client()
	return f, c
}

func (f *fakeServer) record(r *http.Request, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, r.Method+" "+r.URL.Path+" "+body)
}

func TestAPI(t *testing.T) {
	f, c := newFake(t)
	ctx := context.Background()
	if m, err := c.Meta(ctx); err != nil || m.User != "you@github" || m.Version != "v0.1.0" {
		t.Fatalf("meta: %+v %v", m, err)
	}
	in, err := c.Inbox(ctx)
	if err != nil || len(in.Sessions) != 4 || len(in.Projects) != 2 || in.Projects[0].Harnesses[1] != "codex" {
		t.Fatalf("inbox: %+v %v", in, err)
	}
	s, err := c.NewSession(ctx, "bifract", "claude")
	if err != nil || s.ID != "new1" {
		t.Fatalf("new session: %+v %v", s, err)
	}
	// A session started here is a terminal, not a chat.
	if f.got[0] != "POST /api/projects/bifract/sessions claude/terminal" {
		t.Fatalf("request %q", f.got[0])
	}
	if _, err := c.NewSession(ctx, "bifract", "nope"); err == nil || !strings.Contains(err.Error(), `harness "nope" is not enabled`) {
		t.Fatalf("server error not passed on: %v", err)
	}
	if s, err := c.ChatTerminal(ctx, "s1"); err != nil || s.ID != "t-s1" {
		t.Fatalf("chat terminal: %+v %v", s, err)
	}
	if _, err := c.ChatTerminal(ctx, "s2"); err == nil || !strings.Contains(err.Error(), "working") {
		t.Fatalf("busy chat: %v", err)
	}
}

func TestEndpointEscapesSegments(t *testing.T) {
	c, _ := New("https://skybreaker.example.ts.net/")
	// An id or project name can't walk to another endpoint.
	if got := c.endpoint("api", "sessions", "../projects/x?y", "terminal"); got != "https://skybreaker.example.ts.net/api/sessions/..%2Fprojects%2Fx%3Fy/terminal" {
		t.Fatalf("endpoint %q", got)
	}
	if got := c.wsEndpoint("api", "sessions", "s1", "attach"); got != "wss://skybreaker.example.ts.net/api/sessions/s1/attach" {
		t.Fatalf("ws endpoint %q", got)
	}
	d, _ := New("http://127.0.0.1:7700")
	if got := d.wsEndpoint("api", "sessions", "s1", "attach"); got != "ws://127.0.0.1:7700/api/sessions/s1/attach" {
		t.Fatalf("dev ws endpoint %q", got)
	}
}

func TestForbiddenExplainsTailnetLogin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()
	c, _ := New(srv.URL)
	_, err := c.Meta(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Tailscale") {
		t.Fatalf("403: %v", err)
	}
}

func TestAttach(t *testing.T) {
	f, c := newFake(t)
	done := make(chan struct{})
	f.attachK = func(ws *wsock.Conn) {
		defer close(done)
		ctx := context.Background()
		for i := 0; i < 3; i++ {
			bin, b, err := ws.Read(ctx)
			if err != nil {
				return
			}
			f.mu.Lock()
			f.frames = append(f.frames, map[bool]string{true: "bin ", false: "text "}[bin]+string(b))
			f.mu.Unlock()
		}
		// The sandbox tries to write the clipboard and draw.
		ws.Write(ctx, true, []byte("\x1b]52;c;cm0gLXJmIH4=\x07\x1b[1mhello\x1b[0m"))
		ws.Close(1000, "session ended")
	}
	in := make(chan []byte, 1)
	resize := make(chan struct{}, 1)
	var out bytes.Buffer
	size := func() (int, int) { return 40, 120 }
	in <- []byte("ls\r")
	resize <- struct{}{}
	err := c.Attach(context.Background(), "s3", AttachIO{In: in, Out: &out, Size: size, Resize: resize})
	if err != nil {
		t.Fatal(err)
	}
	<-done
	if out.String() != "\x1b[1mhello\x1b[0m" {
		t.Fatalf("out %q", out.String())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.frames) != 3 || f.frames[0] != `text {"type":"resize","rows":40,"cols":120}` {
		t.Fatalf("frames %q", f.frames)
	}
	// Keystrokes go as binary; a resize as text.
	rest := strings.Join(f.frames[1:], "|")
	if !strings.Contains(rest, "bin ls\r") || !strings.Contains(rest, `text {"type":"resize"`) {
		t.Fatalf("frames %q", f.frames)
	}
}

func TestAttachEndsWithContext(t *testing.T) {
	f, c := newFake(t)
	f.attachK = func(ws *wsock.Conn) { ws.Read(context.Background()); time.Sleep(5 * time.Second) }
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	c.Attach(ctx, "s3", AttachIO{In: make(chan []byte), Out: &bytes.Buffer{}, Size: func() (int, int) { return 24, 80 }})
	if time.Since(start) > 3*time.Second {
		t.Fatal("attach outlived its context")
	}
}

func TestEscaper(t *testing.T) {
	for _, c := range []struct {
		chunks []string
		out    string
		detach bool
	}{
		{[]string{"~."}, "", true},
		{[]string{"ls\r~."}, "ls\r", true},
		{[]string{"ls\r", "~", "."}, "ls\r", true},
		{[]string{"a~."}, "a~.", false},
		{[]string{"~~"}, "~", false},
		{[]string{"~x"}, "~x", false},
		{[]string{"\n~."}, "\n", true},
		{[]string{"vim ~/.bashrc\r"}, "vim ~/.bashrc\r", false},
	} {
		var e escaper
		var out []byte
		detach := false
		for _, ch := range c.chunks {
			o, d := e.feed([]byte(ch))
			out = append(out, o...)
			if d {
				detach = true
				break
			}
		}
		if string(out) != c.out || detach != c.detach {
			t.Errorf("%q: sent %q detach=%v, want %q %v", c.chunks, out, detach, c.out, c.detach)
		}
	}
}

func TestAttachLeavesOnTildeDot(t *testing.T) {
	// A sandbox can rebind tmux's detach key; ~. leaves from this side.
	f, c := newFake(t)
	f.attachK = func(ws *wsock.Conn) {
		for {
			if _, _, err := ws.Read(context.Background()); err != nil {
				return
			}
		}
	}
	in := make(chan []byte, 2)
	in <- []byte("\r")
	in <- []byte("~.")
	done := make(chan error, 1)
	go func() {
		done <- c.Attach(context.Background(), "s3", AttachIO{In: in, Out: &bytes.Buffer{}, Size: func() (int, int) { return 24, 80 }})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("~. didn't leave the session")
	}
}
