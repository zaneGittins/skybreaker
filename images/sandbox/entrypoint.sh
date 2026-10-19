#!/bin/sh
# Sandbox init: trust the broker CA, start the inner dockerd, then idle.
# Sessions arrive later as tmux sessions via `docker exec`.
set -eu

if [ -s /etc/skybreaker/ca.crt ]; then
  cp /etc/skybreaker/ca.crt /usr/local/share/ca-certificates/skybreaker-broker.crt
  update-ca-certificates >/dev/null 2>&1 || echo "skybreaker: update-ca-certificates failed" >&2
fi

# Inner Docker for builds and `docker compose`. Its own iptables setup fails
# under gVisor (no addrtype match), so it runs with iptables off (see
# /etc/docker/daemon.json) and this adds the one rule its containers need:
# NAT behind the sandbox's address, so the broker sees them as this sandbox.
# Like the sandbox they have no DNS: the Docker CLI hands them, and builds,
# the broker as their proxy, by address since they can't resolve "broker".
# Image pulls go through HTTPS_PROXY like everything else.
if command -v dockerd >/dev/null 2>&1; then
  dockerd >/var/log/dockerd.log 2>&1 &
  out=$(ip -o -4 route show default 2>/dev/null | awk '{print $5; exit}')
  if [ -n "$out" ] && command -v iptables-legacy >/dev/null 2>&1; then
    iptables-legacy -t nat -C POSTROUTING -o "$out" -j MASQUERADE 2>/dev/null ||
      iptables-legacy -t nat -A POSTROUTING -o "$out" -j MASQUERADE ||
      echo "skybreaker: inner containers have no NAT" >&2
  fi
  bip=$(awk '$2 == "broker" {print $1; exit}' /etc/hosts)
  if [ -n "$bip" ] && [ -n "${HTTPS_PROXY:-}" ]; then
    proxy=$(printf %s "$HTTPS_PROXY" | sed "s/@broker:/@$bip:/")
    cfg=/root/.docker/config.json
    mkdir -p /root/.docker
    [ -s "$cfg" ] || echo '{}' >"$cfg"
    # Keep whatever else is there (registry logins); replace only the proxy.
    jq --arg p "$proxy" --arg n "localhost,127.0.0.1,$bip" \
      '.proxies.default = {httpProxy: $p, httpsProxy: $p, noProxy: $n}' "$cfg" >"$cfg.tmp" &&
      mv "$cfg.tmp" "$cfg" && chmod 600 "$cfg" ||
      echo "skybreaker: couldn't set the inner Docker's proxy in $cfg" >&2
  fi
fi

# sshd, for VS Code Remote-SSH from your machines (Skybreaker relays to it).
# Its sessions get this container's environment (the broker proxy and the
# rest), which sshd would otherwise drop.
if [ -x /usr/sbin/sshd ]; then
  mkdir -p /run/sshd /run/skybreaker
  [ -s /etc/ssh/ssh_host_ed25519_key ] || ssh-keygen -q -t ed25519 -N '' -f /etc/ssh/ssh_host_ed25519_key
  skybreaker-sshd-env >/run/skybreaker/sshd-env.conf
  chmod 600 /run/skybreaker/sshd-env.conf
  /usr/sbin/sshd -E /var/log/sshd.log || echo "skybreaker: sshd didn't start" >&2
fi

# Tell the control plane the trust store is ready (it waits for this before
# cloning, since git checks TLS against the system bundle).
touch /run/skybreaker-ready

exec sleep infinity
