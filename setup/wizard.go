package setup

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/term"
)

// field is one text input bound to an answer.
type field struct {
	label, hint string
	in          textinput.Model
	get         func(*Answers) string
	set         func(*Answers, string)
}

type page struct {
	name, title string
	intro       []string
	fields      []field
	check       func(*Answers) error
}

// Wizard asks for an install's answers, page by page: the host's checks,
// Tailscale, the GitHub App, the first project, and a summary to confirm.
type Wizard struct {
	a      Answers
	checks []Check
	pages  []page
	cur    int // 0 is the checks page; len(pages)+1 the summary
	focus  int
	err    string
	width  int

	Confirmed bool
}

func list(s []string) string { return strings.Join(s, ", ") }

// SplitList splits a list typed with commas, spaces or both.
func SplitList(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' })
}

func NewWizard(defaults Answers, checks []Check) *Wizard {
	w := &Wizard{a: defaults, checks: checks}
	w.pages = []page{
		{
			name: "Tailscale", title: "Join your tailnet",
			intro: []string{
				"Skybreaker joins your tailnet as its own machine, skybreaker, and",
				"answers only the logins you list here.",
			},
			fields: []field{
				{label: "Your tailnet login", hint: "as the admin console shows it: you@github; separate several with commas",
					get: func(a *Answers) string { return list(a.Users) }, set: func(a *Answers, s string) { a.Users = SplitList(s) }},
				{label: "Tailnet name", hint: "admin console, DNS: such as tail1234.ts.net",
					get: func(a *Answers) string { return a.Tailnet }, set: func(a *Answers, s string) { a.Tailnet = strings.ToLower(strings.TrimSpace(s)) }},
				{label: "Auth key", hint: "admin console, Settings, Keys, Generate auth key; needed for the first start only",
					get: func(a *Answers) string { return a.AuthKey }, set: func(a *Answers, s string) { a.AuthKey = strings.TrimSpace(s) }},
			},
			check: (*Answers).ValidateTailscale,
		},
		{
			name: "GitHub", title: "Connect a GitHub App",
			intro: []string{
				"Agents reach GitHub only through the broker, with a GitHub App's tokens.",
				"Create one at github.com/settings/apps/new with Contents, Pull requests",
				"and Issues read and write, and Metadata read. Install it on your repos",
				"and generate a private key. Copy the .pem to this host.",
			},
			fields: []field{
				{label: "App ID", hint: "on the App's settings page",
					get: func(a *Answers) string { return a.AppID }, set: func(a *Answers, s string) { a.AppID = strings.TrimSpace(s) }},
				{label: "Private key file", hint: "path to the .pem on this host; it's copied to /etc/skybreaker",
					get: func(a *Answers) string { return a.AppKey }, set: func(a *Answers, s string) { a.AppKey = strings.TrimSpace(s) }},
			},
			check: (*Answers).ValidateGitHub,
		},
		{
			name: "Project", title: "Your first project",
			intro: []string{
				"A project is one repo with its own sandbox. Add more later in",
				"/etc/skybreaker/skybreaker.yaml.",
			},
			fields: []field{
				{label: "Repository", hint: "owner/name on GitHub",
					get: func(a *Answers) string { return a.Repo }, set: func(a *Answers, s string) { a.Repo = strings.TrimSpace(s) }},
				{label: "Branch", hint: "agents branch from it and open pull requests against it",
					get: func(a *Answers) string { return a.Branch }, set: func(a *Answers, s string) { a.Branch = strings.TrimSpace(s) }},
				{label: "Harnesses", hint: "any of " + list(Harnesses),
					get: func(a *Answers) string { return list(a.Harnesses) }, set: func(a *Answers, s string) { a.Harnesses = SplitList(s) }},
				{label: "Commit author name", hint: "optional",
					get: func(a *Answers) string { return a.GitName }, set: func(a *Answers, s string) { a.GitName = strings.TrimSpace(s) }},
				{label: "Commit author email", hint: "optional",
					get: func(a *Answers) string { return a.GitEmail }, set: func(a *Answers, s string) { a.GitEmail = strings.TrimSpace(s) }},
			},
			check: (*Answers).ValidateProject,
		},
	}
	for i := range w.pages {
		for j := range w.pages[i].fields {
			f := &w.pages[i].fields[j]
			f.in = textinput.New()
			f.in.Prompt = "› "
			f.in.PromptStyle = PromptStyle
			f.in.TextStyle = lipgloss.NewStyle().Foreground(Text)
			f.in.Width = 56
			f.in.SetValue(f.get(&w.a))
			if f.label == "Auth key" {
				f.in.EchoMode = textinput.EchoPassword
			}
		}
	}
	return w
}

// Answers returns what was entered.
func (w *Wizard) Answers() Answers { return w.a }

func (w *Wizard) Init() tea.Cmd { return textinput.Blink }

func (w *Wizard) summary() int { return len(w.pages) + 1 }

func (w *Wizard) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		w.width = msg.Width
		return w, nil
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC:
			return w, tea.Quit
		case tea.KeyEsc:
			if w.cur > 0 {
				w.goTo(w.cur - 1)
			}
			return w, nil
		}
		switch {
		case w.cur == 0:
			switch {
			case msg.Type == tea.KeyEnter && len(Blocked(w.checks)) == 0:
				w.goTo(1)
			case msg.String() == "q":
				return w, tea.Quit
			}
			return w, nil
		case w.cur == w.summary():
			switch {
			case msg.Type == tea.KeyEnter:
				w.Confirmed = true
				return w, tea.Quit
			case msg.String() == "q":
				return w, tea.Quit
			}
			return w, nil
		}
		p := &w.pages[w.cur-1]
		switch msg.Type {
		case tea.KeyTab, tea.KeyDown:
			w.focusField((w.focus + 1) % len(p.fields))
			return w, nil
		case tea.KeyShiftTab, tea.KeyUp:
			w.focusField((w.focus + len(p.fields) - 1) % len(p.fields))
			return w, nil
		case tea.KeyEnter:
			w.save()
			if w.focus < len(p.fields)-1 {
				w.focusField(w.focus + 1)
				return w, nil
			}
			if err := p.check(&w.a); err != nil {
				w.err = err.Error()
				return w, nil
			}
			w.goTo(w.cur + 1)
			return w, nil
		}
		var cmd tea.Cmd
		p.fields[w.focus].in, cmd = p.fields[w.focus].in.Update(msg)
		return w, cmd
	}
	return w, nil
}

// save copies the current page's inputs into the answers.
func (w *Wizard) save() {
	if w.cur < 1 || w.cur > len(w.pages) {
		return
	}
	for _, f := range w.pages[w.cur-1].fields {
		f.set(&w.a, f.in.Value())
	}
}

func (w *Wizard) goTo(n int) {
	w.save()
	w.cur, w.err = n, ""
	if n >= 1 && n <= len(w.pages) {
		w.focusField(0)
	}
}

func (w *Wizard) focusField(i int) {
	p := &w.pages[w.cur-1]
	for j := range p.fields {
		p.fields[j].in.Blur()
	}
	w.focus = i
	p.fields[i].in.Focus()
}

func (w *Wizard) View() string {
	var b strings.Builder
	b.WriteString(Banner())
	b.WriteString("\n" + w.breadcrumb() + "\n")
	var body, keys string
	switch {
	case w.cur == 0:
		body, keys = w.viewChecks()
	case w.cur == w.summary():
		body, keys = w.viewSummary(), "enter install · esc back · q quit"
	default:
		body, keys = w.viewPage(&w.pages[w.cur-1]), "enter next · tab move · esc back · ctrl+c quit"
	}
	panel := PanelStyle.Width(86)
	if w.width > 0 && w.width < 90 {
		panel = panel.Width(w.width - 4)
	}
	b.WriteString(panel.Render(strings.TrimRight(body, "\n")))
	b.WriteString("\n" + HintStyle.Render("  "+keys) + "\n")
	return b.String()
}

func (w *Wizard) breadcrumb() string {
	names := []string{"Checks"}
	for _, p := range w.pages {
		names = append(names, p.name)
	}
	names = append(names, "Confirm")
	var parts []string
	for i, n := range names {
		switch {
		case i == w.cur:
			parts = append(parts, StepOnStyle.Render(n))
		case i < w.cur:
			parts = append(parts, StepDoneStyle.Render("✓ "+n))
		default:
			parts = append(parts, StepTodoStyle.Render(n))
		}
	}
	return "  " + strings.Join(parts, DimStyle.Render("  ›  "))
}

func (w *Wizard) viewChecks() (string, string) {
	var b strings.Builder
	b.WriteString(LabelStyle.Render("Checking this host") + "\n\n")
	for _, c := range w.checks {
		mark, detail := OKStyle.Render("✓"), DimStyle.Render(c.Detail)
		switch {
		case !c.OK && c.Fix != nil:
			mark, detail = WarnStyle.Render("•"), WarnStyle.Render(c.Fix.Title)
		case !c.OK:
			mark, detail = ErrorStyle.Render("✗"), ErrorStyle.Render(c.Detail)
		}
		fmt.Fprintf(&b, "  %s %-18s %s\n", mark, c.Name, detail)
	}
	if len(Blocked(w.checks)) > 0 {
		b.WriteString("\n" + ErrorStyle.Render("Install what's missing, then run skybreaker-setup again."))
		return b.String(), "q quit"
	}
	b.WriteString("\n" + DimStyle.Render("Items marked • are set up for you when you confirm."))
	return b.String(), "enter continue · q quit"
}

func (w *Wizard) viewPage(p *page) string {
	var b strings.Builder
	b.WriteString(LabelStyle.Render(p.title) + "\n\n")
	for _, l := range p.intro {
		b.WriteString(DimStyle.Render(l) + "\n")
	}
	for i, f := range p.fields {
		label := LabelStyle.Render(f.label)
		if i == w.focus {
			label = StepOnStyle.Render(f.label)
		}
		fmt.Fprintf(&b, "\n%s\n%s\n%s\n", label, f.in.View(), HintStyle.Render(f.hint))
	}
	if w.err != "" {
		b.WriteString("\n" + ErrorStyle.Render(w.err) + "\n")
	}
	return b.String()
}

func (w *Wizard) viewSummary() string {
	a := &w.a
	key := "not set"
	if a.AuthKey != "" {
		key = a.AuthKey[:min(len(a.AuthKey), 14)] + "…"
	}
	row := func(k, v string) string {
		return "  " + DimStyle.Render(fmt.Sprintf("%-14s", k)) + " " + ValueStyle.Render(v) + "\n"
	}
	var b strings.Builder
	b.WriteString(LabelStyle.Render("Ready to install") + "\n\n")
	b.WriteString(row("Address", a.UIURL()))
	b.WriteString(row("Logins", list(a.Users)))
	b.WriteString(row("Auth key", key))
	b.WriteString(row("GitHub App", a.AppID+"  "+a.AppKey))
	b.WriteString(row("Project", ProjectName(a.Repo)+"  ("+a.Repo+", "+a.Branch+")"))
	b.WriteString(row("Harnesses", list(a.Harnesses)))
	if len(a.Block) > 0 {
		b.WriteString(row("LAN kept out", list(a.Block)))
	}
	b.WriteString("\n" + DimStyle.Render("Also:") + "\n")
	for _, c := range w.checks {
		if !c.OK && c.Fix != nil {
			b.WriteString("  " + WarnStyle.Render("• "+c.Fix.Title) + "\n")
		}
	}
	for _, s := range []string{
		"Download skybreaker and install it as a systemd service",
		"Write /etc/skybreaker, readable by the skybreaker user only",
		"Pull the sandbox image and start Skybreaker",
	} {
		b.WriteString("  " + DimStyle.Render("• "+s) + "\n")
	}
	return b.String()
}

// RunWizard runs the wizard on the terminal. It returns ErrCancelled if the
// user quits before confirming.
func RunWizard(defaults Answers, checks []Check) (*Answers, error) {
	w := NewWizard(defaults, checks)
	opts := []tea.ProgramOption{tea.WithAltScreen()}
	if !term.IsTerminal(os.Stdin.Fd()) {
		opts = append(opts, tea.WithInputTTY()) // run from curl | sh
	}
	if _, err := tea.NewProgram(w, opts...).Run(); err != nil {
		return nil, err
	}
	if !w.Confirmed {
		if b := Blocked(checks); len(b) > 0 {
			return nil, fmt.Errorf("this host isn't ready: %s", b[0].Name+": "+b[0].Detail)
		}
		return nil, ErrCancelled
	}
	a := w.Answers()
	return &a, nil
}
