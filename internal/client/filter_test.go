package client

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// run feeds the filter src in chunks of every size, so a sequence split
// across reads is handled the same as a whole one.
func run(t *testing.T, src string) string {
	t.Helper()
	var whole Filter
	want := string(whole.Write(nil, []byte(src)))
	for size := 1; size < len(src); size++ {
		var f Filter
		var out []byte
		for i := 0; i < len(src); i += size {
			out = f.Write(out, []byte(src[i:min(i+size, len(src))]))
		}
		if string(out) != want {
			t.Fatalf("chunks of %d: %q, whole: %q", size, out, want)
		}
	}
	return want
}

func TestFilterPassesScreenDrawing(t *testing.T) {
	for _, s := range []string{
		"plain text, ünïcödé and 日本語\r\n",
		"\x1b[1;31mred\x1b[0m \x1b[2J\x1b[H\x1b[?1049h\x1b[?2026h\x1b[38;2;1;2;3m",
		"\x1b7\x1b8\x1b=\x1b>\x1b(B\x1bM", // cursor save/restore, keypad, charset, reverse index
		"tab\tbell\x07backspace\x08",      // C0 controls a TUI uses
		"\x1b[<0;10;5M",                   // a mouse report echoed
	} {
		if got := run(t, s); got != s {
			t.Errorf("%q came out as %q", s, got)
		}
	}
}

func TestFilterDropsStringSequences(t *testing.T) {
	for name, c := range map[string]struct{ in, want string }{
		"clipboard write, BEL":   {"a\x1b]52;c;ZWNobyBwd25lZA==\x07b", "ab"},
		"clipboard write, ST":    {"a\x1b]52;c;ZWNobyBwd25lZA==\x1b\\b", "ab"},
		"hyperlink":              {"\x1b]8;;https://evil.example/\x1b\\click\x1b]8;;\x1b\\", "click"},
		"window title":           {"\x1b]0;sudo rm -rf ~\x07ok", "ok"},
		"tmux passthrough (DCS)": {"x\x1bPtmux;\x1b\x1b]52;c;aGk=\x07\x1b\\y", "xy"},
		"DCS query":              {"\x1bP+q544e\x1b\\z", "z"},
		"APC (kitty graphics)":   {"\x1b_Ga=T;AAAA\x1b\\z", "z"},
		"PM":                     {"\x1b^secret\x1b\\z", "z"},
		"SOS":                    {"\x1bXsecret\x1b\\z", "z"},
		"C1 OSC in UTF-8":        {"a\u009d52;c;aGk=\u009cb", "ab"},
		"C1 DCS in UTF-8":        {"a\u0090tmux;x\u009cb", "ab"},
		"C1 CSI dropped":         {"a\u009b2Jb", "a2Jb"},
		"CAN aborts a string":    {"\x1b]52;c;aGk=\x18after", "after"},
		"SUB aborts a string":    {"\x1b]52;c;aGk=\x1aafter", "after"},
		"ESC aborts a string":    {"\x1b]52;c;aGk=\x1b[31mred", "\x1b[31mred"},
		"nested introducer":      {"\x1b]0;\x1b]52;c;aGk=\x07z", "z"},
	} {
		if got := run(t, c.in); got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
}

func TestFilterKeepsALoneESC(t *testing.T) {
	// ESC then ESC: as in a terminal, the first is void and the second
	// starts the sequence. A stray string terminator is void too.
	if got := run(t, "\x1b\x1b[0m\x1b\\"); got != "\x1b[0m" {
		t.Fatalf("got %q", got)
	}
	// A lone ESC at the end of a read is held, not lost.
	var f Filter
	out := f.Write(nil, []byte("a\x1b"))
	out = f.Write(out, []byte("[1m"))
	if string(out) != "a\x1b[1m" {
		t.Fatalf("got %q", out)
	}
}

func TestVisible(t *testing.T) {
	for in, want := range map[string]string{
		"fix the login":         "fix the login",
		"evil\u202eexe.txt":     "evil<U+202E>exe.txt",
		"zero\u200bwidth":       "zero<U+200B>width",
		"line\nbreak\ttab":      "line<U+000A>break<U+0009>tab",
		"esc\x1b]52;c;aGk=\x07": "esc<U+001B>]52;c;aGk=<U+0007>",
		"c1\u009b2J":            "c1<U+009B>2J",
		"tag\U000E0041":         "tag<U+E0041>",
		"sep\u2028":             "sep<U+2028>",
		"bad\xffbyte":           "bad<0xFF>byte",
		"日本語 ok":                "日本語 ok",
	} {
		if got := Visible(in); got != want {
			t.Errorf("Visible(%q) = %q, want %q", in, got, want)
		}
	}
	if !strings.Contains(Visible("\x1b"), "<U+001B>") {
		t.Fatal("ESC not revealed")
	}
}

// Terminals that decode 8-bit controls (a Latin-1 locale, PuTTY, the Linux
// console after ESC % @) act on raw C1 bytes; overlong UTF-8 can smuggle
// them past a lazy decoder. Only well-formed UTF-8 passes, C1 never.
func TestFilterStrictUTF8(t *testing.T) {
	const rep = "�"
	for name, c := range map[string]struct{ in, want string }{
		"raw OSC and ST":          {"a\x9d52;c;aGk=\x9cb", "a" + rep + "52;c;aGk=" + rep + "b"},
		"raw DCS and APC":         {"\x90+q544e\x9c\x9f_G\x9c", rep + "+q544e" + rep + rep + "_G" + rep},
		"leave UTF-8 first":       {"\x1b%@\x9d0;owned\x07", rep + "0;owned\x07"},
		"select UTF-8 too":        {"\x1b%Gx", "x"},
		"overlong C1":             {"a\xe0\x82\x9db", "a" + rep + "b"},
		"overlong ESC":            {"a\xc0\x9b[2Jb", "a" + rep + rep + "[2Jb"},
		"surrogate":               {"a\xed\xa0\x80b", "a" + rep + "b"},
		"truncated sequence":      {"a\xe6\x97b", "a" + rep + "b"},
		"lone continuation":       {"a\x80b", "a" + rep + "b"},
		"valid text kept":         {"日本語 ünï \U0001F600 �", "日本語 ünï \U0001F600 �"},
		"private ESC k title":     {"\x1bkevil title\x07ok", "evil title\x07ok"},
		"Terminology ESC }":       {"\x1b}bt;evil\x00ok", "bt;evilok"},
		"NUL dropped":             {"a\x00b", "ab"},
		"RIS not passed":          {"a\x1bcb", "ab"},
		"C1 ST ends a string too": {"\x1b]0;x\x9cafter", "after"},
	} {
		if got := run(t, c.in); got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
}

func FuzzFilter(f *testing.F) {
	for _, s := range []string{"\x1b]52;c;aGk=\x07", "\x1bPtmux;\x1b\x1b]0;x\x07\x1b\\", "\xc2\x9d0;x\xc2\x9c", "\x9d\x1b%@\xe0\x82\x9d", "a\x1b[31mb"} {
		f.Add([]byte(s), 3)
	}
	f.Fuzz(func(t *testing.T, in []byte, split int) {
		var whole Filter
		out := whole.Write(nil, in)
		if !utf8.Valid(out) {
			t.Fatalf("invalid UTF-8 out: %q", out)
		}
		for i, r := range string(out) {
			if r >= 0x80 && r <= 0x9f || r == 0 {
				t.Fatalf("control %U out at %d: %q", r, i, out)
			}
		}
		for i := 0; i+1 < len(out); i++ {
			if out[i] == 0x1b && strings.IndexByte("]P_^X%", out[i+1]) >= 0 {
				t.Fatalf("string or charset sequence out: %q", out)
			}
		}
		if split = max(split, 1)%16 + 1; split < len(in) {
			var f2 Filter
			var parts []byte
			for i := 0; i < len(in); i += split {
				parts = f2.Write(parts, in[i:min(i+split, len(in))])
			}
			if string(parts) != string(out) {
				t.Fatalf("chunked %q, whole %q", parts, out)
			}
		}
	})
}
