//go:build linux

package terminal

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

// wait returns the process's exit status once it has gone.
func (p *linuxPTY) wait() error {
	err := <-p.waited
	p.waited <- err
	return err
}

func readAll(t *testing.T, p PTY) string {
	t.Helper()
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() { io.Copy(&buf, p); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("PTY output did not end")
	}
	return buf.String()
}

func TestStartPTYSize(t *testing.T) {
	p, err := StartPTY(context.Background(), []string{"sh", "-c", "stty size; test -t 0 && echo tty"}, 30, 100)
	if err != nil {
		t.Skipf("no PTY support here: %v", err)
	}
	out := readAll(t, p)
	p.(*linuxPTY).wait()
	if !strings.Contains(out, "30 100") || !strings.Contains(out, "tty") {
		t.Fatalf("output %q", out)
	}
}

func TestPTYResizeAndInput(t *testing.T) {
	p, err := StartPTY(context.Background(), []string{"sh", "-c", "read line; stty size; echo got:$line"}, 24, 80)
	if err != nil {
		t.Skipf("no PTY support here: %v", err)
	}
	if err := p.Resize(40, 120); err != nil {
		t.Fatal(err)
	}
	p.Write([]byte("hello\n"))
	out := readAll(t, p)
	if !strings.Contains(out, "40 120") || !strings.Contains(out, "got:hello") {
		t.Fatalf("output %q", out)
	}
}

func TestPTYCloseEndsProcess(t *testing.T) {
	p, err := StartPTY(context.Background(), []string{"sleep", "30"}, 24, 80)
	if err != nil {
		t.Skipf("no PTY support here: %v", err)
	}
	p.Close()
	done := make(chan struct{})
	go func() { p.(*linuxPTY).wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("process survived Close")
	}
}

func TestStartPTYErrors(t *testing.T) {
	if _, err := StartPTY(context.Background(), nil, 24, 80); err == nil {
		t.Error("empty argv accepted")
	}
	if _, err := StartPTY(context.Background(), []string{"/nonexistent/binary"}, 24, 80); err == nil {
		t.Error("missing binary accepted")
	}
}
