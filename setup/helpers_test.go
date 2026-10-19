package setup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zaneGittins/skybreaker/internal/control/controld"
)

type syncBuilder struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuilder) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuilder) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatal("timed out")
}

// fakeRunner stands in for the host's commands. Each call is recorded as
// one line. A skybreaker binary is a text file "<version> [crash] [rejects]":
// its version subcommand prints the version, check runs the real
// controld.Check unless it "rejects", and while the installed one says
// "crash" systemd reports skybreaker restarting.
type fakeRunner struct {
	mu       sync.Mutex
	l        Layout
	calls    []string
	fail     map[string]error  // line prefix → error
	out      map[string]string // line prefix → output
	restarts int
}

func newFakeRunner(l Layout) *fakeRunner {
	return &fakeRunner{l: l, fail: map[string]error{}, out: map[string]string{}}
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	line := strings.TrimSpace(name + " " + strings.Join(args, " "))
	f.calls = append(f.calls, line)
	if filepath.Dir(name) == f.l.Bin && strings.HasPrefix(filepath.Base(name), "skybreaker") && len(args) > 0 {
		b, err := os.ReadFile(name)
		if err != nil {
			return nil, err
		}
		switch args[0] {
		case "version":
			return []byte("skybreaker " + strings.Fields(string(b))[0] + "\n"), nil
		case "check":
			if strings.Contains(string(b), "rejects") {
				return []byte("unknown field orbit_board"), errors.New("exit status 1")
			}
			return nil, controld.Check(args[2])
		}
	}
	installed, _ := os.ReadFile(f.l.Binary())
	crashing := strings.Contains(string(installed), "crash")
	switch line {
	case "systemctl is-active skybreaker":
		if crashing {
			return []byte("activating\n"), errors.New("exit status 3")
		}
		return []byte("active\n"), nil
	case "systemctl show -p NRestarts --value skybreaker":
		if crashing {
			f.restarts++
		}
		return []byte(strconv.Itoa(f.restarts) + "\n"), nil
	}
	for p, err := range f.fail {
		if strings.HasPrefix(line, p) {
			return nil, fmt.Errorf("%s: %w", line, err)
		}
	}
	for p, out := range f.out {
		if strings.HasPrefix(line, p) {
			return []byte(out), nil
		}
	}
	return nil, nil
}

// index is the position of the first call starting with prefix, or -1.
func (f *fakeRunner) index(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return i
		}
	}
	return -1
}

func (f *fakeRunner) ran(prefix string) bool { return f.index(prefix) >= 0 }

func (f *fakeRunner) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}
