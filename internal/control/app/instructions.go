package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zaneGittins/skybreaker/internal/control/sandbox"
)

// SetInstructions gives every agent these instructions: written now into
// the etc/ of each project that has one, and by writeEtc into a sandbox
// started later. Claude Code and Codex read them when a session starts,
// so running sessions keep what they had.
func (a *App) SetInstructions(text string) error {
	a.instrMu.Lock()
	a.instructions = text
	a.instrMu.Unlock()
	var errs []error
	for project := range a.cfg.Projects {
		errs = append(errs, a.RefreshInstructions(project))
	}
	return errors.Join(errs...)
}

// RefreshInstructions rewrites a project's instructions, after its own
// (an assistant's personality or memories) changed. A project never
// started has none yet: writeEtc writes them when it starts.
func (a *App) RefreshInstructions(project string) error {
	// One rewrite at a time, each with the latest text, so a slower one
	// can't put back what a newer change replaced.
	a.etcMu.Lock()
	defer a.etcMu.Unlock()
	etc := filepath.Join(a.ProjectDir(project), "etc")
	if _, err := os.Stat(etc); err != nil {
		return nil
	}
	var errs []error
	for name, b := range instructionFiles(a.instructionsFor(project)) {
		errs = append(errs, writeInPlace(filepath.Join(etc, name), b))
	}
	return errors.Join(errs...)
}

// instructionsFor is every agent's instructions, then the project's own.
func (a *App) instructionsFor(project string) string {
	a.instrMu.Lock()
	text := a.instructions
	a.instrMu.Unlock()
	own := a.projectInstructions(project)
	switch {
	case own == "":
		return text
	case text == "":
		return own
	}
	return text + "\n\n" + own
}

// instructionFiles are the instructions as each harness reads them.
func instructionFiles(text string) map[string][]byte {
	codex := ""
	if text != "" {
		codex = "# Every agent's instructions, set in Skybreaker.\ndeveloper_instructions = " + tomlString(text) + "\n"
	}
	return map[string][]byte{sandbox.InstructionsFile: []byte(text), sandbox.CodexConfigFile: []byte(codex)}
}

// writeInPlace rewrites a file without replacing it, for a file a sandbox
// has mounted on its own: a rename would leave the mount on the old one.
func writeInPlace(path string, b []byte) error {
	return os.WriteFile(path, b, 0o644)
}

// tomlString quotes s as a TOML basic string.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
