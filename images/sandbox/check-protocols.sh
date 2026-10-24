#!/bin/sh
# Checks the harness CLIs in this image still have every flag and protocol
# method Skybreaker's chat drivers use, and the tools agents count on are
# there. The weekly rebuild
# (skybreaker-sandbox-update) and the image workflow run it before an image
# is promoted:
#   docker run --rm --network none --entrypoint skybreaker-check-protocols IMAGE
set -e
go version # a toolchain for another architecture fails here, not in a sandbox
for t in rg fd jq shellcheck sqlite3 unzip uv mise gh; do
  command -v "$t" >/dev/null || { echo "the image lost $t" >&2; exit 1; }
done
if command -v playwright >/dev/null; then
  shot=$(mktemp -d)/shot.png
  playwright screenshot "data:text/html,<p>ok</p>" "$shot" >/dev/null && [ -s "$shot" ] \
    || { echo "Chromium can't take a screenshot" >&2; exit 1; }
  # An assistant's browser: the Chromium Skybreaker launches, answering on
  # its DevTools pipe (sandbox.BrowserChromium), as a user that owns its
  # profile and with no capabilities, as sandbox.BrowserSpec runs it.
  bin=$(ls -d /opt/ms-playwright/chromium-*/chrome-linux*/chrome 2>/dev/null | head -n 1)
  [ -x "$bin" ] || { echo "no Chromium at /opt/ms-playwright/chromium-*/chrome-linux*/chrome" >&2; exit 1; }
  profile=$(mktemp -d)
  chown 65534:65534 "$profile" && chmod 700 "$profile"
  (printf '{"id":1,"method":"Browser.getVersion"}\000'; sleep 10) \
    | HOME=/tmp timeout 20 setpriv --reuid=65534 --regid=65534 --clear-groups --inh-caps=-all --bounding-set=-all --no-new-privs \
      sh -c 'exec "$0" --headless --no-sandbox --remote-debugging-pipe --user-data-dir="$1" 3<&0 4>&1 >/dev/null 2>&1' "$bin" "$profile" \
    | tr '\000' '\n' | grep -q '"product"' || { echo "Chromium doesn't answer on its DevTools pipe as a user without capabilities" >&2; exit 1; }
  rm -rf "$profile"
fi
claude --version
codex --version
pi --version
# has HELP FLAG: the help lists the flag itself, not one it starts.
has() { printf "%s" "$1" | grep -qE -- "(^|[ ,])$2([ ,=]|\$)"; }
pi_help=$(pi --help)
for flag in --print --mode --model --session-id --session --fork; do
  has "$pi_help" "$flag" || { echo "pi no longer has $flag" >&2; exit 1; }
done
# The chat driver's RPC mode, which answers state offline.
pi_dir=$(mktemp -d)
printf '{"type":"get_state"}\n' | PI_OFFLINE=1 PI_SKIP_VERSION_CHECK=1 PI_TELEMETRY=0 PI_CODING_AGENT_DIR="$pi_dir" pi --mode rpc --no-session 2>/dev/null \
  | grep -q '"command":"get_state","success":true' || { echo "pi no longer answers get_state in RPC mode" >&2; exit 1; }
rm -rf "$pi_dir"
# The chat driver's command line and env, in each permission mode it uses.
# claude parses it before reading an empty stdin, offline, so a flag it lost
# fails here; --help can't tell, as it leaves out the SDK's
# --permission-prompt-tool. IS_SANDBOX=1 lets root skip permissions.
for mode in default acceptEdits bypassPermissions plan; do
  IS_SANDBOX=1 claude -p --input-format stream-json --output-format stream-json --verbose --include-partial-messages \
    --permission-prompt-tool stdio --permission-mode "$mode" --allow-dangerously-skip-permissions \
    --mcp-config '{"mcpServers":{}}' --model=haiku </dev/null >/dev/null \
    || { echo "claude no longer takes the chat driver's command line in mode $mode" >&2; exit 1; }
done
# What a resume, a fork and a scheduled run add.
help=$(claude --help)
for flag in --session-id --resume --fork-session --bare --permission-prompts --max-budget-usd --allowedTools; do
  has "$help" "$flag" || { echo "claude no longer has $flag" >&2; exit 1; }
done
codex resume --help | grep -q -- --no-alt-screen || { echo "codex resume lost --no-alt-screen" >&2; exit 1; }
# Terminals, resumes and runs get Skybreaker's MCP server as a -c override,
# and runs approve its tools, since codex exec can't ask.
for cmd in "" exec resume; do
  codex $cmd --help | grep -q -- "--config <key=value>" || { echo "codex ${cmd:-(top level)} lost -c" >&2; exit 1; }
done
codex -c 'mcp_servers.skybreaker={url="http://localhost", default_tools_approval_mode="approve"}' mcp get skybreaker --json >/dev/null \
  || { echo "codex no longer takes Skybreaker's MCP server as an override" >&2; exit 1; }
codex -c 'mcp_servers.skybreaker={url="http://localhost", default_tools_approval_mode="none-such"}' mcp get skybreaker 2>&1 | grep -q '`approve`' \
  || { echo "codex no longer has default_tools_approval_mode approve" >&2; exit 1; }
d=$(mktemp -d)
codex app-server generate-json-schema --out "$d" >/dev/null
need() {
  for m in $2; do
    grep -q "\"$m\"" "$d/$1" || { echo "codex app-server no longer has $m in $1" >&2; exit 1; }
  done
}
need ClientRequest.json "initialize thread/start thread/resume thread/fork thread/compact/start turn/start turn/steer turn/interrupt account/read account/login/start model/list"
need ServerRequest.json "item/commandExecution/requestApproval item/fileChange/requestApproval item/tool/requestUserInput"
need ServerNotification.json "turn/started turn/completed item/started item/completed item/agentMessage/delta account/login/completed thread/tokenUsage/updated"
grep -q chatgptDeviceCode "$d/v2/LoginAccountParams.json" || { echo "codex lost device-code sign-in" >&2; exit 1; }
rm -rf "$d"
echo "protocols ok"
