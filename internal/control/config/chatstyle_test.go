package config

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestChatStyleTitle(t *testing.T) {
	for _, c := range []struct {
		style   ChatStyle
		in, out string
	}{
		{ChatStyle{}, "  Fix the   login  flow ", "Fix the login flow"},
		{ChatStyle{Format: "lower"}, "Fix the Login flow", "fix the login flow"},
		{ChatStyle{Format: "kebab"}, "Fix the Login: flow (OAuth)!", "fix-the-login-flow-oauth"},
		{ChatStyle{Format: "snake"}, "Fix the login flow", "fix_the_login_flow"},
		{ChatStyle{Format: "kebab"}, "Größe ändern", "größe-ändern"},
		// A cut ends on a whole word when there is one to end on.
		{ChatStyle{Format: "kebab", MaxLength: 20}, "token leak in the login redirect", "token-leak-in-the"},
		{ChatStyle{MaxLength: 12}, "token leak in login", "token leak"},
		{ChatStyle{Format: "kebab", MaxLength: 10}, "supercalifragilistic", "supercalif"},
		{ChatStyle{Format: "kebab"}, "!!!", ""},
	} {
		if got := c.style.Title(c.in); got != c.out {
			t.Errorf("%+v %q: %q, want %q", c.style, c.in, got, c.out)
		}
	}
}

func TestCheckChatStyle(t *testing.T) {
	good := ChatStyle{Format: "kebab", MaxLength: 30, Guide: "Start with the area", Colors: map[string]string{"mars": "a security vulnerability"}}
	if err := CheckChatStyle(good); err != nil {
		t.Fatal(err)
	}
	for name, s := range map[string]ChatStyle{
		"format":       {Format: "camel"},
		"short":        {MaxLength: 5},
		"long":         {MaxLength: 81},
		"guide":        {Guide: strings.Repeat("x", 501)},
		"colour":       {Colors: map[string]string{"chartreuse": "bugs"}},
		"empty when":   {Colors: map[string]string{"mars": "  "}},
		"long when":    {Colors: map[string]string{"mars": strings.Repeat("x", 201)}},
		"control char": {Guide: "a\x00b"},
	} {
		if CheckChatStyle(s) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestChatStyleNote(t *testing.T) {
	if (ChatStyle{}).Set() {
		t.Fatal("an empty style is set")
	}
	s := ChatStyle{Format: "kebab", MaxLength: 30, Guide: "Start with the area", Colors: map[string]string{"mars": "a security vulnerability", "aurora": "UI work"}}
	note := s.Note()
	for _, want := range []string{"name_chat", "kebab-case", "30 characters", "Start with the area", "mars (red): a security vulnerability", "aurora (green): UI work"} {
		if !strings.Contains(note, want) {
			t.Errorf("note lacks %q:\n%s", want, note)
		}
	}
	if strings.Index(note, "mars (") > strings.Index(note, "aurora (") {
		t.Error("colours aren't in palette order")
	}
}

func TestChatStylesFile(t *testing.T) {
	path := ChatStylesIn(t.TempDir())
	if m, err := ReadChatStyles(path); err != nil || len(m) != 0 {
		t.Fatalf("missing file: %v %v", m, err)
	}
	in := ChatStyles{"orbit": {Format: "kebab", Colors: map[string]string{"sun": "bugs"}}}
	if _, err := EditChatStyles(path, func(m ChatStyles) error { copyStyles(m, in); return nil }); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", fi, err)
	}
	if m, err := ReadChatStyles(path); err != nil || !reflect.DeepEqual(m, in) {
		t.Fatalf("%v %v", m, err)
	}
	if _, err := EditChatStyles(path, func(m ChatStyles) error { m["orbit"] = ChatStyle{Format: "camel"}; return nil }); err == nil {
		t.Error("saved a bad style")
	}
	if m, _ := ReadChatStyles(path); m["orbit"].Format != "kebab" {
		t.Errorf("a refused save changed the file: %v", m)
	}
	// An empty style is no style.
	m, err := EditChatStyles(path, func(m ChatStyles) error { m["orbit"] = ChatStyle{}; return nil })
	if _, ok := m["orbit"]; err != nil || ok {
		t.Errorf("empty style kept: %v %v", m, err)
	}
}

func copyStyles(dst, src ChatStyles) {
	for k, v := range src {
		dst[k] = v
	}
}
