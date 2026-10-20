package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cgi"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const contentRules = `"content_rules": {
    "commit": {"max_subject": 50, "forbid": ["(?im)^co-authored-by:.*claude"]},
    "pull_request": {"max_title": 30, "forbid": ["(?i)generated with \\[claude code\\]"]}
  },
`

// contentPolicy is the test policy with content rules, REST pull request
// rules, and receive-pack's action swapped for push.
func contentPolicy(push string) string {
	p := strings.Replace(testConfig, "{\n  \"projects\"", "{\n  "+contentRules+"  \"projects\"", 1)
	// A broad rule, so path variants it still matches are tried too.
	p = strings.Replace(p, `{"match": "GET /repos/{repo}/**", "action": "auto"},`,
		`{"match": "GET /repos/{repo}/**", "action": "auto"},
         {"match": "POST,PATCH /repos/{repo}/**", "action": "auto"},`, 1)
	return strings.Replace(p, `"POST /{repo}.git/git-receive-pack", "action": "auto"`, `"POST /{repo}.git/git-receive-pack", "action": "`+push+`"`, 1)
}

// gitEnv is a clean environment for git: none of the caller's config, and
// no proxy or CA settings (a sandbox has both) that would outrank the
// test's -c ones.
func gitEnv(home string) []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch strings.ToUpper(k) {
		case "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "GIT_SSL_CAINFO", "GIT_SSL_CAPATH", "GIT_SSL_NO_VERIFY", "SSL_CERT_FILE", "SSL_CERT_DIR":
			continue
		}
		env = append(env, kv)
	}
	return append(env, "HOME="+home, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
}

func runGit(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnv(t.TempDir())
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := runGit(t, dir, args...)
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(out)
}

// gitWorld is a world whose github.test is git http-backend serving
// zaneGittins/orbit.git, and a work tree whose git goes through the broker.
type gitWorld struct {
	*world
	bare, work string
	gitArgs    []string // -c settings for the proxy and the broker's CA
	remote     string

	mu       sync.Mutex
	received [][]byte // every request body github.test got, whole or cut short
}

func newGitWorld(t *testing.T, policy string) *gitWorld {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	bare := filepath.Join(root, "zaneGittins", "orbit.git")
	os.MkdirAll(filepath.Dir(bare), 0o755)
	mustGit(t, root, "init", "-q", "--bare", "-b", "main", bare)
	mustGit(t, bare, "config", "http.receivepack", "true")
	execPath := mustGit(t, root, "--exec-path")
	backend := &cgi.Handler{
		Path: filepath.Join(execPath, "git-http-backend"),
		Env:  []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1", "GIT_CONFIG_NOSYSTEM=1"},
	}
	// The backend is handed whole bodies only: git http-backend spins on a
	// body that ends early, which is what a refused push is upstream.
	var g *gitWorld
	whole := http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		g.mu.Lock()
		g.received = append(g.received, b)
		g.mu.Unlock()
		if err != nil {
			http.Error(rw, "body cut short", http.StatusBadRequest)
			return
		}
		r.Body, r.ContentLength = io.NopCloser(bytes.NewReader(b)), int64(len(b))
		backend.ServeHTTP(rw, r)
	})
	w := newWorldWith(t, policy, map[string]http.Handler{"github.test": whole})
	caFile := filepath.Join(root, "broker-ca.pem")
	os.WriteFile(caFile, w.brokerCA.CertPEM(), 0o644)
	proxy := strings.Replace(w.proxy.URL, "http://", "http://orbit:"+w.token+"@", 1)
	g = &gitWorld{world: w, bare: bare, work: t.TempDir(), remote: "https://github.test/zaneGittins/orbit.git",
		gitArgs: []string{"-c", "http.proxy=" + proxy, "-c", "http.sslCAInfo=" + caFile}}
	mustGit(t, g.work, "init", "-q", "-b", "main")
	return g
}

func (g *gitWorld) commit(msg, file string) string {
	g.t.Helper()
	os.WriteFile(filepath.Join(g.work, "f.txt"), []byte(file), 0o644)
	mustGit(g.t, g.work, "add", "-A")
	mustGit(g.t, g.work, "commit", "-q", "-m", msg)
	return mustGit(g.t, g.work, "rev-parse", "HEAD")
}

func (g *gitWorld) push(ref string) (string, error) {
	g.t.Helper()
	return runGit(g.t, g.work, append(g.gitArgs, "push", g.remote, "HEAD:"+ref)...)
}

func (g *gitWorld) upstreamRef(ref string) string {
	out, _ := runGit(g.t, g.bare, "rev-parse", "-q", "--verify", ref)
	return strings.TrimSpace(out)
}

// TestCommitRulesWithGit pushes with real git through the broker's MITM to
// a real git server: clean pushes land (the second on top of existing
// history, which git would send as a thin pack had the broker not asked
// for no-thin), and a push that breaks the rules shows why and lands
// nothing.
func TestCommitRulesWithGit(t *testing.T) {
	g := newGitWorld(t, contentPolicy("auto"))
	big := strings.Repeat("content that stays the same between versions\n", 400)
	first := g.commit("First change", big+"v1\n")
	if out, err := g.push("refs/heads/agent/ORB-1"); err != nil {
		t.Fatalf("clean push: %v\n%s", err, out)
	}
	second := g.commit("Second change", big+"v2\n")
	if out, err := g.push("refs/heads/agent/ORB-1"); err != nil {
		t.Fatalf("push on top: %v\n%s", err, out)
	}
	if got := g.upstreamRef("refs/heads/agent/ORB-1"); got != second {
		t.Fatalf("upstream ref = %q, want %q (first %s)", got, second, first)
	}

	bad := g.commit("A subject line that goes on far longer than fifty characters\n\nCo-Authored-By: Claude <noreply@anthropic.com>", big+"v3\n")
	out, err := g.push("refs/heads/agent/ORB-1")
	if err == nil {
		t.Fatalf("refused push exited 0:\n%s", out)
	}
	for _, want := range []string{
		"remote: skybreaker: commit " + bad[:12] + ": subject is 60 characters; the limit is 50",
		`remote: skybreaker: commit ` + bad[:12] + `: message matches forbidden pattern (?im)^co-authored-by:.*claude: "Co-Authored-By: Claude"`,
		"remote: skybreaker: Fix the message (git commit --amend",
		"! [remote rejected] HEAD -> agent/ORB-1 (commit rules)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("git output lacks %q:\n%s", want, out)
		}
	}
	if got := g.upstreamRef("refs/heads/agent/ORB-1"); got != second {
		t.Fatalf("refused push moved the ref to %q", got)
	}
	if obj, _ := runGit(t, g.bare, "cat-file", "-t", bad); strings.TrimSpace(obj) == "commit" {
		t.Error("refused commit is in the upstream repository")
	}

	// The ref policy's refusals read the same way.
	out, err = g.push("refs/heads/main")
	if err == nil || !strings.Contains(out, "remote: skybreaker: ref policy: refs/heads/main is outside refs/heads/agent/*") {
		t.Errorf("ref policy refusal: %v\n%s", err, out)
	}
	if !strings.Contains(g.auditBuf.String(), "commit rules") {
		t.Error("refusal not audited")
	}
}

// TestCommitRulesRefuseThinPack sends a thin pack, as a client that ignores
// no-thin would: it can't be read whole, so it is refused, and the
// upstream never gets the whole pack.
func TestCommitRulesRefuseThinPack(t *testing.T) {
	g := newGitWorld(t, contentPolicy("auto"))
	big := strings.Repeat("content that stays the same between versions\n", 400)
	base := g.commit("Base", big+"v1\n")
	if out, err := g.push("refs/heads/agent/x"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	tip := g.commit("Tip", big+"v2\n")
	cmd := exec.Command("git", "pack-objects", "--stdout", "--revs", "--thin", "--delta-base-offset", "-q")
	cmd.Dir, cmd.Env = g.work, gitEnv(t.TempDir())
	cmd.Stdin = strings.NewReader(tip + "\n^" + base + "\n")
	pack, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	body := pkt(base+" "+tip+" refs/heads/agent/x\x00report-status side-band-64k\n") + "0000" + string(pack)
	code, resp, _ := do(t, g.orbit(), "POST", "https://github.test/zaneGittins/orbit.git/git-receive-pack", body,
		map[string]string{"Content-Type": "application/x-git-receive-pack-request"})
	if code != 200 || !strings.Contains(resp, "thin pack") || !strings.Contains(resp, "ng refs/heads/agent/x push can't be checked") {
		t.Fatalf("thin pack: %d %q", code, resp)
	}
	if got := g.upstreamRef("refs/heads/agent/x"); got != base {
		t.Fatalf("thin push moved the ref to %q", got)
	}
}

// TestCommitRulesBeforeApproval: a push that waits for approval is
// checked first, so nobody is asked about a push that would be refused.
func TestCommitRulesBeforeApproval(t *testing.T) {
	g := newGitWorld(t, contentPolicy("ask"))
	g.commit("Fine\n\nCo-authored-by: claude <x@example.com>", "a\n")
	out, err := g.push("refs/heads/agent/x")
	if err == nil || !strings.Contains(out, "forbidden pattern") {
		t.Fatalf("%v\n%s", err, out)
	}
	if n := len(g.approvals.List()); n != 0 {
		t.Fatalf("%d approvals raised for a refused push", n)
	}
}

func TestPullRequestRules(t *testing.T) {
	w := newWorldWith(t, contentPolicy("auto"), nil)
	c := w.orbit()
	jsonHdr := map[string]string{"Content-Type": "application/json"}
	gql := func(query string, vars any) string {
		b, _ := json.Marshal(map[string]any{"query": query, "variables": vars})
		return string(b)
	}
	const create = `mutation($input: CreatePullRequestInput!) { createPullRequest(input: $input) { pullRequest { url } } }`
	footer := "Body\n\n🤖 Generated with [Claude Code](https://claude.com/claude-code)"
	cases := []struct {
		name, method, url, body string
		refused                 string // "" when it goes through
	}{
		{"gh pr create, clean", "POST", "https://api.github.test/graphql", gql(create, map[string]any{"input": map[string]any{"title": "Fix", "body": "Body"}}), ""},
		{"gh pr create, footer", "POST", "https://api.github.test/graphql", gql(create, map[string]any{"input": map[string]any{"title": "Fix", "body": footer}}),
			`skybreaker: pull request body matches forbidden pattern (?i)generated with \\[claude code\\]: \"Generated with [Claude Code]\"`},
		{"inline title too long", "POST", "https://api.github.test/graphql", gql(`mutation { createPullRequest(input: {title: "A title well over thirty characters"}) { clientMutationId } }`, nil),
			"skybreaker: pull request title is 35 characters; the limit is 30"},
		{"unreadable input", "POST", "https://api.github.test/graphql", gql(`mutation($b: String = "x") { createPullRequest(input: {title: "T", body: $b}) { clientMutationId } }`, nil),
			"can't be checked against the pull request rules"},
		{"other mutations aren't checked", "POST", "https://api.github.test/graphql", gql(`mutation { addComment(input: {subjectId: "S", body: "Generated with [Claude Code]"}) { clientMutationId } }`, nil), ""},
		{"REST create, clean", "POST", "https://api.github.test/repos/zaneGittins/orbit/pulls", `{"title": "Fix", "head": "agent/x", "base": "main", "body": "ok"}`, ""},
		{"REST create, footer", "POST", "https://api.github.test/repos/zaneGittins/orbit/pulls", `{"title": "Fix", "body": "Generated with [claude code]"}`, "forbidden pattern"},
		{"REST edit, long title", "PATCH", "https://api.github.test/repos/zaneGittins/orbit/pulls/7", `{"title": "A title well over thirty characters"}`, "title is 35 characters"},
		{"REST duplicate keys", "PATCH", "https://api.github.test/repos/zaneGittins/orbit/pulls/7", `{"body": "ok", "body": "Generated with [Claude Code]"}`, "duplicate"},
		{"REST trailing slash", "POST", "https://api.github.test/repos/zaneGittins/orbit/pulls/", `{"title": "Generated with [Claude Code]"}`, "forbidden pattern"},
		{"REST other case", "PATCH", "https://api.github.test/repos/zaneGittins/orbit/PULLS/7", `{"title": "Generated with [Claude Code]"}`, "forbidden pattern"},
		{"REST POST to a pull", "POST", "https://api.github.test/repos/zaneGittins/orbit/pulls/7", `{"title": "Generated with [Claude Code]"}`, "forbidden pattern"},
		{"REST issue comment isn't a pull", "POST", "https://api.github.test/repos/zaneGittins/orbit/issues/7/comments", `{"body": "Generated with [Claude Code]"}`, ""},
		{"GraphQL other case", "POST", "https://api.github.test/GraphQL", gql(create, map[string]any{"input": map[string]any{"title": "Fix", "body": footer}}), "forbidden pattern"},
	}
	for _, tc := range cases {
		before := len(w.hitsFor("api.github.test"))
		code, body, _ := do(t, c, tc.method, tc.url, tc.body, jsonHdr)
		reached := len(w.hitsFor("api.github.test")) > before
		if tc.refused == "" {
			if code != 200 || !reached {
				t.Errorf("%s: %d %s (reached upstream: %v)", tc.name, code, body, reached)
			}
			continue
		}
		if code != http.StatusForbidden || reached || !strings.Contains(body, tc.refused) {
			t.Errorf("%s: %d %s (reached upstream: %v), want refusal with %q", tc.name, code, body, reached, tc.refused)
		}
		var msg struct {
			Message string
			Errors  []struct{ Message string }
		}
		if json.Unmarshal([]byte(body), &msg) != nil || !strings.HasPrefix(msg.Message, "skybreaker: ") || len(msg.Errors) == 0 {
			t.Errorf("%s: not GitHub's error shape: %s", tc.name, body)
		}
	}
}

// Pull request rules apply on GitHub's API host only: another service's
// GraphQL endpoint is left as it was.
func TestPullRequestRulesOnlyOnGitHub(t *testing.T) {
	w := newWorldWith(t, contentPolicy("auto"), nil)
	batched := `[{"query": "mutation { createPullRequest(input: {title: \"Generated with [Claude Code]\"}) { clientMutationId } }"}]`
	if code, body, _ := do(t, w.orbit(), "POST", "https://open.test/graphql", batched, map[string]string{"Content-Type": "application/json"}); code != 200 {
		t.Fatalf("open.test/graphql: %d %s", code, body)
	}
}

// TestPaddedPushRefused: bytes after the pack mustn't let its checksum
// through before the verdict. The pack is valid and the padding is
// refused, so the push must land nothing.
func TestPaddedPushRefused(t *testing.T) {
	g := newGitWorld(t, contentPolicy("auto"))
	base := g.commit("Base", "a\n")
	if out, err := g.push("refs/heads/agent/x"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	tip := g.commit("Tip\n\nCo-Authored-By: Claude <noreply@anthropic.com>", "b\n")
	cmd := exec.Command("git", "pack-objects", "--stdout", "--revs", "--delta-base-offset", "-q")
	cmd.Dir, cmd.Env = g.work, gitEnv(t.TempDir())
	cmd.Stdin = strings.NewReader(tip + "\n^" + base + "\n")
	pack, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	for name, pad := range map[string]int{"short": 200, "long": 200 << 10} {
		body := pkt(base+" "+tip+" refs/heads/agent/x\x00report-status side-band-64k\n") + "0000" + string(pack) + strings.Repeat("\x00", pad)
		code, resp, _ := do(t, g.orbit(), "POST", "https://github.test/zaneGittins/orbit.git/git-receive-pack", body,
			map[string]string{"Content-Type": "application/x-git-receive-pack-request"})
		if code != 200 || !strings.Contains(resp, "ng refs/heads/agent/x") {
			t.Errorf("%s padding: %d %q", name, code, resp)
		}
		if got := g.upstreamRef("refs/heads/agent/x"); got != base {
			t.Fatalf("%s padding: the refused push moved the ref to %s", name, got)
		}
	}
	// The pack never reached upstream whole: its checksum stayed behind.
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, b := range g.received {
		if bytes.Contains(b, pack) {
			t.Fatal("upstream got the whole pack, checksum included")
		}
	}
}

// An unusual case in the path doesn't skip the commit check.
func TestCommitRulesPathCase(t *testing.T) {
	g := newGitWorld(t, contentPolicy("auto"))
	tip := g.commit("Tip\n\nCo-Authored-By: Claude <noreply@anthropic.com>", "b\n")
	cmd := exec.Command("git", "pack-objects", "--stdout", "--revs", "--delta-base-offset", "-q")
	cmd.Dir, cmd.Env = g.work, gitEnv(t.TempDir())
	cmd.Stdin = strings.NewReader(tip + "\n")
	pack, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	body := pkt(strings.Repeat("0", 40)+" "+tip+" refs/heads/agent/x\x00report-status side-band-64k\n") + "0000" + string(pack)
	code, resp, _ := do(t, g.orbit(), "POST", "https://github.test/zaneGittins/orbit.git/GIT-RECEIVE-PACK", body,
		map[string]string{"Content-Type": "application/x-git-receive-pack-request"})
	if code != 200 || !strings.Contains(resp, "forbidden pattern") {
		t.Fatalf("%d %q", code, resp)
	}
}

// With no content rules, nothing about a push changes: the advertisement
// passes as it is.
func TestNoContentRulesNoRewrite(t *testing.T) {
	g := newGitWorld(t, testConfig)
	g.commit("x", "a\n")
	if out, err := g.push("refs/heads/agent/x"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	code, body, _ := do(t, g.orbit(), "GET", "https://github.test/zaneGittins/orbit.git/info/refs?service=git-receive-pack", "", nil)
	if code != 200 || bytes.Contains([]byte(body), []byte("no-thin")) {
		t.Fatalf("%d %q", code, body)
	}
	g2 := newGitWorld(t, contentPolicy("auto"))
	code, body, _ = do(t, g2.orbit(), "GET", "https://github.test/zaneGittins/orbit.git/info/refs?service=git-receive-pack", "", nil)
	if code != 200 || !strings.Contains(body, "no-thin") {
		t.Fatalf("with rules: %d %q", code, body)
	}
}
