package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Prompts round-trip through their file, cleaned, in the order saved; a
// file that isn't there is none, and one edited into a bad state is refused.
func TestPromptsFile(t *testing.T) {
	path := PromptsIn(t.TempDir())
	if ps, err := ReadPrompts(path); err != nil || ps == nil || len(ps) != 0 {
		t.Fatalf("missing file: %v %v", ps, err)
	}
	in := []Prompt{{Name: "tests", Text: "Run the tests.\r\nFix what fails.\n"}, {Name: "a-b_2", Text: "é"}}
	if err := WritePrompts(path, in); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", fi, err)
	}
	ps, err := ReadPrompts(path)
	want := []Prompt{{Name: "tests", Text: "Run the tests.\nFix what fails."}, {Name: "a-b_2", Text: "é"}}
	if err != nil || !reflect.DeepEqual(ps, want) {
		t.Fatalf("%q %v", ps, err)
	}
	if err := WritePrompts(path, []Prompt{{Name: "x", Text: ""}}); err == nil {
		t.Error("saved a prompt with no text")
	}
	if ps, _ := ReadPrompts(path); len(ps) != 2 {
		t.Errorf("a refused save changed the file: %q", ps)
	}
	os.WriteFile(path, []byte(`[{"name":"Bad Name","text":"x"}]`), 0o600)
	if _, err := ReadPrompts(path); err == nil || !strings.Contains(err.Error(), filepath.Base(path)) {
		t.Errorf("bad file: %v", err)
	}
}

func TestCheckPrompts(t *testing.T) {
	many := make([]Prompt, MaxPrompts+1)
	for i := range many {
		many[i] = Prompt{Name: "p" + strings.Repeat("x", i%30) + string(rune('a'+i%26)) + string(rune('a'+i/26)), Text: "x"}
	}
	for name, ps := range map[string][]Prompt{
		"too many":  many,
		"too long":  {{Name: "a", Text: strings.Repeat("x", MaxPromptsText+1)}},
		"long name": {{Name: strings.Repeat("a", 41), Text: "x"}},
		"dash lead": {{Name: "-a", Text: "x"}},
		"no name":   {{Name: "", Text: "x"}},
		"bad utf8":  {{Name: "a", Text: "\xff"}},
	} {
		if err := CheckPrompts(ps); err == nil {
			t.Errorf("%s: allowed", name)
		}
	}
	if err := CheckPrompts([]Prompt{{Name: strings.Repeat("a", 40), Text: "x"}, {Name: "0-fix_it", Text: "y"}}); err != nil {
		t.Error(err)
	}
}
