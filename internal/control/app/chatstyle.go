package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"slices"
	"strings"

	"github.com/zaneGittins/skybreaker/internal/control/config"
	"github.com/zaneGittins/skybreaker/internal/control/mcp"
	"github.com/zaneGittins/skybreaker/internal/control/store"
)

// ChatStyle is how a project's agents name and colour their chats.
func (a *App) ChatStyle(project string) (config.ChatStyle, error) {
	return a.cfg.ChatStyle(project)
}

// SetChatStyle saves a project's chat style; the zero style clears it.
func (a *App) SetChatStyle(project string, s config.ChatStyle) (config.ChatStyle, error) {
	if _, ok := a.cfg.Projects[project]; !ok {
		return config.ChatStyle{}, fmt.Errorf("project %q: %w", project, ErrNotFound)
	}
	if _, err := a.assistantProject(project); err == nil {
		return config.ChatStyle{}, errors.New("an assistant has one thread, not chats to name")
	}
	if err := config.CheckChatStyle(s); err != nil {
		return config.ChatStyle{}, err
	}
	m, err := config.EditChatStyles(a.cfg.ChatStyles(), func(m config.ChatStyles) error { m[project] = s; return nil })
	if err != nil {
		return config.ChatStyle{}, err
	}
	a.Publish(Event{Type: "project", Project: project, Data: "chat style"})
	return m[project], nil
}

// chatStyleOf is the style a chat's agent names it by, if it has one.
func (a *App) chatStyleOf(s store.Session) (config.ChatStyle, bool) {
	if s.Mode != store.ModeChat || s.Task != "" {
		return config.ChatStyle{}, false
	}
	if _, err := a.assistantProject(s.Project); err == nil {
		return config.ChatStyle{}, false
	}
	style, err := a.cfg.ChatStyle(s.Project)
	if err != nil {
		log.Printf("skybreaker: chat styles: %v", err)
	}
	return style, style.Set()
}

// styleFirstMessage asks a new chat's agent to name it, ahead of its first
// message.
func (a *App) styleFirstMessage(id string) {
	s, ok := a.store.Session(id)
	if !ok || s.Title != "" || s.Resume != "" {
		return
	}
	style, ok := a.chatStyleOf(s)
	if !ok {
		return
	}
	a.store.UpdateSession(id, func(x *store.Session) {
		x.Handoff = strings.TrimPrefix(x.Handoff+"\n\n"+style.Note(), "\n\n")
	})
}

// ChatStyleTools is name_chat, for an agent in a chat of a project with a
// chat style.
func (a *App) ChatStyleTools() func(mcp.Caller) []mcp.Tool {
	return func(c mcp.Caller) []mcp.Tool {
		if c.Project == "" || c.Session == "" || c.Session == "-" || !a.ChatIn(c.Project, c.Session) {
			return nil
		}
		s, _ := a.store.Session(c.Session)
		style, ok := a.chatStyleOf(s)
		if !ok {
			return nil
		}
		props := `"title":{"type":"string","description":"The chat's name"}`
		if len(style.Colors) > 0 {
			colors, _ := json.Marshal(slices.Sorted(maps.Keys(style.Colors)))
			props += `,"color":{"type":"string","enum":` + string(colors) + `,"description":"The colour that fits the work, if one does"}`
		}
		return []mcp.Tool{{
			Name: "name_chat",
			Description: "Name the Skybreaker chat you're in for the work it's about, and colour it, as this project asks. " +
				"Call it at the start of the chat, and again only if the work changes." + style.Rules(),
			Input: json.RawMessage(`{"type":"object","properties":{` + props + `},"required":["title"],"additionalProperties":false}`),
			Call: func(ctx context.Context, c mcp.Caller, args json.RawMessage) (string, error) {
				return a.nameChat(c, style, args)
			},
		}}
	}
}

func (a *App) nameChat(c mcp.Caller, style config.ChatStyle, args json.RawMessage) (string, error) {
	var in struct {
		Title string `json:"title"`
		Color string `json:"color"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", errors.New("give the chat's name as title")
	}
	title := style.Title(in.Title)
	if title == "" {
		return "", errors.New("the name is empty in this project's style: use letters or digits")
	}
	if _, ok := style.Colors[in.Color]; in.Color != "" && !ok {
		return "", fmt.Errorf("this project's colours are %s", strings.Join(slices.Sorted(maps.Keys(style.Colors)), ", "))
	}
	var kept []string
	err := a.store.UpdateSession(c.Session, func(s *store.Session) {
		if s.OwnTitle {
			kept = append(kept, "name")
		} else {
			s.Title = title
		}
		if in.Color != "" && s.OwnColor {
			kept = append(kept, "colour")
		} else if in.Color != "" {
			s.Color = in.Color
		}
	})
	if err != nil {
		return "", err
	}
	a.Publish(Event{Type: "session", Project: c.Project, Session: c.Session, Data: "edited"})
	out := "Named the chat " + title
	if in.Color != "" {
		out += ", coloured " + in.Color
	}
	if len(kept) > 0 {
		out += ". The user kept the " + strings.Join(kept, " and ") + " they gave it"
	}
	return out + ".", nil
}
