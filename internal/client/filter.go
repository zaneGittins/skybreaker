package client

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Filter strips what a sandbox could use against the terminal you attach
// from. A session's bytes come from a hostile sandbox, which can reconfigure
// its tmux to pass anything through, and a real terminal emulator does far
// more with escape sequences than the browser's xterm.js. So every string
// sequence is dropped: OSC (clipboard writes, hyperlinks, titles), DCS
// (passthrough, queries), APC, PM and SOS, along with C1 controls. CSI and
// the short ESC sequences that draw the screen pass untouched.
//
// It is a byte-stream state machine, so a sequence split across reads is
// handled like a whole one. The zero value is ready to use.
type Filter struct {
	state  int
	pendC2 bool // a 0xC2 byte, which may start a C1 control in UTF-8
}

const (
	ground    = iota
	escape    // after ESC
	str       // inside a string sequence, dropped
	strEscape // ESC inside a string: ST, or the start of the next sequence
)

const (
	esc = 0x1b
	can = 0x18
	sub = 0x1a
	bel = 0x07
)

// Write appends what's safe of src to dst.
func (f *Filter) Write(dst, src []byte) []byte {
	for _, b := range src {
		// C1 controls arrive UTF-8 encoded, as 0xC2 0x80–0x9F.
		if f.pendC2 {
			f.pendC2 = false
			if b >= 0x80 && b <= 0x9f {
				f.c1(b)
				continue
			}
			if f.state == ground {
				dst = append(dst, 0xc2)
			}
		}
		if b == 0xc2 {
			f.pendC2 = true
			continue
		}
		switch f.state {
		case ground:
			if b == esc {
				f.state = escape
				continue
			}
			dst = append(dst, b)
		case escape:
			switch b {
			case ']', 'P', '_', '^', 'X':
				f.state = str
			case esc:
				// The earlier ESC is void; this one starts the sequence.
			case '\\', can, sub:
				f.state = ground
			default:
				dst = append(dst, esc, b)
				f.state = ground
			}
		case str:
			switch b {
			case bel, can, sub:
				f.state = ground
			case esc:
				f.state = strEscape
			}
		case strEscape:
			if b == '\\' {
				f.state = ground
				continue
			}
			// ESC ends the string and starts a sequence of its own.
			f.state = escape
			dst = f.Write(dst, []byte{b})
		}
	}
	return dst
}

// c1 acts on a C1 control. None is passed on: string introducers start a
// string, ST ends one, and the rest are dropped.
func (f *Filter) c1(b byte) {
	switch b {
	case 0x90, 0x98, 0x9d, 0x9e, 0x9f: // DCS, SOS, OSC, PM, APC
		f.state = str
	case 0x9c: // ST
		if f.state == str || f.state == strEscape {
			f.state = ground
		}
	}
}

// Visible shows characters that could hide or reorder a line you read
// (controls, bidi and zero-width characters, separators, invalid bytes) as
// <U+XXXX>, as approval cards do.
func Visible(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && n == 1:
			fmt.Fprintf(&b, "<0x%02X>", s[i])
		case unicode.In(r, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp):
			fmt.Fprintf(&b, "<U+%04X>", r)
		default:
			b.WriteString(s[i : i+n])
		}
		i += n
	}
	return b.String()
}
