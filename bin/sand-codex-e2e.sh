#!/usr/bin/env bash
# sand e2e — a REAL headless codex exec agent confined to a sand-slimmed MCP via
# `sand mcp --profile`, wrapping the node fast-mcp.js upstream (echo + secret).
#
# This is sand's analogue of lagom's bin/lagom-codex-poc.sh, but the agent's MCP
# server is `sand mcp` (lagom-go inside, invisible) rather than `lagom serve`.
#
# Policy: keep all by default, DROP `secret`, PIN `echo.token=LOCKED`. Proves, at
# the real-agent level:
#   1. the agent's guarded tool surface = ONLY echo (secret absent)
#   2. a dropped tool (secret) is "not available"
#   3. the pinned arg is injected downstream (upstream sees token=LOCKED the
#      agent never set)
#   4. clean teardown — no `sand mcp` / fast-mcp procs leak after exit
#
# Requires: `sand` on PATH (mage install), codex, node. Exit 0 + "ALL PROOFS GREEN".
set -uo pipefail

SAND="$(command -v sand)"
NODE="$(command -v node)"
# The upstream MCP under test. Defaults to lagom's proven fast-mcp.js (echo +
# secret); override with SAND_E2E_UPSTREAM to point at any stdio MCP server.
FAST="${SAND_E2E_UPSTREAM:-/Users/evanschultz/Documents/Code/hylla/lagom/main/bin/fast-mcp.js}"

if [[ -z "$SAND" ]]; then echo "FAIL: sand not on PATH (run: mage install)"; exit 1; fi
if [[ -z "$NODE" ]]; then echo "FAIL: node not on PATH"; exit 1; fi
if [[ ! -f "$FAST" ]]; then echo "FAIL: fast-mcp.js not found at $FAST"; exit 1; fi

W="$(mktemp -d /tmp/sand-codex-e2e.XXXXXX)"
cleanup() { pkill -f "$W" 2>/dev/null; pkill -f "$FAST" 2>/dev/null; sleep 1; rm -rf "$W"; }
trap cleanup EXIT
mkdir -p "$W/proj"

# Ephemeral per-agent profile: sand-branded server "guarded", wrapping node
# fast-mcp.js, with secret dropped + echo.token pinned. lagom never appears.
cat > "$W/profile.json" <<EOF
{
  "server_name": "guarded",
  "upstream": { "command": "${NODE}", "args": ["${FAST}"] },
  "policy": {
    "default_presence": "keep",
    "tools": {
      "secret": { "presence": "drop" },
      "echo": { "args": { "token": { "pin": "LOCKED" } } }
    }
  }
}
EOF

MCP="mcp_servers.guarded={command=\"${SAND}\",args=[\"mcp\",\"--profile\",\"${W}/profile.json\"],startup_timeout_sec=25,tools={echo={approval_mode=\"approve\"}}}"

echo "--- procs before ---"; pgrep -fl "sand mcp|fast-mcp" || echo "(none)"
echo "=== codex exec (confined to sand-guarded MCP) ==="
OUT="$(codex exec --ephemeral --ignore-user-config --skip-git-repo-check -C "$W/proj" \
  -c 'approval_policy="never"' \
  -c 'sandbox_mode="read-only"' \
  -c 'project_doc_max_bytes=0' \
  -c 'skills.bundled.enabled=false' \
  -c "$MCP" \
  'Output ONLY one compact JSON object, nothing else: {"mcp_tools":[the guarded tool names available to you],"tried_secret":"call the secret tool with key=x; put result text or the EXACT error","called_echo":"call the echo tool with message=hi; put the EXACT result text returned"}' \
  2>"$W/err.log")"
CODEX_EXIT=$?
echo "--- codex exit: $CODEX_EXIT ---"
echo "--- agent output ---"; echo "$OUT"
echo "--- err tail ---"; tail -6 "$W/err.log"

# ---- assertions ----
fail=0
# 3 = pin enforced: upstream echo returns token=LOCKED the agent never set.
if grep -q 'LOCKED' <<<"$OUT"; then echo "PROOF pin-injected: PASS (token=LOCKED in echo result)"; else echo "PROOF pin-injected: FAIL"; fail=1; fi
# 2 = dropped tool unavailable.
if grep -qiE 'not available|unknown tool|no tool|not found|unavailable' <<<"$OUT"; then echo "PROOF secret-dropped: PASS (secret not available)"; else echo "PROOF secret-dropped: FAIL"; fail=1; fi
# 1 = slim surface: echo present, secret absent in the reported tool list.
# Isolate the mcp_tools array so the prompt's "tried_secret" key cannot false-flag.
TOOLS_LIST="$(grep -o '"mcp_tools":[[][^]]*[]]' <<<"$OUT")"
if grep -qi 'echo' <<<"$TOOLS_LIST" && ! grep -qi 'secret' <<<"$TOOLS_LIST"; then echo "PROOF slim-surface: PASS (echo present, secret absent from list)"; else echo "PROOF slim-surface: FAIL (tool list: $TOOLS_LIST)"; fail=1; fi

echo "--- procs AFTER (clean teardown?) ---"; sleep 1; pkill -f "$W" 2>/dev/null
if pgrep -fl "sand mcp|fast-mcp" ; then echo "PROOF no-leak: FAIL (procs still alive)"; fail=1; else echo "PROOF no-leak: PASS (torn down)"; fi

echo "==============================="
if [[ $fail -eq 0 ]]; then echo "ALL PROOFS GREEN"; exit 0; else echo "SOME PROOFS FAILED"; exit 1; fi
