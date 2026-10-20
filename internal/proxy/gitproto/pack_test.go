package gitproto

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func needGit(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	return p
}

// repo is a scratch repository with a fixed identity and no user config.
type repo struct {
	t   testing.TB
	dir string
}

func newRepo(t *testing.T) *repo {
	needGit(t)
	r := &repo{t, t.TempDir()}
	r.git("init", "-q", "-b", "main")
	return r
}

func (r *repo) git(args ...string) string {
	r.t.Helper()
	return gitIn(r.t, r.dir, nil, args...)
}

func gitIn(t testing.TB, dir string, stdin []byte, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// gitEnv is a clean environment for git: none of the caller's config, and
// no proxy or CA settings (a sandbox has both).
func gitEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch strings.ToUpper(k) {
		case "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "GIT_SSL_CAINFO", "GIT_SSL_CAPATH", "GIT_SSL_NO_VERIFY", "SSL_CERT_FILE", "SSL_CERT_DIR":
			continue
		}
		env = append(env, kv)
	}
	return append(env, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
}

func (r *repo) commit(msg string, files map[string]string) string {
	r.t.Helper()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(r.dir, name), []byte(body), 0o644); err != nil {
			r.t.Fatal(err)
		}
	}
	r.git("add", "-A")
	r.git("commit", "-q", "--allow-empty", "-m", msg)
	return strings.TrimSpace(r.git("rev-parse", "HEAD"))
}

// pack runs pack-objects over revs, with extra flags such as --thin.
func (r *repo) pack(revs string, flags ...string) []byte {
	r.t.Helper()
	cmd := exec.Command("git", append([]string{"pack-objects", "--stdout", "--revs", "--delta-base-offset", "-q"}, flags...)...)
	cmd.Dir, cmd.Env = r.dir, gitEnv()
	cmd.Stdin = strings.NewReader(revs)
	out, err := cmd.Output()
	if err != nil {
		r.t.Fatalf("pack-objects: %v", err)
	}
	return out
}

func pushBody(old, new, ref string, pack []byte) []byte {
	var b bytes.Buffer
	b.WriteString(pkt(old + " " + new + " " + ref + "\x00report-status side-band-64k\n"))
	b.WriteString("0000")
	b.Write(pack)
	return b.Bytes()
}

func readAll(t *testing.T, body []byte) ([]Commit, error) {
	t.Helper()
	var got []Commit
	_, err := ReadPush(bytes.NewReader(body), func(c Commit) error { got = append(got, c); return nil }, nil)
	return got, err
}

func TestReadPushCommits(t *testing.T) {
	r := newRepo(t)
	// Long, similar messages and files, so pack-objects stores deltas,
	// commits included.
	long := strings.Repeat("A line of the message that repeats so git deltifies it.\n", 40)
	var want []string
	for i := 0; i < 6; i++ {
		msg := fmt.Sprintf("Change %d\n\n%s\nCo-Authored-By: Claude <noreply@anthropic.com>\n", i, long)
		if i%2 == 0 {
			msg = fmt.Sprintf("Change %d\n\n%s", i, long)
		}
		want = append(want, r.commit(msg, map[string]string{"f.txt": strings.Repeat(fmt.Sprintf("line %d\n", i), 500) + long}))
	}
	pack := r.pack("HEAD\n", "--window=50", "--depth=50")
	got, err := readAll(t, pushBody(zero, want[len(want)-1], "refs/heads/main", pack))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d commits, want %d", len(got), len(want))
	}
	byOID := map[string]string{}
	for _, c := range got {
		byOID[c.OID] = c.Message
	}
	for i, oid := range want {
		msg, ok := byOID[oid]
		if !ok {
			t.Fatalf("commit %d (%s) missing", i, oid)
		}
		if body := r.git("log", "-1", "--format=%B", oid); strings.TrimSpace(body) != strings.TrimSpace(msg) {
			t.Errorf("commit %d message:\n got %q\nwant %q", i, msg, body)
		}
		if has := strings.Contains(msg, "Co-Authored-By: Claude"); has != (i%2 == 1) {
			t.Errorf("commit %d: trailer present = %v", i, has)
		}
	}
}

func TestReadPushThinPackRefused(t *testing.T) {
	r := newRepo(t)
	big := strings.Repeat("some content that stays the same between versions\n", 400)
	base := r.commit("base", map[string]string{"f.txt": big + "v1\n"})
	tip := r.commit("tip", map[string]string{"f.txt": big + "v2\n"})
	thin := r.pack(tip+"\n^"+base+"\n", "--thin")
	if _, err := readAll(t, pushBody(base, tip, "refs/heads/main", thin)); !errors.Is(err, ErrThinPack) {
		t.Fatalf("thin pack: err = %v, want ErrThinPack", err)
	}
	// The same push without --thin is self-contained and reads.
	full := r.pack(tip + "\n^" + base + "\n")
	got, err := readAll(t, pushBody(base, tip, "refs/heads/main", full))
	if err != nil || len(got) != 1 || got[0].OID != tip {
		t.Fatalf("full pack: %v, %v", got, err)
	}
}

func TestReadPushRefusesDamage(t *testing.T) {
	r := newRepo(t)
	tip := r.commit("one", map[string]string{"f.txt": "hello\n"})
	pack := r.pack("HEAD\n")
	good := pushBody(zero, tip, "refs/heads/main", pack)
	if _, err := readAll(t, good); err != nil {
		t.Fatal(err)
	}
	flip := bytes.Clone(good)
	flip[len(flip)-30] ^= 0xff
	cases := map[string][]byte{
		"truncated":       good[:len(good)-5],
		"trailing data":   append(bytes.Clone(good), 'x'),
		"corrupted":       flip,
		"no pack":         pushBody(zero, tip, "refs/heads/main", nil),
		"not a pack":      pushBody(zero, tip, "refs/heads/main", []byte("PACX\x00\x00\x00\x02\x00\x00\x00\x00")),
		"pack on deletes": pushBody(tip, zero, "refs/heads/main", pack),
	}
	for name, body := range cases {
		if _, err := readAll(t, body); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestReadPushProbeAndDeletes(t *testing.T) {
	if p, err := ReadPush(strings.NewReader("0000"), nil, nil); err != nil || len(p.Updates) != 0 {
		t.Fatalf("probe: %v %v", p, err)
	}
	del := pkt(a1+" "+zero+" refs/heads/x\x00report-status\n") + "0000"
	p, err := ReadPush(strings.NewReader(del), func(Commit) error { t.Fatal("commit in a delete"); return nil }, nil)
	if err != nil || len(p.Updates) != 1 {
		t.Fatalf("delete: %v %v", p, err)
	}
}

func TestReadPushOptions(t *testing.T) {
	r := newRepo(t)
	tip := r.commit("one", nil)
	var b bytes.Buffer
	b.WriteString(pkt(zero + " " + tip + " refs/heads/main\x00report-status push-options\n"))
	b.WriteString("0000")
	b.WriteString(pkt("ci.skip\n"))
	b.WriteString("0000")
	b.Write(r.pack("HEAD\n"))
	got, err := readAll(t, b.Bytes())
	if err != nil || len(got) != 1 {
		t.Fatalf("%v %v", got, err)
	}
}

func TestApplyDeltaRejectsBadOps(t *testing.T) {
	base := []byte("0123456789")
	for name, d := range map[string][]byte{
		"wrong base size": {5, 1, 1, 'x'},
		"copy past base":  {10, 5, 0x91, 8, 5},
		"zero op":         {10, 1, 0},
		"short insert":    {10, 3, 3, 'a'},
		"size mismatch":   {10, 4, 1, 'a'},
	} {
		if _, err := applyDelta(base, d); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	got, err := applyDelta(base, []byte{10, 5, 0x91, 2, 3, 2, 'x', 'y'})
	if err != nil || string(got) != "234xy" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestReject(t *testing.T) {
	push := Push{Updates: []RefUpdate{{a1, b2, "refs/heads/main"}}, Caps: []string{"report-status", "side-band-64k"}}
	var b bytes.Buffer
	if !Reject(&b, push, "commit rules", []string{"commit abc: subject is 90 characters; the limit is 72"}) {
		t.Fatal("Reject declined")
	}
	out := b.String()
	for _, want := range []string{"\x02commit abc: subject is 90", "\x01", "ng refs/heads/main commit rules\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("response lacks %q:\n%q", want, out)
		}
	}
	if Reject(&b, Push{Updates: push.Updates}, "x", nil) {
		t.Error("Reject wrote a status the client didn't ask for")
	}
	// Plain side-band carries at most 1000-byte packets.
	b.Reset()
	Reject(&b, Push{Updates: push.Updates, Caps: []string{"report-status", "side-band"}}, "x", []string{strings.Repeat("é", 800)})
	for rd := bytes.NewReader(b.Bytes()); rd.Len() > 0; {
		line, flush, err := readPkt(rd)
		if err != nil {
			t.Fatal(err)
		}
		if !flush && len(line)+4 > 1000 {
			t.Fatalf("side-band packet of %d bytes", len(line)+4)
		}
	}
}

func TestNoThin(t *testing.T) {
	adv := pkt("# service=git-receive-pack\n") + "0000" +
		pkt(a1+" refs/heads/main\x00report-status side-band-64k ofs-delta agent=github/x\n") +
		pkt(b2+" refs/heads/other\n") + "0000"
	out, err := NoThin(strings.NewReader(adv))
	if err != nil {
		t.Fatal(err)
	}
	want := pkt("# service=git-receive-pack\n") + "0000" +
		pkt(a1+" refs/heads/main\x00report-status side-band-64k ofs-delta agent=github/x no-thin\n") +
		pkt(b2+" refs/heads/other\n") + "0000"
	if string(out) != want {
		t.Errorf("got\n%q\nwant\n%q", out, want)
	}
	// Once is enough.
	again, _ := NoThin(bytes.NewReader(out))
	if !bytes.Equal(again, out) {
		t.Error("no-thin added twice")
	}
	if _, err := NoThin(strings.NewReader("zzzz")); err == nil {
		t.Error("garbage accepted")
	}
}

// TestPushThroughGate pushes with real git over HTTP to git http-backend,
// through a gate that adds no-thin to the advertisement and checks every
// pushed commit, as the broker does. It shows git honours no-thin (a push
// on top of existing history arrives self-contained), that a clean push
// lands, and that a refused one shows the reason and moves no ref.
func TestPushThroughGate(t *testing.T) {
	needGit(t)
	execPath := strings.TrimSpace(gitIn(t, t.TempDir(), nil, "--exec-path"))
	root := t.TempDir()
	gitIn(t, root, nil, "init", "-q", "--bare", "r.git")
	gitIn(t, filepath.Join(root, "r.git"), nil, "config", "http.receivepack", "true")
	backend := &cgi.Handler{
		Path: filepath.Join(execPath, "git-http-backend"),
		Env:  []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1", "GIT_CONFIG_NOSYSTEM=1"},
	}
	var thin bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Query().Get("service") == "git-receive-pack":
			rec := httptest.NewRecorder()
			backend.ServeHTTP(rec, r)
			out, err := NoThin(rec.Body)
			if err != nil {
				t.Errorf("NoThin: %v", err)
			}
			for k, v := range rec.Header() {
				w.Header()[k] = v
			}
			w.Write(out)
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/git-receive-pack"):
			body, _ := io.ReadAll(r.Body)
			var why []string
			push, err := ReadPush(bytes.NewReader(body), func(c Commit) error {
				if strings.Contains(c.Message, "Co-Authored-By: Claude") {
					why = append(why, "commit "+c.OID[:12]+": has a Co-Authored-By: Claude trailer")
				}
				return nil
			}, nil)
			if errors.Is(err, ErrThinPack) {
				thin = true
			}
			if err != nil {
				why = append(why, err.Error())
			}
			if len(why) > 0 {
				w.Header().Set("Content-Type", ResultType)
				Reject(w, push, "commit rules", why)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			r.ContentLength = int64(len(body))
			backend.ServeHTTP(w, r)
		default:
			backend.ServeHTTP(w, r)
		}
	}))
	defer srv.Close()
	url := srv.URL + "/r.git"

	w := newRepo(t)
	big := strings.Repeat("content that stays the same between versions\n", 400)
	w.commit("first", map[string]string{"f.txt": big + "v1\n"})
	gitIn(t, w.dir, nil, "push", "-q", url, "HEAD:main")

	w.commit("second", map[string]string{"f.txt": big + "v2\n"})
	gitIn(t, w.dir, nil, "push", "-q", url, "HEAD:main")
	if thin {
		t.Fatal("git sent a thin pack despite no-thin")
	}

	bad := w.commit("third\n\nCo-Authored-By: Claude <noreply@anthropic.com>", map[string]string{"f.txt": big + "v3\n"})
	cmd := exec.Command("git", "push", url, "HEAD:main")
	cmd.Dir = w.dir
	cmd.Env = gitEnv()
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("refused push exited 0:\n%s", out)
	}
	for _, want := range []string{"remote: commit " + bad[:12] + ": has a Co-Authored-By: Claude trailer", "[remote rejected] HEAD -> main (commit rules)"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("git output lacks %q:\n%s", want, out)
		}
	}
	head := strings.TrimSpace(gitIn(t, filepath.Join(root, "r.git"), nil, "rev-parse", "main"))
	if head == bad {
		t.Error("refused commit landed")
	}
}

func FuzzReadPush(f *testing.F) {
	f.Add([]byte("0000"))
	f.Add(pushBody(zero, a1, "refs/heads/x", []byte("PACK\x00\x00\x00\x02\x00\x00\x00\x01\x95\x0ax\x9c")))
	if _, err := exec.LookPath("git"); err == nil {
		r := &repo{f, f.TempDir()}
		gitIn(f, r.dir, nil, "init", "-q")
		r.commit("one\n\nbody", map[string]string{"f": "x"})
		tip := r.commit("two", map[string]string{"f": "y"})
		f.Add(pushBody(zero, tip, "refs/heads/x", r.pack("HEAD\n")))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		ReadPush(bytes.NewReader(body), func(Commit) error { return nil }, func(int64) {})
		applyDelta([]byte("0123456789"), body)
	})
}

// buildPack writes a pack of the given objects: full ones, and OFS_DELTAs
// against an earlier object.
type packObj struct {
	typ  byte
	data []byte
	base int // for a delta: the index of its base
}

func buildPack(objs []packObj) []byte {
	var b bytes.Buffer
	b.WriteString("PACK\x00\x00\x00\x02")
	binary.Write(&b, binary.BigEndian, uint32(len(objs)))
	var starts []int
	for _, o := range objs {
		starts = append(starts, b.Len())
		size := len(o.data)
		c := o.typ<<4 | byte(size&15)
		size >>= 4
		for size > 0 {
			b.WriteByte(c | 0x80)
			c, size = byte(size&0x7f), size>>7
		}
		b.WriteByte(c)
		if o.typ == objOfsDelta {
			off := starts[len(starts)-1] - starts[o.base]
			enc := []byte{byte(off & 0x7f)}
			for off >>= 7; off > 0; off >>= 7 {
				off--
				enc = append([]byte{byte(0x80 | off&0x7f)}, enc...)
			}
			b.Write(enc)
		}
		z := zlib.NewWriter(&b)
		z.Write(o.data)
		z.Close()
	}
	sum := sha1.Sum(b.Bytes())
	b.Write(sum[:])
	return b.Bytes()
}

// TestReadPushDeltaCommit reads a commit stored as a delta of another, and
// a delta of that delta, built by hand so the test doesn't hang on git's
// choice of what to deltify.
func TestReadPushDeltaCommit(t *testing.T) {
	base := []byte("tree " + strings.Repeat("a", 40) + "\nauthor t <t> 1 +0000\ncommitter t <t> 1 +0000\n\nFirst\n")
	// Each delta copies the base's headers and inserts a new message.
	keep := len(base) - len("First\n")
	delta := func(from []byte, msg string) []byte {
		return append([]byte{byte(len(from)), byte(keep + len(msg)), 0x90, byte(keep), byte(len(msg))}, msg...)
	}
	second := append(append([]byte{}, base[:keep]...), "Second\n"...)
	d1, d2 := delta(base, "Second\n"), delta(second, "Third\n\nCo-Authored-By: Claude\n")
	pack := buildPack([]packObj{{typ: objCommit, data: base}, {typ: objOfsDelta, data: d1, base: 0}, {typ: objOfsDelta, data: d2, base: 1}})
	got, err := readAll(t, pushBody(zero, a1, "refs/heads/x", pack))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Message != "First\n" || got[1].Message != "Second\n" || got[2].Message != "Third\n\nCo-Authored-By: Claude\n" {
		t.Fatalf("got %+v", got)
	}
	third := append(append([]byte{}, base[:keep]...), "Third\n\nCo-Authored-By: Claude\n"...)
	if want := objectID(sha1.New, "commit", third); got[2].OID != want {
		t.Errorf("oid %s, want %s", got[2].OID, want)
	}
}

// TestReadPushSafe: what ReadPush says is safe to send never reaches the
// pack's checksum, however the body is padded.
func TestReadPushSafe(t *testing.T) {
	// Incompressible, so the body is several buffers long.
	noise := make([]byte, 300<<10)
	rng := rand.New(rand.NewPCG(1, 2))
	for i := range noise {
		noise[i] = byte(rng.Uint32())
	}
	pack := buildPack([]packObj{{typ: objBlob, data: noise}, {typ: objCommit, data: []byte("tree x\n\nmsg\n")}})
	body := pushBody(zero, a1, "refs/heads/x", pack)
	checksumAt := int64(len(body) - 20)
	for name, b := range map[string][]byte{
		"exact":  body,
		"padded": append(bytes.Clone(body), bytes.Repeat([]byte{0}, 300<<10)...),
	} {
		var most int64
		_, err := ReadPush(bytes.NewReader(b), func(Commit) error { return nil }, func(n int64) { most = max(most, n) })
		if name == "padded" && err == nil {
			t.Errorf("%s: padding accepted", name)
		}
		if most >= checksumAt || most == 0 {
			t.Errorf("%s: reported %d safe; the checksum starts at %d", name, most, checksumAt)
		}
	}
	// A body with no pack is never safe: a server could act on deletes as
	// soon as it had the command list.
	var del strings.Builder
	for del.Len() < 70<<10 {
		del.WriteString(pkt(a1 + " " + zero + " refs/heads/x" + strconv.Itoa(del.Len()) + "\n"))
	}
	del.WriteString("0000junk")
	if _, err := ReadPush(strings.NewReader(del.String()), nil, func(n int64) { t.Fatalf("deletes reported %d safe", n) }); err == nil {
		t.Error("junk after deletes accepted")
	}
}
