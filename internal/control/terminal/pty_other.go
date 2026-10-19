//go:build !linux

package terminal

import (
	"context"
	"errors"
)

// StartPTY needs Linux: the server runs only there, but the binary is also
// the terminal client on macOS.
func StartPTY(ctx context.Context, argv []string, rows, cols uint16) (PTY, error) {
	return nil, errors.New("terminal: the server's terminals need Linux")
}
