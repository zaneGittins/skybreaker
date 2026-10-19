package skybreaker

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// TestNoHiddenCharacters fails on characters in the source that a reader
// can't see or that reorder what they read (Trojan Source): controls, bidi
// and zero-width characters, separators. Tests that need them write them
// as escapes.
func TestNoHiddenCharacters(t *testing.T) {
	skipDirs := map[string]bool{".git": true, "bin": true, "vendor": true, "testdata": true, "node_modules": true}
	skipExt := map[string]bool{".png": true, ".woff2": true, ".ico": true, ".svg": true}
	filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if skipExt[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil || !utf8.Valid(b) {
			return nil // binary
		}
		line := 1
		for _, r := range string(b) {
			switch {
			case r == '\n':
				line++
			case r == '\t' || r == '\r':
			case unicode.In(r, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp):
				t.Errorf("%s:%d: U+%04X", path, line, r)
			}
		}
		return nil
	})
}
