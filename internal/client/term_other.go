//go:build !linux && !darwin

package client

import (
	"errors"
	"os"
)

type termState struct{}

func isTerminal(fd int) bool { return false }
func makeRaw(fd int) (*termState, error) {
	return nil, errors.New("the terminal client runs on Linux and macOS")
}
func restore(fd int, s *termState) error { return nil }
func termSize(fd int) (rows, cols int)   { return 24, 80 }
func notifyResize(c chan<- os.Signal)    {}
