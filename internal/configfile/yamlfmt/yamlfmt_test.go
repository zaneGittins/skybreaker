//go:build !nodeps

package yamlfmt

import (
	"strings"
	"testing"
	"time"

	"github.com/zaneGittins/skybreaker/internal/configfile"
)

type sample struct {
	Name  string              `json:"name"`
	Hold  configfile.Duration `json:"hold"`
	Rules []struct {
		Match  string `json:"match"`
		Action string `json:"action"`
	} `json:"rules"`
}

func TestDecode(t *testing.T) {
	var s sample
	err := Decode([]byte("name: x\nhold: 30s\nrules:\n  - { match: \"GET /**\", action: auto }\n"), &s)
	if err != nil || s.Name != "x" || s.Hold.D() != 30*time.Second || len(s.Rules) != 1 || s.Rules[0].Action != "auto" {
		t.Fatalf("%+v %v", s, err)
	}
}

func TestStrict(t *testing.T) {
	var s sample
	if err := Decode([]byte("name: x\nrulez: []\n"), &s); err == nil || !strings.Contains(err.Error(), "rulez") {
		t.Fatalf("unknown key: %v", err)
	}
	if err := Decode([]byte("name: x\n---\nname: y\n"), &s); err == nil {
		t.Fatal("two documents accepted")
	}
	if err := Decode([]byte("1: x\n"), &s); err == nil {
		t.Fatal("non-string key accepted")
	}
}

func TestCaseVariantKeysRejected(t *testing.T) {
	var s sample
	if err := Decode([]byte("name: x\nRules: []\nrules:\n  - { match: \"GET /**\", action: auto }\n"), &s); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("Rules + rules: %v", err)
	}
}
