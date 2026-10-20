package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstructions(t *testing.T) {
	path := InstructionsIn(t.TempDir())
	if text, err := ReadInstructions(path); err != nil || text != "" {
		t.Fatalf("none: %q %v", text, err)
	}
	if err := WriteInstructions(path, "Use pnpm.\r\nRun the tests."); err != nil {
		t.Fatal(err)
	}
	if text, err := ReadInstructions(path); err != nil || text != "Use pnpm.\nRun the tests." {
		t.Fatalf("read %q %v", text, err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}
	for name, text := range map[string]string{
		"too long": strings.Repeat("x", MaxInstructions+1),
		"not utf8": "\xff\xfe",
		"nul":      "a\x00b",
	} {
		if err := WriteInstructions(path, text); err == nil {
			t.Errorf("%s: written", name)
		}
	}
	// A file edited by hand into junk is refused, not passed on.
	os.WriteFile(path, []byte("a\x00"), 0o600)
	if _, err := ReadInstructions(path); err == nil {
		t.Error("read junk")
	}
	if filepath.Base(path) != "instructions.md" {
		t.Error(path)
	}
}
