#!/usr/bin/env bash
#
# bin/agent-dispatch.sh — chain-mode subagent dispatcher
#
# Usage:
#   echo "<task prompt>" | ./bin/agent-dispatch.sh --role ta-go-builder
#   ./bin/agent-dispatch.sh --role ta-go-builder --prompt-file ./task.md
#   ./bin/agent-dispatch.sh --role ta-go-qa-falsification --cwd $(pwd) --dry-run
#   ./bin/agent-dispatch.sh --role ta-go-builder --model qwen3-coder:30b
#
# The dispatcher walks the role's fallback chain (defined in
# .claude/agent-chains.sh). Each tier:
#   1. Preflight check (backend reachable + model available + auth).
#   2. For ollama-* tiers: acquire an mkdir-protected slot, wait up to wait_max.
#      For codex-exec / claude-native: no slot — dispatch immediately.
#   3. Dispatch. On success: write response to stdout, log served_by, exit 0.
#   4. On preflight fail, slot timeout, or non-zero dispatch exit: advance.
# All tiers exhausted → "CHAIN FAILED" to stderr, exit 1.
#
# Lock policy: only Ollama tiers (local + cloud) use lock dirs. External APIs
# (Codex, Anthropic) self-rate-limit via 429/401 responses — we listen to their
# exit codes instead of trying to predict their capacity.
#
# Locking primitive: `mkdir` is atomic on POSIX filesystems and ships on every
# Unix (no `flock(1)` dependency — macOS doesn't have it). Stale-lock cleanup
# uses PID files inside each lock dir + kill -0 liveness check.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHAINS_FILE="${REPO_ROOT}/.claude/agent-chains.sh"
PERSONA_DIR="${REPO_ROOT}/.claude/agents"
LOCK_DIR="/tmp/agent-dispatch"

ROLE=""
CWD="${PWD}"
PROMPT_FILE=""
PROMPT_STRING=""
DRY_RUN=0
MODEL_OVERRIDE=""
HELD_LOCK=""

usage() {
  cat >&2 <<'USAGE'
Usage: agent-dispatch.sh --role <role-name> [options]

Walks the role's fallback chain (.claude/agent-chains.sh) until one tier
succeeds. Writes the chosen backend's response to stdout, dispatch diagnostic
to stderr. Exit 0 on success (any tier), exit 1 if all tiers exhausted.

Options:
  --role <name>          Required. e.g. ta-go-builder, ta-go-qa-falsification
  --cwd <abs-path>       Working directory for the dispatched agent (default: PWD)
  --prompt <string>      Inline task prompt string. Highest precedence: wins over
                         --prompt-file and stdin. Use when the orchestrator wants
                         to avoid `echo "..." | dispatcher` (each new echo + pipe
                         triggers a fresh permission prompt; --prompt does not).
  --prompt-file <path>   Read task prompt from this file (default: stdin)
  --model <tag>          Override tier-1 model (later tiers unchanged).
                         For atomic 1-4 block builder droplets per cascade
                         methodology, the default tier-1 is already a small
                         coder model; override only when a larger model is
                         needed for an unusual case.
  --dry-run              Print the tier-1 command that would be executed; exit.
                         Skips slot acquisition so you can dry-run while the
                         system is under load.
  -h | --help            This message

The task prompt is read from stdin unless --prompt-file is given.

Stderr diagnostic format:
  [disp] tier N: backend model
  [disp]   SKIP — <reason>            (preflight, slot timeout, dispatch error)
  [disp] served_by=backend:model      (tier 1 succeeded)
  [disp] served_by=X originally_requested=Y FALLBACK   (tier N>1 succeeded)
  [disp] CHAIN FAILED: N tiers exhausted for role=...   (all tiers failed)

Stdout output format (orchestrator parses based on stderr served_by line):
  ollama-local / claude-native → Claude Code JSON envelope:
    {"type":"result", "result":"<text>", "usage":{...}, ...}
  codex-exec → raw codex stream output. Reply is the contiguous
    non-marker block before the "tokens used" footer.

Cost-reporting caveat: total_cost_usd in the JSON envelope is reported
by Claude Code based on the requested model name. For ollama-routed tiers
(ANTHROPIC_BASE_URL redirected to localhost), the actual Anthropic bill is
$0 — the local GPU served the request. Trust served_by= over total_cost_usd.
USAGE
  exit 2
}

# --- Argument parsing ------------------------------------------------------

while [[ $# -gt 0 ]]; do
  case "$1" in
    --role)         ROLE="$2";           shift 2 ;;
    --cwd)          CWD="$2";            shift 2 ;;
    --prompt)       PROMPT_STRING="$2";  shift 2 ;;
    --prompt-file)  PROMPT_FILE="$2";    shift 2 ;;
    --model)        MODEL_OVERRIDE="$2"; shift 2 ;;
    --dry-run)      DRY_RUN=1;           shift ;;
    -h|--help)      usage ;;
    *)              echo "Unknown arg: $1" >&2; usage ;;
  esac
done

[[ -z "${ROLE}" ]] && { echo "Missing --role" >&2; usage; }
[[ ! -f "${CHAINS_FILE}" ]] && { echo "Chains file missing: ${CHAINS_FILE}" >&2; exit 1; }
mkdir -p "${LOCK_DIR}"

# Ensure any held lock is released on any exit path.
cleanup() {
  if [[ -n "${HELD_LOCK}" && -d "${HELD_LOCK}" ]]; then
    rm -rf "${HELD_LOCK}" 2>/dev/null || true
  fi
}
trap cleanup EXIT INT TERM

# shellcheck source=/dev/null
source "${CHAINS_FILE}"

TIER_TABLE="$(emit_chain_for_role "${ROLE}")"
[[ -z "${TIER_TABLE}" ]] && { echo "No chain defined for role: ${ROLE}" >&2; exit 1; }

PERSONA_FILE="${PERSONA_DIR}/${ROLE}.md"
[[ ! -f "${PERSONA_FILE}" ]] && { echo "Persona file missing: ${PERSONA_FILE}" >&2; exit 1; }

# Parse YAML frontmatter from persona.
PERSONA_BODY="$(awk '
  BEGIN { state = "pre" }
  /^---$/ {
    if      (state == "pre")   { state = "in_fm"; next }
    else if (state == "in_fm") { state = "post";  next }
  }
  state == "post" { print }
' "${PERSONA_FILE}")"

TOOLS_LINE="$(awk '
  BEGIN { state = "pre" }
  /^---$/ {
    if      (state == "pre")   { state = "in_fm"; next }
    else if (state == "in_fm") { exit }
  }
  state == "in_fm" && /^tools:/ {
    sub(/^tools:[[:space:]]*/, "")
    print
    exit
  }
' "${PERSONA_FILE}")"

# Read task prompt. Precedence: --prompt (inline string) > --prompt-file > stdin.
# --prompt is the orchestrator-friendly path: avoids `echo "..." | dispatcher`
# which triggers a fresh permission prompt on every distinct compound command.
if [[ -n "${PROMPT_STRING}" ]]; then
  TASK_PROMPT="${PROMPT_STRING}"
elif [[ -n "${PROMPT_FILE}" ]]; then
  [[ ! -f "${PROMPT_FILE}" ]] && { echo "Prompt file missing: ${PROMPT_FILE}" >&2; exit 1; }
  TASK_PROMPT="$(cat "${PROMPT_FILE}")"
else
  TASK_PROMPT="$(cat)"
fi

ANTI_RECURSION='

---

DISPATCH CONTEXT: You are the '"${ROLE}"' agent, dispatched via bin/agent-dispatch.sh. Execute the task below directly using YOUR role-appropriate tools (the orchestrator restricts them per the persona'"'"'s `tools:` allowlist). Do NOT call agent-dispatch.sh. Do NOT use the Agent tool to spawn other roles. Do NOT route the task elsewhere. You ARE the role. The orchestrator coordinates further dispatches.'

cd "${CWD}"

# --- Preflight per backend -------------------------------------------------

PREFLIGHT_REASON=""

preflight() {
  local backend=$1 model=$2
  PREFLIGHT_REASON=""

  case "$backend" in
    ollama-local|ollama-cloud)
      if ! curl -sf --max-time 3 http://localhost:11434/api/version >/dev/null; then
        PREFLIGHT_REASON="ollama daemon unreachable at localhost:11434"
        return 1
      fi
      if [[ "$backend" == "ollama-local" ]]; then
        if ! ollama list 2>/dev/null | awk 'NR>1 {print $1}' | grep -qx "$model"; then
          PREFLIGHT_REASON="model $model not pulled locally"
          return 1
        fi
      fi
      if [[ "$backend" == "ollama-cloud" ]]; then
        if [[ -z "${OLLAMA_API_KEY:-}" ]]; then
          PREFLIGHT_REASON="OLLAMA_API_KEY unset; cloud auth missing"
          return 1
        fi
      fi
      ;;
    codex-exec)
      if ! command -v codex >/dev/null 2>&1; then
        PREFLIGHT_REASON="codex CLI not on PATH"
        return 1
      fi
      ;;
    claude-native)
      if ! command -v claude >/dev/null 2>&1; then
        PREFLIGHT_REASON="claude CLI not on PATH"
        return 1
      fi
      ;;
    *)
      PREFLIGHT_REASON="unknown backend: $backend"
      return 1
      ;;
  esac
  return 0
}

# --- Ollama slot acquisition (mkdir-based, portable) ----------------------

is_lock_stale() {
  local lock=$1
  [[ -f "${lock}/pid" ]] || return 1   # no pid file → can't tell, assume not stale
  local pid
  pid="$(cat "${lock}/pid" 2>/dev/null || true)"
  [[ -z "$pid" ]] && return 1
  # kill -0 succeeds if the pid exists; fails if it's gone.
  if kill -0 "$pid" 2>/dev/null; then
    return 1   # still alive, not stale
  fi
  return 0     # stale
}

acquire_ollama_slot() {
  local backend=$1 slots=$2 wait_max=$3
  local i lock deadline

  [[ -z "$slots" || "$slots" -le 0 ]] && { echo ""; return 0; }

  deadline=$(($(date +%s) + wait_max))
  while (( $(date +%s) <= deadline )); do
    for ((i=1; i<=slots; i++)); do
      lock="${LOCK_DIR}/${backend}.${i}.lock"

      # Reap stale locks (process died holding the lock).
      if [[ -d "$lock" ]] && is_lock_stale "$lock"; then
        rm -rf "$lock" 2>/dev/null || true
      fi

      # Atomic claim: mkdir succeeds for exactly one process.
      if mkdir "$lock" 2>/dev/null; then
        echo $$ > "${lock}/pid"
        echo "$lock"
        return 0
      fi
    done
    sleep 0.5
  done
  return 1
}

release_ollama_slot() {
  local lock=$1
  [[ -n "$lock" && -d "$lock" ]] && rm -rf "$lock" 2>/dev/null || true
}

# --- Per-backend dispatch -------------------------------------------------

dispatch_ollama() {
  local model=$1
  # Role-appropriate sandbox for ollama-routed inference:
  #   --bare                  — skip CLAUDE.md auto-discovery, hooks, LSP, auto-memory,
  #                             keychain, plugin sync. Saves ~30K input tokens per call.
  #                             Strict ANTHROPIC_API_KEY auth (we set it to "ollama").
  #   --mcp-config <project>/.mcp.json — explicitly load the project's MCP servers
  #                             (ta, hylla, context7). --bare suppresses auto-discovery,
  #                             so we re-enable them here for the role's MCP needs.
  #   --allowedTools "<list>"  — RESTRICT tools to the persona's `tools:` line.
  #                             Each role gets a role-appropriate allowlist (planners
  #                             get ta create/update + read tools; builders get
  #                             Edit/Write/Read/Bash + ta-read; QA gets read-only +
  #                             git diff). The persona file IS the sandbox spec.
  #   --append-system-prompt   — persona body + ANTI_RECURSION suffix.
  # The spawned model DOES have role-appropriate tool access. The persona's
  # `tools:` allowlist is what limits what it can do, not a blanket disable.
  local cmd=( claude -p
    --bare
    --model "${model}"
    --output-format json
    --no-session-persistence
    --append-system-prompt "${PERSONA_BODY}${ANTI_RECURSION}"
  )
  [[ -f "${REPO_ROOT}/.mcp.json" ]] && cmd+=( --mcp-config "${REPO_ROOT}/.mcp.json" )
  [[ -n "${TOOLS_LINE}" ]] && cmd+=( --allowedTools "${TOOLS_LINE}" )

  if [[ "${DRY_RUN}" -eq 1 ]]; then
    printf '  ANTHROPIC_BASE_URL=http://localhost:11434 ANTHROPIC_API_KEY=ollama \\\n' >&2
    printf '  cd %q && \\\n' "${CWD}" >&2
    printf '  ' >&2
    printf '%q ' "${cmd[@]}" >&2
    printf '\n  <<< <stdin task prompt>\n' >&2
    return 0
  fi

  # ANTHROPIC_API_KEY (not AUTH_TOKEN) — --bare enforces strict key-based auth.
  # Ollama accepts any value and doesn't validate.
  ANTHROPIC_BASE_URL="http://localhost:11434" \
  ANTHROPIC_API_KEY="ollama" \
    "${cmd[@]}" <<<"${TASK_PROMPT}"
}

dispatch_codex() {
  local model=$1 opts=$2
  local full_prompt="${PERSONA_BODY}${ANTI_RECURSION}

---

${TASK_PROMPT}"

  local cmd=( codex exec
    --ephemeral
    --ignore-rules
    --skip-git-repo-check
    -C "${CWD}"
    -m "${model}"
  )

  # Codex MCP injection. Background:
  #   (1) --ignore-user-config NOT set — codex loads ~/.codex/config.toml
  #       + overlays our -c overrides.
  #   (2) ta is NOT in user codex config (Claude-Code-only MCP today). Inject
  #       ta server definition + per-tool approval explicitly.
  #   (3) Approval syntax: per-tool `approval_mode = "approve"` is the
  #       form that ACTUALLY pre-approves under --ephemeral (approval:
  #       never). Server-level `default_tools_approval_mode = "auto"` is
  #       documented but NOT implemented for raw mcp_servers per upstream
  #       issue #16501 — verified empirically: hylla calls still cancel
  #       with that setting. Per-tool form mirrors user's working gopls
  #       config in ~/.codex/config.toml and is the pattern codex docs
  #       show. "approve" in codex's per-tool config = pre-approved,
  #       NOT requires-TTY (the docs are inconsistent; empirical
  #       behavior confirms pre-approval).
  local ta_tools_toml="" tool
  for tool in get update list_sections search schema create delete move init; do
    [[ -n "${ta_tools_toml}" ]] && ta_tools_toml+=","
    ta_tools_toml+="${tool}={approval_mode=\"approve\"}"
  done
  cmd+=( -c "mcp_servers.ta={command=\"ta\",args=[\"--project\",\"${CWD}\"],tools={${ta_tools_toml}}}" )

  # Hylla MCP injection (stdio transport — hylla CLI added stdio support
  # 2026-05-21). Tool names are the canonical names hylla MCP server
  # registers (queried directly via tools/list JSON-RPC — see hylla
  # source). All read-only; excludes write tools (hylla.config.refresh,
  # hylla.ingest). Per-tool quoted keys required because dots inside an
  # inline-table key otherwise create nested structure.
  local hylla_tools_toml="" hylla_tool
  for hylla_tool in hylla.artifact.list hylla.artifact.metadata hylla.artifact.overview hylla.dql.query hylla.graph.list hylla.graph.nav hylla.node.full hylla.refs.find hylla.run.get hylla.run.list hylla.search hylla.search.keyword hylla.search.vector hylla.task.get; do
    [[ -n "${hylla_tools_toml}" ]] && hylla_tools_toml+=","
    hylla_tools_toml+="\"${hylla_tool}\"={approval_mode=\"approve\"}"
  done
  cmd+=( -c "mcp_servers.hylla={command=\"/Users/evanschultz/go/bin/hylla\",args=[\"mcp\"],tools={${hylla_tools_toml}}}" )

  if [[ -n "${opts}" ]]; then
    # shellcheck disable=SC2206  # intentional word-split on opts
    local opt_arr=( ${opts} )
    cmd+=( "${opt_arr[@]}" )
  fi

  if [[ "${DRY_RUN}" -eq 1 ]]; then
    printf '  ' >&2
    printf '%q ' "${cmd[@]}" >&2
    printf '\n  <<< <persona + task>\n' >&2
    return 0
  fi

  "${cmd[@]}" <<<"${full_prompt}"
}

dispatch_claude_native() {
  local model=$1
  # --bare + --mcp-config mirror dispatch_ollama: the persona body IS the
  # role's complete spec, so we skip CLAUDE.md auto-discovery, hooks, plugin
  # context, and auto-memory (saves ~30-50K input tokens per call). The
  # persona body in --append-system-prompt + the tools allowlist + the
  # project's MCP servers cover everything the role needs.
  local cmd=( claude -p
    --bare
    --model "${model}"
    --output-format json
    --no-session-persistence
    --append-system-prompt "${PERSONA_BODY}${ANTI_RECURSION}"
  )
  [[ -f "${REPO_ROOT}/.mcp.json" ]] && cmd+=( --mcp-config "${REPO_ROOT}/.mcp.json" )
  [[ -n "${TOOLS_LINE}" ]] && cmd+=( --allowedTools "${TOOLS_LINE}" )

  if [[ "${DRY_RUN}" -eq 1 ]]; then
    printf '  (env -u ANTHROPIC_BASE_URL -u ANTHROPIC_AUTH_TOKEN) \\\n' >&2
    printf '  cd %q && \\\n' "${CWD}" >&2
    printf '  ' >&2
    printf '%q ' "${cmd[@]}" >&2
    printf '\n  <<< <stdin task prompt>\n' >&2
    return 0
  fi

  # Subshell unsets Ollama redirect vars so claude-native hits api.anthropic.com.
  (
    unset ANTHROPIC_BASE_URL ANTHROPIC_AUTH_TOKEN
    "${cmd[@]}" <<<"${TASK_PROMPT}"
  )
}

# --- Chain walk -----------------------------------------------------------

PRIMARY_BACKEND=""
PRIMARY_MODEL=""
TIER_NUM=0

while IFS='|' read -r backend model opts wait_max slots; do
  [[ -z "$backend" || "$backend" =~ ^[[:space:]]*# ]] && continue

  TIER_NUM=$((TIER_NUM + 1))

  if [[ "$TIER_NUM" -eq 1 && -n "${MODEL_OVERRIDE}" ]]; then
    model="${MODEL_OVERRIDE}"
  fi

  if [[ -z "$PRIMARY_BACKEND" ]]; then
    PRIMARY_BACKEND="$backend"
    PRIMARY_MODEL="$model"
  fi

  echo "[disp] tier ${TIER_NUM}: ${backend} ${model}" >&2

  if ! preflight "$backend" "$model"; then
    echo "[disp]   SKIP — preflight: ${PREFLIGHT_REASON}" >&2
    continue
  fi

  # Slot acquisition for Ollama tiers only. Skipped entirely in dry-run mode
  # so dry-run can run while the system is under load without hijacking a slot.
  HELD_LOCK=""
  if [[ "${DRY_RUN}" -ne 1 ]]; then
    case "$backend" in
      ollama-local|ollama-cloud)
        if ! HELD_LOCK="$(acquire_ollama_slot "$backend" "$slots" "$wait_max")"; then
          echo "[disp]   SKIP — slot timeout after ${wait_max}s (${slots} slots full)" >&2
          continue
        fi
        ;;
    esac
  fi

  # Run the dispatch, capturing the REAL exit code. `if ! cmd; then $?` is 0
  # because `!` inverts the exit code; we use `cmd && rc=0 || rc=$?` so DISPATCH_EXIT
  # holds cmd's actual exit code (and `set -e` doesn't fire because the `||` branch
  # is part of a compound conditional).
  DISPATCH_OK=1
  DISPATCH_EXIT=0
  case "$backend" in
    ollama-local|ollama-cloud)
      dispatch_ollama "$model" && DISPATCH_EXIT=0 || DISPATCH_EXIT=$?
      ;;
    codex-exec)
      dispatch_codex "$model" "$opts" && DISPATCH_EXIT=0 || DISPATCH_EXIT=$?
      ;;
    claude-native)
      dispatch_claude_native "$model" && DISPATCH_EXIT=0 || DISPATCH_EXIT=$?
      ;;
  esac
  [[ "${DISPATCH_EXIT}" -ne 0 ]] && DISPATCH_OK=0

  release_ollama_slot "${HELD_LOCK}"
  HELD_LOCK=""

  if [[ "${DISPATCH_OK}" -eq 1 ]]; then
    if [[ "${TIER_NUM}" -eq 1 ]]; then
      echo "[disp] served_by=${backend}:${model}" >&2
    else
      echo "[disp] served_by=${backend}:${model} originally_requested=${PRIMARY_BACKEND}:${PRIMARY_MODEL} FALLBACK" >&2
    fi
    exit 0
  fi

  echo "[disp]   SKIP — dispatch exited ${DISPATCH_EXIT}" >&2

  [[ "${DRY_RUN}" -eq 1 ]] && exit 0
done <<<"${TIER_TABLE}"

echo "[disp] CHAIN FAILED: ${TIER_NUM} tiers exhausted for role=${ROLE}" >&2
exit 1
