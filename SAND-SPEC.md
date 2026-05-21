# SAND — Go MCP server for agent dispatch

> **Project:** `sand` (ASCII command + repo name)
> **Swedish:** `sänd` ("send") — shown in README to make the origin clear; never used in filenames or commands
> **Location:** `/Users/evanschultz/Documents/Code/hylla/sand/main`
> **Replaces:** `bin/agent-dispatch.sh` + `.claude/agent-chains.sh` in the `ta` project
> **Build cascade:** use existing `ta/main` cascade methodology via the EXISTING `bin/agent-dispatch.sh` (bootstrap). Once sand ships, migrate orchestrator + retire `.sh`.

---

## 0. TL;DR for the build agent

Read this whole document, then read the reference files listed in §13, THEN start the build cascade in `ta/main/.ta/` substrate per §14.

- **What sand is**: a Go MCP server that exposes 4 tools (`dispatch`, `preflight`, `persona_get`, `chains_list`) for routing agent prompts to the right backend (ollama / codex / claude-native) per a fallback chain config.
- **What sand is NOT**: a workflow tracker. ta MCP stays the workflow substrate. Sand is pure transport — fire-prompt → return-response. Cascade record CRUD happens via `mcp__ta__*` from the orchestrator, not from sand.
- **Why sand exists**: bash dispatcher has hit walls — inline TOML escaping for codex `-c` flags, `/tmp` file inflation, per-backend MCP-injection asymmetries, awkward permission allowlist patterns, no type safety on dispatch args, hard to maintain.
- **Sand fixes all of those** by being a real MCP server with typed parameters. Orchestrator calls `mcp__sand__dispatch(role="...", prompt="...")` instead of `Bash(./bin/agent-dispatch.sh ...)`. One MCP-tool-allowlist entry covers every dispatch. Go structs replace bash-string escaping. Goroutines handle dozens of parallel dispatches per project.

---

## 1. Why we're building this (lessons learned from the bash dispatcher)

Read `ta/main/bin/agent-dispatch.sh` end-to-end before reading this section. Then come back.

The .sh dispatcher works, but every pain point below burned hours this session:

- 1.1 **Permission-prompt churn**: every distinct `Bash(echo "..." | ./bin/agent-dispatch.sh ...)` triggered a fresh approval. Required `--prompt <string>` flag retrofit + manually broadening the allowlist. Sand: one entry, `mcp__sand__dispatch`, auto-approved like any other MCP tool.
- 1.2 **MCP attachment asymmetries**: ollama + claude-native use `--mcp-config <path>` (needs `.mcp.json`); codex uses `-c "mcp_servers.<name>={...}"` (inline TOML). Bash had to escape TOML inside double-quoted strings inside bash arrays. Multiple iterations to get it right. Sand: Go struct → TOML serialization, no escaping bugs.
- 1.3 **Codex per-tool approval gap**: codex defaults non-configured MCP tools to `approval_mode = "prompt"` which auto-cancels in `--ephemeral` mode (no TTY). The .sh fix loops over a hardcoded tool list to inject `approval_mode = "approve"` per tool. Sand: same logic but in Go, easier to add tools / change defaults.
- 1.4 **/tmp file inflation**: long task prompts went to `/tmp/dispatch-*.md` files via `--prompt-file`. This led to STUFFING work content into prompts when it belonged in ta records. Sand: `dispatch` takes prompt as a string argument; thin pointer prompts become the norm by mechanism, not by discipline.
- 1.5 **Chain config in bash**: `.claude/agent-chains.sh` is a sourceable bash file with heredoc tables. Hard to validate, no schema. Sand: TOML config with a schema validated at startup.
- 1.6 **No structured response**: bash dispatcher emits Claude Code JSON envelope (ollama / claude-native) OR raw codex stream (codex). Orchestrator manually parses + extracts. Sand: returns TOON-encoded structured response uniformly across backends.
- 1.7 **No type safety on dispatch args**: shell-array construction + word-splitting on `opts` field = fragile. Sand: typed args, validated at MCP request time.

---

## 2. Architecture

- 2.1 **Single Go binary**: `sand` (built by `mage install` → `$HOME/.local/bin/sand`, never `$GOBIN` — see ta/main CLAUDE.md feedback on install paths).
- 2.2 **MCP server**: runs as the project-local MCP via `.mcp.json` registration. Same launch pattern as ta MCP. Pins project dir via `--project <abs-path>` argument (mirror ta's pattern; see ta/main CLAUDE.md § MCP server — pinning the project directory).
- 2.3 **Per-tool-call concurrency**: each MCP tool invocation handled in its own goroutine. The Go MCP framework (mark3labs/mcp-go or anthropic's official Go SDK — builder's call, mark3labs is more mature) handles request multiplexing. Dozens of parallel `sand.dispatch` calls per project = supported by design.
- 2.4 **Concurrency controls preserved**:
   - Ollama tier: mkdir-based slot locks at `/tmp/sand-dispatch/<backend>.<N>.lock` (interop with `.sh`'s `/tmp/agent-dispatch/` is OPTIONAL; default to separate dir to avoid cross-tool interference during migration). Per-tier slot count from chain config; default 4 slots. Stale-lock cleanup via PID file + `kill -0` liveness check (port from .sh).
   - Codex tier: no locks. External API self-rate-limits via 429; on non-zero exit, advance chain.
   - Claude-native tier: no locks. Same as codex.
- 2.5 **Backend dispatch**: shells out to `claude -p` and `codex exec` via `os/exec`. Same CLIs the .sh uses. Sand DOES NOT need a new Anthropic SDK dependency or new auth flow — inherits `~/.claude/.claude.json`-style auth (claude CLI) and `~/.codex/auth.json` (codex CLI).
- 2.6 **Per-dispatch logs**: structured logs at `/tmp/sand-dispatch/log/<dispatch-uuid>.json` (UUID per dispatch). Captures full envelope (Claude Code JSON or codex stream), tool-use events, permission_denials, timing, served_by chain trace. Orchestrator gets the TOON-summarized response via MCP; deep inspection via the log file.

---

## 3. MCP tool schemas

All tools return TOON-encoded strings (per §4). Sand serializes Go structs to TOON.

### 3.1 `sand.dispatch`

Fire one dispatch. Sync per-call; orchestrator parallelism via multiple concurrent MCP calls.

```
input:
  role: string (required)               # e.g. "ta-go-planning"
  prompt: string (required)             # task prompt for the agent (THIN — points at ta records)
  cwd: string (optional)                # working dir for the dispatched agent (default: server's project dir)
  model_override: string (optional)     # replaces tier-1 model only; e.g. "qwen3-coder:30b"
  dry_run: bool (optional, default false)  # print constructed command for tier 1, don't dispatch
```

Response is a TOON-encoded string per the canonical TOON spec at https://github.com/toon-format/toon. CRITICAL: TOON's compactness comes from **tabular arrays** where fields are declared ONCE in the header and rows stream as bare CSV — NOT JSON-style objects-per-row. Use `key[N]{field1,field2}:` headers.

```toon
result: <agent's final text response — the .result field from Claude Code JSON, OR the contiguous body block from codex stream>
served_by: <backend>:<model>
tier: <int>
fallback: <bool>
duration_ms: <int>
cost_usd: <float>
tokens:
  input: <int>
  output: <int>
  cache_read: <int>
  cache_creation: <int>
tools_used[<N>]{name,count}:
  <tool_name>,<count>
  <tool_name>,<count>
permission_denials[<N>]{tool,count}:
  <tool_name>,<count>
log_path: <absolute path to /tmp/sand-dispatch/log/<uuid>.json>
```

Concrete example (a hypothetical real dispatch response):

```toon
result: The planner record has been amended and transitioned to complete+success.
served_by: claude-native:opus
tier: 3
fallback: true
duration_ms: 168793
cost_usd: 0.626
tokens:
  input: 10
  output: 13741
  cache_read: 120482
  cache_creation: 35481
tools_used[4]{name,count}:
  mcp__ta__get,4
  mcp__ta__update,1
  mcp__hylla__hylla_search_keyword,6
  Read,8
permission_denials[1]{tool,count}:
  Bash,0
log_path: /tmp/sand-dispatch/log/abc123.json
```

Notes for the build agent on TOON edge cases:
- `result` is free-form text and likely multi-line. TOON supports block scalars (YAML-like `|` and `>` indicators); pick the right strategy per the canonical spec. If the value contains `\n` or YAML-significant characters (`:`, `#`, leading whitespace), use a block scalar.
- Tabular arrays: length `[N]` is REQUIRED in the header. Empty arrays should still emit `tools_used[0]{name,count}:` with no rows (verify against the canonical spec; the build agent should adopt whatever the official encoder does for empty arrays).
- Top-level is an object (key-value pairs). No root array wrapping needed.

**Explicitly NOT included** in the response: thinking blocks, conversation turns, raw tool-call arguments+results, agent internal reasoning prose. Those are in the log file at `log_path` for debugging.

### 3.2 `sand.preflight`

Backend health check for a role's chain. Used by orchestrator before firing a dispatch when chain state is uncertain (e.g. after a long idle period or a codex rate-limit event).

```
input:
  role: string (required)
```

```toon
role: <string>
tiers[<N>]{tier,backend,model,ok,reason}:
  1,ollama-local,qwen2.5-coder:7b,true,
  2,codex-exec,gpt-5.5,false,model not pulled locally
  3,claude-native,opus,true,
```

`reason` is empty string when `ok=true`; populated with a short diagnostic when `ok=false`. Reason values come straight from the preflight check (e.g. `"ollama daemon unreachable at localhost:11434"`, `"codex CLI not on PATH"`).

Sand performs the same preflight checks as `.sh::preflight()`:
- ollama-local: HTTP GET `localhost:11434/api/version` + `ollama list` parse for model presence
- codex-exec: `command -v codex`
- claude-native: `command -v claude`

### 3.3 `sand.persona_get`

Read parsed persona. Debug helper. Useful when verifying frontmatter changes after `mcp__ta__update` on agent records.

```
input:
  role: string (required)
```

```toon
name: <from frontmatter name field>
description: <from frontmatter description field>
model: <from frontmatter model field, empty string if absent>
tools[<N>]: <tool1>,<tool2>,<tool3>
body: |
  <full markdown body after frontmatter close — use block scalar for multi-line>
```

`tools[N]` is a primitive (string) array, inline CSV form. `body` is a free-form text field; use TOON block scalar (`|` literal indicator per canonical spec) for the multi-line markdown body.

### 3.4 `sand.chains_list`

Enumerate all defined roles + their chains. Debug helper for sanity-checking chain config after edits.

```
input: (none)
```

Nested tabular emission — outer rows of `roles` each carry a tabular `tiers` array. TOON supports this via the standard nesting (each `roles` row is an object that contains a `tiers[N]{...}:` sub-block at its indentation depth):

```toon
roles[<N>]:
  - role: ta-go-builder
    tiers[3]{tier,backend,model,opts,wait_max,slots}:
      1,ollama-local,qwen2.5-coder:7b,,20,4
      2,codex-exec,gpt-5.5,--sandbox workspace-write -c model_reasoning_effort=low,,
      3,claude-native,haiku,,,
  - role: ta-go-planning
    tiers[4]{tier,backend,model,opts,wait_max,slots}:
      1,codex-exec,gpt-5.5,--sandbox read-only -c model_reasoning_effort=low,,
      2,codex-exec,gpt-5.5,--sandbox read-only -c model_reasoning_effort=medium,,
      3,claude-native,sonnet,,,
      4,claude-native,opus,,,
```

The outer `roles[N]:` declares a list whose elements are objects (not tabular — they don't share a uniform field set since each `tiers` sub-array is potentially different length). Empty values in the CSV rows (e.g. `opts` when not set, `wait_max`/`slots` for non-ollama tiers) are literal empty strings between commas. The build agent should verify this exact nested pattern against the canonical TOON spec at https://github.com/toon-format/toon and adjust to the most idiomatic encoding the encoder supports.

---

## 4. TOON response format

Sand serializes all MCP responses to TOON (Token-Oriented Object Notation) per the canonical spec at https://github.com/toon-format/toon (Context7 library id: `/toon-format/toon`). 30-60% token reduction vs JSON for the response shapes sand emits (heavy on uniform tabular arrays like `tools_used` + `permission_denials` + `tiers` — TOON's strongest case).

- 4.1 **Tabular arrays are TOON's killer feature.** Fields declared ONCE in the header `key[N]{f1,f2,f3}:`, rows stream as bare CSV `v1,v2,v3`. Sand's response shapes are designed for this — `tools_used`, `permission_denials`, `tiers` all use tabular emission. Do NOT emit JSON-style objects-per-row inside a YAML-ish array (the redundancy that TOON exists to eliminate).
- 4.2 **Length is required.** Array headers always include the row count `[N]` even when empty (`tools_used[0]{name,count}:` with no rows below). The build agent verifies the encoder's empty-array convention against the canonical spec.
- 4.3 **Strings with special characters or newlines** use TOON block scalars (`|` literal indicator per canonical spec). The `result` field (full agent response) and `body` field (persona markdown) are the main consumers.
- 4.4 **Encoding goal**: every `sand.*` MCP tool response body is a single TOON-encoded string. MCP wrapping is the usual JSON `{content: [{type: "text", text: "<TOON string>"}]}`. The TOON string is what the orchestrator parses.
- 4.5 **Implementation**: search for an existing Go TOON encoder before porting. Python has at least two (`xaviviro/python-toon`, `toon-format/toon-python`); if a Go port exists by build time, use it. If not, port the reference encoder — small surface (~200 LOC). Add as an atomic builder droplet in the L2-E scaffold (see §14.5).
- 4.6 **Orchestrator-side parsing**: ship a small Go TOON decoder utility as a sub-package of sand (or a sibling module) so orchestrator-side tooling that wants to introspect sand responses programmatically has a stable parser. Reference implementations on context7: `/toon-format/toon`, `/toon-format/toon-python`, `/xaviviro/python-toon`.
- 4.7 **Verify against the spec before shipping**: pull the canonical syntax cheatsheet from https://github.com/toon-format/toon/blob/main/docs/reference/syntax-cheatsheet.md (or via Context7 query against `/toon-format/toon`) and round-trip every sand response shape through the encoder + decoder before marking the L2-E droplet complete.

---

## 5. Chain config (TOML, replaces `.claude/agent-chains.sh`)

File: `.claude/sand-chains.toml` in each project. Loaded at sand startup. Hot-reloadable on SIGHUP (nice-to-have for v2; v1 OK with restart).

```toml
# Per-role fallback chain. Each tier table is one entry; tiers walked in order.
# backend ∈ {ollama-local, codex-exec, claude-native}.
# opts is the same opaque string passed to the backend's CLI (e.g. codex --sandbox + reasoning effort).
# wait_max + slots only apply to ollama-local; ignored for codex / claude.

[[chains."ta-go-builder".tiers]]
backend = "ollama-local"
model = "qwen2.5-coder:7b"
wait_max = 20
slots = 4

[[chains."ta-go-builder".tiers]]
backend = "codex-exec"
model = "gpt-5.5"
opts = "--sandbox workspace-write -c model_reasoning_effort=low"

[[chains."ta-go-builder".tiers]]
backend = "claude-native"
model = "haiku"

# ... one [[chains."<role>".tiers]] block per tier per role ...
```

- 5.1 Migration: port `ta/main/.claude/agent-chains.sh`'s 5 role chains (builder, planning, qa_falsification, qa_proof, closeout) to this TOML at sand cascade time.
- 5.2 Validation: sand validates at startup. Reject on unknown backend, missing fields, or invalid `slots` (must be ≥0; 0 = unlimited).
- 5.3 Persona-to-chain mapping: same as .sh's `emit_chain_for_role`. `ta-go-builder` and `ta-fe-builder` both → `chain_builder`-equivalent. Document the chain-sharing pattern in the TOML (a `chains.<role>.alias_of = "<other-role>"` shortcut MAY be added in v2 to avoid duplication).

---

## 6. Persona file parsing

Same input as .sh: `.claude/agents/<role>.md` (project install path; see `claude_agents` schema at `ta/main/.ta/schema.toml` for the canonical mount).

YAML frontmatter (fenced by `---` lines), then markdown body:

```markdown
---
description: <short description shown in tool routing>
name: <role name, matches filename stem>
tools: Read, Edit, Bash(mage testFunc *), mcp__ta__get, ...
model: <optional model identifier>
color: <optional UI hint>
---

<full markdown body — becomes the agent's system prompt>
```

- 6.1 Use [goccy/go-yaml](https://github.com/goccy/go-yaml) or stdlib `gopkg.in/yaml.v3` for frontmatter parsing. Don't reimplement.
- 6.2 The `tools:` field is a single comma-separated string (matches Claude Code's YAML scalar convention). Split on `,`, trim whitespace per element.
- 6.3 Body extraction: everything after the closing `---` line, preserving leading/trailing whitespace as-is (the body becomes the system prompt verbatim; downstream `--append-system-prompt` accepts arbitrary markdown).
- 6.4 NEVER edit persona files directly (per ta/main CLAUDE.md § Editing role personas). Sand only READS them. Mutation goes through `mcp__ta__update` on the `claude_agents.agent` record id.

---

## 7. Backend-specific dispatch — exact commands

Read `ta/main/bin/agent-dispatch.sh` dispatch_ollama / dispatch_codex / dispatch_claude_native functions for the exact current shapes. Sand should produce equivalent commands using Go's `os/exec`.

### 7.1 ollama-local

```
env ANTHROPIC_BASE_URL=http://localhost:11434 ANTHROPIC_API_KEY=ollama \
  claude -p \
    --bare \
    --model <model> \
    --output-format json \
    --no-session-persistence \
    --append-system-prompt "<PERSONA_BODY + ANTI_RECURSION>" \
    --mcp-config "<REPO_ROOT>/.mcp.json" \
    --allowedTools "<persona tools: line>" \
    <<< <TASK_PROMPT>
```

The `<<<` is heredoc in bash; Go equivalent: `cmd.Stdin = strings.NewReader(taskPrompt)`.

ANTI_RECURSION is a constant suffix appended to the persona body — prevents the spawned session from recursively calling sand.dispatch on itself (see `.sh::ANTI_RECURSION`).

### 7.2 codex-exec

```
codex exec \
  --ephemeral \
  --ignore-rules \
  --skip-git-repo-check \
  -C <CWD> \
  -m <model> \
  -c "mcp_servers.ta={command=\"ta\",args=[\"--project\",\"<CWD>\"],tools={get={approval_mode=\"approve\"},update={approval_mode=\"approve\"},list_sections={approval_mode=\"approve\"},search={approval_mode=\"approve\"},schema={approval_mode=\"approve\"},create={approval_mode=\"approve\"},delete={approval_mode=\"approve\"},move={approval_mode=\"approve\"},init={approval_mode=\"approve\"}}}" \
  <opts from chain config> \
  <<< "<PERSONA_BODY + ANTI_RECURSION>\n---\n<TASK_PROMPT>"
```

CRITICAL:
- Do NOT pass `--ignore-user-config` (was stripping all `~/.codex/config.toml` MCPs; removed during ta's drop_006 work).
- DO inject `mcp_servers.ta` explicitly (codex user config doesn't have ta MCP — Claude-Code-only today).
- DO set per-tool `approval_mode = "approve"` for every ta tool the agent might call. Codex's default `prompt` mode auto-cancels in `--ephemeral` mode.
- Construct the `-c` TOML inline table from a Go struct (NEVER hand-build the escape-laden string). Reference Go implementation: build the inner map, serialize with `pelletier/go-toml` to compact form, then wrap as `mcp_servers.ta=<inline-table>`.

### 7.3 claude-native

```
env -u ANTHROPIC_BASE_URL -u ANTHROPIC_AUTH_TOKEN \
  claude -p \
    --bare \
    --model <model> \
    --output-format json \
    --no-session-persistence \
    --append-system-prompt "<PERSONA_BODY + ANTI_RECURSION>" \
    --mcp-config "<REPO_ROOT>/.mcp.json" \
    --allowedTools "<persona tools: line>" \
    <<< <TASK_PROMPT>
```

Same as ollama-local but UNSETS the ollama-redirect env vars (subshell scoping in bash; in Go, build the env slice via `os.Environ()` filtered to drop those two keys).

### 7.4 ANTI_RECURSION constant

Append to persona body before dispatch:

```
---
DISPATCH CONTEXT: You are the <ROLE> agent, dispatched via sand. Execute the task below directly using YOUR role-appropriate tools (the orchestrator restricts them per the persona's `tools:` allowlist). Do NOT call sand.dispatch. Do NOT use the Agent tool to spawn other roles. Do NOT route the task elsewhere. You ARE the role. The orchestrator coordinates further dispatches.
```

---

## 8. Concurrency + slot locks

- 8.1 **Per-call goroutine**: MCP framework dispatches each tool call to a goroutine. No global serialization.
- 8.2 **Ollama slot acquisition**: ONLY for `ollama-local` tier. Mkdir-based atomic claim at `/tmp/sand-dispatch/<backend>.<N>.lock`, port the loop from `.sh::acquire_ollama_slot`. Wait up to `wait_max` seconds; if no slot in time, advance chain (mark tier SKIPPED in tier trace).
- 8.3 **Stale-lock cleanup**: write PID to `<lock>/pid` on acquire; on next acquisition attempt, `kill -0 <pid>`-check if the holder is alive; if not, `rm -rf <lock>` and retry. Port from .sh.
- 8.4 **Codex / claude rate-limit fallback**: no locks. Dispatch directly. On non-zero exit (or stderr-detected "usage limit" / 429), advance chain. The .sh treats ANY non-zero exit as "advance"; sand should do the same for parity, but consider distinguishing 429-equivalents from genuine errors in v2 (informative log entry).
- 8.5 **Parallel dispatch verified design**: orchestrator can fire N parallel `sand.dispatch` MCP calls in one Claude Code message; sand handles each in its own goroutine; ollama-tier concurrency capped by slots; codex/claude-tier unbounded.

---

## 9. Logging

Per-dispatch log file at `/tmp/sand-dispatch/log/<uuid>.json` (UUID per dispatch, returned in the MCP response as `log_path`):

```json
{
  "dispatch_id": "<uuid>",
  "role": "ta-go-planning",
  "started_at": "2026-05-20T22:30:00Z",
  "completed_at": "2026-05-20T22:32:48Z",
  "tier_trace": [
    {"tier": 1, "backend": "codex-exec", "model": "gpt-5.5", "result": "skip", "reason": "exited 1 (usage limit)"},
    {"tier": 3, "backend": "claude-native", "model": "opus", "result": "served"}
  ],
  "served_by": "claude-native:opus",
  "tier": 3,
  "fallback": true,
  "duration_ms": 168793,
  "raw_envelope": "<full Claude Code JSON OR codex raw stream>",
  "tools_used": {...},
  "permission_denials": {...},
  "task_prompt": "<full prompt string>",
  "persona_body_hash": "<sha256 of persona body for cache-busting verification>",
  "cwd": "/abs/path",
  "exit_code": 0
}
```

- 9.1 Rotation: log files are reboot-volatile (`/tmp`). No retention policy needed.
- 9.2 The TOON response to the orchestrator is a SUMMARY of this log. Deep inspection = read `log_path`.

---

## 10. Build conventions

- 10.1 `magefile.go` at `sand/main/`. Targets: `mage build`, `mage test`, `mage cover`, `mage check`, `mage install` (→ `$HOME/.local/bin/sand` per ta/main CLAUDE.md feedback `feedback_install_local_bin`).
- 10.2 `gofumpt` via mage target (NEVER raw `gofmt` or `gofumpt` — see ta/main CLAUDE.md feedback `feedback_no_raw_gofmt`).
- 10.3 Module path: `github.com/evanmschultz/sand`.
- 10.4 Go version: latest stable (1.23+ likely fine).
- 10.5 MCP framework choice: builder's call. Recommend [mark3labs/mcp-go](https://github.com/mark3labs/mcp-go) (most mature Go MCP SDK as of 2026-05). Alternative: anthropic's official MCP Go SDK if it exists by build time.
- 10.6 Dependencies (minimal): MCP server framework, TOML parser (`pelletier/go-toml/v2`), YAML parser (`goccy/go-yaml` or stdlib `gopkg.in/yaml.v3`), TOON encoder (port if needed, see §4).
- 10.7 Tests: table-driven where applicable; per ta-go-* persona Go conventions. Coverage target ≥70% per module.

---

## 11. ta MCP usage — CRITICAL

- 11.1 **Sand does NOT track workflow state.** Sand has no concept of cascade records, droplets, planners, QA twins. Those are ta MCP concerns.
- 11.2 The orchestrator that uses sand persists dispatch outcomes to ta cascade records via `mcp__ta__*` tools, exactly as it does today with the .sh dispatcher.
- 11.3 **The BUILD project for sand uses ta cascade**: when an agent builds sand, that agent's cascade lives in `ta/main/.ta/cascade/drops/drop_NNN/drop.toml` (NOT in sand's own .ta — sand probably doesn't need its own .ta substrate at all).
- 11.4 Read `ta/main/CLAUDE.md` § "Cascade-managed development — use ta to manage ta" + § "ta CLI usage" + § "MCP server — pinning the project directory" for the canonical ta usage rules.
- 11.5 ta records carry HTML-renderable content via Track A (`internal/templates_html_basic`) and Track B (Astro). The user's "all md edits via ta MCP" rule (currently blocked by an `agents_md.section` resolver bug — see ta's drop_006) does NOT apply to sand's source code or docs; it applies to ta-mounted markdown records (CLAUDE.md, AGENTS.md sections). Sand's own README + spec MD are NOT ta records.

---

## 12. Migration from `.sh` to sand

Phase 1 (drop_006-style build slice): ship sand. Keep `.sh` operational.

Phase 2 (orchestrator migration):
- Update `ta/main/.mcp.json` to register sand MCP alongside ta + hylla.
- Update `ta/main/CLAUDE.md` § "Agent Routing — Backend Dispatch" to document `mcp__sand__dispatch(...)` as the primary dispatch path.
- Update orchestrator habits: stop firing `Bash(./bin/agent-dispatch.sh ...)`; start firing `mcp__sand__dispatch(...)`.
- Keep `.sh` for one cycle as fallback in case sand has issues.

Phase 3 (retirement):
- After one clean cycle on sand, mark `bin/agent-dispatch.sh` + `.claude/agent-chains.sh` deprecated.
- Move `.claude/agent-chains.sh` content into `.claude/sand-chains.toml` (the new chain config sand reads).
- Eventually delete `.sh` and the bash chain file from `ta/main`.

---

## 13. Files to read FIRST (build agent: do this before anything else)

In this order:

- 13.1 `/Users/evanschultz/Documents/Code/hylla/ta/main/bin/agent-dispatch.sh` — the entire current dispatcher. ~450 lines bash. Understand every function.
- 13.2 `/Users/evanschultz/Documents/Code/hylla/ta/main/.claude/agent-chains.sh` — per-role chain definitions in bash heredoc tables. ~120 lines.
- 13.3 `/Users/evanschultz/Documents/Code/hylla/ta/main/.claude/agents/ta-go-builder.md`, `ta-go-planning.md`, `ta-go-qa-proof.md`, `ta-go-qa-falsification.md`, `ta-closeout.md` — persona file shape. YAML frontmatter + markdown body. Read at least 2-3.
- 13.4 `/Users/evanschultz/Documents/Code/hylla/ta/main/.mcp.json` — project MCP config (ta + hylla servers). Sand needs to be registered here once built.
- 13.5 `/Users/evanschultz/Documents/Code/hylla/ta/main/CLAUDE.md` — full project guidance. Especially: § Agent Routing, § Editing role personas, § Cascade-managed development, § ta CLI usage, § MCP server — pinning the project directory.
- 13.6 `~/.codex/config.toml` (specifically the `[mcp_servers.*]` blocks for hylla, gopls, tillsyn) — for codex MCP shape reference. The dispatcher's codex -c overrides mirror this pattern.
- 13.7 [johnnyreilly/toon](https://github.com/johnnyreilly/toon) — TOON spec for response encoding.
- 13.8 [mark3labs/mcp-go](https://github.com/mark3labs/mcp-go) — recommended MCP server framework.

---

## 14. Bootstrap cascade — how to BUILD sand

- 14.1 **Where the build cascade lives**: in `ta/main/.ta/cascade/drops/drop_NNN/drop.toml` (next available drop number). NOT in sand's own substrate. The build is a ta-managed project; sand becomes a sibling AFTER it ships.
- 14.2 **Use the existing `.sh` dispatcher** to dispatch ta-go-* agents for the build. Sand doesn't exist yet; bootstrap from current tools. After sand ships, future iterations can be self-hosted.
- 14.3 **Cascade methodology** per `ta/main/docs/cascade-methodology.md`:
   - Create `cascade.drop` for sand-MVP (e.g. `drop_NNN.drop.sand_mvp`)
   - Create `cascade.planner` child for L1 decomposition
   - Dispatch `ta-go-planning` via `./bin/agent-dispatch.sh --role ta-go-planning --cwd "$(pwd)" --prompt "..."` to decompose into L2 sub-planners
   - Plan-QA twins (F23 v2 auto-spawn) BLOCK descent until both PASS
   - L2 sub-planners decompose into atomic builder droplets (≤2 small code blocks INCLUDING tests per droplet)
   - Build-QA twins (auto-spawn per `cascade.droplet`) gate each droplet
   - Closeout per `ta-closeout` after L1 build-QA passes
- 14.4 **Hard rules for the build cascade** (same as ta/main/CLAUDE.md § 7):
   - Section 0 — SEMI-FORMAL REASONING at start of every substantive orchestrator response
   - ta MCP for record CRUD (NOT the CLI for cascade records; CLI is read-only inspection)
   - Mage-only Bash allowlists in personas; no raw `go`/`pnpm`/`cargo` for agents
   - Atomicity rule: builder droplets ≤2 small code blocks INCLUDING tests
   - NO semver phrasing ("v0.1.0" etc.) — use "pre-mvp-feature-complete" / "next slice"
   - Persona files NEVER edited via Edit/Write — use `mcp__ta__update` + `ta template save`
   - Hylla for committed Go evidence (Hylla artifact ref: build ingests after first push: `github.com/evanmschultz/sand@main`)
- 14.5 **Expected L2 decomposition shape** (planner agent should arrive at something like this):
   - **L2-A scaffold**: Go module + magefile + go.mod + minimal MCP server skeleton (exposes ping tool).
   - **L2-B persona + chain parsers**: read .claude/agents/*.md + sand-chains.toml; return structured Go values. Unit tests with table-driven fixtures.
   - **L2-C backend dispatchers**: port dispatch_ollama / dispatch_codex / dispatch_claude_native to Go. Integration tests with mocked CLI binaries.
   - **L2-D MCP tool wiring**: register dispatch / preflight / persona_get / chains_list tools; thread them to the parsers + dispatchers.
   - **L2-E TOON encoding**: encode all responses per johnnyreilly/toon. Decoder utility sub-package optional.
   - **L2-F slot locks**: port mkdir-based ollama slot acquisition. Concurrency tests via parallel goroutines.
   - **L2-G end-to-end**: real dispatch against a test persona (mock-role) verifying full envelope parse + TOON output. Include a tier-fallback test (deliberately failed tier 1 → succeeds at tier 2).
   - **L2-H docs + migration**: README (showing `sänd` Swedish origin), MIGRATION.md from `.sh`, update `ta/main/CLAUDE.md` to reference sand.
- 14.6 Each L2 emits 3-6 atomic builder droplets per cascade rule. Plan-QA twins fire at every level. Build-QA twins fire at droplet close.

---

## 15. Hard rules — never violate

- 15.1 Sand is TRANSPORT ONLY. No workflow state. No cascade tracking. ta MCP stays the workflow substrate.
- 15.2 Sand RESPONSES are TOON-encoded with structured metadata + tool usage counts, but NEVER thinking blocks, conversation turns, or raw tool args/results.
- 15.3 Sand RESPECTS persona `tools:` allowlists. Pass them as `--allowedTools` to claude / via per-tool approval_mode for codex. NEVER expand a persona's tool surface from sand.
- 15.4 Sand does NOT edit persona files. Read-only. Mutations go through `mcp__ta__update` on `claude_agents.agent` records.
- 15.5 Sand SHELLS OUT to claude CLI + codex CLI. Does NOT bundle a new Anthropic/OpenAI SDK. Inherits auth from those CLIs.
- 15.6 Sand RESPECTS the `--ignore-rules` + `--skip-git-repo-check` flags for codex (port from .sh dispatch_codex). Do NOT add `--ignore-user-config` back — it strips MCP servers (removed during ta's drop_006).
- 15.7 Sand RUNS multiple dispatches in parallel via goroutines. Mkdir-locks cap ollama-tier concurrency; codex/claude rate-limit-self-regulated. Dozens-per-project supported.
- 15.8 NO semver phrasing in sand's own docs / commits / cascade records.
- 15.9 Build cascade lives in `ta/main/.ta/`, not in `sand/main/.ta/`. Sand probably doesn't need its own ta substrate.
- 15.10 If a needed capability is missing (e.g. MCP framework gap, codex CLI option absent), REPORT to dev and PAUSE. Do NOT silently work around per the project CLAUDE.md "Dogfood discipline (MCP-first)" rule (which applies in spirit to all dev-mandated patterns).

---

## 16. Open questions for the build agent

- 16.1 **MCP framework choice**: mark3labs/mcp-go vs anthropic-official (if it exists by build time). Builder's call after evaluating both for: stability, parallelism support, response streaming, test ergonomics.
- 16.2 **TOON Go encoder**: does one exist? If not, port the JS reference. Single atomic droplet, well-bounded.
- 16.3 **Per-dispatch log persistence**: `/tmp/sand-dispatch/log/<uuid>.json` is reboot-volatile by design. If dev wants longer retention, propose `$XDG_STATE_HOME/sand/log/` instead — surface to dev before deciding.
- 16.4 **Future v2 features (out of MVP scope)**: chain config hot-reload on SIGHUP; per-tier opt-in streaming back through MCP; `sand.dispatch_batch(specs[])` for orchestrator-side spec lists; `sand.cancel(dispatch_id)` for in-flight kill.

---

## 17. Done definition

Sand is "done for MVP" when:
- 17.1 `mage install` builds + installs `sand` binary to `$HOME/.local/bin/sand`.
- 17.2 `ta/main/.mcp.json` can register sand alongside ta + hylla, and Claude Code session in `ta/main/` sees `mcp__sand__*` tools.
- 17.3 `mcp__sand__dispatch(role="ta-go-planning", prompt="...")` from a Claude Code session in `ta/main/` produces equivalent output to `./bin/agent-dispatch.sh --role ta-go-planning --cwd "$(pwd)" --prompt "..."` for the same prompt + chain state.
- 17.4 `mcp__sand__preflight`, `mcp__sand__persona_get`, `mcp__sand__chains_list` all work for at least one role.
- 17.5 TOON-encoded responses verified parseable by a reference TOON decoder (Go or JS).
- 17.6 Concurrent dispatch test: 10 parallel `sand.dispatch` calls complete successfully with no slot-lock deadlock or response interleaving.
- 17.7 Tier fallback test: forcing tier 1 to fail (e.g. unreachable ollama daemon) advances to tier 2 + serves correctly.
- 17.8 `mage check` green; coverage ≥70% per package; gofumpt clean (all routed through mage).
- 17.9 `ta/main/.ta/cascade/drops/drop_NNN/drop.toml` reports `state=complete + outcome=success` for the sand-MVP drop with all L2 sub-planners + droplets closed.
- 17.10 README + MIGRATION.md committed; `ta/main/CLAUDE.md` § Agent Routing updated to reference `mcp__sand__dispatch` as the primary dispatch path.

---

## 18. Out of scope for MVP

- 18.1 Streaming responses back through MCP (v2).
- 18.2 Chain config hot-reload (v2).
- 18.3 Job-id based async dispatch tools (sand.dispatch is sync per-call; parallelism via concurrent MCP calls).
- 18.4 Workflow tracking (NEVER — ta MCP's job).
- 18.5 Cost aggregation across dispatches (orchestrator-side concern; sand returns per-call cost_usd).
- 18.6 Cross-project chain sharing (each project has its own .claude/sand-chains.toml).
- 18.7 Custom MCP framework — use an off-the-shelf Go MCP SDK.

---

**End of spec.** Build agent: confirm via reply that you've read §13 reference files before starting the build cascade per §14.
