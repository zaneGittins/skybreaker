package setup

import "github.com/charmbracelet/lipgloss"

// Skybreaker's UI palette (web/shared/base.css).
var (
	Sun    = lipgloss.Color("#F6C443")
	Planet = lipgloss.Color("#F08A5D")
	Green  = lipgloss.Color("#8CD7AA")
	Red    = lipgloss.Color("#FF8A7A")
	Amber  = lipgloss.Color("#F1B85A")
	Text   = lipgloss.Color("#F1EEE7")
	Muted  = lipgloss.Color("#9A9487")
	Border = lipgloss.Color("#423F39")

	TitleStyle    = lipgloss.NewStyle().Foreground(Sun).Bold(true)
	DimStyle      = lipgloss.NewStyle().Foreground(Muted)
	OKStyle       = lipgloss.NewStyle().Foreground(Green)
	ErrorStyle    = lipgloss.NewStyle().Foreground(Red)
	WarnStyle     = lipgloss.NewStyle().Foreground(Amber)
	AccentStyle   = lipgloss.NewStyle().Foreground(Planet)
	LabelStyle    = lipgloss.NewStyle().Foreground(Text).Bold(true)
	ValueStyle    = lipgloss.NewStyle().Foreground(Green)
	HintStyle     = lipgloss.NewStyle().Foreground(Muted).Italic(true)
	PanelStyle    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(Border).Padding(1, 3).MarginTop(1)
	StepOnStyle   = lipgloss.NewStyle().Foreground(Sun).Bold(true)
	StepDoneStyle = lipgloss.NewStyle().Foreground(Green)
	StepTodoStyle = lipgloss.NewStyle().Foreground(Muted)
	PromptStyle   = lipgloss.NewStyle().Foreground(Sun).Bold(true)
)

const bannerArt = `   _____ __          __                    __
  / ___// /____  __ / /_  ________  ____ _/ /_____  _____
  \__ \/ //_/ / / // __ \/ ___/ _ \/ __ ` + "`" + `/ //_/ _ \/ ___/
 ___/ / ,< / /_/ // /_/ / /  /  __/ /_/ / ,< /  __/ /
/____/_/|_|\__, //_.___/_/   \___/\__,_/_/|_|\___/_/
          /____/`

// Banner is the title shown above the wizard and the step output.
func Banner() string {
	return TitleStyle.Render(bannerArt) + "\n" +
		DimStyle.Render("  Coding agents in gVisor sandboxes, on your tailnet. "+Version) + "\n"
}
