package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// ChatColors are the colours a chat can be given, to tell chats apart, with
// the hue each looks like.
var ChatColors = []string{"sun", "titan", "mars", "aurora", "stormlight", "voidlight", "venus", "comet"}

var colorHues = map[string]string{"sun": "yellow", "titan": "orange", "mars": "red", "aurora": "green",
	"stormlight": "blue", "voidlight": "purple", "venus": "pink", "comet": "teal"}

// ChatStyle is how a project's agents name and colour their chats.
type ChatStyle struct {
	Format    string `json:"format,omitempty"`     // "", "lower", "kebab" or "snake"
	MaxLength int    `json:"max_length,omitempty"` // in characters; 0 for no limit of its own
	Guide     string `json:"guide,omitempty"`      // how to name them, in your words
	// Colors says when to use each colour: colour → what it marks.
	Colors map[string]string `json:"colors,omitempty"`
}

// ChatStyles are each project's chat style.
type ChatStyles map[string]ChatStyle

const (
	minTitleLength = 10
	maxTitleLength = 80
	maxStyleGuide  = 500
	maxColorWhen   = 200
)

var chatFormats = map[string]string{"": "", "lower": "lowercase", "kebab": "kebab-case", "snake": "snake_case"}

// Set reports whether the style asks anything of a chat's name or colour.
func (s ChatStyle) Set() bool {
	return s.Format != "" || s.MaxLength != 0 || s.Guide != "" || len(s.Colors) > 0
}

// CheckChatStyle refuses a style the app shouldn't keep.
func CheckChatStyle(s ChatStyle) error {
	if _, ok := chatFormats[s.Format]; !ok {
		return fmt.Errorf("format %q is not one of lower, kebab or snake", s.Format)
	}
	if s.MaxLength != 0 && (s.MaxLength < minTitleLength || s.MaxLength > maxTitleLength) {
		return fmt.Errorf("a name's length limit is %d to %d characters", minTitleLength, maxTitleLength)
	}
	if err := styleText("the naming guide", s.Guide, maxStyleGuide); err != nil {
		return err
	}
	for _, c := range slices.Sorted(maps.Keys(s.Colors)) {
		if !slices.Contains(ChatColors, c) {
			return fmt.Errorf("colours are %s", strings.Join(ChatColors, ", "))
		}
		if strings.TrimSpace(s.Colors[c]) == "" {
			return fmt.Errorf("say what %s is for", c)
		}
		if err := styleText("what "+c+" is for", s.Colors[c], maxColorWhen); err != nil {
			return err
		}
	}
	return nil
}

func styleText(what, s string, max int) error {
	switch {
	case utf8.RuneCountInString(s) > max:
		return fmt.Errorf("%s is at most %d characters", what, max)
	case !utf8.ValidString(s) || strings.ContainsFunc(s, func(r rune) bool { return unicode.IsControl(r) && r != '\n' }):
		return fmt.Errorf("%s must be plain text", what)
	}
	return nil
}

// Title puts a title in the style: its case, its separator, and cut to its
// length on a word boundary when there is one.
func (s ChatStyle) Title(title string) string {
	words := strings.Fields(title)
	sep := " "
	switch s.Format {
	case "lower":
		title = strings.ToLower(strings.Join(words, " "))
	case "kebab", "snake":
		sep = map[string]string{"kebab": "-", "snake": "_"}[s.Format]
		words = strings.FieldsFunc(strings.ToLower(title), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
		title = strings.Join(words, sep)
	default:
		title = strings.Join(words, " ")
	}
	r := []rune(title)
	if s.MaxLength == 0 || len(r) <= s.MaxLength {
		return title
	}
	cut := string(r[:s.MaxLength])
	if string(r[s.MaxLength]) != sep {
		if i := strings.LastIndex(cut, sep); i > 0 {
			cut = cut[:i]
		}
	}
	return strings.Trim(cut, sep)
}

// Note tells an agent how to name and colour its chat, ahead of the first
// message.
func (s ChatStyle) Note() string {
	return "Before you start on the message below, call the skybreaker tool name_chat to name this chat for the work it asks for." + s.Rules()
}

// Rules are the style, in words for an agent.
func (s ChatStyle) Rules() string {
	var b strings.Builder
	var rules []string
	if f := chatFormats[s.Format]; f != "" {
		rules = append(rules, "written in "+f)
	}
	if s.MaxLength > 0 {
		rules = append(rules, fmt.Sprintf("at most %d characters", s.MaxLength))
	}
	if len(rules) > 0 {
		b.WriteString(" Names here are short, " + strings.Join(rules, ", ") + ".")
	}
	if g := strings.TrimSpace(s.Guide); g != "" {
		b.WriteString(" The user's naming guide: " + g)
	}
	if len(s.Colors) > 0 {
		b.WriteString("\nAlso give it the colour that fits, if one does:")
		for _, c := range ChatColors {
			if w := s.Colors[c]; w != "" {
				fmt.Fprintf(&b, "\n- %s (%s): %s", c, colorHues[c], strings.TrimSpace(w))
			}
		}
	}
	return b.String()
}

// ChatStyle is project's chat style; none is the zero style.
func (c *Config) ChatStyle(project string) (ChatStyle, error) {
	m, err := ReadChatStyles(c.ChatStyles())
	return m[project], err
}

// ChatStyles is the file of chat styles: <data_dir>/settings/chat-styles.json.
func (c *Config) ChatStyles() string { return ChatStylesIn(c.DataDir) }

// ChatStylesIn is where ChatStyles are for a data directory.
func ChatStylesIn(dataDir string) string {
	return filepath.Join(dataDir, "settings", "chat-styles.json")
}

// ReadChatStyles reads the file; none sets nothing.
func ReadChatStyles(path string) (ChatStyles, error) {
	m := ChatStyles{}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return ChatStyles{}, fmt.Errorf("%s: %w", path, err)
	}
	for _, p := range slices.Sorted(maps.Keys(m)) {
		if err := CheckChatStyle(m[p]); err != nil {
			return ChatStyles{}, fmt.Errorf("%s: project %s: %w", path, p, err)
		}
	}
	return m, nil
}

var chatStylesMu sync.Mutex

// EditChatStyles applies change to the saved styles and saves them,
// checked, one edit at a time. An empty style is dropped.
func EditChatStyles(path string, change func(ChatStyles) error) (ChatStyles, error) {
	chatStylesMu.Lock()
	defer chatStylesMu.Unlock()
	m, err := ReadChatStyles(path)
	if err != nil {
		return nil, err
	}
	if err := change(m); err != nil {
		return nil, err
	}
	maps.DeleteFunc(m, func(_ string, s ChatStyle) bool { return !s.Set() })
	for _, p := range slices.Sorted(maps.Keys(m)) {
		if err := CheckChatStyle(m[p]); err != nil {
			return nil, err
		}
	}
	b, err := jsonIndent(m)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	tmp, err := writeTemp(path, b)
	if err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return nil, err
	}
	return m, nil
}
