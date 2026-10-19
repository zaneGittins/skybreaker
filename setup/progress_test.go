package setup

import (
	"strings"
	"testing"
)

func TestProgressPlain(t *testing.T) {
	var b strings.Builder
	p := &Progress{W: &b}
	p.Steps(2)
	p.Step("Downloading")
	p.Done("Downloaded")
	p.Step("Checking")
	p.Fail("Check failed")
	p.Warn("loose line")
	out := b.String()
	for _, want := range []string{"[~] [1/2] Downloading\n", "[+] [1/2] Downloaded\n", "[x] [2/2] Check failed\n", "[!] loose line\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\033[") {
		t.Errorf("control codes off a terminal:\n%q", out)
	}
}

func TestProgressSpinnerCollapses(t *testing.T) {
	var b syncBuilder
	p := &Progress{W: &b, TTY: true}
	p.Steps(1)
	p.Step("Pulling")
	waitFor(t, func() bool { return strings.Contains(b.String(), "Pulling") })
	p.Done("Pulled")
	out := b.String()
	if !strings.HasSuffix(out, "\r\033[K[+] [1/1] Pulled\n") {
		t.Fatalf("spinner not replaced by its result:\n%q", out)
	}
}
