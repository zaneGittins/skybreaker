package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/zaneGittins/skybreaker/internal/proxy/audit"
	"github.com/zaneGittins/skybreaker/internal/proxy/content"
	"github.com/zaneGittins/skybreaker/internal/proxy/gitproto"
	"github.com/zaneGittins/skybreaker/internal/proxy/graphql"
	"github.com/zaneGittins/skybreaker/internal/webutil"
)

// Content rules (package content) in the request path: commits in a push
// are read from its pack as it streams upstream, and pull request titles
// and bodies from the REST or GraphQL request that sets them. What breaks a
// rule, or can't be read, is refused in a form the agent's tool shows: a
// git push gets git's own "remote rejected", an API call GitHub's error
// JSON. Each problem is one line, all of them at once, so one fix covers
// them.

const (
	maxProblems = 30
	// maxDrain is how much of a refused push the broker still reads, so git
	// gets to read the refusal instead of a broken pipe.
	maxDrain = 512 << 20
)

const commitFix = "Fix the message (git commit --amend for the last commit, git rebase -i for older ones), then push again."

// commitProblems reads a whole push and returns what breaks the commit
// rules, each line naming its commit. err means the push couldn't be read.
// safe is as for gitproto.ReadPush.
func commitProblems(rules *content.Set, r io.Reader, safe func(int64)) (gitproto.Push, []string, error) {
	var out []string
	more := 0
	push, err := gitproto.ReadPush(r, func(c gitproto.Commit) error {
		for _, p := range rules.CheckCommit(c.Message) {
			if len(out) == maxProblems {
				more++
				continue
			}
			out = append(out, "commit "+short(c.OID)+": "+p)
		}
		return nil
	}, safe)
	if more > 0 {
		out = append(out, fmt.Sprintf("and %d more", more))
	}
	return push, out, err
}

var (
	errGateClosed = errors.New("push ended before it was checked")
	errRefused    = errors.New("push refused by the commit rules")
)

// pushGate streams a push upstream while a parser reads the same bytes. It
// sends only what the parser has read short of the pack's checksum, and
// the rest (the checksum and anything after it) only once the parser has
// checked every commit and found the body ends there. A push that breaks a
// rule, or that the parser can't read, never arrives whole: Read fails
// instead, which aborts the upstream request, and a git server takes none
// of a pack it didn't get whole.
type pushGate struct {
	src  io.ReadCloser
	pw   *io.PipeWriter
	done chan struct{}
	once sync.Once
	cut  atomic.Bool  // the parser was stopped before the body ended
	safe atomic.Int64 // how much of the body may go upstream before the verdict
	mu   sync.Mutex   // Read, and drain once the push is refused

	// Set by the parser before done closes.
	push     gitproto.Push
	problems []string
	err      error

	buf   []byte
	held  []byte // read from the client, not yet sent
	sent  int64
	clean bool // the parser passed the whole body
	fail  error
}

func newPushGate(src io.ReadCloser, rules *content.Set) *pushGate {
	pr, pw := io.Pipe()
	g := &pushGate{src: src, pw: pw, done: make(chan struct{}), buf: make([]byte, 32<<10)}
	go func() {
		g.push, g.problems, g.err = commitProblems(rules, pr, g.safe.Store)
		pr.CloseWithError(errRefused) // a write after the parser stopped fails
		close(g.done)
	}()
	return g
}

func (g *pushGate) Read(p []byte) (int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for {
		n := int64(len(g.held))
		if !g.clean {
			n = min(n, g.safe.Load()-g.sent)
		}
		if n > 0 {
			c := copy(p, g.held[:n])
			g.held = g.held[c:]
			g.sent += int64(c)
			return c, nil
		}
		switch {
		case g.fail != nil:
			return 0, g.fail
		case g.clean:
			return 0, io.EOF
		}
		r, err := g.src.Read(g.buf)
		if r > 0 {
			if _, werr := g.pw.Write(g.buf[:r]); werr != nil {
				// The parser stopped early: the push is unreadable.
				g.fail = errRefused
				continue
			}
			g.held = append(g.held, g.buf[:r]...)
		}
		switch {
		case err == io.EOF:
			g.pw.Close()
			<-g.done
			// Only a parse that ended without error, at the body's end,
			// lets the rest through.
			if g.err == nil && len(g.problems) == 0 {
				g.clean = true
			} else {
				g.fail = errRefused
			}
		case err != nil:
			g.stop()
			g.fail = err
		}
	}
}

// stop ends the parser if it's still reading, which is no fault of the
// push.
func (g *pushGate) stop() {
	g.once.Do(func() {
		select {
		case <-g.done:
		default:
			g.cut.Store(true)
			g.pw.CloseWithError(errGateClosed)
		}
	})
}

// refused reports whether the push itself was refused: it broke a rule or
// couldn't be read, rather than being cut off.
func (g *pushGate) refused() bool {
	return len(g.problems) > 0 || g.err != nil && !g.cut.Load()
}

// Close stops the parser; the transport calls it when it's done with the
// body, possibly from another goroutine. The client's body stays open: a
// refused push is still read to its end, so git can read the refusal.
func (g *pushGate) Close() error {
	g.stop()
	return nil
}

// drain reads the rest of the client's push, after which Read fails.
func (g *pushGate) drain() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.fail = errRefused
	drain(g.src)
}

// verdict waits for the parser and reports whether it refused the push.
func (g *pushGate) verdict() (gitproto.Push, []string, error, bool) {
	g.stop()
	<-g.done
	return g.push, g.problems, g.err, g.refused()
}

// refuseCommits answers a push that broke the commit rules or couldn't be
// read for them.
func (s *Server) refuseCommits(w http.ResponseWriter, e audit.Entry, push gitproto.Push, problems []string, err error) {
	why := problems
	reason := "commit rules"
	if err != nil {
		why = []string{"the push can't be checked against the commit rules: " + err.Error()}
		reason = "push can't be checked"
	} else {
		why = append(why, commitFix)
	}
	s.refusePush(w, e, push, reason, why)
}

// refusePush refuses a push as a git server does, so git prints why; a
// client that asked for no status report gets an HTTP error instead.
func (s *Server) refusePush(w http.ResponseWriter, e audit.Entry, push gitproto.Push, reason string, why []string) {
	lines := make([]string, len(why))
	for i, l := range why {
		lines[i] = "skybreaker: " + l
	}
	var b bytes.Buffer
	if len(push.Updates) == 0 || !gitproto.Reject(&b, push, reason, lines) {
		s.deny(w, e, http.StatusForbidden, reason+": "+strings.Join(why, "; "))
		return
	}
	e.Decision, e.Reason, e.Status = "deny", reason+": "+strings.Join(why, "; "), http.StatusOK
	s.log(e)
	w.Header().Set("Content-Type", gitproto.ResultType)
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	w.Write(b.Bytes())
}

// drain reads what's left of a refused request body, so the client reads
// the answer rather than a reset connection.
func drain(r io.Reader) { io.Copy(io.Discard, io.LimitReader(r, maxDrain)) }

// canonPath is a path as rules match it, so a variant a rule still matches
// can't slip past a check: lowercased, without a trailing slash.
func canonPath(path string) string {
	if len(path) > 1 {
		path = strings.TrimSuffix(path, "/")
	}
	return strings.ToLower(path)
}

// GitHub's pull request requests: REST on /repos/{owner}/{repo}/pulls (a
// POST to an existing one is checked too, in case it's taken as an edit),
// and GraphQL's createPullRequest and updatePullRequest.
var (
	pullsCreate = regexp.MustCompile(`^/repos/[^/]+/[^/]+/pulls$`)
	pullsEdit   = regexp.MustCompile(`^/repos/[^/]+/[^/]+/pulls/[0-9]+$`)
	prMutations = []string{"createPullRequest", "updatePullRequest"}
)

func isPullsREST(method, path string) bool {
	switch method {
	case http.MethodPost:
		return pullsCreate.MatchString(path) || pullsEdit.MatchString(path)
	case http.MethodPatch:
		return pullsEdit.MatchString(path)
	}
	return false
}

// restPullProblems checks a REST pull request body: its title and body
// fields, refusing a body that isn't one JSON object with string (or null)
// values for them.
func restPullProblems(rules *content.Set, body []byte) ([]string, error) {
	var fields map[string]json.RawMessage
	if err := strictObject(body, &fields); err != nil {
		return nil, err
	}
	get := func(k string) (*string, error) {
		raw, ok := fields[k]
		if !ok || string(raw) == "null" {
			return nil, nil
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("%s is not a string", k)
		}
		return &s, nil
	}
	title, err := get("title")
	if err != nil {
		return nil, err
	}
	text, err := get("body")
	if err != nil {
		return nil, err
	}
	return rules.CheckPullRequest(title, text), nil
}

// strictObject decodes a JSON object's top level, refusing duplicate keys:
// GitHub and the broker could each read a different "body".
func strictObject(b []byte, out *map[string]json.RawMessage) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return errors.New("body must be one JSON object")
	}
	m := map[string]json.RawMessage{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		k, _ := tok.(string)
		if _, dup := m[k]; dup {
			return fmt.Errorf("duplicate %q", k)
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return err
		}
		m[k] = raw
	}
	if _, err := dec.Token(); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("text after the JSON object")
	}
	*out = m
	return nil
}

// graphQLPullProblems checks the pull request mutations in an operation.
func graphQLPullProblems(rules *content.Set, q graphql.Request) ([]string, error) {
	if q.Op != "mutation" {
		return nil, nil
	}
	if q.Indirect {
		return nil, errors.New("a mutation that uses fragments at its top level can't be checked")
	}
	var out []string
	for _, c := range q.Calls {
		if !slices.Contains(prMutations, c.Field) {
			continue
		}
		f, err := q.Strings(c, "input", "title", "body")
		if err != nil {
			return nil, err
		}
		out = append(out, rules.CheckPullRequest(f["title"], f["body"])...)
	}
	return out, nil
}

// refusePull answers a pull request that broke the rules or couldn't be
// read for them, in GitHub's error shape: gh, the GitHub MCP server and
// curl all show "message", and GraphQL clients read "errors".
func (s *Server) refusePull(w http.ResponseWriter, e audit.Entry, problems []string, err error) {
	var why []string
	for _, p := range problems {
		why = append(why, "pull request "+p)
	}
	if err != nil {
		why = []string{"the request can't be checked against the pull request rules: " + err.Error()}
	} else {
		why = append(why, "Fix the title or body, then try again.")
	}
	msg := "skybreaker: pull request refused by the content rules: " + strings.Join(why, "; ")
	errs := make([]map[string]string, len(why))
	for i, l := range why {
		errs[i] = map[string]string{"message": "skybreaker: " + l}
	}
	e.Decision, e.Reason, e.Status = "deny", msg, http.StatusForbidden
	s.log(e)
	webutil.WriteJSON(w, http.StatusForbidden, map[string]any{"message": msg, "errors": errs})
}

// isReceivePackAdvert is the ref advertisement git reads before a push;
// path is canonPath's.
func isReceivePackAdvert(r *http.Request, path string) bool {
	return r.Method == http.MethodGet && strings.HasSuffix(path, "/info/refs") && r.URL.Query().Get("service") == "git-receive-pack"
}

// relayAdvert relays a receive-pack ref advertisement with no-thin added,
// so git sends a pack the commit check can read whole. If it can't be
// rewritten it goes as it is: a thin pack is refused with a message that
// says what to do.
func (s *Server) relayAdvert(w http.ResponseWriter, resp *http.Response, e audit.Entry) {
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Encoding") != "" {
		s.relay(w, resp, e)
		return
	}
	defer resp.Body.Close()
	b, err := gitproto.NoThin(resp.Body)
	if err != nil {
		s.deny(w, e, http.StatusBadGateway, "upstream ref advertisement: "+err.Error())
		return
	}
	stripHop(resp.Header)
	resp.Header.Del("Content-Length")
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)
	e.Decision, e.Status = "allow", resp.StatusCode
	s.log(e)
	w.Write(b)
}
