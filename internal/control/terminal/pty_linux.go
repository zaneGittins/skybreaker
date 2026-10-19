//go:build linux

package terminal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

type winsize struct{ Row, Col, X, Y uint16 }

func ioctl(fd uintptr, req uint, arg unsafe.Pointer) error {
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(req), uintptr(arg)); e != 0 {
		return e
	}
	return nil
}

type linuxPTY struct {
	master *os.File
	cmd    *exec.Cmd
	once   sync.Once
	waited chan error
}

// StartPTY runs argv on a new pseudo-terminal of the given size. It uses
// /dev/ptmx directly, which is all a Linux control plane needs.
func StartPTY(ctx context.Context, argv []string, rows, cols uint16) (PTY, error) {
	if len(argv) == 0 {
		return nil, errors.New("pty: empty command")
	}
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("pty: %w", err)
	}
	fail := func(err error) (PTY, error) { master.Close(); return nil, fmt.Errorf("pty: %w", err) }
	unlock := int32(0)
	if err := ioctl(master.Fd(), syscall.TIOCSPTLCK, unsafe.Pointer(&unlock)); err != nil {
		return fail(err)
	}
	var n uint32
	if err := ioctl(master.Fd(), syscall.TIOCGPTN, unsafe.Pointer(&n)); err != nil {
		return fail(err)
	}
	slave, err := os.OpenFile("/dev/pts/"+strconv.FormatUint(uint64(n), 10), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return fail(err)
	}
	defer slave.Close()
	p := &linuxPTY{master: master, waited: make(chan error, 1)}
	if err := p.Resize(rows, cols); err != nil {
		return fail(err)
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	// A new session with the slave as its controlling terminal (fd 0).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		return fail(err)
	}
	p.cmd = cmd
	go func() { p.waited <- cmd.Wait() }()
	return p, nil
}

func (p *linuxPTY) Read(b []byte) (int, error) {
	n, err := p.master.Read(b)
	// Once the child side is gone Linux reports EIO; that is end of output.
	if errors.Is(err, syscall.EIO) {
		err = os.ErrClosed
	}
	return n, err
}

func (p *linuxPTY) Write(b []byte) (int, error) { return p.master.Write(b) }

func (p *linuxPTY) Resize(rows, cols uint16) error {
	ws := winsize{Row: rows, Col: cols}
	return ioctl(p.master.Fd(), syscall.TIOCSWINSZ, unsafe.Pointer(&ws))
}

// Close hangs up the terminal, which ends `docker exec` (and so detaches the
// tmux client), and makes sure the process is gone.
func (p *linuxPTY) Close() error {
	var err error
	p.once.Do(func() {
		err = p.master.Close()
		if p.cmd != nil && p.cmd.Process != nil {
			p.cmd.Process.Signal(syscall.SIGHUP)
			go func() {
				select {
				case e := <-p.waited:
					p.waited <- e
				case <-time.After(2 * time.Second):
					p.cmd.Process.Kill()
				}
			}()
		}
	})
	return err
}
