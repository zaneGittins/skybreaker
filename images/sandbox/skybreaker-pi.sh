#!/bin/sh
# pi as Skybreaker runs it: what Skybreaker provides (its MCP server, model
# servers behind broker routes) goes into pi's own config first, then pi,
# with every agent's instructions, set in the app, added to its prompt.
# Usage: skybreaker-pi [--skybreaker-mcp <mcp.json>] [pi's arguments]
if [ "$1" = "--skybreaker-mcp" ]; then
  node /usr/local/lib/skybreaker/pi-config.mjs --skybreaker-mcp "$2" || echo "skybreaker: couldn't update pi's config" >&2
  shift 2
else
  node /usr/local/lib/skybreaker/pi-config.mjs || echo "skybreaker: couldn't update pi's config" >&2
fi
instructions=${SKYBREAKER_ETC:-/etc/skybreaker}/instructions.md
if [ -s "$instructions" ]; then
  set -- --append-system-prompt "$instructions" "$@"
fi
exec pi "$@"
