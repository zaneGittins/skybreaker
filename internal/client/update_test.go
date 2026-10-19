package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"v0.1.0", "v0.1.1", -1},
		{"v0.1.10", "v0.1.9", 1},
		{"v0.2.0-dev.1", "v0.2.0", -1},
		{"v0.2.0-dev.2", "v0.2.0-dev.10", -1},
		{"0.1.0", "v0.1.0", 0},
		{"v1.0.0", "v0.9.9", 1},
	} {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%s, %s) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

// fakeGitHub serves one latest release with a binary for this platform.
func fakeGitHub(t *testing.T, tag, asset string, bin []byte, sum string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/zaneGittins/skybreaker/releases/latest":
			json.NewEncoder(w).Encode(map[string]any{"tag_name": tag, "assets": []map[string]any{
				{"name": asset, "url": srv.URL + "/asset/bin", "browser_download_url": srv.URL + "/dl/bin"},
				{"name": "checksums.txt", "url": srv.URL + "/asset/sums", "browser_download_url": srv.URL + "/dl/sums"},
			}})
		case "/dl/bin", "/asset/bin":
			w.Write(bin)
		case "/dl/sums", "/asset/sums":
			w.Write([]byte(sum + "  " + asset + "\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func sha(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func TestUpdate(t *testing.T) {
	ctx := context.Background()
	asset := AssetName()
	newBin := []byte("#!new skybreaker")
	exe := filepath.Join(t.TempDir(), "skybreaker")
	os.WriteFile(exe, []byte("old"), 0o755)

	gh := fakeGitHub(t, "v0.2.0", asset, newBin, sha(newBin))
	u := Updater{API: gh.URL, Exe: exe, Current: "v0.1.0", HTTP: gh.Client()}
	msg, err := u.Run(ctx)
	if err != nil || !strings.Contains(msg, "v0.2.0") {
		t.Fatalf("update: %q %v", msg, err)
	}
	if b, _ := os.ReadFile(exe); string(b) != string(newBin) {
		t.Fatalf("binary %q", b)
	}
	if fi, _ := os.Stat(exe); fi.Mode().Perm() != 0o755 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}

	// Already current: nothing written.
	u.Current = "v0.2.0"
	os.WriteFile(exe, []byte("current"), 0o755)
	if msg, err := u.Run(ctx); err != nil || !strings.Contains(msg, "up to date") {
		t.Fatalf("current: %q %v", msg, err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "current" {
		t.Fatal("rewrote a current binary")
	}
}

func TestUpdateRefusesBadChecksum(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "skybreaker")
	os.WriteFile(exe, []byte("old"), 0o755)
	gh := fakeGitHub(t, "v0.2.0", AssetName(), []byte("tampered"), sha([]byte("the real one")))
	u := Updater{API: gh.URL, Exe: exe, Current: "v0.1.0", HTTP: gh.Client()}
	if _, err := u.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("bad checksum: %v", err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "old" {
		t.Fatal("binary replaced despite a bad checksum")
	}
	entries, _ := os.ReadDir(filepath.Dir(exe))
	if len(entries) != 1 {
		t.Fatalf("left files behind: %v", entries)
	}
}

func TestUpdateLeavesTheServerToSetup(t *testing.T) {
	u := Updater{Exe: "/usr/local/bin/skybreaker", Current: "v0.1.0", ServerInstalled: func() bool { return true }}
	if _, err := u.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "skybreaker-setup upgrade") {
		t.Fatalf("server install: %v", err)
	}
}
