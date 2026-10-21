package config

import (
	"strings"
	"testing"
)

func TestMisnamed(t *testing.T) {
	for _, c := range []struct {
		uiURL, self string
		ok          bool
	}{
		{"https://skybreaker.tail1234.ts.net", "skybreaker.tail1234.ts.net", true},
		{"https://skybreaker.tail1234.ts.net/", "skybreaker.tail1234.ts.net", true},
		{"https://SKYBREAKER.tail1234.ts.net", "skybreaker.tail1234.ts.net", true},
		{"https://skybreaker.tail1234.ts.net", "", true}, // not known: nothing to say
		{"", "skybreaker.tail1234.ts.net", true},         // no ui_url: previews say so
		{"https://skybreaker.tail1234.ts.net", "skybreaker-1.tail1234.ts.net", false},
	} {
		msg := Misnamed(c.uiURL, c.self)
		if (msg == "") != c.ok {
			t.Errorf("Misnamed(%q, %q) = %q", c.uiURL, c.self, msg)
		}
		if msg != "" && (!strings.Contains(msg, c.self) || !strings.Contains(msg, "tailscale.hostname")) {
			t.Errorf("message doesn't say what to do: %q", msg)
		}
	}
}
