package sandbox_test

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// sshd-env.sh turns the container's environment into sshd SetEnv lines. A
// value that can't be quoted safely is dropped, never split into a second
// directive.
func TestSSHDEnv(t *testing.T) {
	for _, tool := range []string{"sh", "awk", "tr"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
	cmd := exec.Command("sh", "sshd-env.sh")
	cmd.Env = []string{
		"HTTPS_PROXY=http://orbit:secret-tok@broker:8080",
		"PATH=/usr/local/go/bin:/usr/bin",
		"HOME=/root",
		"SPACED=a b",
		"SMUGGLED=x\nAuthorizedKeysFile /tmp/mine",
		"QUOTED=a\"b",
		"SLASHED=a\\b",
		"BELL=a\ab",
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	// One line: sshd keeps only the first SetEnv it reads.
	line, ok := strings.CutPrefix(string(out), "SetEnv ")
	if !ok || strings.Count(string(out), "\n") != 1 {
		t.Fatalf("want one SetEnv line, got\n%s", out)
	}
	got := strings.Fields(strings.ReplaceAll(strings.TrimSpace(line), `"a b"`, `"a_b"`))
	slices.Sort(got)
	want := []string{
		`HTTPS_PROXY="http://orbit:secret-tok@broker:8080"`,
		`PATH="/usr/local/go/bin:/usr/bin"`,
		`SPACED="a_b"`,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	if strings.Contains(string(out), "AuthorizedKeysFile") {
		t.Fatal("a value smuggled in a directive")
	}
}
