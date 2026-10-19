package api

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zaneGittins/skybreaker/internal/control/sshkeys"
	"github.com/zaneGittins/skybreaker/internal/control/terminal"
	"github.com/zaneGittins/skybreaker/internal/wsock"
)

func (b *fakeBackend) SetAuthorizedKeys(k []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.authorized = string(k)
	return nil
}

// SSHConn hands the relay one end of a pipe whose other end echoes, as a
// sandbox's sshd would answer.
func (b *fakeBackend) SSHConn(_ context.Context, project string) (net.Conn, error) {
	a, z := net.Pipe()
	go func() { io.Copy(z, z) }()
	return a, nil
}

func pubKey() string {
	blob := binary.BigEndian.AppendUint32(nil, 11)
	blob = append(blob, "ssh-ed25519"...)
	blob = binary.BigEndian.AppendUint32(blob, 32)
	blob = append(blob, make([]byte, 32)...)
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob) + " me@laptop"
}

func withSSH(t *testing.T) func(*Deps) {
	keys, err := sshkeys.Open(filepath.Join(t.TempDir(), "ssh-keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	return func(d *Deps) {
		d.SSHKeys = keys
		d.Upgrade = func(w http.ResponseWriter, r *http.Request) (terminal.Conn, error) { return wsock.Accept(w, r) }
	}
}

func TestSSHKeysAPI(t *testing.T) {
	f := newFixture(t, you, withSSH(t))
	code, body := f.do(t, "POST", "/api/ssh-keys", `{"name":"laptop","key":"`+pubKey()+`"}`, nil)
	if code != 200 || !strings.Contains(body, `"added_by":"zane@example.com"`) {
		t.Fatalf("add: %d %s", code, body)
	}
	if !strings.HasPrefix(f.backend.authorized, "ssh-ed25519 ") || !strings.Contains(f.backend.authorized, " skybreaker-") {
		t.Fatalf("sandboxes got %q", f.backend.authorized)
	}
	if code, _ := f.do(t, "POST", "/api/ssh-keys", `{"name":"x","key":"command=\"sh\" `+pubKey()+`"}`, nil); code != http.StatusBadRequest {
		t.Fatalf("key with options: %d", code)
	}
	code, body = f.do(t, "GET", "/api/ssh-keys", "", nil)
	if code != 200 || !strings.Contains(body, `"name":"laptop"`) {
		t.Fatalf("list: %d %s", code, body)
	}
	id := body[strings.Index(body, `"id":"`)+6:]
	id = id[:strings.Index(id, `"`)]
	if code, _ := f.do(t, "DELETE", "/api/ssh-keys/"+id, "", nil); code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	if f.backend.authorized != "" {
		t.Fatalf("after delete sandboxes have %q", f.backend.authorized)
	}
}

func TestSSHRelay(t *testing.T) {
	f := newFixture(t, you, withSSH(t))
	u := "ws" + strings.TrimPrefix(f.ts.URL, "http") + "/api/projects/orbit/ssh"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := wsock.Dial(ctx, u, http.Header{"Origin": {f.ts.URL}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(1000, "")
	c.Write(ctx, true, []byte("SSH-2.0-OpenSSH_9.6\r\n"))
	if _, b, err := c.Read(ctx); err != nil || string(b) != "SSH-2.0-OpenSSH_9.6\r\n" {
		t.Fatalf("relay echo %q %v", b, err)
	}
	// Only the app's own pages, never a preview (another port of this host).
	host, _ := url.Parse(f.ts.URL)
	if _, err := wsock.Dial(ctx, u, http.Header{"Origin": {"http://" + host.Hostname() + ":1"}}, nil); err == nil {
		t.Fatal("a preview page opened an SSH relay")
	}
	if _, err := wsock.Dial(ctx, "ws"+strings.TrimPrefix(f.ts.URL, "http")+"/api/projects/ghost/ssh", http.Header{"Origin": {f.ts.URL}}, nil); err == nil {
		t.Fatal("relay to a project that doesn't exist")
	}
}

// sshd checks a key only at login, so removing one must cut the connections
// already open.
func TestSSHKeyDeleteCutsRelays(t *testing.T) {
	f := newFixture(t, you, withSSH(t))
	code, body := f.do(t, "POST", "/api/ssh-keys", `{"name":"laptop","key":"`+pubKey()+`"}`, nil)
	if code != 200 {
		t.Fatalf("add: %d %s", code, body)
	}
	id := body[strings.Index(body, `"id":"`)+6:]
	id = id[:strings.Index(id, `"`)]
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := wsock.Dial(ctx, "ws"+strings.TrimPrefix(f.ts.URL, "http")+"/api/projects/orbit/ssh", http.Header{"Origin": {f.ts.URL}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(1000, "")
	c.Write(ctx, true, []byte("x"))
	if _, _, err := c.Read(ctx); err != nil {
		t.Fatal(err)
	}
	if code, _ := f.do(t, "DELETE", "/api/ssh-keys/"+id, "", nil); code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	if _, _, err := c.Read(ctx); err == nil || ctx.Err() != nil {
		t.Fatalf("relay still open after its key went: %v", err)
	}
}

func TestSSHRelayIdle(t *testing.T) {
	old := sshIdle
	sshIdle = 80 * time.Millisecond
	defer func() { sshIdle = old }()
	f := newFixture(t, you, withSSH(t))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := wsock.Dial(ctx, "ws"+strings.TrimPrefix(f.ts.URL, "http")+"/api/projects/orbit/ssh", http.Header{"Origin": {f.ts.URL}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(1000, "")
	if _, _, err := c.Read(ctx); err == nil || ctx.Err() != nil {
		t.Fatalf("a silent client wasn't dropped: %v", err)
	}
}
