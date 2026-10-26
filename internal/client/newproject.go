package client

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/zaneGittins/skybreaker/internal/names"
)

const newProjectUsage = "usage: skybreaker new-project <folder> [name] [--harness h1,h2] [--yes]"

// pollEvery is how often a new project is looked for while Skybreaker
// restarts to add it.
var pollEvery = 1500 * time.Millisecond

// newProject makes a project from a local folder and copies everything in
// it, dot files too, into the project's workspace. A folder whose own
// origin is on GitHub gets that repo. Run again for a project made
// already, it copies into that one, which Skybreaker allows only while its
// workspace is empty. It returns what to attach to.
func newProject(ctx context.Context, c *Client, git Runner, args []string, in io.Reader, out io.Writer, interactive bool) (Entry, error) {
	var pos, harnesses []string
	yes := false
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--yes" || a == "-y":
			yes = true
		case a == "--harness":
			if i++; i == len(args) {
				return Entry{}, errors.New(newProjectUsage)
			}
			harnesses = strings.Split(args[i], ",")
		case strings.HasPrefix(a, "-"):
			return Entry{}, fmt.Errorf("unknown option %s; %s", a, newProjectUsage)
		default:
			pos = append(pos, a)
		}
	}
	if len(pos) < 1 || len(pos) > 2 {
		return Entry{}, errors.New(newProjectUsage)
	}
	dir, err := filepath.Abs(pos[0])
	if err == nil {
		dir, err = filepath.EvalSymlinks(dir)
	}
	if err != nil {
		return Entry{}, err
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return Entry{}, fmt.Errorf("%s isn't a folder", pos[0])
	}
	name := ProjectNameFor(dir)
	if len(pos) == 2 {
		name = pos[1]
	}
	if !names.Project(name) {
		if name == "" {
			return Entry{}, fmt.Errorf("can't make a project name from %q; give one after the folder", filepath.Base(dir))
		}
		return Entry{}, fmt.Errorf("project name %q: use lowercase letters, digits and dashes, up to 40", name)
	}

	inbox, err := c.Inbox(ctx)
	if err != nil {
		return Entry{}, err
	}
	i := slices.IndexFunc(inbox.Projects, func(p Project) bool { return p.Name == name })
	exists := i >= 0
	repo := ""
	if !exists {
		repo = GitHubRepo(ctx, git, dir)
		if harnesses, err = chooseHarnesses(ctx, c, harnesses); err != nil {
			return Entry{}, err
		}
	} else if len(harnesses) == 0 {
		harnesses = inbox.Projects[i].Harnesses
	}

	f, err := ScanFolder(dir)
	if err != nil {
		return Entry{}, err
	}
	if exists {
		fmt.Fprintf(out, "Copy %s (%s) into the workspace of %s, which must be empty.\n", Visible(dir), describe(f), name)
	} else {
		fmt.Fprintf(out, "Copy %s (%s) into a new project, %s.\n", Visible(dir), describe(f), name)
		if repo != "" {
			fmt.Fprintf(out, "Its repository is %s, from the folder's origin.\n", repo)
		} else {
			fmt.Fprintln(out, "The folder has no GitHub remote, so the project has no repository: no pull requests or worktrees.")
		}
	}
	if len(f.Secrets) > 0 {
		fmt.Fprintln(out, "These look like credentials, and agents in the sandbox will be able to read them:")
		listPaths(out, f.Secrets)
	}
	if len(f.Skipped) > 0 {
		fmt.Fprintln(out, "These can't be copied (sockets, pipes, devices) and are left out:")
		listPaths(out, f.Skipped)
	}
	if !yes {
		if !interactive {
			return Entry{}, errors.New("nothing copied: add --yes to copy without being asked")
		}
		fmt.Fprint(out, "Continue? [y/N] ")
		line, _ := bufio.NewReader(in).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			return Entry{}, errors.New("nothing copied")
		}
	}

	if !exists {
		if err := create(ctx, c, name, repo, harnesses, out); err != nil {
			return Entry{}, err
		}
	}
	fmt.Fprintf(out, "Copying %s...\n", size(f.Bytes))
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(WriteArchive(pw, dir)) }()
	err = c.FillWorkspace(ctx, name, pr)
	pr.CloseWithError(errors.New("upload ended")) // stops the archive if the server answered early
	if err != nil {
		return Entry{}, err
	}
	fmt.Fprintf(out, "Copied into %s.\n", name)
	e := Entry{Project: name}
	if len(harnesses) > 0 {
		e.Harness = harnesses[0]
	}
	return e, nil
}

// chooseHarnesses checks the agents asked for, or picks Claude and Codex
// where the server has them, as the app does.
func chooseHarnesses(ctx context.Context, c *Client, asked []string) ([]string, error) {
	m, err := c.Meta(ctx)
	if err != nil {
		return nil, err
	}
	for _, h := range asked {
		if !slices.Contains(m.Harnesses, h) {
			return nil, fmt.Errorf("no agent %q; this Skybreaker has %s", h, strings.Join(m.Harnesses, ", "))
		}
	}
	if len(asked) > 0 {
		return asked, nil
	}
	var out []string
	for _, h := range []string{"claude", "codex"} {
		if slices.Contains(m.Harnesses, h) {
			out = append(out, h)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("choose the agents with --harness")
	}
	return out, nil
}

// create makes the project with the access the app gives a new one, then
// waits for Skybreaker to restart with it.
func create(ctx context.Context, c *Client, name, repo string, harnesses []string, out io.Writer) error {
	caps, err := c.Capabilities(ctx)
	if err != nil {
		return err
	}
	wanted := []string{"github", "tools"}
	for _, h := range harnesses {
		wanted = append(wanted, h+"-login")
	}
	chosen := []string{}
	for _, w := range wanted {
		if slices.Contains(caps, w) {
			chosen = append(chosen, w)
		}
	}
	added, err := c.CreateProject(ctx, NewProject{Name: name, Repo: repo, Harnesses: harnesses, Capabilities: chosen})
	if err != nil {
		return err
	}
	if g := added.GitHub; g != nil && !g.Installed {
		if g.InstallURL != "" {
			fmt.Fprintf(out, "The GitHub App isn't installed on %s yet, so agents can't push. Install it: %s\n", repo, Visible(g.InstallURL))
		} else {
			fmt.Fprintf(out, "Couldn't check GitHub access to %s: %s\n", repo, Visible(g.Error))
		}
	}
	if added.Restart == "when_idle" {
		fmt.Fprintln(out, "Skybreaker restarts to add it once no agent is working. Waiting (Ctrl-C stops waiting; run this again later to copy the folder in)...")
	} else {
		fmt.Fprintln(out, "Restarting Skybreaker to add it...")
	}
	for {
		// Errors are the restart under way.
		if in, err := c.Inbox(ctx); err == nil && slices.ContainsFunc(in.Projects, func(p Project) bool { return p.Name == name }) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollEvery):
		}
	}
}

func describe(f Folder) string {
	files := "files"
	if f.Files == 1 {
		files = "file"
	}
	return fmt.Sprintf("%d %s, %s", f.Files, files, size(f.Bytes))
}

func listPaths(out io.Writer, paths []string) {
	const most = 10
	for _, p := range paths[:min(len(paths), most)] {
		fmt.Fprintln(out, "  "+Visible(p))
	}
	if len(paths) > most {
		fmt.Fprintf(out, "  and %d more\n", len(paths)-most)
	}
}

func size(n int64) string {
	switch {
	case n < 1<<10:
		return fmt.Sprintf("%d bytes", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	case n < 1<<30:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
}
