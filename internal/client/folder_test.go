package client

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// makeFolder lays out a project folder with dot directories, a credential
// and a symlink.
func makeFolder(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range map[string]string{
		"README.md":             "hello",
		".env":                  "TOKEN=x",
		".env.example":          "TOKEN=",
		".git/HEAD":             "ref: refs/heads/main\n",
		".claude/settings.json": "{}",
		"certs/server.key":      "key",
		"bin/run.sh":            "#!/bin/sh\n",
		"empty/.keep":           "",
	} {
		p := filepath.Join(root, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	os.Chmod(filepath.Join(root, "bin/run.sh"), 0o755)
	if err := os.Symlink("../README.md", filepath.Join(root, "bin/readme")); err != nil {
		t.Fatal(err)
	}
	return root
}

// untar reads a gzipped tar into name → header and body.
func untar(t *testing.T, b []byte) (map[string]*tar.Header, map[string]string) {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(zr)
	hdrs, bodies := map[string]*tar.Header{}, map[string]string{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(tr)
		hdrs[h.Name], bodies[h.Name] = h, string(data)
	}
	return hdrs, bodies
}

func TestScanFolder(t *testing.T) {
	root := makeFolder(t)
	f, err := ScanFolder(root)
	if err != nil {
		t.Fatal(err)
	}
	if f.Files != 9 || f.Bytes != int64(len("hello")+len("TOKEN=x")+len("TOKEN=")+len("ref: refs/heads/main\n")+len("{}")+len("key")+len("#!/bin/sh\n")) {
		t.Errorf("files %d, bytes %d", f.Files, f.Bytes)
	}
	if !slices.Equal(f.Secrets, []string{".env", "certs/server.key"}) {
		t.Errorf("secrets %q", f.Secrets)
	}
}

func TestScanFolderSkipsWhatTarCantCarry(t *testing.T) {
	root := makeFolder(t)
	l, err := net.Listen("unix", filepath.Join(root, "agent.sock"))
	if err != nil {
		t.Skip("no unix sockets here:", err)
	}
	defer l.Close()
	f, err := ScanFolder(root)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.Skipped, []string{"agent.sock"}) {
		t.Errorf("skipped %q", f.Skipped)
	}
	var b bytes.Buffer
	if err := WriteArchive(&b, root); err != nil {
		t.Fatal(err)
	}
	if hdrs, _ := untar(t, b.Bytes()); hdrs["agent.sock"] != nil {
		t.Error("a socket went into the archive")
	}
}

func TestWriteArchive(t *testing.T) {
	root := makeFolder(t)
	var b bytes.Buffer
	if err := WriteArchive(&b, root); err != nil {
		t.Fatal(err)
	}
	hdrs, bodies := untar(t, b.Bytes())
	for _, name := range []string{".env", ".git/HEAD", ".claude/settings.json", "README.md", "empty/.keep"} {
		if hdrs[name] == nil {
			t.Errorf("%s missing; have %v", name, slices.Sorted(func(yield func(string) bool) {
				for k := range hdrs {
					if !yield(k) {
						return
					}
				}
			}))
		}
	}
	if bodies[".env"] != "TOKEN=x" {
		t.Errorf(".env = %q", bodies[".env"])
	}
	if h := hdrs[".git/"]; h == nil || h.Typeflag != tar.TypeDir {
		t.Errorf(".git/ = %+v", h)
	}
	if h := hdrs["bin/run.sh"]; h == nil || h.Mode&0o100 == 0 {
		t.Errorf("run.sh lost its execute bit: %+v", h)
	}
	// A link stays a link, never what it points at.
	if h := hdrs["bin/readme"]; h == nil || h.Typeflag != tar.TypeSymlink || h.Linkname != "../README.md" || bodies["bin/readme"] != "" {
		t.Errorf("bin/readme = %+v", h)
	}
	for name, h := range hdrs {
		if h.Uname != "" || h.Gname != "" || filepath.IsAbs(name) {
			t.Errorf("%s: %+v", name, h)
		}
	}
}

func TestGitHubRepo(t *testing.T) {
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, ".git"), 0o755)
	for url, want := range map[string]string{
		"https://github.com/zaneGittins/orbit.git":      "zaneGittins/orbit",
		"https://github.com/zaneGittins/orbit":          "zaneGittins/orbit",
		"https://x-access-token:abc@github.com/a/b.git": "a/b",
		"git@github.com:zaneGittins/orbit.git":          "zaneGittins/orbit",
		"ssh://git@github.com/zaneGittins/orbit.git":    "zaneGittins/orbit",
		"https://gitlab.com/zaneGittins/orbit.git":      "",
		"git@github.com:zaneGittins/orbit/extra.git":    "",
		"https://github.com/zaneGittins/../etc.git":     "",
		"": "",
	} {
		run := func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name != "git" || !slices.Contains(args, root) || !slices.Contains(args, "remote.origin.url") {
				t.Errorf("ran %s %q", name, args)
			}
			if url == "" {
				return nil, errors.New("exit status 1")
			}
			return []byte(url + "\n"), nil
		}
		if got := GitHubRepo(context.Background(), run, root); got != want {
			t.Errorf("%q: got %q, want %q", url, got, want)
		}
	}
	// A folder inside a repo isn't the repo: cloning would put the rest
	// around it.
	inner := filepath.Join(root, "sub")
	os.Mkdir(inner, 0o755)
	never := func(context.Context, string, ...string) ([]byte, error) {
		t.Error("asked git about a folder without its own .git")
		return nil, nil
	}
	if got := GitHubRepo(context.Background(), never, inner); got != "" {
		t.Errorf("inner folder: %q", got)
	}
}

func TestProjectNameFor(t *testing.T) {
	for dir, want := range map[string]string{
		"/home/z/My App":    "my-app",
		"/home/z/web.app_2": "web-app-2",
		"/home/z/--x--":     "x",
		"/home/z/___":       "",
		"/home/z/" + "abcdefghijabcdefghijabcdefghijabcdefghij-more": "abcdefghijabcdefghijabcdefghijabcdefghij",
	} {
		if got := ProjectNameFor(dir); got != want {
			t.Errorf("%q: got %q, want %q", dir, got, want)
		}
	}
}
