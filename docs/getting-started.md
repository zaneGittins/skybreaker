# Getting started

## Requirements

- **A Linux host** with Docker. A VM is fine; allow about 4 GB of memory and a few cores per active project.
- **A Tailscale tailnet.** Skybreaker joins it as its own machine, `skybreaker`.
- **A GitHub account or organization** for the repos agents will work on. Skybreaker creates its GitHub App for you (see [GitHub](#github)).

gVisor is required, and the installer adds it if it's missing.

## Install

```sh
curl -fsSL https://github.com/zaneGittins/skybreaker/releases/latest/download/install.sh | sh
```

The installer asks for your tailnet login, a Tailscale auth key (admin console → Settings → Keys) and your first repo. Leave the GitHub App's fields empty to create the App afterwards in Settings, or give the ID and private key of one you made. It then installs gVisor and nftables, writes the config to `/etc/skybreaker`, validates it, installs the systemd units and sandbox image, and starts Skybreaker. If a step fails, fix the cause and run `sudo skybreaker-setup install` again; it picks up where it stopped.

For unattended installs:

```sh
sudo TS_AUTHKEY=tskey-auth-... skybreaker-setup install --yes \
  --login you@github --tailnet tail1234.ts.net --repo you/project
```

Add `--app-id 123456 --app-key app.pem` to use a GitHub App you made yourself.

To install a specific version or prerelease, pass it to the script: `... | SKYBREAKER_VERSION=v0.1.0 sh`.

To install without the wizard, see [Install by hand](manual-install.md).

## GitHub

Open **Settings** and choose **Create GitHub App** (for your account or an organization). GitHub opens with the App filled in; confirm, and you come back to Settings to install it on your repos. Skybreaker keeps the App's key; you never copy a `.pem`.

- **App permissions:** `contents:write`, `pull_requests:write`, `issues:write` and `metadata:read`. Never `workflows`, `administration` or `secrets`.
- **Ruleset on `main`:** block direct pushes, force pushes and deletion, and keep the App off the bypass list.
- **Branches:** the broker only allows pushes to the branch globs in the project's rules (`feature/*`, `fix/*`, `docs/*` and `chore/*` by default), and refuses deletes and tags.

## First steps

Open `https://skybreaker.<your-tailnet>.ts.net`, tap **+** and pick Claude or Codex.

- **Sign in** once per project. For Claude, open the chat's terminal and run `/login`; Codex shows a sign-in code in the chat. Logins persist across restarts.
- **Notifications:** on a phone, add Skybreaker to the home screen, open it from there and tap the bell.
- **In a chat**, `/rename <title>` names it, `/color <colour>` tints it to tell it apart, and `/goal <text>` pins a goal and gives it to the agent. Other slash commands go to the agent as usual.
- **Boards:** each project has one, and agents use it through Skybreaker's MCP tools. To give your own Claude Code access to every board:

  ```sh
  claude mcp add --transport http --scope user skybreaker https://skybreaker.<your-tailnet>.ts.net/mcp
  ```

## From your terminal

The same `skybreaker` binary is a terminal client for your own Linux or Mac. It reaches Skybreaker over your tailnet as you, so there's nothing to sign in to.

```sh
curl -fsSL https://github.com/zaneGittins/skybreaker/releases/latest/download/install-cli.sh | sh
skybreaker              # pick a session and attach; Ctrl-b d brings you back
```

`skybreaker new <project> [harness]` starts a session, `skybreaker ls` lists them, and `skybreaker update` (or the same one-liner) updates the client. Picking a chat continues it in the harness's own terminal UI; your next message from the phone resumes the chat. The client finds the machine named `skybreaker` on your tailnet and remembers it in `~/.skybreaker/config.json`; `skybreaker connect <address>` sets another.

## VS Code

Desktop VS Code can work in a sandbox over SSH, with no Microsoft tunnel. Install the terminal client and VS Code's **Remote - SSH** extension, then on each computer:

```sh
skybreaker vscode setup     # once: makes this computer a key, adds it, writes ~/.ssh/config
skybreaker code <project>   # or Open in VS Code in the project's menu
```

The project is in `/workspace`. A VS Code window is one more shell in the sandbox: it reaches only what the project's policy allows, through the broker. The sandbox can't download VS Code's server itself, so Remote - SSH downloads it on your computer and copies it over; set `"remote.downloadExtensionsLocally": true` in VS Code to install extensions the same way. Remove a computer's key in the same menu; its open windows disconnect.

## Previews

To see what an agent is building, open the project's menu (or **Preview** on a desktop) and pick a port the sandbox is listening on. The page opens on its own address, `https://skybreaker.<your-tailnet>.ts.net:84xx`, apart from Skybreaker. Dev servers must listen on `0.0.0.0` rather than `localhost` (`vite --host 0.0.0.0`, `next dev -H 0.0.0.0`). If you use Tailscale ACLs, allow your login ports 8401-8499 on the `skybreaker` machine as well as 443. Agents can open a preview themselves with Skybreaker's `preview` tool, which puts a link in the chat.

A preview page loads only from its own server, so fonts or scripts from CDNs don't load there. Its cookies are `HttpOnly`, so frameworks that read a CSRF cookie from script (Django's `csrftoken`, `XSRF-TOKEN`) need it sent another way while previewed.

## Configuration

The base is `/etc/skybreaker/skybreaker.yaml` (users, projects, policy) and `secrets.yaml`. Each project lists what its sandbox may reach in `allow` and `capabilities` (bundles); anything else is denied. Restart `skybreaker` after editing. [deploy/skybreaker.example.yaml](../deploy/skybreaker.example.yaml) is a commented example.

Most of it can also be changed in the app, by the users in `admins` (everyone in `users` unless you list them):

- **Settings** creates the GitHub App, adds secrets (each bound to the hosts you list, and never shown again), makes bundles, and adds bundles and hosts to any project. Every change to what a project reaches is shown as the broker compiles it before you save.
- **New project** on the home page creates a project from the bundles there are.

The app saves beside the config file, in `projects.json`, `access.json` and `app-secrets.json`, and only ever adds to it: what the file says is shown read-only, and a name in both is an error. Changes apply when Skybreaker restarts itself, once no agent is working. `app_settings: false` in the config file turns changes in the app off.

## Updating

```sh
sudo skybreaker-setup upgrade     # install the latest release
sudo skybreaker-setup rollback    # return to the previous version
sudo skybreaker-setup status
```

`upgrade` checks your config against the new version before changing anything, and restores the previous version if the new one doesn't stay up. Restarting stops running chats, so it asks first.
