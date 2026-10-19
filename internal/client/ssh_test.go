package client

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zaneGittins/skybreaker/internal/wsock"
)

func TestUpsertSSHConfig(t *testing.T) {
	block := SSHBlock("/usr/local/bin/skybreaker", "/home/me/.skybreaker/ssh/id_ed25519")
	if !strings.HasPrefix(block, sshBegin+"\n") || !strings.HasSuffix(block, sshEnd+"\n") {
		t.Fatalf("block:\n%s", block)
	}
	for _, want := range []string{"Host *.skybreaker", "ProxyCommand /usr/local/bin/skybreaker ssh-proxy %h", "IdentitiesOnly yes", "ForwardAgent no", "ForwardX11 no", "User root"} {
		if !strings.Contains(block, want) {
			t.Errorf("block lacks %q", want)
		}
	}
	mine := "Host work\n  HostName work.example.com\n\nHost *\n  ForwardAgent yes\n"
	got, err := UpsertSSHConfig([]byte(mine), block)
	// First: ssh takes the first value it finds, so a Host * above ours
	// would win.
	if err != nil || string(got) != block+"\n"+mine {
		t.Fatalf("%v\n%s", err, got)
	}
	again, err := UpsertSSHConfig(got, block)
	if err != nil || !bytes.Equal(again, got) {
		t.Fatalf("not idempotent: %v\n%s", err, again)
	}
	// An old block anywhere is replaced, the rest kept as it was.
	old := "Host a\n  User a\n" + sshBegin + "\nHost old\n" + sshEnd + "\nHost b\n  User b\n"
	got, err = UpsertSSHConfig([]byte(old), block)
	if err != nil || string(got) != block+"\nHost a\n  User a\nHost b\n  User b\n" {
		t.Fatalf("%v\n%s", err, got)
	}
	if got, _ := UpsertSSHConfig(nil, block); string(got) != block {
		t.Fatalf("empty config:\n%s", got)
	}
	// A begin marker without its end: refuse rather than eat the rest.
	if _, err := UpsertSSHConfig([]byte(sshBegin+"\nHost mine\n"), block); err == nil {
		t.Fatal("an unterminated block was replaced")
	}
}

func TestSSHBlockQuotesThePath(t *testing.T) {
	b := SSHBlock("/Users/me/my bin/skybreaker", "/Users/me/.skybreaker/ssh/id_ed25519")
	if !strings.Contains(b, `ProxyCommand '/Users/me/my bin/skybreaker' ssh-proxy %h`) {
		t.Fatalf("%s", b)
	}
	if err := checkPath("/tmp/x'; touch /tmp/pwned; '"); err == nil {
		t.Fatal("a quote in the path")
	}
	if err := checkPath("/tmp/a\nHost *"); err == nil {
		t.Fatal("a newline in the path")
	}
}

func TestHostProject(t *testing.T) {
	if p, err := HostProject("orbit.skybreaker"); err != nil || p != "orbit" {
		t.Fatalf("%q %v", p, err)
	}
	for _, bad := range []string{"orbit", "orbit.skybreaker.evil.com", "../x.skybreaker", "a;b.skybreaker", ".skybreaker", "Orbit Space.skybreaker"} {
		if _, err := HostProject(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestSSHProxy(t *testing.T) {
	f, c := newFake(t)
	var origin string
	f.Config.Handler.(*http.ServeMux).HandleFunc("GET /api/projects/{p}/ssh", func(w http.ResponseWriter, r *http.Request) {
		origin = r.Header.Get("Origin")
		ws, err := wsock.Accept(w, r)
		if err != nil {
			return
		}
		defer ws.Close(1000, "")
		ws.Write(context.Background(), true, []byte("SSH-2.0-sandbox\r\n"))
		for {
			bin, b, err := ws.Read(context.Background())
			if err != nil {
				return
			}
			if bin {
				ws.Write(context.Background(), true, bytes.ToUpper(b))
			}
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pr, pw := io.Pipe()
	var out syncBuffer
	done := make(chan error, 1)
	go func() { done <- c.SSHProxy(ctx, "bifract", pr, &out) }()
	pw.Write([]byte("hello"))
	for !strings.Contains(out.String(), "HELLO") && ctx.Err() == nil {
		time.Sleep(10 * time.Millisecond)
	}
	pw.Close() // ssh hung up
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if out.String() != "SSH-2.0-sandbox\r\nHELLO" || origin != c.origin() {
		t.Fatalf("out %q origin %q", out.String(), origin)
	}
}

func TestVSCodeSetup(t *testing.T) {
	f, c := newFake(t)
	var keys []string
	f.Config.Handler.(*http.ServeMux).HandleFunc("POST /api/ssh-keys", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Name, Key string }
		json.NewDecoder(r.Body).Decode(&in)
		for _, k := range keys {
			if k == in.Key {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"error":"that key is already added, as \"laptop\""}`))
				return
			}
		}
		keys = append(keys, in.Key)
		w.Write([]byte(`{"id":"k1"}`))
	})
	home := t.TempDir()
	// ssh-keygen, as far as setup can tell.
	keygen := 0
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		keygen++
		path := args[len(args)-1]
		os.WriteFile(path, []byte("PRIVATE"), 0o600)
		os.WriteFile(path+".pub", []byte("ssh-ed25519 AAAAC3 me@laptop\n"), 0o644)
		return nil, nil
	}
	// Your config is a symlink into your dotfiles: it stays one.
	os.MkdirAll(filepath.Join(home, "dotfiles"), 0o700)
	os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	os.WriteFile(filepath.Join(home, "dotfiles", "ssh_config"), []byte("Host *\n  User me\n"), 0o600)
	os.Symlink(filepath.Join(home, "dotfiles", "ssh_config"), filepath.Join(home, ".ssh", "config"))

	var out bytes.Buffer
	for range 2 {
		if err := setupVSCode(context.Background(), c, vscodeEnv{home: home, exe: "/usr/local/bin/skybreaker", host: "laptop", run: run}, &out); err != nil {
			t.Fatal(err)
		}
	}
	if keygen != 1 || len(keys) != 1 || keys[0] != "ssh-ed25519 AAAAC3 me@laptop" {
		t.Fatalf("keygen %d, keys %q", keygen, keys)
	}
	if fi, err := os.Lstat(filepath.Join(home, ".ssh", "config")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the symlink was replaced: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(home, "dotfiles", "ssh_config"))
	if !strings.HasPrefix(string(b), sshBegin) || !strings.HasSuffix(string(b), "Host *\n  User me\n") || strings.Count(string(b), sshBegin) != 1 {
		t.Fatalf("config:\n%s", b)
	}
	if !strings.Contains(string(b), "IdentityFile "+filepath.Join(home, ".skybreaker", "ssh", "id_ed25519")) {
		t.Fatalf("config names another key:\n%s", b)
	}
	if !strings.Contains(out.String(), "bifract.skybreaker") {
		t.Fatalf("setup didn't say how to connect:\n%s", out.String())
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestCodeCommand(t *testing.T) {
	srv := plainServer(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SKYBREAKER_URL", srv.URL)
	var ran []string
	old := runCode
	runCode = func(args ...string) error { ran = args; return nil }
	defer func() { runCode = old }()
	if err := command(context.Background(), "code", []string{"bifract"}, "v0.1.0", io.Discard); err != nil {
		t.Fatal(err)
	}
	if strings.Join(ran, " ") != "--remote ssh-remote+bifract.skybreaker /workspace" {
		t.Fatalf("ran code %q", ran)
	}
	for _, bad := range []string{"ghost", "--help", "a b"} {
		if err := command(context.Background(), "code", []string{bad}, "v0.1.0", io.Discard); err == nil {
			t.Errorf("code %q accepted", bad)
		}
	}
	if err := command(context.Background(), "ssh-proxy", []string{"evil.example.com"}, "v0.1.0", io.Discard); err == nil {
		t.Error("ssh-proxy to a host that isn't a project's")
	}
}
