package preview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/zaneGittins/skybreaker/internal/control/chat"
	"github.com/zaneGittins/skybreaker/internal/control/mcp"
)

// Backend is what the preview tool needs from the app.
type Backend interface {
	Listening(ctx context.Context, project string) ([]Listening, error)
	// ChatIn reports whether session is a running chat of project's.
	ChatIn(project, session string) bool
	ChatPreview(session string, p chat.Preview) error
}

// Instructions tell agents how to show the user a web page.
const Instructions = `To show the user a web page you're working on, start its dev server listening on 0.0.0.0, not localhost (vite --host 0.0.0.0, next dev -H 0.0.0.0, python -m http.server --bind 0.0.0.0), then call preview with its port. The user gets a link that opens the page on its own address.`

const maxCaption = 200

// Tools is the preview tool, for agents in a sandbox of a project that has
// a preview. It points the project's preview at a port of its own sandbox;
// the address the user opens is always Skybreaker's.
func Tools(reg *Registry, b Backend) func(mcp.Caller) []mcp.Tool {
	return func(c mcp.Caller) []mcp.Tool {
		if c.Project == "" || reg.Origin(c.Project) == "" {
			return nil
		}
		return []mcp.Tool{{
			Name: "preview",
			Description: "Show the user a web page served from your sandbox, such as your dev server, so they can try it in their browser. " +
				"The server must listen on 0.0.0.0, not localhost. You get back the address the user opens.",
			Input: json.RawMessage(`{"type":"object","properties":{` +
				`"port":{"type":"integer","minimum":1,"maximum":65535,"description":"The port your server listens on"},` +
				`"title":{"type":"string","description":"A short caption for the user, such as what to look at"}},` +
				`"required":["port"],"additionalProperties":false}`),
			Call: func(ctx context.Context, c mcp.Caller, args json.RawMessage) (string, error) {
				return previewCall(ctx, reg, b, c, args)
			},
		}}
	}
}

func previewCall(ctx context.Context, reg *Registry, b Backend, c mcp.Caller, args json.RawMessage) (string, error) {
	var in struct {
		Port  *int   `json:"port"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal(args, &in); err != nil || in.Port == nil {
		return "", errors.New("give the port your server listens on, as a number")
	}
	port := *in.Port
	if port < 1 || port > 65535 {
		return "", ErrPort
	}
	listening, err := b.Listening(ctx, c.Project)
	if err != nil {
		return "", fmt.Errorf("couldn't list the sandbox's ports: %w", err)
	}
	found := false
	for _, l := range listening {
		if int(l.Port) != port {
			continue
		}
		if !l.Public {
			return "", fmt.Errorf("port %d listens on localhost only, which the preview can't reach. Restart the server listening on 0.0.0.0 "+
				"(vite --host 0.0.0.0, next dev -H 0.0.0.0, python -m http.server --bind 0.0.0.0) and call preview again", port)
		}
		found = true
	}
	if !found {
		return "", fmt.Errorf("nothing is listening on port %d in the sandbox. Start the server first, listening on 0.0.0.0, then call preview again", port)
	}
	if err := reg.SetTarget(c.Project, port); err != nil {
		return "", err
	}
	url := reg.Origin(c.Project)
	if c.Session != "" && c.Session != "-" && b.ChatIn(c.Project, c.Session) {
		title := strings.TrimSpace(in.Title)
		for len(title) > maxCaption {
			_, n := utf8.DecodeLastRuneInString(title)
			title = title[:len(title)-n]
		}
		if err := b.ChatPreview(c.Session, chat.Preview{Port: port, URL: url, Title: title}); err != nil {
			return "", err
		}
		return fmt.Sprintf("Done: the user has a link in the chat to port %d, at %s. It works while your server runs.", port, url), nil
	}
	return fmt.Sprintf("The preview of port %d is at %s. Give the user that address; it works while your server runs.", port, url), nil
}
