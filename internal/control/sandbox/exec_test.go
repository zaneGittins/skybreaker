package sandbox

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// A sandbox controls what its commands print (it can replace git, sh or
// tmux), so the host must stop reading at a limit, not after the command
// returns.
func TestExecRunnerBoundsOutput(t *testing.T) {
	if _, err := exec.LookPath("yes"); err != nil {
		t.Skip("no yes")
	}
	r := ExecRunner{MaxOutput: 1 << 20}
	start := time.Now()
	out, err := r.Run(context.Background(), "yes", "a line that never ends")
	if !errors.Is(err, ErrOutputLimit) {
		t.Fatalf("err %v", err)
	}
	if len(out) > 1<<20 {
		t.Fatalf("kept %d bytes", len(out))
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("the command wasn't stopped at the limit")
	}
}

func TestExecRunnerBoundsStderr(t *testing.T) {
	r := ExecRunner{MaxOutput: 1 << 20}
	// 4 MiB of stderr, then a failure: the error carries a bounded tail.
	_, err := r.Run(context.Background(), "sh", "-c", `head -c 4194304 /dev/zero | tr '\0' x >&2; exit 3`)
	if err == nil || len(err.Error()) > 70<<10 {
		t.Fatalf("stderr unbounded: %d bytes", len(err.Error()))
	}
	if errors.Is(err, ErrOutputLimit) {
		t.Fatal("stderr counted against the stdout limit")
	}
}

func TestExecRunnerKeepsOrdinaryOutput(t *testing.T) {
	out, err := ExecRunner{}.Run(context.Background(), "sh", "-c", "echo hello; echo oops >&2")
	if err != nil || string(out) != "hello\n" {
		t.Fatalf("%q %v", out, err)
	}
	_, err = ExecRunner{}.Run(context.Background(), "sh", "-c", "echo 'no such container' >&2; exit 1")
	if err == nil || !strings.Contains(err.Error(), "no such container") {
		t.Fatalf("docker's message lost: %v", err)
	}
}
