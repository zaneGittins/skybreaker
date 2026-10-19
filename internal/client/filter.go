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
// more with escape sequences than the browser's xterm.js. So:
//   - every string sequence is dropped: OSC (clipboard writes, hyperlinks,
//     titles), DCS (passthrough, queries), APC, PM and SOS;
//   - only well-formed UTF-8 passes. Raw 8-bit bytes, overlong forms and
//     surrogates become U+FFFD, so a terminal that decodes 8-bit controls
//     never sees one, and C1 controls (U+0080 to U+009F) are dropped;
//   - ESC passes only before CSI and the short sequences that draw the
//     screen; private ones (ESC k titles, ESC % leaving UTF-8, ESC } and
//     the like) are dropped, as is NUL.
//
// It is a byte-stream state machine, so a sequence split across reads is
// handled like a whole one. The zero value is ready to use.
type Filter struct {
	state int
	pend  []byte // an incomplete UTF-8 sequence, or a 0xC2 inside a string
}

const (
	ground    = iota
	escape    // after ESC
	escSkip   // ESC %: its next byte is dropped too
	str       // inside a string sequence, dropped
	strEscape // ESC inside a string: ST, or the start of the next sequence
)

const (
	esc = 0x1b
	can = 0x18
	sub = 0x1a
	bel = 0x07
)

// drawing are the bytes ESC may be followed by: CSI, cursor save and
// restore, keypad modes, charset designation, index and line feeds,
// DEC line attributes.
var drawing = map[byte]bool{'[': true, '7': true, '8': true, '=': true, '>': true, '(': true, ')': true, '*': true, '+': true,
	'-': true, '.': true, '/': true, 'M': true, 'D': true, 'E': true, 'H': true, '#': true}

var replacement = []byte("\uFFFD")

// Write appends what's safe of src to dst.
func (f *Filter) Write(dst, src []byte) []byte {
	for _, b := range src {
		dst = f.byte(dst, b)
	}
	return dst
}

func (f *Filter) byte(dst []byte, b byte) []byte {
	switch f.state {
	case str, strEscape:
		if len(f.pend) > 0 { // after 0xC2: U+009C is ST
			f.pend = f.pend[:0]
			if b == 0x9c {
				f.state = ground
				return dst
			}
		}
		switch {
		case b == 0xc2:
			f.pend = append(f.pend, b)
		case b == 0x9c: // a raw 8-bit ST
			f.state = ground
		case f.state == strEscape:
			if b == '\\' {
				f.state = ground
				return dst
			}
			// ESC ends the string and starts a sequence of its own.
			f.state = escape
			return f.byte(dst, b)
		case b == bel, b == can, b == sub:
			f.state = ground
		case b == esc:
			f.state = strEscape
		}
		return dst
	case escSkip:
		f.state = ground
		return dst
	case escape:
		switch {
		case b == ']', b == 'P', b == '_', b == '^', b == 'X':
			f.state = str
		case b == esc:
			// The earlier ESC is void; this one starts the sequence.
		case b == '%':
			f.state = escSkip
		case drawing[b]:
			dst = append(dst, esc, b)
			f.state = ground
		default: // ESC \, CAN, SUB, or a sequence that doesn't draw
			f.state = ground
		}
		return dst
	}
	if len(f.pend) > 0 || b >= 0x80 {
		return f.utf8(dst, b)
	}
	switch b {
	case esc:
		f.state = escape
	case 0:
	default:
		dst = append(dst, b)
	}
	return dst
}

// utf8Len is the length of the sequence a lead byte starts, or 0.
func utf8Len(b byte) int {
	switch {
	case b >= 0xc2 && b <= 0xdf:
		return 2
	case b >= 0xe0 && b <= 0xef:
		return 3
	case b >= 0xf0 && b <= 0xf4:
		return 4
	}
	return 0
}

// utf8 collects a multi-byte sequence and passes it only when well formed
// and not a C1 control.
func (f *Filter) utf8(dst []byte, b byte) []byte {
	if len(f.pend) == 0 {
		if utf8Len(b) == 0 {
			return append(dst, replacement...)
		}
		f.pend = append(f.pend, b)
		return dst
	}
	if b&0xc0 != 0x80 { // not a continuation: the sequence was cut short
		f.pend = f.pend[:0]
		return f.byte(append(dst, replacement...), b)
	}
	f.pend = append(f.pend, b)
	if len(f.pend) < utf8Len(f.pend[0]) {
		return dst
	}
	r, n := utf8.DecodeRune(f.pend)
	seq := f.pend
	f.pend = f.pend[:0]
	switch {
	case r == utf8.RuneError && n != len(seq):
		return append(dst, replacement...)
	case r >= 0x80 && r <= 0x9f:
		f.c1(byte(r))
		return dst
	}
	return append(dst, seq...)
}

// c1 acts on a C1 control. None is passed on: string introducers start a
// string, and the rest are dropped.
func (f *Filter) c1(b byte) {
	switch b {
	case 0x90, 0x98, 0x9d, 0x9e, 0x9f: // DCS, SOS, OSC, PM, APC
		f.state = str
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
