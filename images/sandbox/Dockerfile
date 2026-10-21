# The Skybreaker sandbox image: one long-lived runsc container per project, with
# the repo, toolchains, tmux, the harness CLIs, Chromium and an inner dockerd.
# Build on the Skybreaker host (or pull ghcr.io/zanegittins/skybreaker-sandbox):
#   docker build -t skybreaker-sandbox:0.0.1 images/sandbox
# skybreaker-sandbox-update (deploy/) rebuilds it weekly with the newest CLIs and
# promotes it only if they still speak what Skybreaker's chat drivers expect.
ARG UV_VERSION=0.13.4
FROM ghcr.io/astral-sh/uv:${UV_VERSION} AS uv

FROM ubuntu:24.04

ARG DEBIAN_FRONTEND=noninteractive
# PLAYWRIGHT=0 leaves the browser out, for a smaller image.
ARG PLAYWRIGHT=1

RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl gnupg git tmux tini bash less procps jq ripgrep make \
      build-essential python3 python3-pip python3-venv bubblewrap \
      docker.io docker-compose-v2 iproute2 openssh-server \
      fd-find file tree unzip zip xz-utils sqlite3 shellcheck \
    && ln -s /usr/bin/fdfind /usr/local/bin/fd \
    && rm -rf /var/lib/apt/lists/* /etc/ssh/ssh_host_*

# Node, the current LTS line, for the harness CLIs (Ubuntu's is too old for
# some of them).
RUN curl -fsSL https://deb.nodesource.com/gpgkey/nodesource-repo.gpg.key | gpg --dearmor -o /usr/share/keyrings/nodesource.gpg \
    && echo "deb [signed-by=/usr/share/keyrings/nodesource.gpg] https://deb.nodesource.com/node_24.x nodistro main" > /etc/apt/sources.list.d/nodesource.list \
    && apt-get update && apt-get install -y --no-install-recommends nodejs && rm -rf /var/lib/apt/lists/*

# gh, for pull requests (it talks to api.github.com through the broker).
RUN curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg -o /usr/share/keyrings/githubcli.gpg \
    && echo "deb [signed-by=/usr/share/keyrings/githubcli.gpg] https://cli.github.com/packages stable main" > /etc/apt/sources.list.d/github-cli.list \
    && apt-get update && apt-get install -y --no-install-recommends gh && rm -rf /var/lib/apt/lists/*

# uv, for the Python tools agents install themselves (ansible, ...), under
# /root, which outlives the container.
COPY --from=uv /uv /uvx /usr/local/bin/

# Go, pinned by checksum, for the image's own architecture (an arm64 host
# builds an arm64 image). Toolchain switches and modules come via the broker.
ARG GO_VERSION=1.27.2
ARG GO_SHA256_amd64=ecbadb99091a3f46e31f5f934b068b1864eafa7995211b39eaddf76996045fe5
ARG GO_SHA256_arm64=94f3e30b8e374bc285e7dadc11e0865726b9bc6e85b841ccceaabc0214c6b7c8
ARG TARGETARCH
RUN arch="${TARGETARCH:-$(dpkg --print-architecture)}" \
    && case "$arch" in amd64) sum="$GO_SHA256_amd64" ;; arm64) sum="$GO_SHA256_arm64" ;; *) echo "no Go for $arch" >&2; exit 1 ;; esac \
    && curl -fsSL -o /tmp/go.tgz "https://go.dev/dl/go${GO_VERSION}.linux-${arch}.tar.gz" \
    && echo "${sum}  /tmp/go.tgz" | sha256sum -c - \
    && tar -C /usr/local -xzf /tmp/go.tgz && rm /tmp/go.tgz
# What agents install goes last, so it never shadows the harnesses' own.
ENV PATH=/usr/local/go/bin:/root/go/bin:$PATH:/root/.local/bin:/root/.local/share/mise/shims

# Harness CLIs, from the builtin harness manifests' install lines. Retried:
# a CLI can resolve a dependency version (an AWS SDK package, say) while its
# publisher is still uploading it, and npm reports that as ETARGET.
RUN for try in 1 2 3; do \
      npm install -g @anthropic-ai/claude-code @openai/codex @earendil-works/pi-coding-agent && break; \
      [ "$try" = 3 ] && exit 1; \
      echo "npm install failed; retrying in 60s"; sleep 60; \
    done \
    && npm cache clean --force

# mise, for the other tools agents install themselves (terraform, doctl,
# ...), likewise under /root. npm has it for every architecture.
RUN npm install -g @jdxcode/mise && npm cache clean --force

# Chromium and its libraries, for screenshots and browser tests. Outside
# /root, which the home directory mounts over. Projects pinning another
# Playwright fetch their own build from its CDN.
ENV PLAYWRIGHT_BROWSERS_PATH=/opt/ms-playwright
RUN if [ "$PLAYWRIGHT" = "1" ]; then \
      npm install -g playwright && playwright install --with-deps chromium \
      && npm cache clean --force && rm -rf /var/lib/apt/lists/*; \
    fi

COPY docker-daemon.json /etc/docker/daemon.json
COPY gitconfig /etc/gitconfig
COPY entrypoint.sh /usr/local/bin/skybreaker-entrypoint
# Ahead of /usr/sbin/runc on PATH, so BuildKit finds it too.
COPY runtime.sh /usr/local/sbin/runc
COPY check-protocols.sh /usr/local/bin/skybreaker-check-protocols
# sshd for VS Code Remote-SSH. Host keys are made per container, at start.
COPY sshd_config /etc/ssh/sshd_config
COPY authorized-keys.sh /usr/local/sbin/skybreaker-authorized-keys
COPY sshd-env.sh /usr/local/sbin/skybreaker-sshd-env
# pi, with Skybreaker's MCP server and model servers merged into its config.
COPY skybreaker-pi.sh /usr/local/bin/skybreaker-pi
COPY pi-config.mjs /usr/local/lib/skybreaker/pi-config.mjs
RUN chmod 755 /usr/local/bin/skybreaker-entrypoint /usr/local/bin/skybreaker-check-protocols /usr/local/sbin/runc \
      /usr/local/sbin/skybreaker-authorized-keys /usr/local/sbin/skybreaker-sshd-env /usr/local/bin/skybreaker-pi

# Credentials never live here: git and gh get them added by the broker.
ENV GIT_TERMINAL_PROMPT=0 \
    GH_PROMPT_DISABLED=1 \
    DISABLE_AUTOUPDATER=1 \
    MISE_USE_VERSIONS_HOST=false \
    TERM=xterm-256color

WORKDIR /workspace
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/skybreaker-entrypoint"]
