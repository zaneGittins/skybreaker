#!/bin/sh
# pi as Skybreaker runs it: what Skybreaker provides (its MCP server, model
# servers behind broker routes) goes into pi's own config first, then pi,
# with every agent's instructions, set in the app, added to its prompt, and
# the extensions the image ships loaded.
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
# The image's own extensions, in reverse so they load in name order.
extensions=${SKYBREAKER_PI_EXTENSIONS:-/usr/local/lib/skybreaker/pi-extensions}
for f in $(ls -r "$extensions" 2>/dev/null); do
  case $f in *.mjs) set -- -e "$extensions/$f" "$@" ;; esac
done
exec pi "$@"
