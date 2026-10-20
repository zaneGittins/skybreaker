#!/bin/sh
# Checks the harness CLIs in this image still have every flag and protocol
# method Skybreaker's chat drivers use. The weekly rebuild
# (skybreaker-sandbox-update) and the image workflow run it before an image
# is promoted:
#   docker run --rm --network none --entrypoint skybreaker-check-protocols IMAGE
set -e
claude --version
codex --version
help=$(claude --help)
for flag in --print --input-format --output-format --include-partial-messages --permission-prompt-tool --model \
            --permission-mode --allow-dangerously-skip-permissions --session-id --resume --fork-session --mcp-config --verbose; do
  printf "%s" "$help" | grep -q -- "$flag" || { echo "claude no longer has $flag" >&2; exit 1; }
done
for mode in acceptEdits bypassPermissions plan; do
  printf "%s" "$help" | grep -q -- "$mode" || { echo "claude no longer has permission mode $mode" >&2; exit 1; }
done
codex resume --help | grep -q -- --no-alt-screen || { echo "codex resume lost --no-alt-screen" >&2; exit 1; }
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
