package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

// MaxPrompts is how many prompts can be saved, and MaxPromptsText the most
// all their text can hold together, in bytes.
const (
	MaxPrompts     = 100
	MaxPromptsText = 48 << 10
)

var promptNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)

// Prompt is a canned prompt, set in the app: typing /prompt <name> in any
// chat sends its text to the agent as a message of your own.
type Prompt struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

// Prompts is the file of prompts: <data_dir>/settings/prompts.json.
func (c *Config) Prompts() string { return PromptsIn(c.DataDir) }

// PromptsIn is where Prompts are for a data directory.
func PromptsIn(dataDir string) string {
	return filepath.Join(dataDir, "settings", "prompts.json")
}

// CleanPrompts puts prompts in the form they're kept: Unix line ends and
// no space around the text.
func CleanPrompts(ps []Prompt) []Prompt {
	out := make([]Prompt, len(ps))
	for i, p := range ps {
		out[i] = Prompt{Name: p.Name, Text: strings.TrimSpace(strings.ReplaceAll(p.Text, "\r\n", "\n"))}
	}
	return out
}

// CheckPrompts refuses what can't be prompts: a name that isn't short,
// lower case and unique, empty text, text that isn't UTF-8 or holds NUL,
// or too many or too long.
func CheckPrompts(ps []Prompt) error {
	if len(ps) > MaxPrompts {
		return fmt.Errorf("there can be at most %d prompts", MaxPrompts)
	}
	seen := map[string]bool{}
	total := 0
	for _, p := range ps {
		switch {
		case !promptNameRe.MatchString(p.Name):
			return fmt.Errorf("prompt name %q: use up to 40 lower-case letters, digits, - and _", p.Name)
		case seen[p.Name]:
			return fmt.Errorf("there are two prompts named %s", p.Name)
		case strings.TrimSpace(p.Text) == "":
			return fmt.Errorf("prompt %s has no text", p.Name)
		case !utf8.ValidString(p.Text):
			return fmt.Errorf("prompt %s must be UTF-8 text", p.Name)
		case strings.ContainsRune(p.Text, 0):
			return fmt.Errorf("prompt %s can't hold NUL", p.Name)
		}
		seen[p.Name] = true
		total += len(p.Text)
	}
	if total > MaxPromptsText {
		return fmt.Errorf("prompts can hold at most %d KiB of text together", MaxPromptsText>>10)
	}
	return nil
}

// ReadPrompts reads the file; none is no prompts.
func ReadPrompts(path string) ([]Prompt, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return []Prompt{}, nil
	}
	if err != nil {
		return nil, err
	}
	var ps []Prompt
	if err := json.Unmarshal(b, &ps); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := CheckPrompts(ps); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if ps == nil {
		ps = []Prompt{}
	}
	return ps, nil
}

// WritePrompts saves prompts, cleaned and checked, in the order given.
func WritePrompts(path string, ps []Prompt) error {
	ps = CleanPrompts(ps)
	if err := CheckPrompts(ps); err != nil {
		return err
	}
	b, err := json.MarshalIndent(ps, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := writeTemp(path, append(b, '\n'))
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
