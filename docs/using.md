# Using Skybreaker

## Chats

**Branches.** Each chat works in its own copy of the repo, on branch `skybreaker/<session>`, so agents working side by side never touch each other's files. Untick **On its own branch** in the picker to work in the project's shared `/workspace` instead. A fresh copy has no ignored files such as `node_modules` or `.env`, so the agent installs what it needs.

**Shipping.** **Changes** shows a chat's diff against its base branch, after any screenshots the agent showed of its work. **Ship it** has the agent name the branch, push it and open a pull request.

**After shipping.** Skybreaker checks the pull request every two minutes. When its checks finish failing, or someone with write access to the repo comments, the idle chat gets a message about it and the agent carries on. After 3 of these without a reply from you, it stops and notifies you.

**Ending.** Click a finished chat's check, swipe it left on a phone, or pick **End chat** in its menu. A chat whose pull request was merged ends by itself once you've read it. An ended chat's branch is kept, and sending it a message picks up where it stopped.

**Commands.**

| Command | Does |
|---|---|
| `/rename <title>` | Renames the chat |
| `/color <colour>` | Tints the chat to tell it apart |
| `/goal <text>` | Pins a goal and gives it to the agent |
| `/compact` | Summarises the conversation to free up context |
| `/fork` | Copies the chat into a new one, on its own branch |
| `/clear` | Ends the chat and starts a fresh one on the same branch |
| `/prompt <name>` | Sends a prompt saved in Settings |
| `/model <name>` | Switches the chat's model |
| `/stop`, `/exit` | Stops the agent |

Anything else goes to the agent as usual.

**Attachments.** Paste or drop files on a desktop, or tap the paperclip. Up to 10 files per message, 10 MB each. The agent finds them under `/root/.skybreaker/attachments/<session>`.

**Context.** The ring at the top right fills as the chat uses up the model's context: yellow past 60%, red past 85%. Tap it to compact or start fresh.

## Previews

To see what an agent is building, pick **Preview** in the project's menu and choose a port. Agents can also open one with the `preview` tool.

- Dev servers must listen on `0.0.0.0`, not `localhost` (`vite --host 0.0.0.0`, `next dev -H 0.0.0.0`).
- Previews open on `https://skybreaker.<your-tailnet>.ts.net:84xx`. With Tailscale ACLs, allow ports 8401-8499 as well as 443.
- Pages load only from their own server, so fonts and scripts from CDNs don't load.

## Boards

Each project has a board of bugs, features and chores. Agents pick up, claim and hand back items through Skybreaker's MCP tools. To use every board from your own Claude Code:

```sh
claude mcp add --transport http --scope user skybreaker https://skybreaker.<your-tailnet>.ts.net/mcp
```

## Assistants

An assistant is an agent for everything that isn't a repo, with memory you approve, scheduled routines and a browser of its own. See [Assistants](assistants.md) to make one and put it to work.

A code project made in the app picks its agents the same way, under **Agents** in its menu. Admins change these, and a change applies when Skybreaker restarts.

## Terminal

The `skybreaker` client attaches to sessions in each agent's own terminal UI, over your tailnet, with nothing to sign in to. [Install it](getting-started.md#optional) on Linux or macOS.

| Command | Does |
|---|---|
| `skybreaker` | Pick a session and attach (`Ctrl-b d` to detach) |
| `skybreaker new <project> [agent]` | Start a session |
| `skybreaker new-project <folder> [name]` | Make a project from a local folder (admins) |
| `skybreaker ls` | List sessions |
| `skybreaker update` | Update the client |
| `skybreaker connect <address>` | Use a Skybreaker not named `skybreaker` |

A chat continued here carries on in the phone and web app with your next message there.

`new-project` copies everything in the folder into the new project's `/workspace`, dot files and `.git` included, then attaches. A folder whose own `origin` is on GitHub gets that repository; one without has none, so no pull requests or worktrees. It lists anything that looks like a credential (`.env`, `*.pem`, `*.key`) and asks before copying; agents in the sandbox can read whatever is copied. `--harness claude,codex` picks the agents and `--yes` skips the question.

## VS Code

Work in a sandbox from desktop VS Code over SSH. Install the terminal client and the **Remote - SSH** extension, then:

```sh
skybreaker vscode setup     # once per computer
skybreaker code <project>   # or Open in VS Code in the project's menu
```

The repo is in `/workspace`; chats on their own branch are in `/skybreaker/worktrees/<session>`. Set `"remote.downloadExtensionsLocally": true` so extensions install too, since the sandbox can't download them itself.

## Tools in the sandbox

The sandbox has git, gh, Go, Node, Python, Docker, ripgrep, jq, sqlite3 and Chromium with Playwright. Agents install anything else under `/root`, which survives restarts:

```sh
mise use -g terraform doctl     # most CLIs
uv tool install ansible-core    # Python tools
```

For setup every sandbox needs, commit `.skybreaker/setup.sh` to the repo. It runs as root in `/workspace` when a new sandbox starts, and chats wait for it to finish. Its log is `/var/log/skybreaker-setup.log`.
