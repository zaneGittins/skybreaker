package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/zaneGittins/skybreaker/internal/control/cron"
	"github.com/zaneGittins/skybreaker/internal/control/mcp"
)

// Instructions for the MCP server, said once to every agent.
const ToolInstructions = `An assistant (a project without a repository) has its own memory: recall finds what it remembers, and propose_memory asks the user to remember something new. post_update leaves a short note for the user's daily brief; propose_routine asks the user to schedule recurring work. Nothing proposed takes effect until the user approves it. show_file keeps what it makes for the user (podcasts, reports, pages) in its library, set_collection says how a series is shown and kept, and list_library shows what the user has heard or read.`

var routineNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,47}$`)

// Tools are an assistant's MCP tools, for its own sandbox only: a coding
// project, or you on the UI's mount, gets none.
func Tools(s *Store, assistant func(project string) bool) func(mcp.Caller) []mcp.Tool {
	return func(c mcp.Caller) []mcp.Tool {
		if c.Project == "" || !assistant(c.Project) {
			return nil
		}
		t := &tools{s: s, name: c.Project, session: c.Session}
		if t.session == "-" {
			t.session = ""
		}
		return []mcp.Tool{t.recall(), t.proposeMemory(), t.proposeRoutine(), t.postUpdate(), t.listLibrary()}
	}
}

type tools struct {
	s             *Store
	name, session string
}

func schema(props string, required ...string) json.RawMessage {
	req, _ := json.Marshal(required)
	if required == nil {
		req = []byte("[]")
	}
	return json.RawMessage(`{"type":"object","properties":{` + props + `},"required":` + string(req) + `,"additionalProperties":false}`)
}

func (t *tools) recall() mcp.Tool {
	return mcp.Tool{Name: "recall", ReadOnly: true,
		Description: "Look up what you remember about the user, with each memory's full text. Give a word or phrase to search for, or nothing for everything.",
		Input:       schema(`"query":{"type":"string","description":"text to look for in memories; empty for all"}`),
		Call: func(_ context.Context, _ mcp.Caller, raw json.RawMessage) (string, error) {
			var in struct{ Query string }
			if err := mcp.Args(raw, &in); err != nil {
				return "", err
			}
			ms := t.s.Recall(t.name, in.Query)
			if len(ms) == 0 {
				return "No memories match.", nil
			}
			var b strings.Builder
			for _, m := range ms {
				fmt.Fprintf(&b, "- [%s] %s\n", m.ID, m.Title)
				if m.Body != "" {
					fmt.Fprintf(&b, "  %s\n", strings.ReplaceAll(m.Body, "\n", "\n  "))
				}
			}
			return b.String(), nil
		}}
}

func (t *tools) proposeMemory() mcp.Tool {
	return mcp.Tool{Name: "propose_memory",
		Description: "Ask the user to remember something about them for future sessions: a preference, a fact or a standing rule. The user approves, edits or declines it; until then it isn't remembered.",
		Input:       schema(`"title":{"type":"string","description":"one line, e.g. Prefers podcasts under 20 minutes"},"body":{"type":"string","description":"optional detail or the reason, e.g. what the user said"}`, "title"),
		Call: func(_ context.Context, _ mcp.Caller, raw json.RawMessage) (string, error) {
			var in struct{ Title, Body string }
			if err := mcp.Args(raw, &in); err != nil {
				return "", err
			}
			p, err := t.s.Propose(t.name, Proposal{Kind: ProposeMemory, Title: in.Title, Body: in.Body, Session: t.session})
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("Proposed (%s). The user decides whether you remember it; don't treat it as remembered until recall returns it.", p.ID), nil
		}}
}

func (t *tools) proposeRoutine() mcp.Tool {
	return mcp.Tool{Name: "propose_routine",
		Description: "Ask the user to schedule recurring work for you, such as a weekday morning brief. An admin approves it; you can't schedule anything yourself.",
		Input: schema(`"name":{"type":"string","description":"lowercase-with-dashes, e.g. morning-brief"},"schedule":{"type":"string","description":"cron: minute hour day-of-month month day-of-week, e.g. 30 7 * * 1-5"},"prompt":{"type":"string","description":"what to do on each run"},"harness":{"type":"string","description":"optional: claude, codex or pi"}`,
			"name", "schedule", "prompt"),
		Call: func(_ context.Context, _ mcp.Caller, raw json.RawMessage) (string, error) {
			var in Routine
			if err := mcp.Args(raw, &in); err != nil {
				return "", err
			}
			if !routineNameRe.MatchString(in.Name) {
				return "", fmt.Errorf("name %q: use lowercase letters, digits and dashes", in.Name)
			}
			sched, err := cron.Parse(in.Schedule)
			if err != nil {
				return "", fmt.Errorf("schedule: %w", err)
			}
			if strings.TrimSpace(in.Prompt) == "" {
				return "", fmt.Errorf("prompt is empty")
			}
			in.Schedule = sched.String()
			p, err := t.s.Propose(t.name, Proposal{Kind: ProposeRoutine, Title: in.Name, Body: in.Prompt, Routine: &in, Session: t.session})
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("Proposed (%s). It runs only once an admin approves it.", p.ID), nil
		}}
}

func (t *tools) postUpdate() mcp.Tool {
	return mcp.Tool{Name: "post_update",
		Description: "Leave the user a one-line note for their brief, such as something you finished or noticed. Use it sparingly: a few a day at most.",
		Input:       schema(`"text":{"type":"string","description":"one line, e.g. Renewed the library hold, due back November 7"}`, "text"),
		Call: func(_ context.Context, _ mcp.Caller, raw json.RawMessage) (string, error) {
			var in struct{ Text string }
			if err := mcp.Args(raw, &in); err != nil {
				return "", err
			}
			if _, err := t.s.PostUpdate(t.name, t.session, in.Text); err != nil {
				return "", err
			}
			return "Posted.", nil
		}}
}

func (t *tools) listLibrary() mcp.Tool {
	return mcp.Tool{Name: "list_library", ReadOnly: true,
		Description: "See what's in your library for the user, newest first, and how far they got in each: new, part heard or read, finished, starred. Use it to build on what they've heard and skip what they have.",
		Input:       schema(`"collection":{"type":"string","description":"only this collection, e.g. Morning briefing"}`),
		Call: func(_ context.Context, _ mcp.Caller, raw json.RawMessage) (string, error) {
			var in struct{ Collection string }
			if err := mcp.Args(raw, &in); err != nil {
				return "", err
			}
			a := t.s.Get(t.name)
			if len(a.Library) == 0 {
				return "Your library is empty: share files with show_file.", nil
			}
			want := Slug(in.Collection)
			titles := map[string]string{}
			var b strings.Builder
			for _, c := range a.Collections {
				titles[c.ID] = c.Title
				if want == "" || c.ID == want {
					fmt.Fprintf(&b, "Collection %s (%s, keeps %s)\n", c.Title, c.Layout, keeps(c))
				}
			}
			n := 0
			for _, it := range slices.Backward(a.Library) {
				if (want != "" && it.Collection != want) || n == 50 {
					continue
				}
				n++
				fmt.Fprintf(&b, "- [%s] %s (%s", it.ID, it.Title, it.Kind)
				if c := titles[it.Collection]; c != "" {
					fmt.Fprintf(&b, ", %s", c)
				}
				fmt.Fprintf(&b, ", %s): %s\n", it.At.Format("Jan 2"), where(it))
			}
			if n == 0 {
				return "No items in that collection.", nil
			}
			return b.String(), nil
		}}
}

func keeps(c Collection) string {
	switch {
	case c.Keep > 0 && c.KeepDays > 0:
		return fmt.Sprintf("the newest %d, for %d days", c.Keep, c.KeepDays)
	case c.Keep > 0:
		return fmt.Sprintf("the newest %d", c.Keep)
	case c.KeepDays > 0:
		return fmt.Sprintf("%d days", c.KeepDays)
	}
	return "everything"
}

// where says how far the user got in an item.
func where(it Item) string {
	var s string
	switch {
	case it.Finished:
		s = "finished"
	case !it.Seen:
		s = "new"
	case it.Kind == KindAudio && it.Duration > 0:
		s = fmt.Sprintf("%d%% heard", int(100*it.Progress/it.Duration))
	case it.Kind == KindAudio:
		s = fmt.Sprintf("heard to %d:%02d", int(it.Progress)/60, int(it.Progress)%60)
	case it.Kind == KindPDF && it.Progress >= 1:
		s = fmt.Sprintf("on page %d", int(it.Progress))
	case it.Progress > 0 && it.Progress <= 1:
		s = fmt.Sprintf("%d%% read", int(100*it.Progress))
	default:
		s = "opened"
	}
	if it.Star {
		s += ", starred"
	}
	return s
}
