package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// MaxInstructions is the most the agents' instructions can hold, in bytes.
const MaxInstructions = 32 << 10

// Instructions is the file of instructions every agent gets, set in the
// app: <data_dir>/settings/instructions.md. Claude Code reads them as its
// managed CLAUDE.md, Codex as developer instructions and pi as an addition
// to its system prompt.
func (c *Config) Instructions() string { return InstructionsIn(c.DataDir) }

// InstructionsIn is where Instructions are for a data directory.
func InstructionsIn(dataDir string) string {
	return filepath.Join(dataDir, "settings", "instructions.md")
}

// CleanInstructions puts instructions in the form they're kept: Unix line
// ends.
func CleanInstructions(text string) string {
	return strings.ReplaceAll(text, "\r\n", "\n")
}

// CheckInstructions refuses what can't be instructions: too long, not
// UTF-8, or holding NUL.
func CheckInstructions(text string) error {
	switch {
	case len(text) > MaxInstructions:
		return fmt.Errorf("instructions can be at most %d KiB", MaxInstructions>>10)
	case !utf8.ValidString(text):
		return errors.New("instructions must be UTF-8 text")
	case strings.ContainsRune(text, 0):
		return errors.New("instructions can't hold NUL")
	}
	return nil
}

// ReadInstructions reads the file; none is no instructions.
func ReadInstructions(path string) (string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	text := string(b)
	if err := CheckInstructions(text); err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	return text, nil
}

// WriteInstructions saves instructions, checked and cleaned.
func WriteInstructions(path, text string) error {
	text = CleanInstructions(text)
	if err := CheckInstructions(text); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := writeTemp(path, []byte(text))
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
