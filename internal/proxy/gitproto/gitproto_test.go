package gitproto

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

const (
	zero = "0000000000000000000000000000000000000000"
	a1   = "1111111111111111111111111111111111111111"
	b2   = "2222222222222222222222222222222222222222"
	s256 = "3333333333333333333333333333333333333333333333333333333333333333"
	z256 = "0000000000000000000000000000000000000000000000000000000000000000"
)

func pkt(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }

// push builds a receive-pack body: commands, flush, then pack bytes.
func push(lines ...string) string {
	var b strings.Builder
	for i, l := range lines {
		if i == 0 {
			l += "\x00report-status side-band-64k agent=git/2.47"
		}
		b.WriteString(pkt(l + "\n"))
	}
	b.WriteString("0000")
	b.WriteString("PACK\x00\x00\x00\x02rest-of-pack")
	return b.String()
}

func TestReadCommands(t *testing.T) {
	body := push(
		a1+" "+b2+" refs/heads/agent/x",
		zero+" "+b2+" refs/heads/agent/new",
		a1+" "+zero+" refs/heads/agent/gone",
	)
	got, err := ReadCommands(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	want := []RefUpdate{
		{a1, b2, "refs/heads/agent/x"},
		{zero, b2, "refs/heads/agent/new"},
		{a1, zero, "refs/heads/agent/gone"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d commands, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("command %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if got[1].IsDelete() {
		t.Error("a create was read as a delete")
	}
	if !got[2].IsDelete() {
		t.Error("delete not recognised")
	}
}

func TestReadCommandsSHA256AndNoNewline(t *testing.T) {
	body := pkt(s256+" "+z256+" refs/heads/agent/x\x00report-status") + "0000"
	got, err := ReadCommands(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Ref != "refs/heads/agent/x" || !got[0].IsDelete() {
		t.Fatalf("got %+v", got)
	}
}

func TestReadCommandsRejects(t *testing.T) {
	long := strings.Repeat("a", 65521)
	cases := map[string]string{
		"empty body":          "",
		"truncated length":    "00",
		"bad hex length":      "zzzz",
		"length too short":    "0003",
		"delim packet":        "0001",
		"response end":        "0002",
		"truncated payload":   "0040" + a1,
		"no flush":            pkt(a1 + " " + b2 + " refs/heads/agent/x\n"),
		"oversized line":      fmt.Sprintf("%04x", 65521) + long,
		"length over max":     "ffff" + strings.Repeat("a", 0xffff-4),
		"missing fields":      push(a1 + " refs/heads/agent/x"),
		"short oid":           push("abc " + b2 + " refs/heads/agent/x"),
		"uppercase oid":       push(strings.ToUpper("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa") + " " + b2 + " refs/heads/agent/x"),
		"mixed oid lengths":   push(a1 + " " + s256 + " refs/heads/agent/x"),
		"push cert":           pkt("push-cert\x00report-status\n") + pkt("certificate version 0.1\n") + "0000",
		"shallow":             pkt("shallow "+a1+"\n") + push(a1+" "+b2+" refs/heads/agent/x"),
		"extra field":         push(a1 + " " + b2 + " refs/heads/agent/x extra"),
		"dotdot ref":          push(a1 + " " + b2 + " refs/heads/agent/../main"),
		"double slash ref":    push(a1 + " " + b2 + " refs/heads/agent//x"),
		"reflog syntax":       push(a1 + " " + b2 + " refs/heads/agent/x@{1}"),
		"control char ref":    push(a1 + " " + b2 + " refs/heads/agent/\x01x"),
		"not under refs":      push(a1 + " " + b2 + " HEAD"),
		"bare main":           push(a1 + " " + b2 + " main"),
		"trailing slash ref":  push(a1 + " " + b2 + " refs/heads/agent/"),
		"lock suffix":         push(a1 + " " + b2 + " refs/heads/agent/x.lock"),
		"dot component":       push(a1 + " " + b2 + " refs/heads/agent/.hidden"),
		"glob char":           push(a1 + " " + b2 + " refs/heads/agent/*"),
		"backslash":           push(a1 + " " + b2 + " refs/heads/agent\\x"),
		"no commands":         "0000",
		"caps on second line": pkt(a1+" "+b2+" refs/heads/agent/a\n") + pkt(a1+" "+b2+" refs/heads/agent/b\x00caps\n") + "0000",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if got, err := ReadCommands(strings.NewReader(body)); err == nil {
				t.Errorf("accepted: %+v", got)
			}
		})
	}
}

func TestReadCommandsLimit(t *testing.T) {
	var b strings.Builder
	for i := 0; i <= maxCommands; i++ {
		b.WriteString(pkt(fmt.Sprintf("%s %s refs/heads/agent/%d\n", a1, b2, i)))
	}
	b.WriteString("0000")
	if _, err := ReadCommands(strings.NewReader(b.String())); err == nil {
		t.Error("accepted more than maxCommands commands")
	}
}

type closeTracker struct {
	io.Reader
	closed bool
}

func (c *closeTracker) Close() error { c.closed = true; return nil }

func TestPeekReplaysBody(t *testing.T) {
	body := push(a1+" "+b2+" refs/heads/agent/x", zero+" "+b2+" refs/heads/agent/y")
	src := &closeTracker{Reader: strings.NewReader(body)}
	ups, rc, err := Peek(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(ups) != 2 {
		t.Fatalf("got %d updates", len(ups))
	}
	all, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(all, []byte(body)) {
		t.Errorf("replayed body differs:\n got %q\nwant %q", all, body)
	}
	rc.Close()
	if !src.closed {
		t.Error("Close did not reach the original body")
	}
}

func TestPeekError(t *testing.T) {
	if _, _, err := Peek(io.NopCloser(strings.NewReader("zzzz"))); err == nil {
		t.Error("Peek accepted garbage")
	}
}

func TestCheck(t *testing.T) {
	allow := []string{"refs/heads/agent/*"}
	ok := [][]RefUpdate{
		{{a1, b2, "refs/heads/agent/x"}},
		{{zero, b2, "refs/heads/agent/feature/deep"}},
		{{a1, b2, "refs/heads/agent/a"}, {zero, b2, "refs/heads/agent/b"}},
	}
	for _, u := range ok {
		if err := Check(u, allow); err != nil {
			t.Errorf("Check(%+v) = %v, want nil", u, err)
		}
	}
	bad := map[string][]RefUpdate{
		"main":               {{a1, b2, "refs/heads/main"}},
		"one bad in many":    {{a1, b2, "refs/heads/agent/a"}, {a1, b2, "refs/heads/main"}},
		"delete":             {{a1, zero, "refs/heads/agent/x"}},
		"tag":                {{zero, b2, "refs/tags/v1"}},
		"prefix lookalike":   {{a1, b2, "refs/heads/agents/x"}},
		"bare prefix":        {{a1, b2, "refs/heads/agent"}},
		"empty":              {},
		"dotdot":             {{a1, b2, "refs/heads/agent/../main"}},
		"sha256 delete":      {{s256, z256, "refs/heads/agent/x"}},
		"other namespace":    {{a1, b2, "refs/notes/agent/x"}},
		"case trick":         {{a1, b2, "refs/heads/Agent/x"}},
		"trailing component": {{a1, b2, "refs/heads/agent/"}},
	}
	for name, u := range bad {
		err := Check(u, allow)
		if err == nil {
			t.Errorf("%s: Check accepted %+v", name, u)
			continue
		}
		if !errors.Is(err, ErrRefPolicy) {
			t.Errorf("%s: error %v does not wrap ErrRefPolicy", name, err)
		}
	}
}

func TestCheckAnyOfSeveralPatterns(t *testing.T) {
	allow := []string{"refs/heads/feature/*", "refs/heads/fix/*"}
	if err := Check([]RefUpdate{{a1, b2, "refs/heads/feature/x"}, {zero, b2, "refs/heads/fix/y"}}, allow); err != nil {
		t.Errorf("feature and fix: %v", err)
	}
	bad := map[string][]RefUpdate{
		"main":             {{a1, b2, "refs/heads/main"}},
		"one bad in many":  {{a1, b2, "refs/heads/feature/x"}, {a1, b2, "refs/heads/main"}},
		"prefix lookalike": {{a1, b2, "refs/heads/features/x"}},
		"delete":           {{a1, zero, "refs/heads/fix/y"}},
	}
	for name, u := range bad {
		if err := Check(u, allow); !errors.Is(err, ErrRefPolicy) {
			t.Errorf("%s: Check(%+v) = %v, want ErrRefPolicy", name, u, err)
		}
	}
	if err := Check([]RefUpdate{{a1, b2, "refs/heads/feature/x"}}, nil); !errors.Is(err, ErrRefPolicy) {
		t.Errorf("no patterns: %v, want ErrRefPolicy", err)
	}
}

func TestCheckNamesOffendingRef(t *testing.T) {
	err := Check([]RefUpdate{{a1, b2, "refs/heads/main"}}, []string{"refs/heads/agent/*"})
	if err == nil || !strings.Contains(err.Error(), "refs/heads/main") {
		t.Errorf("error %v should name the ref", err)
	}
}

func TestGlob(t *testing.T) {
	cases := []struct {
		pattern, s string
		want       bool
	}{
		{"refs/heads/agent/*", "refs/heads/agent/x", true},
		{"refs/heads/agent/*", "refs/heads/agent/x/y", true},
		{"refs/heads/agent/*", "refs/heads/agent/", true},
		{"refs/heads/agent/*", "refs/heads/agentx", false},
		{"refs/heads/main", "refs/heads/main", true},
		{"refs/heads/main", "refs/heads/main2", false},
		{"refs/heads/*/wip", "refs/heads/a/b/wip", true},
		{"refs/heads/*/wip", "refs/heads/a/b/wipx", false},
		{"*", "anything", true},
	}
	for _, c := range cases {
		if got := glob(c.pattern, c.s); got != c.want {
			t.Errorf("glob(%q, %q) = %v, want %v", c.pattern, c.s, got, c.want)
		}
	}
}

func FuzzReadCommands(f *testing.F) {
	f.Add([]byte(push(a1 + " " + b2 + " refs/heads/agent/x")))
	f.Add([]byte("0000"))
	f.Fuzz(func(t *testing.T, body []byte) {
		ups, err := ReadCommands(bytes.NewReader(body))
		if err != nil {
			return
		}
		for _, u := range ups {
			if validRef(u.Ref) != nil {
				t.Fatalf("returned invalid ref %q", u.Ref)
			}
		}
	})
}

func TestPeekProbe(t *testing.T) {
	// Before a push larger than http.postBuffer, git probes receive-pack
	// with a bare flush-pkt. It changes nothing and must get through.
	ups, rest, err := Peek(io.NopCloser(strings.NewReader("0000")))
	if err != nil || len(ups) != 0 {
		t.Fatalf("probe: %v %v", ups, err)
	}
	if b, _ := io.ReadAll(rest); string(b) != "0000" {
		t.Fatalf("probe body not replayed: %q", b)
	}
	// An empty command list followed by data is not a probe.
	if _, _, err := Peek(io.NopCloser(strings.NewReader("0000PACK"))); err == nil {
		t.Fatal("empty command list with a pack accepted")
	}
}
