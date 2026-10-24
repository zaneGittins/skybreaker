package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Chat modes round-trip through their file; a file that isn't there sets
// nothing, and one edited into a bad state is refused.
func TestChatModesFile(t *testing.T) {
	path := ChatModesIn(t.TempDir())
	if m, err := ReadChatModes(path); err != nil || m.Default != "" || len(m.Projects) != 0 {
		t.Fatalf("missing file: %v %v", m, err)
	}
	in := ChatModes{Default: "auto", Projects: map[string]string{"billing": "ask"}}
	if err := writeChatModes(path, in); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", fi, err)
	}
	if m, err := ReadChatModes(path); err != nil || !reflect.DeepEqual(m, in) {
		t.Fatalf("%v %v", m, err)
	}
	if err := writeChatModes(path, ChatModes{Default: "yolo"}); err == nil {
		t.Error("saved a mode that isn't one")
	}
	if m, _ := ReadChatModes(path); m.Default != "auto" {
		t.Errorf("a refused save changed the file: %v", m)
	}
	os.WriteFile(path, []byte(`{"projects":{"x":"nope"}}`), 0o600)
	if _, err := ReadChatModes(path); err == nil || !strings.Contains(err.Error(), filepath.Base(path)) {
		t.Errorf("bad file: %v", err)
	}
}

func TestCheckChatModes(t *testing.T) {
	for name, m := range map[string]ChatModes{
		"bad default": {Default: "Auto"},
		"bad project": {Projects: map[string]string{"a": "never"}},
		"empty mode":  {Projects: map[string]string{"a": ""}},
	} {
		if err := CheckChatModes(m); err == nil {
			t.Errorf("%s: allowed", name)
		}
	}
	if err := CheckChatModes(ChatModes{Default: "plan", Projects: map[string]string{"a": "edits", "b": "ask"}}); err != nil {
		t.Error(err)
	}
}

// A project's own mode wins over the default; with neither, nothing is set.
func TestChatModesFor(t *testing.T) {
	m := ChatModes{Default: "auto", Projects: map[string]string{"billing": "ask"}}
	if got := m.For("billing"); got != "ask" {
		t.Errorf("billing: %q", got)
	}
	if got := m.For("web"); got != "auto" {
		t.Errorf("web: %q", got)
	}
	if got := (ChatModes{}).For("web"); got != "" {
		t.Errorf("unset: %q", got)
	}
}

// An edit changes what's saved; a refused one leaves the file as it was.
func TestEditChatModes(t *testing.T) {
	path := ChatModesIn(t.TempDir())
	m, err := EditChatModes(path, func(m *ChatModes) error { m.Default = "auto"; return nil })
	if err != nil || m.Default != "auto" {
		t.Fatalf("%v %v", m, err)
	}
	if _, err := EditChatModes(path, func(m *ChatModes) error { m.Default = "yolo"; return nil }); err == nil {
		t.Error("saved a mode that isn't one")
	}
	if _, err := EditChatModes(path, func(m *ChatModes) error { m.Default = "ask"; return errors.New("no") }); err == nil {
		t.Error("a failed change returned no error")
	}
	if m, _ := ReadChatModes(path); m.Default != "auto" {
		t.Errorf("a refused edit changed the file: %v", m)
	}
}
