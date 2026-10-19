package setup

import (
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func press(w *Wizard, keys ...any) {
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k := k.(type) {
		case tea.KeyType:
			msg = tea.KeyMsg{Type: k}
		case string:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		w.Update(msg)
	}
}

// clear empties the focused input.
const clear = tea.KeyCtrlU

func TestWizardHappyPath(t *testing.T) {
	key := writeAppKey(t)
	w := NewWizard(Answers{Tailnet: "tail1234.ts.net", Users: []string{"you@github"}, Branch: "main", Harnesses: []string{"claude", "codex"}, Image: DefaultImage}, readyChecks())
	press(w, tea.KeyEnter) // checks
	if !strings.Contains(w.View(), "Join your tailnet") {
		t.Fatalf("view:\n%s", w.View())
	}
	// The detected login and tailnet are prefilled; only the key is typed.
	press(w, tea.KeyEnter, tea.KeyEnter, "tskey-auth-abc", tea.KeyEnter)
	if strings.Contains(w.View(), "tskey-auth-abc") {
		t.Error("auth key echoed")
	}
	press(w, "123456", tea.KeyEnter, key, tea.KeyEnter)
	press(w, "you/orbit", tea.KeyEnter, tea.KeyEnter, tea.KeyEnter, "You", tea.KeyEnter, "you@example.com", tea.KeyEnter)
	view := w.View()
	if !strings.Contains(view, "Ready to install") || !strings.Contains(view, "https://skybreaker.tail1234.ts.net") {
		t.Fatalf("summary:\n%s", view)
	}
	press(w, tea.KeyEnter)
	a := w.Answers()
	if !w.Confirmed || a.AuthKey != "tskey-auth-abc" || a.AppID != "123456" || a.AppKey != key || a.Repo != "you/orbit" ||
		a.GitName != "You" || !slices.Equal(a.Harnesses, []string{"claude", "codex"}) {
		t.Fatalf("answers: %+v", a)
	}
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestWizardStaysOnAPageUntilValid(t *testing.T) {
	w := NewWizard(Answers{}, readyChecks())
	press(w, tea.KeyEnter, "you", tea.KeyEnter, "tail1234.ts.net", tea.KeyEnter, "tskey-auth-x", tea.KeyEnter)
	if v := w.View(); !strings.Contains(v, "Join your tailnet") || !strings.Contains(v, "you@github") {
		t.Fatalf("moved on with a bad login:\n%s", v)
	}
	press(w, tea.KeyShiftTab, tea.KeyShiftTab, clear, "you@github", tea.KeyEnter, tea.KeyEnter, tea.KeyEnter)
	if v := w.View(); !strings.Contains(v, "Connect a GitHub App") {
		t.Fatalf("didn't move on:\n%s", v)
	}
	// esc goes back with the answers kept.
	press(w, tea.KeyEsc)
	if a := w.Answers(); a.Users[0] != "you@github" || !strings.Contains(w.View(), "Join your tailnet") {
		t.Fatalf("back: %+v", a)
	}
}

func TestWizardBlockedHost(t *testing.T) {
	w := NewWizard(Answers{}, []Check{{Name: "Docker", Detail: "install Docker Engine"}})
	press(w, tea.KeyEnter)
	if v := w.View(); !strings.Contains(v, "install Docker Engine") || strings.Contains(v, "Join your tailnet") {
		t.Fatalf("view:\n%s", v)
	}
	press(w, "q")
	if w.Confirmed {
		t.Error("confirmed on a blocked host")
	}
}

func TestWizardSummaryListsFixes(t *testing.T) {
	w := NewWizard(Answers{}, append(readyChecks(), Check{Name: "gVisor in Docker", Fix: &Fix{Title: "Register gVisor with Docker (restarts Docker)"}}))
	if !strings.Contains(w.View(), "Register gVisor") {
		t.Errorf("checks page doesn't say what it will fix:\n%s", w.View())
	}
	w.goTo(w.summary())
	if !strings.Contains(w.View(), "restarts Docker") {
		t.Errorf("summary:\n%s", w.View())
	}
}
