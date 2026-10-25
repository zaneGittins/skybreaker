package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The same instructions again leave the file alone: an agent starting
// as they're written never reads half a file for nothing.
func TestWriteInPlaceSkipsTheSame(t *testing.T) {
	path := filepath.Join(t.TempDir(), "CLAUDE.md")
	if err := writeInPlace(path, []byte("be brief")); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	os.Chtimes(path, old, old)
	if err := writeInPlace(path, []byte("be brief")); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); !fi.ModTime().Equal(old) {
		t.Errorf("rewrote the same text")
	}
	writeInPlace(path, []byte("be warm"))
	if b, _ := os.ReadFile(path); string(b) != "be warm" {
		t.Errorf("%q", b)
	}
}
