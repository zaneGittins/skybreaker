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
	defer a.instrMu.Unlock()
	a.instructions = text
	var errs []error
	for project := range a.cfg.Projects {
		etc := filepath.Join(a.ProjectDir(project), "etc")
		if _, err := os.Stat(etc); err != nil {
			continue // never started; writeEtc will write it
		}
		for name, b := range instructionFiles(text) {
			errs = append(errs, writeInPlace(filepath.Join(etc, name), b))
		}
	}
	return errors.Join(errs...)
}

func (a *App) currentInstructions() string {
	a.instrMu.Lock()
	defer a.instrMu.Unlock()
	return a.instructions
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
