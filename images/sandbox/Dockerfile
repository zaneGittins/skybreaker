# The Skybreaker sandbox image: one long-lived runsc container per project, with
# the repo, toolchains, tmux, the harness CLIs and an inner dockerd.
# Build on the Skybreaker host (or pull ghcr.io/zanegittins/skybreaker-sandbox):
#   docker build -t skybreaker-sandbox:0.0.1 images/sandbox
# skybreaker-sandbox-update (deploy/) rebuilds it weekly with the newest CLIs and
# promotes it only if they still speak what Skybreaker's chat drivers expect.
FROM ubuntu:24.04

ARG DEBIAN_FRONTEND=noninteractive
ARG PLAYWRIGHT=0

RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl gnupg git tmux tini bash less procps jq ripgrep make \
      build-essential python3 python3-pip python3-venv bubblewrap \
      docker.io docker-compose-v2 iproute2 \
    && rm -rf /var/lib/apt/lists/*

# Node, the current LTS line, for the harness CLIs (Ubuntu's is too old for
# some of them).
RUN curl -fsSL https://deb.nodesource.com/gpgkey/nodesource-repo.gpg.key | gpg --dearmor -o /usr/share/keyrings/nodesource.gpg \
    && echo "deb [signed-by=/usr/share/keyrings/nodesource.gpg] https://deb.nodesource.com/node_24.x nodistro main" > /etc/apt/sources.list.d/nodesource.list \
    && apt-get update && apt-get install -y --no-install-recommends nodejs && rm -rf /var/lib/apt/lists/*

# gh, for pull requests (it talks to api.github.com through the broker).
RUN curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg -o /usr/share/keyrings/githubcli.gpg \
    && echo "deb [signed-by=/usr/share/keyrings/githubcli.gpg] https://cli.github.com/packages stable main" > /etc/apt/sources.list.d/github-cli.list \
    && apt-get update && apt-get install -y --no-install-recommends gh && rm -rf /var/lib/apt/lists/*

# Go, pinned by checksum. Toolchain switches and modules come via the broker.
ARG GO_VERSION=1.27.2
ARG GO_SHA256=ecbadb99091a3f46e31f5f934b068b1864eafa7995211b39eaddf76996045fe5
RUN curl -fsSL -o /tmp/go.tgz https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz \
    && echo "${GO_SHA256}  /tmp/go.tgz" | sha256sum -c - \
    && tar -C /usr/local -xzf /tmp/go.tgz && rm /tmp/go.tgz
ENV PATH=/usr/local/go/bin:/root/go/bin:$PATH

# Harness CLIs, from the builtin harness manifests' install lines.
RUN npm install -g @anthropic-ai/claude-code @openai/codex @qwen-code/qwen-code opencode-ai @earendil-works/pi-coding-agent \
    && npm cache clean --force

RUN if [ "$PLAYWRIGHT" = "1" ]; then npx -y playwright install --with-deps chromium; fi

COPY docker-daemon.json /etc/docker/daemon.json
COPY gitconfig /etc/gitconfig
COPY entrypoint.sh /usr/local/bin/skybreaker-entrypoint
# Ahead of /usr/sbin/runc on PATH, so BuildKit finds it too.
COPY runtime.sh /usr/local/sbin/runc
COPY check-protocols.sh /usr/local/bin/skybreaker-check-protocols
RUN chmod 755 /usr/local/bin/skybreaker-entrypoint /usr/local/bin/skybreaker-check-protocols /usr/local/sbin/runc

# Credentials never live here: git and gh get them added by the broker.
ENV GIT_TERMINAL_PROMPT=0 \
    GH_PROMPT_DISABLED=1 \
    DISABLE_AUTOUPDATER=1 \
    TERM=xterm-256color

WORKDIR /workspace
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/skybreaker-entrypoint"]
