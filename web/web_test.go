package web

import (
	"io/fs"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// TestJS runs the JavaScript unit tests when Node is available.
func TestJS(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	out, err := exec.Command(node, "--test", "shared/lib.test.js", "skybreaker/chatlib.test.js").CombinedOutput()
	if err != nil {
		t.Fatalf("node --test failed:\n%s", out)
	}
}

func TestOverlay(t *testing.T) {
	l := Skybreaker()
	for _, name := range []string{"index.html", "app.js", "base.css", "dom.js", "fonts/figtree.woff2", "icon.svg"} {
		if _, err := fs.Stat(l, name); err != nil {
			t.Errorf("skybreaker UI lacks %s: %v", name, err)
		}
	}
	if _, err := fs.Stat(l, "lib.test.js"); err == nil {
		t.Error("a test file is served")
	}
	n := 0
	fs.WalkDir(l, ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return nil
	})
	if n < 10 {
		t.Errorf("walk found %d files", n)
	}
}

// TestPagesReferenceEmbeddedFiles catches a script or stylesheet added to a
// page but not to the build (it would 404 only in a built binary), and inline
// scripts or handlers, which the CSP blocks, and style attributes, which the
// app's CSP allows (xterm.js needs inline styles), so this test keeps out.
func TestPagesReferenceEmbeddedFiles(t *testing.T) {
	for name, fsys := range map[string]fs.FS{"skybreaker": Skybreaker()} {
		index, err := fs.ReadFile(fsys, "index.html")
		if err != nil {
			t.Fatal(err)
		}
		refs := regexp.MustCompile(`(?:src|href)="/([^"]+)"`).FindAllStringSubmatch(string(index), -1)
		if len(refs) < 4 {
			t.Fatalf("%s: only %d references", name, len(refs))
		}
		for _, m := range refs {
			if !strings.Contains(m[1], ".") {
				continue // a page of the app, such as /activity
			}
			// A ?v= version busts a cache (browsers keep favicons apart).
			if _, err := fs.Stat(fsys, strings.SplitN(m[1], "?", 2)[0]); err != nil {
				t.Errorf("%s/index.html references /%s, which is not embedded", name, m[1])
			}
		}
		if regexp.MustCompile(`<script>|\sstyle="|\son[a-z]+="`).Match(index) {
			t.Errorf("%s/index.html has inline script, style or handler attributes", name)
		}
	}
}

// scripts are the page's scripts, as index.html loads them, and the service
// worker: every script the app runs.
func scripts(t *testing.T) []string {
	t.Helper()
	index, err := fs.ReadFile(Skybreaker(), "index.html")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range regexp.MustCompile(`<script src="/([^"?]+)"`).FindAllStringSubmatch(string(index), -1) {
		out = append(out, m[1])
	}
	if len(out) < 6 {
		t.Fatalf("only %d scripts in index.html", len(out))
	}
	return append(out, "sw.js")
}

func TestNoInnerHTML(t *testing.T) {
	// Agent-written text reaches these pages; it must only ever be text.
	for _, f := range scripts(t) {
		b, err := fs.ReadFile(Skybreaker(), f)
		if err != nil {
			t.Fatal(err)
		}
		for _, bad := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "eval("} {
			if strings.Contains(string(b), bad) {
				t.Errorf("%s uses %s", f, bad)
			}
		}
		// An attribute is set with a string: el('div', {style: '...'}).
		if regexp.MustCompile("\\bstyle:\\s*['\"`]").Match(b) {
			t.Errorf("%s sets a style attribute; the app's CSP allows them, so nothing else stops one", f)
		}
	}
}

// TestNoGlobalCollisions: the pages' scripts share one global scope, so a
// top-level function or const declared in two files silently replaces the
// other (a sidebar's newChat once replaced the chat's).
func TestNoGlobalCollisions(t *testing.T) {
	decl := regexp.MustCompile(`(?m)^(?:async\s+)?(?:function\s+([A-Za-z_$][\w$]*)|(?:const|let|var)\s+([A-Za-z_$][\w$]*))`)
	seen := map[string]string{}
	for _, f := range scripts(t) {
		if f == "sw.js" {
			continue // a worker: its own scope
		}
		b, err := fs.ReadFile(Skybreaker(), f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range decl.FindAllStringSubmatch(string(b), -1) {
			name := m[1] + m[2]
			if prev, ok := seen[name]; ok && prev != f {
				t.Errorf("%s is declared in both %s and %s", name, prev, f)
			}
			seen[name] = f
		}
	}
}
