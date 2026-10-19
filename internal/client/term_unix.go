//go:build linux || darwin

package client

import (
	"os"
	"os/signal"
	"syscall"
	"unsafe"
)

func ioctl(fd int, req uint, arg unsafe.Pointer) error {
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(req), uintptr(arg)); e != 0 {
		return e
	}
	return nil
}

// termState is a terminal's settings, to put back on the way out.
type termState struct{ t syscall.Termios }

func isTerminal(fd int) bool {
	var t syscall.Termios
	return ioctl(fd, ioctlGetTermios, unsafe.Pointer(&t)) == nil
}

// makeRaw puts the terminal in raw mode, as cfmakeraw does: keys arrive
// as typed, nothing is echoed or turned into signals.
func makeRaw(fd int) (*termState, error) {
	var t syscall.Termios
	if err := ioctl(fd, ioctlGetTermios, unsafe.Pointer(&t)); err != nil {
		return nil, err
	}
	old := &termState{t}
	t.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP | syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON
	t.Oflag &^= syscall.OPOST
	t.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	t.Cflag &^= syscall.CSIZE | syscall.PARENB
	t.Cflag |= syscall.CS8
	t.Cc[syscall.VMIN] = 1
	t.Cc[syscall.VTIME] = 0
	if err := ioctl(fd, ioctlSetTermios, unsafe.Pointer(&t)); err != nil {
		return nil, err
	}
	return old, nil
}

func restore(fd int, s *termState) error {
	return ioctl(fd, ioctlSetTermios, unsafe.Pointer(&s.t))
}

func termSize(fd int) (rows, cols int) {
	var ws struct{ Row, Col, X, Y uint16 }
	if ioctl(fd, syscall.TIOCGWINSZ, unsafe.Pointer(&ws)) != nil || ws.Row == 0 || ws.Col == 0 {
		return 24, 80
	}
	return int(ws.Row), int(ws.Col)
}

func notifyResize(c chan<- os.Signal) { signal.Notify(c, syscall.SIGWINCH) }
