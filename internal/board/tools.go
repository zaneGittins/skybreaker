package board

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zaneGittins/skybreaker/internal/control/mcp"
)

// Instructions tell agents how to work the board.
const Instructions = `This project has a task board in Skybreaker: bugs, features and chores.

Columns: backlog (captured, not ready) → todo (ready to be worked) → in_progress → review → done.
Items are referenced as KEY-N, e.g. BIF-12.

Working an item:
1. Find work with list_items or next_item (next_item takes the top of Todo).
2. claim_item before starting, so no other agent picks it up.
3. Leave short add_note updates for decisions, blockers or anything the user should know.
4. When finished, update_item to status "review" with a note summarising what changed and how it was verified. The user moves it to done.
If you find a new bug or follow-up while working, create_item it in backlog rather than expanding scope.`

// Tools are the board's MCP tools. An agent in a sandbox works only its own
// project's board: a board other projects' agents could write to would be a
// channel for planting instructions. It may file items only in backlog or
// todo, never mark one done and never take one by force. You, on the UI's
// mount, work every board.
func Tools(s *Store) func(mcp.Caller) []mcp.Tool {
	return func(c mcp.Caller) []mcp.Tool {
		if c.Project == "" && c.User == "" {
			return nil
		}
		t := &tools{s: s, c: c}
		list := []mcp.Tool{
			t.listItems(), t.getItem(), t.createItem(), t.updateItem(), t.claimItem(), t.nextItem(), t.addNote(),
		}
		if t.user() {
			list = append([]mcp.Tool{t.listProjects()}, list...)
		}
		return list
	}
}

type tools struct {
	s *Store
	c mcp.Caller
}

func (t *tools) user() bool { return t.c.Project == "" }

// board is the board a call may use: the caller's own for an agent, which
// any project argument must name; the named one for you.
func (t *tools) board(arg string) (string, error) {
	if t.user() {
		if arg == "" {
			return "", errors.New("project is required")
		}
		return arg, nil
	}
	if arg != "" {
		p, err := t.s.GetProject(arg)
		if err != nil || p.Slug != t.c.Project {
			return "", fmt.Errorf("this sandbox works only the %s board", t.c.Project)
		}
	}
	return t.c.Project, nil
}

// own checks an agent's ref belongs to its board.
func (t *tools) own(ref string) error {
	if t.user() {
		return nil
	}
	key, _, err := ParseRef(ref)
	if err != nil {
		return err
	}
	p, err := t.s.GetProject(t.c.Project)
	if err != nil {
		return err
	}
	if !strings.EqualFold(p.Key, key) {
		return fmt.Errorf("%s is not on the %s board", strings.ToUpper(ref), t.c.Project)
	}
	return nil
}

// agent is who the call is from: what the model said, else the client's own
// name ("claude-code" becomes "claude"), else your login.
func (t *tools) agent(given string) string {
	if given = strings.TrimSpace(given); given != "" {
		return given
	}
	name := strings.ToLower(t.c.Client)
	switch {
	case strings.Contains(name, "claude"):
		return "claude"
	case strings.Contains(name, "codex"):
		return "codex"
	case name != "":
		return name
	case t.c.User != "":
		return t.c.User
	}
	return "agent"
}

// claim is who a call claims as: its name, held by its MCP session so two
// sessions with the same name are kept apart.
func (t *tools) claim(agent string, force bool) Claim {
	c := Claim{Agent: t.agent(agent), Force: force}
	if t.c.MCP != "" {
		c.Claimant = "mcp:" + t.c.MCP
	}
	return c
}

func schema(props string, required ...string) json.RawMessage {
	req, _ := json.Marshal(required)
	if required == nil {
		req = []byte("[]")
	}
	return json.RawMessage(`{"type":"object","properties":{` + props + `},"required":` + string(req) + `,"additionalProperties":false}`)
}

const (
	pProject  = `"project":{"type":"string","description":"project name or board key"}`
	pRef      = `"ref":{"type":"string","description":"item ref such as BIF-12"}`
	pAgent    = `"agent":{"type":"string","description":"your name, e.g. claude or codex"}`
	pStatuses = `"status":{"type":"array","items":{"type":"string"},"description":"only these statuses: backlog, todo, in_progress, review, done"}`
)

func (t *tools) listProjects() mcp.Tool {
	return mcp.Tool{Name: "list_projects", Description: "List every board with its item counts per column.", ReadOnly: true, Input: schema(""),
		Call: func(context.Context, mcp.Caller, json.RawMessage) (string, error) {
			var b strings.Builder
			for _, slug := range t.s.Slugs() {
				p, _ := t.s.GetProject(slug)
				fmt.Fprintf(&b, "- %s (key %s): %d backlog, %d todo, %d in progress, %d review, %d done\n",
					p.Slug, p.Key, p.Counts["backlog"], p.Counts["todo"], p.Counts["in_progress"], p.Counts["review"], p.Counts["done"])
			}
			if b.Len() == 0 {
				return "No boards yet.", nil
			}
			return b.String(), nil
		}}
}

func (t *tools) listItems() mcp.Tool {
	props := pStatuses + `,"type":{"type":"string","description":"bug, feature or chore"},"query":{"type":"string","description":"text to search for in titles, descriptions and labels"},"include_done":{"type":"boolean","description":"include done items when no status is given"},"limit":{"type":"integer","description":"maximum items to return (default 100)"}`
	desc := "List this project's items, open ones by default, in board order. Filter by status, type or text."
	if t.user() {
		props = pProject + `,` + props
		desc = "List items, open ones by default, in board order. Filter by project, status, type or text; omit project for every board."
	} else {
		props = pProject + `,` + props
	}
	return mcp.Tool{Name: "list_items", Description: desc, ReadOnly: true, Input: schema(props),
		Call: func(_ context.Context, _ mcp.Caller, raw json.RawMessage) (string, error) {
			var in struct {
				Project     string   `json:"project"`
				Status      []string `json:"status"`
				Type        string   `json:"type"`
				Query       string   `json:"query"`
				IncludeDone bool     `json:"include_done"`
				Limit       int      `json:"limit"`
			}
			if err := mcp.Args(raw, &in); err != nil {
				return "", err
			}
			project := in.Project
			if !t.user() || project != "" {
				var err error
				if project, err = t.board(in.Project); err != nil {
					return "", err
				}
			}
			if in.Limit <= 0 {
				in.Limit = 100
			}
			items, err := t.s.ListItems(Filter{Project: project, Statuses: in.Status, Type: in.Type, Query: in.Query, IncludeDone: in.IncludeDone, Limit: in.Limit})
			if err != nil {
				return "", err
			}
			if len(items) == 0 {
				return "No matching items.", nil
			}
			var b strings.Builder
			current := ""
			for _, it := range items {
				if it.Project != current {
					current = it.Project
					fmt.Fprintf(&b, "\n## %s\n", current)
				}
				b.WriteString(itemLine(it) + "\n")
			}
			if len(items) == in.Limit {
				fmt.Fprintf(&b, "\n(stopped at %d; narrow the filter or raise limit)\n", in.Limit)
			}
			return strings.TrimSpace(b.String()), nil
		}}
}

func (t *tools) getItem() mcp.Tool {
	return mcp.Tool{Name: "get_item", Description: "Get one item in full: description and activity history.", ReadOnly: true, Input: schema(pRef, "ref"),
		Call: func(_ context.Context, _ mcp.Caller, raw json.RawMessage) (string, error) {
			var in struct {
				Ref string `json:"ref"`
			}
			if err := mcp.Args(raw, &in); err != nil {
				return "", err
			}
			if err := t.own(in.Ref); err != nil {
				return "", err
			}
			it, err := t.s.GetItem(in.Ref)
			if err != nil {
				return "", err
			}
			return itemDetail(it), nil
		}}
}

func (t *tools) createItem() mcp.Tool {
	statusDesc := "starting column: backlog (default) or todo"
	if t.user() {
		statusDesc = "starting column (default backlog)"
	}
	props := pProject + `,"title":{"type":"string","description":"short imperative title"},"type":{"type":"string","description":"bug, feature or chore (default feature)"},"body":{"type":"string","description":"markdown description: context, repro steps, acceptance criteria"},"status":{"type":"string","description":"` + statusDesc + `"},"priority":{"type":"string","description":"low, normal, high or urgent (default normal)"},"labels":{"type":"array","items":{"type":"string"},"description":"free-form labels such as ui"}`
	required := []string{"title"}
	if t.user() {
		required = append(required, "project")
	}
	return mcp.Tool{Name: "create_item", Description: "Add a bug, feature or chore. Lands in backlog unless a status is given.", Input: schema(props, required...),
		Call: func(_ context.Context, _ mcp.Caller, raw json.RawMessage) (string, error) {
			var in struct {
				Project  string   `json:"project"`
				Title    string   `json:"title"`
				Type     string   `json:"type"`
				Body     string   `json:"body"`
				Status   string   `json:"status"`
				Priority string   `json:"priority"`
				Labels   []string `json:"labels"`
			}
			if err := mcp.Args(raw, &in); err != nil {
				return "", err
			}
			project, err := t.board(in.Project)
			if err != nil {
				return "", err
			}
			if st := NormalizeStatus(in.Status); !t.user() && st != "" && st != "backlog" && st != "todo" {
				return "", errors.New("agents file new items in backlog or todo")
			}
			prio, err := parsePriority(in.Priority)
			if err != nil {
				return "", err
			}
			it, err := t.s.CreateItem(project, ItemInput{Type: in.Type, Title: in.Title, Body: in.Body, Status: in.Status, Priority: prio, Labels: in.Labels}, t.agent(""))
			if err != nil {
				return "", err
			}
			return "Created " + itemLine(it), nil
		}}
}

func (t *tools) updateItem() mcp.Tool {
	statusDesc := "move to backlog, todo, in_progress or review; in_progress claims it for you"
	if t.user() {
		statusDesc = "move to backlog, todo, in_progress, review or done; in_progress claims it"
	}
	props := pRef + `,"status":{"type":"string","description":"` + statusDesc + `"},"title":{"type":"string"},"body":{"type":"string","description":"replaces the whole description; an empty string clears it"},"type":{"type":"string","description":"bug, feature or chore"},"priority":{"type":"string","description":"low, normal, high or urgent"},"labels":{"type":"array","items":{"type":"string"},"description":"replaces all labels"},"note":{"type":"string","description":"a note to add in the same step, e.g. a summary when moving to review"},` + pAgent
	return mcp.Tool{Name: "update_item", Description: "Change an item's status, title, description, type, priority or labels, optionally adding a note in the same step.", Input: schema(props, "ref"),
		Call: func(_ context.Context, _ mcp.Caller, raw json.RawMessage) (string, error) {
			var in struct {
				Ref      string    `json:"ref"`
				Status   string    `json:"status"`
				Title    string    `json:"title"`
				Body     *string   `json:"body"`
				Type     string    `json:"type"`
				Priority string    `json:"priority"`
				Labels   *[]string `json:"labels"`
				Note     string    `json:"note"`
				Agent    string    `json:"agent"`
			}
			if err := mcp.Args(raw, &in); err != nil {
				return "", err
			}
			if err := t.own(in.Ref); err != nil {
				return "", err
			}
			if in.Note != "" && strings.TrimSpace(in.Note) == "" {
				return "", errors.New("note is blank")
			}
			prio, err := parsePriority(in.Priority)
			if err != nil {
				return "", err
			}
			who := t.agent(in.Agent)
			var patch ItemPatch
			if in.Status != "" {
				switch st := NormalizeStatus(in.Status); {
				case st == "done" && !t.user():
					return "", errors.New("only the user marks an item done; move it to review")
				case st == "in_progress":
					// Starting work is a claim, with the same conflict check.
					if _, err := t.s.ClaimItem(in.Ref, t.claim(in.Agent, false)); err != nil {
						return "", err
					}
				default:
					patch.Status = &in.Status
				}
			}
			if in.Title != "" {
				patch.Title = &in.Title
			}
			patch.Body = in.Body
			if in.Type != "" {
				patch.Type = &in.Type
			}
			patch.Labels = in.Labels
			patch.Priority = prio
			update := func() (Item, error) { return t.s.UpdateItem(in.Ref, patch, who) }
			if !t.user() {
				update = func() (Item, error) { return t.s.UpdateAs(in.Ref, patch, t.claim(in.Agent, false)) }
			}
			it, err := update()
			if err != nil {
				return "", err
			}
			if in.Note != "" {
				if _, err := t.s.AddNote(it.Ref, who, in.Note); err != nil {
					return "", err
				}
			}
			return "Updated " + itemLine(it), nil
		}}
}

func (t *tools) claimItem() mcp.Tool {
	props := pRef + `,` + pAgent
	if t.user() {
		props += `,"force":{"type":"boolean","description":"take it even if another agent holds it"}`
	}
	return mcp.Tool{Name: "claim_item", Description: "Claim an item before working on it: moves it to in_progress under your name. Fails if another agent already has it.", Input: schema(props, "ref"),
		Call: func(_ context.Context, _ mcp.Caller, raw json.RawMessage) (string, error) {
			var in struct {
				Ref   string `json:"ref"`
				Agent string `json:"agent"`
				Force bool   `json:"force"`
			}
			if err := mcp.Args(raw, &in); err != nil {
				return "", err
			}
			if err := t.own(in.Ref); err != nil {
				return "", err
			}
			c := t.claim(in.Agent, in.Force && t.user())
			it, err := t.s.ClaimItem(in.Ref, c)
			if err != nil {
				return "", err
			}
			return "Claimed by " + c.Agent + ".\n\n" + itemDetail(it), nil
		}}
}

func (t *tools) nextItem() mcp.Tool {
	props := pProject + `,"claim":{"type":"boolean","description":"also claim the item"},` + pAgent
	var required []string
	if t.user() {
		required = []string{"project"}
	}
	return mcp.Tool{Name: "next_item", Description: "Get the next item to work on: the highest-priority item at the top of Todo. Optionally claim it.", Input: schema(props, required...),
		Call: func(_ context.Context, _ mcp.Caller, raw json.RawMessage) (string, error) {
			var in struct {
				Project string `json:"project"`
				Claim   bool   `json:"claim"`
				Agent   string `json:"agent"`
			}
			if err := mcp.Args(raw, &in); err != nil {
				return "", err
			}
			project, err := t.board(in.Project)
			if err != nil {
				return "", err
			}
			if in.Claim {
				// Pick and claim in one step, so agents asking together
				// get different items.
				c := t.claim(in.Agent, false)
				it, err := t.s.ClaimNext(project, c)
				if err != nil {
					return "", err
				}
				return "Claimed by " + c.Agent + ".\n\n" + itemDetail(it), nil
			}
			it, err := t.s.NextItem(project)
			if err != nil {
				return "", err
			}
			return itemDetail(it), nil
		}}
}

func (t *tools) addNote() mcp.Tool {
	props := pRef + `,"body":{"type":"string","description":"markdown note"},` + pAgent
	return mcp.Tool{Name: "add_note", Description: "Add a markdown note to an item: progress, a decision, a blocker or a question for the user.", Input: schema(props, "ref", "body"),
		Call: func(_ context.Context, _ mcp.Caller, raw json.RawMessage) (string, error) {
			var in struct {
				Ref   string `json:"ref"`
				Body  string `json:"body"`
				Agent string `json:"agent"`
			}
			if err := mcp.Args(raw, &in); err != nil {
				return "", err
			}
			if err := t.own(in.Ref); err != nil {
				return "", err
			}
			if _, err := t.s.AddNote(in.Ref, t.agent(in.Agent), in.Body); err != nil {
				return "", err
			}
			return "Note added to " + strings.ToUpper(in.Ref) + ".", nil
		}}
}

func parsePriority(p string) (*int, error) {
	if p == "" {
		return nil, nil
	}
	for i, name := range PriorityNames {
		if strings.EqualFold(p, name) || p == fmt.Sprint(i) {
			return &i, nil
		}
	}
	return nil, fmt.Errorf("priority must be one of %s", strings.Join(PriorityNames, ", "))
}

func priorityName(p int) string {
	if p >= 0 && p < len(PriorityNames) {
		return PriorityNames[p]
	}
	return fmt.Sprint(p)
}

func itemLine(it Item) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s [%s] %s", it.Ref, it.Type, it.Title)
	meta := []string{it.Status}
	if it.Priority != PriorityNormal {
		meta = append(meta, priorityName(it.Priority))
	}
	if it.Assignee != "" {
		meta = append(meta, "@"+it.Assignee)
	}
	if len(it.Labels) > 0 {
		meta = append(meta, "#"+strings.Join(it.Labels, " #"))
	}
	fmt.Fprintf(&b, " (%s)", strings.Join(meta, ", "))
	return b.String()
}

func itemDetail(it Item) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s: %s\n\n", it.Ref, it.Title)
	fmt.Fprintf(&b, "- project: %s\n- type: %s\n- status: %s\n- priority: %s\n", it.Project, it.Type, it.Status, priorityName(it.Priority))
	if it.Assignee != "" {
		fmt.Fprintf(&b, "- assignee: %s\n", it.Assignee)
	}
	if len(it.Labels) > 0 {
		fmt.Fprintf(&b, "- labels: %s\n", strings.Join(it.Labels, ", "))
	}
	fmt.Fprintf(&b, "- created: %s\n- updated: %s\n", it.CreatedAt.Format(time.RFC3339), it.UpdatedAt.Format(time.RFC3339))
	if it.Body != "" {
		fmt.Fprintf(&b, "\n## Description\n\n%s\n", it.Body)
	}
	if len(it.Notes) > 0 {
		b.WriteString("\n## Activity\n")
		for _, n := range it.Notes {
			who := n.Author
			if who == "" {
				who = "user"
			}
			if n.Kind == "event" {
				fmt.Fprintf(&b, "\n- %s · %s %s", n.CreatedAt.Format("2006-01-02 15:04"), who, n.Body)
			} else {
				fmt.Fprintf(&b, "\n\n**%s** · %s\n\n%s\n", who, n.CreatedAt.Format("2006-01-02 15:04"), n.Body)
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}
