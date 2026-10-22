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
printf '{"type":"get_state"}\n' | PI_OFFLINE=1 PI_CODING_AGENT_DIR="$pi_dir" pi --mode rpc --no-session 2>/dev/null \
  | grep -q '"command":"get_state","success":true' || { echo "pi no longer answers get_state in RPC mode" >&2; exit 1; }
rm -rf "$pi_dir"
help=$(claude --help)
for flag in --print --input-format --output-format --include-partial-messages --permission-prompt-tool --model \
            --permission-mode --allow-dangerously-skip-permissions --session-id --resume --fork-session --mcp-config --verbose; do
  printf "%s" "$help" | grep -q -- "$flag" || { echo "claude no longer has $flag" >&2; exit 1; }
done
for mode in acceptEdits bypassPermissions plan; do
  printf "%s" "$help" | grep -q -- "$mode" || { echo "claude no longer has permission mode $mode" >&2; exit 1; }
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
