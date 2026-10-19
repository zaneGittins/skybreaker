package client

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// plainServer is a development-mode Skybreaker over plain HTTP.
func plainServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/meta":
			w.Write([]byte(`{"version":"v0.1.0","user":"you@github"}`))
		case "/api/inbox":
			w.Write([]byte(`{"projects":[{"name":"bifract","harnesses":["claude"]}],"sessions":[
			  {"id":"s1","project":"bifract","harness":"claude","title":"idle one","status":"running","mode":"chat","state":"idle"},
			  {"id":"s2","project":"bifract","harness":"claude","title":"evil\u202etxt.exe","status":"running","mode":"chat","state":"waiting"},
			  {"id":"s3","project":"bifract","harness":"shell","status":"running"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestLs(t *testing.T) {
	srv := plainServer(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SKYBREAKER_URL", srv.URL)
	var out bytes.Buffer
	if err := command(context.Background(), "ls", nil, "v0.1.0", &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "s2") || !strings.Contains(lines[0], "waiting") {
		t.Fatalf("ls:\n%s", out.String())
	}
	if !strings.Contains(lines[0], "evil<U+202E>txt.exe") || strings.Contains(out.String(), "\u202e") {
		t.Fatalf("hidden character printed: %q", lines[0])
	}
	if !strings.Contains(out.String(), "terminal") {
		t.Fatalf("shell session not marked as a terminal:\n%s", out.String())
	}
}

func TestConnectSavesTheAddress(t *testing.T) {
	srv := plainServer(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	var out bytes.Buffer
	if err := command(context.Background(), "connect", []string{srv.URL}, "v0.1.0", &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "as you@github") {
		t.Fatalf("connect said %q", out.String())
	}
	c, err := LoadConfig(filepath.Join(home, ".skybreaker", "config.json"))
	if err != nil || c.URL != srv.URL {
		t.Fatalf("saved %+v %v", c, err)
	}

	// An address that doesn't answer isn't saved.
	os.Remove(filepath.Join(home, ".skybreaker", "config.json"))
	if err := command(context.Background(), "connect", []string{"http://127.0.0.1:1"}, "v0.1.0", &out); err == nil {
		t.Fatal("connected to nothing")
	}
	if _, err := os.Stat(filepath.Join(home, ".skybreaker", "config.json")); err == nil {
		t.Fatal("saved an address that didn't answer")
	}
}

func TestCommandErrors(t *testing.T) {
	srv := plainServer(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SKYBREAKER_URL", srv.URL)
	ctx := context.Background()
	for _, c := range []struct {
		cmd  string
		args []string
		want string
	}{
		{"frobnicate", nil, "unknown command"},
		{"attach", nil, "usage"},
		{"new", nil, "usage"},
		{"new", []string{"nope"}, `no project "nope"`},
		{"connect", []string{"a", "b"}, "usage"},
	} {
		err := command(ctx, c.cmd, c.args, "v0.1.0", &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s %v: %v, want %q", c.cmd, c.args, err, c.want)
		}
	}
	var out bytes.Buffer
	if err := command(ctx, "help", nil, "", &out); err != nil || !strings.Contains(out.String(), "skybreaker attach <id>") {
		t.Fatalf("help: %v %q", err, out.String())
	}
}
