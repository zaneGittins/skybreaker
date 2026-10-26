package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/zaneGittins/skybreaker/internal/control/chat"
)

// ChatModes are the permission modes new chats start in, set in the app:
// a default for every project, and a project's own in place of it.
type ChatModes struct {
	Default  string            `json:"default,omitempty"`
	Projects map[string]string `json:"projects,omitempty"`
}

// For is the mode a new chat in project starts in, or "" when none is set.
func (m ChatModes) For(project string) string {
	if p := m.Projects[project]; p != "" {
		return p
	}
	return m.Default
}

// StartMode is the permission mode a new chat in project starts in: the
// one set in the app for it, else the default, else ask. One the driver
// lacks falls back to its safest.
func (c *Config) StartMode(project, driver string) string {
	set, err := ReadChatModes(c.ChatModes())
	if err != nil {
		log.Printf("skybreaker: chat modes: %v", err)
	}
	modes := chat.New(driver).Modes()
	if mode := set.For(project); slices.Contains(modes, mode) {
		return mode
	}
	return modes[0]
}

// ChatModes is the file of chat modes: <data_dir>/settings/chat-modes.json.
func (c *Config) ChatModes() string { return ChatModesIn(c.DataDir) }

// ChatModesIn is where ChatModes are for a data directory.
func ChatModesIn(dataDir string) string {
	return filepath.Join(dataDir, "settings", "chat-modes.json")
}

var chatModes = []string{chat.ModeAsk, chat.ModeEdits, chat.ModePlan, chat.ModeAuto}

// ChatMode reports whether m names a chat mode.
func ChatMode(m string) bool { return slices.Contains(chatModes, m) }

// CheckChatModes refuses a mode that isn't one of the chat modes.
func CheckChatModes(m ChatModes) error {
	if m.Default != "" && !ChatMode(m.Default) {
		return fmt.Errorf("mode %q is not one of %v", m.Default, chatModes)
	}
	for _, p := range slices.Sorted(maps.Keys(m.Projects)) {
		if !ChatMode(m.Projects[p]) {
			return fmt.Errorf("project %s: mode %q is not one of %v", p, m.Projects[p], chatModes)
		}
	}
	return nil
}

// ReadChatModes reads the file; none sets nothing.
func ReadChatModes(path string) (ChatModes, error) {
	var m ChatModes
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return ChatModes{}, fmt.Errorf("%s: %w", path, err)
	}
	if err := CheckChatModes(m); err != nil {
		return ChatModes{}, fmt.Errorf("%s: %w", path, err)
	}
	return m, nil
}

var chatModesMu sync.Mutex

// EditChatModes applies change to the saved modes and saves them, checked,
// one edit at a time so none undoes another.
func EditChatModes(path string, change func(*ChatModes) error) (ChatModes, error) {
	chatModesMu.Lock()
	defer chatModesMu.Unlock()
	m, err := ReadChatModes(path)
	if err != nil {
		return ChatModes{}, err
	}
	if err := change(&m); err != nil {
		return ChatModes{}, err
	}
	if err := writeChatModes(path, m); err != nil {
		return ChatModes{}, err
	}
	return m, nil
}

// writeChatModes saves m, checked.
func writeChatModes(path string, m ChatModes) error {
	if err := CheckChatModes(m); err != nil {
		return err
	}
	b, err := jsonIndent(m)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := writeTemp(path, b)
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
