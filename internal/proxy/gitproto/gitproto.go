// Package gitproto reads git-receive-pack requests: the ref-update commands
// at the start, so the proxy can refuse a push before any of it reaches
// GitHub, and the pack after them, for the commits it carries (pack.go). It
// also writes the refusals git shows (respond.go).
//
// It enforces which refs may move, and refuses deletes and tags. It cannot
// detect a force push: telling a fast-forward from a rewrite needs the object
// graph, which only the server has. The GitHub ruleset on protected branches
// is the backstop for that.
package gitproto

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
)

const (
	maxCommands = 4096
	maxPktLen   = 65520 // LARGE_PACKET_MAX, including the 4-byte length
)

var ErrRefPolicy = errors.New("ref policy")

type RefUpdate struct{ Old, New, Ref string }

func (u RefUpdate) IsDelete() bool { return isZero(u.New) }

func isZero(oid string) bool { return oid != "" && strings.Trim(oid, "0") == "" }

// ReadCommands parses pkt-lines up to the first flush-pkt, returning the
// ref updates and the capabilities the client asked for on the first one.
// Anything it does not fully understand is an error, so an odd request
// fails closed.
func ReadCommands(r io.Reader) ([]RefUpdate, []string, error) {
	var (
		ups  []RefUpdate
		caps []string
	)
	for {
		line, flush, err := readPkt(r)
		if err != nil {
			return nil, nil, err
		}
		if flush {
			break
		}
		if len(ups) == maxCommands {
			return nil, nil, fmt.Errorf("more than %d ref updates", maxCommands)
		}
		if i := bytes.IndexByte(line, 0); i >= 0 {
			if len(ups) > 0 {
				return nil, nil, errors.New("capabilities after the first command")
			}
			caps = strings.Fields(string(line[i+1:]))
			line = line[:i]
		}
		line = bytes.TrimSuffix(line, []byte("\n"))
		u, err := parseCommand(string(line))
		if err != nil {
			return nil, nil, err
		}
		ups = append(ups, u)
	}
	if len(ups) == 0 {
		return nil, nil, errNoCommands
	}
	return ups, caps, nil
}

var errNoCommands = errors.New("no ref updates")

// readPkt reads one pkt-line. Special packets other than flush (delim,
// response-end) never belong in a v0/v1 receive-pack command list.
func readPkt(r io.Reader) (payload []byte, flush bool, err error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, false, fmt.Errorf("reading pkt-line length: %w", err)
	}
	n, err := strconv.ParseUint(string(hdr[:]), 16, 16)
	if err != nil {
		return nil, false, fmt.Errorf("bad pkt-line length %q", hdr[:])
	}
	switch {
	case n == 0:
		return nil, true, nil
	case n < 4:
		return nil, false, fmt.Errorf("unexpected special packet %04x", n)
	case n > maxPktLen:
		return nil, false, fmt.Errorf("pkt-line length %d over %d", n, maxPktLen)
	}
	payload = make([]byte, n-4)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, false, fmt.Errorf("reading pkt-line: %w", err)
	}
	return payload, false, nil
}

func parseCommand(line string) (RefUpdate, error) {
	if strings.HasPrefix(line, "push-cert") {
		return RefUpdate{}, errors.New("signed pushes are not supported")
	}
	if strings.HasPrefix(line, "shallow ") {
		return RefUpdate{}, errors.New("shallow pushes are not supported")
	}
	f := strings.Split(line, " ")
	if len(f) != 3 {
		return RefUpdate{}, fmt.Errorf("malformed command %q", line)
	}
	u := RefUpdate{Old: f[0], New: f[1], Ref: f[2]}
	if !validOID(u.Old) || !validOID(u.New) || len(u.Old) != len(u.New) {
		return RefUpdate{}, fmt.Errorf("bad object ids in %q", line)
	}
	if err := validRef(u.Ref); err != nil {
		return RefUpdate{}, err
	}
	return u, nil
}

func validOID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// validRef follows git check-ref-format, tightened: a ref must live under
// refs/, and anything git itself would refuse is refused here too.
func validRef(ref string) error {
	bad := func(why string) error { return fmt.Errorf("invalid ref %q: %s", ref, why) }
	if !strings.HasPrefix(ref, "refs/") {
		return bad("not under refs/")
	}
	if strings.Contains(ref, "..") || strings.Contains(ref, "//") || strings.Contains(ref, "@{") {
		return bad(`contains "..", "//" or "@{"`)
	}
	if strings.HasSuffix(ref, "/") || strings.HasSuffix(ref, ".") || strings.HasSuffix(ref, ".lock") {
		return bad("bad ending")
	}
	for _, r := range ref {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(" ~^:?*[\\", r) {
			return bad("forbidden character")
		}
	}
	for _, c := range strings.Split(ref, "/") {
		if strings.HasPrefix(c, ".") {
			return bad("component starts with .")
		}
	}
	return nil
}

// Peek reads the commands from body and returns a body that replays every
// byte consumed, followed by the rest, ready to forward upstream.
func Peek(body io.ReadCloser) ([]RefUpdate, []string, io.ReadCloser, error) {
	var seen bytes.Buffer
	ups, caps, err := ReadCommands(io.TeeReader(body, &seen))
	if errors.Is(err, errNoCommands) {
		// git's probe before a large push is a bare flush-pkt and nothing
		// else. It can't change a ref, so it passes with no updates.
		var one [1]byte
		if n, _ := io.ReadFull(body, one[:]); n == 0 {
			return nil, nil, replay{&seen, body}, nil
		}
	}
	if err != nil {
		return nil, nil, nil, err
	}
	return ups, caps, replay{io.MultiReader(&seen, body), body}, nil
}

type replay struct {
	io.Reader
	c io.Closer
}

func (r replay) Close() error { return r.c.Close() }

// Check enforces the ref policy: every ref matches one of allow, nothing is
// deleted, and no tags move.
func Check(updates []RefUpdate, allow []string) error {
	if len(updates) == 0 {
		return fmt.Errorf("%w: no ref updates", ErrRefPolicy)
	}
	for _, u := range updates {
		if err := validRef(u.Ref); err != nil {
			return fmt.Errorf("%w: %v", ErrRefPolicy, err)
		}
		switch {
		case u.IsDelete():
			return fmt.Errorf("%w: deleting %s is not allowed", ErrRefPolicy, u.Ref)
		case strings.HasPrefix(u.Ref, "refs/tags/"):
			return fmt.Errorf("%w: pushing tag %s is not allowed", ErrRefPolicy, u.Ref)
		case !slices.ContainsFunc(allow, func(p string) bool { return glob(p, u.Ref) }):
			return fmt.Errorf("%w: %s is outside %s", ErrRefPolicy, u.Ref, strings.Join(allow, ", "))
		}
	}
	return nil
}

// glob matches s against pattern where '*' matches any run of characters,
// slashes included. No other character is special.
func glob(pattern, s string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == s
	}
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	last := parts[len(parts)-1]
	for _, p := range parts[1 : len(parts)-1] {
		i := strings.Index(s, p)
		if i < 0 {
			return false
		}
		s = s[i+len(p):]
	}
	return len(s) >= len(last) && strings.HasSuffix(s, last)
}
