package gitproto

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode/utf8"
)

// Reject writes a receive-pack response that refuses every update, the way
// a git server refusing a push does, so git shows the reason: each line of
// why as "remote: ..." (when the client takes side-band messages), and
// "! [remote rejected] ref (reason)" for each ref. It reports false when the
// client asked for no status report, which leaves only an HTTP error.
func Reject(w io.Writer, push Push, reason string, why []string) bool {
	if !slices.Contains(push.Caps, "report-status") && !slices.Contains(push.Caps, "report-status-v2") {
		return false
	}
	reason = oneLine(reason)
	var status bytes.Buffer
	status.WriteString(pktLine("unpack ok\n"))
	for _, u := range push.Updates {
		status.WriteString(pktLine("ng " + u.Ref + " " + reason + "\n"))
	}
	status.WriteString("0000")

	band := 0
	switch {
	case slices.Contains(push.Caps, "side-band-64k"):
		band = maxPktLen - 5
	case slices.Contains(push.Caps, "side-band"):
		band = 1000 - 5
	}
	if band == 0 {
		w.Write(status.Bytes())
		return true
	}
	var out bytes.Buffer
	for _, l := range why {
		sideband(&out, 2, []byte(oneLine(l)+"\n"), band)
	}
	sideband(&out, 1, status.Bytes(), band)
	out.WriteString("0000")
	w.Write(out.Bytes())
	return true
}

func sideband(out *bytes.Buffer, ch byte, b []byte, max int) {
	for len(b) > 0 {
		n := min(len(b), max)
		out.WriteString(fmt.Sprintf("%04x", n+5))
		out.WriteByte(ch)
		out.Write(b[:n])
		b = b[n:]
	}
}

func pktLine(s string) string { return fmt.Sprintf("%04x", len(s)+4) + s }

// oneLine keeps a reason to one printable line, short enough for a pkt-line.
func oneLine(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	if len(s) > 1000 {
		cut := 1000
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut]
	}
	return s
}

// ResultType is the content type of a receive-pack response.
const ResultType = "application/x-git-receive-pack-result"

// maxAdvert bounds a ref advertisement read for rewriting.
const maxAdvert = 64 << 20

// NoThin adds the no-thin capability to a receive-pack ref advertisement,
// asking the client for a pack with no deltas against objects outside it,
// so every commit in it can be read. The rest of the advertisement is
// copied as it is.
func NoThin(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxAdvert+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxAdvert {
		return nil, errors.New("ref advertisement too large")
	}
	rd := bytes.NewReader(b)
	var out bytes.Buffer
	for {
		start := len(b) - rd.Len()
		line, flush, err := readPkt(rd)
		if err != nil {
			return nil, fmt.Errorf("ref advertisement: %w", err)
		}
		raw := b[start : len(b)-rd.Len()]
		i := bytes.IndexByte(line, 0)
		switch {
		case flush, bytes.HasPrefix(line, []byte("# service=")), bytes.Equal(bytes.TrimSuffix(line, []byte("\n")), []byte("version 1")):
			out.Write(raw)
			continue
		case i < 0:
			return nil, errors.New("ref advertisement has no capabilities")
		}
		caps := strings.Fields(string(line[i+1:]))
		if !slices.Contains(caps, "no-thin") {
			caps = append(caps, "no-thin")
		}
		first := string(line[:i]) + "\x00" + strings.Join(caps, " ") + "\n"
		if len(first)+4 > maxPktLen {
			return nil, errors.New("ref advertisement line too long")
		}
		out.WriteString(pktLine(first))
		out.Write(b[len(b)-rd.Len():])
		return out.Bytes(), nil
	}
}
