package app

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/zaneGittins/skybreaker/internal/control/sandbox"
)

// SetAuthorizedKeys gives every sandbox's sshd these keys (see sshkeys):
// written now into the etc/ of each project that has one, and by writeEtc
// into a sandbox started later. sshd reads the file on each login, so a
// removed key stops working at once.
func (a *App) SetAuthorizedKeys(keys []byte) error {
	a.sshMu.Lock()
	defer a.sshMu.Unlock()
	a.sshKeys = slices.Clone(keys)
	var errs []error
	for project := range a.cfg.Projects {
		etc := filepath.Join(a.ProjectDir(project), "etc")
		if _, err := os.Stat(etc); err != nil {
			continue // never started; writeEtc will write it
		}
		errs = append(errs, writeAtomic(filepath.Join(etc, "authorized_keys"), keys))
	}
	return errors.Join(errs...)
}

func (a *App) authorizedKeys() []byte {
	a.sshMu.Lock()
	defer a.sshMu.Unlock()
	return slices.Clone(a.sshKeys)
}

// writeAtomic replaces a file so a reader never sees half of it.
func writeAtomic(path string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(b)
	if err == nil {
		err = tmp.Chmod(0o644)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// sshdWait bounds how long SSHConn waits for a sandbox's sshd to listen.
const sshdWait = 20 * time.Second

// SSHConn connects to the project's sandbox sshd, starting the sandbox if
// it isn't running. The connection is the host's own, to the sandbox's
// fixed address: the sandbox is never given a way to reach in.
func (a *App) SSHConn(ctx context.Context, project string) (net.Conn, error) {
	unlock, guardErr := a.beginActivity(project)
	if guardErr != nil {
		return nil, guardErr
	}
	owned := false
	defer func() {
		if !owned {
			unlock()
		}
	}()
	if _, err := a.project(project); err != nil {
		return nil, err
	}
	if err := a.StartSandbox(ctx, project); err != nil {
		return nil, err
	}
	addr := netip.AddrPortFrom(sandbox.Addr(a.cfg.Subnet(project)), 22).String()
	ctx, cancel := context.WithTimeout(ctx, sshdWait)
	defer cancel()
	for {
		c, err := a.dial(ctx, addr)
		if err == nil {
			owned = true
			return &maintenanceConn{Conn: c, release: unlock}, nil
		}
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// maintenanceConn keeps cleanup out for the lifetime of an SSH connection.
type maintenanceConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *maintenanceConn) Close() error { err := c.Conn.Close(); c.once.Do(c.release); return err }
