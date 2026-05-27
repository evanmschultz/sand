# SAND — Go MCP server for agent dispatch

> **Project:** `sand` (ASCII command + repo name)
> **Swedish:** `sänd` ("send") — origin note shown in README; never used in filenames or commands
> **Module:** `github.com/evanmschultz/sand`
> **Replaces:** the bash agent dispatcher previously at `ta/bin/agent-dispatch.sh`

This is the canonical design doc. Architectural sections describe SHIPPED behavior; an item that does not behave as described here is a bug.

---

## 0. TL;DR

- **What sand is**: a Go MCP server that exposes 4 tools (`dispatch`, `preflight`, `persona_get`, `chains_list`) for routing role-based prompts to ollama / codex / claude-native backends along a per-role fallback chain. Cross-process slot enforcement, hierarchical config, error-class-driven auto-fallback, TOON envelope output.
- **What sand is NOT**: a workflow tracker. ta MCP stays the workflow substrate. Sand is pure transport — fire-prompt → return-response. Cascade record CRUD happens via `mcp__ta__*` from the orchestrator, not from sand.
- **Why sand exists**: the bash dispatcher hit walls on inline TOML escaping for codex `-c` flags, `/tmp` file inflation, per-backend MCP-injection asymmetries, awkward permission allowlist patterns, no type safety, hard to maintain. Sand fixes all of those by being a real MCP server with typed parameters. Orchestrator calls `mcp__sand__dispatch(role="...", prompt="...")` instead of shelling out. One MCP-tool-allowlist entry covers every dispatch. Go structs replace bash-string escaping. Goroutines handle dozens of parallel dispatches per project.

---

## 1. Architecture

- 1.1 **Single Go binary**: `sand` (built by `mage install` → `$HOME/.local/bin/sand`).
- 1.2 **MCP server**: runs as a project-local MCP via `.mcp.json` registration. Pins project dir via `--project <abs-path>` argument.
- 1.3 **Per-call concurrency**: each MCP tool invocation handled in its own goroutine via mark3labs/mcp-go's request multiplexer. Dozens of parallel `sand.dispatch` calls per project = supported by design.
- 1.4 **Cross-process slot enforcement** (§7): `syscall.Flock` on `/tmp/sand-slots/<backend>/<model-slug>/slot.<N>.lock`. Kernel-managed → cross-project, cross-process, SIGKILL-safe.
- 1.5 **Hierarchical config** (§5 + §6): both `chains.toml` and `backends.toml` resolve `project → XDG → ~/.config/sand → ~/.sand → ErrNotFound`. Project rung wins; no merge.
- 1.6 **Backend dispatch**: shells out to `claude -p` and `codex exec` via `os/exec`. Inherits auth from those CLIs' own config files (`~/.claude/.claude.json`, `~/.codex/auth.json`). No new SDK dependencies.
- 1.7 **Error classification + auto-fallback** (§9): per-tier outcome classifier (rate_limit / auth_failure / network / timeout / crash / unknown) feeds into the chain walk — advance on recoverable errors, halt on unrecoverable. Optional per-tier `retry_on` whitelist overrides the default policy.
- 1.8 **Per-dispatch audit trail**: every chain attempt records one `FallbackChain` row (success or failure) returned as TOON. Optional per-dispatch JSON log files at `/tmp/sand-dispatch/log/<uuid>.json` (drop_007 polish — currently deferred).

---

## 2. MCP tool schemas

All tools return TOON-encoded strings (§4). Sand serializes Go structs to TOON.

### 2.1 `sand.dispatch`

Fire one dispatch. Sync per-call; orchestrator parallelism via multiple concurrent MCP calls.

```
input:
  role: string (required)               # e.g. "ta-go-planning"
  prompt: string (required)             # task prompt for the agent
  cwd: string (optional)                # working dir for the dispatched agent (default: server's project dir)
  model_override: string (optional)     # replaces tier-1 model only; e.g. "qwen3-coder:30b"
  dry_run: bool (optional, default false)  # render the would-be argv + env + stdin without spawning
```

Response shape (canonical TOON spec at https://github.com/toon-format/toon):

```toon
result: <agent's final text>
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
fallback_chain[N]{tier,backend,model,attempted_at,outcome,reason}:
  1,ollama-cloud,qwen3-coder-cloud-235b,2026-05-22T01:30:00Z,slot_timeout,all 3 slots busy for 10s
  2,ollama-local,qwen3-coder:30b,2026-05-22T01:30:10Z,rate_limit,HTTP 429 from daemon
  3,claude-native,opus,2026-05-22T01:30:15Z,success,
tools_used[N]{name,count}:
  mcp__ta__get,4
  Read,8
permission_denials[N]{tool,count}:
  Bash,0
tool_calls[N]{idx,name,duration_ms,is_error}:
  1,Read,12,false
  2,mcp__ta__get,89,false
  3,Bash,234,true
log_path: /tmp/sand-dispatch/log/abc123.json
```

`fallback_chain[N]` is ALWAYS populated, even on first-tier success (one row with `outcome=success`). `tool_calls[N]` is populated from PARSED EVENTS in the dispatched agent's output, never from the agent's narrative claims.

Explicitly NOT included: thinking blocks, conversation turns, raw tool-call arguments+results, agent internal reasoning prose. Those live in `log_path` when that file is enabled (drop_007 polish).

### 2.2 `sand.preflight`

Per-tier backend connectivity check. Used by the orchestrator before firing a dispatch when chain state is uncertain.

```
input:
  role: string (required)
```

```toon
role: ta-go-builder
tiers[N]{tier,backend,model,ok,reason}:
  1,ollama-cloud,qwen3-coder-cloud-235b,true,
  2,ollama-local,qwen3-coder:30b,false,model not pulled locally
  3,codex-exec,gpt-5.4,true,
  4,claude-native,haiku,true,
```

`reason` empty when `ok=true`; short diagnostic when `ok=false`. Per-backend probes:

```toon
preflight-probes{backend,probe}:
  claude-native,claude CLI on PATH
  ollama-local,GET http://localhost:11434/api/version + `ollama list` parse for model
  ollama-cloud,claude CLI on PATH (auth tested at wet-run via auth_failure → advance)
  codex-exec,codex CLI on PATH
  together-ai,claude CLI on PATH (auth tested at wet-run via auth_failure → advance)
```

### 2.3 `sand.persona_get`

Read a parsed persona. Debug helper for verifying frontmatter after edits.

```
input:
  role: string (required)
```

```toon
name: ta-go-builder
description: Build Go code with TDD ...
model: haiku
tools[N]: Read,Edit,Write,Grep,Glob,Bash(mage testFunc *),LSP
body: |
  You are a Go builder. ...
```

### 2.4 `sand.chains_list`

Enumerate every configured role + its fallback chain.

```
input:
  role: string (optional)               # filter to one role; omit for all
```

```toon
roles[N]:
  - role: ta-go-builder
    tiers[5]{tier,backend,model,opts,wait_max,slots}:
      1,ollama-cloud,qwen3-coder-cloud-235b,,10,3
      2,ollama-local,qwen3-coder:30b,,30,1
      3,codex-exec,gpt-5.4,--sandbox workspace-write,,
      4,claude-native,haiku,,,
      5,claude-native,sonnet,,,
```

---

## 3. Persona file parsing

Personas live at `<cwd>/.claude/agents/<role>.md`. YAML frontmatter (fenced by `---`), then markdown body.

```markdown
---
name: ta-go-builder
description: Build Go code with TDD, idiomatic error handling...
model: haiku
tools: Read, Edit, Write, Grep, Glob, Bash(mage testFunc *), LSP
---

You are a Go builder. ...
```

- 3.1 The `tools:` field is a single comma-separated string; sand splits on `,` and trims whitespace per element.
- 3.2 Body extraction: everything after the closing `---` line, preserved verbatim — it becomes the spawned agent's system prompt via `--append-system-prompt`.
- 3.3 NEVER edit persona files directly. Sand only READS them. Mutation goes through `mcp__ta__update` on the `claude_agents.agent` record id.

---

## 4. TOON response format

Sand serializes all MCP responses to TOON (Token-Oriented Object Notation) per the canonical spec at https://github.com/toon-format/toon. 30-60% token reduction vs JSON for the response shapes sand emits.

- 4.1 **Tabular arrays are TOON's killer feature.** Fields declared ONCE in the header `key[N]{f1,f2,f3}:`; rows stream as bare CSV `v1,v2,v3`. Sand's response shapes (`tools_used`, `permission_denials`, `fallback_chain`, `tool_calls`, `tiers`) all use tabular emission.
- 4.2 **Length required.** Array headers always include the row count `[N]` even when empty (`tools_used[0]{name,count}:` with no rows below).
- 4.3 **Block scalars** for strings with newlines or YAML-significant characters. The `result` field (full agent response) and `body` field (persona markdown) are the main consumers.
- 4.4 **Encoding goal**: every `sand.*` MCP tool response body is a single TOON-encoded string. MCP wrapping is the usual JSON `{content: [{type: "text", text: "<TOON string>"}]}`. The TOON string is what the orchestrator parses.

---

## 5. Chain config (`sand-chains.toml`)

Per-role fallback chains. Hierarchical resolution; first hit wins:

```toon
chains-resolution{rung,path,when-it-wins}:
  1,<projectDir>/.claude/sand-chains.toml,project override — wins if present
  2,$XDG_CONFIG_HOME/sand/chains.toml,XDG_CONFIG_HOME env var is set
  3,$HOME/.config/sand/chains.toml,default XDG location (Linux + macOS modern apps)
  4,$HOME/.sand/chains.toml,dotfile fallback
```

If none exist, sand returns `ErrChainConfigNotFound` with the list of paths checked — never invents implicit defaults. `mage install` seeds `~/.config/sand/chains.toml` (claude-native-only baseline) on first install; never overwrites.

```toml
[chains]

"ta-go-builder" = [
  { backend = "ollama-cloud", model = "qwen3-coder-cloud-235b", slots = 3, wait_max = 10, opts = "" },
  { backend = "ollama-local", model = "qwen3-coder:30b",        slots = 1, wait_max = 30, opts = "" },
  { backend = "codex-exec",   model = "gpt-5.4",                slots = 0, wait_max = 0,  opts = "--sandbox workspace-write" },
  { backend = "claude-native", model = "haiku",                 slots = 0, wait_max = 0,  opts = "" },
  { backend = "claude-native", model = "sonnet",                slots = 0, wait_max = 0,  opts = "" },
]
```

Tier field semantics:

```toon
tier-fields{field,meaning,default}:
  backend,must match a [backends.NAME] entry in backends.toml,required
  model,backend-specific model identifier,required
  slots,cross-process concurrency cap (0 = unlimited),0
  wait_max,seconds to wait for a slot before advancing,0
  opts,opaque extra CLI flags forwarded to the backend command,empty
  retry_on,optional whitelist of outcome strings that advance the chain; non-empty halts on any non-listed outcome,empty (default policy applies)
```

`retry_on` is opt-in. Empty (the default) uses the built-in policy from §9.3. Non-empty acts as an exact-match advance whitelist: outcomes in the list advance; others halt with a wrapped "retry_on policy" error (distinct from `ErrChainExhausted`).

---

## 6. Backend templates config (`sand-backends.toml`)

Per-backend spawn templates. Same hierarchical resolution as chains.toml (project → XDG → `~/.config/sand` → `~/.sand`). `mage install` seeds `~/.config/sand/backends.toml` with one active claude-native block + 4 commented examples (codex-exec, ollama-local, ollama-cloud, together-ai). Uncomment-and-go.

```toml
[backends.claude-native]
command = "claude"
args = ["-p", "--bare", "--model", "{{.Model}}", "--output-format", "json", "--no-session-persistence", "--append-system-prompt", "{{.PersonaBody}}"]
mcp_config_arg = "--mcp-config"
allowed_tools_arg = "--allowedTools"
allowed_tools_csv_template = "{{.PersonaToolsCSV}}"
slots_default = 0
envelope_format = "claude_json"
stdin_prompt = true
```

10 BackendConfig fields per entry:

```toon
backend-fields{field,purpose}:
  command,executable name (resolved against PATH at spawn time)
  args,argv slice — each element rendered through the templating engine
  env,KEY=VALUE strings — values may contain {{env "VAR"}} substitutions
  mcp_config_arg,flag name (e.g. --mcp-config) appended with mcp.json path when present; empty for backends that ignore it
  allowed_tools_arg,flag name (e.g. --allowedTools) used to pass the persona's tool allowlist
  allowed_tools_csv_template,template rendered to produce the value for allowed_tools_arg
  slots_default,default slot count when a chain tier omits the slots field
  envelope_format,output parser hint — "claude_json" or "codex_stream"
  stdin_prompt,true pipes the dispatch prompt to subprocess stdin; false relies on a {{.Prompt}} template substitution
  mcp_injection,reserved for codex-style inline-TOML injection (codex_inline_toml)
```

Templating engine: Go `text/template` with `Option("missingkey=error")` so missing template vars surface as errors, not empty strings. Available substitutions:

```toon
template-vars{var,resolved-to}:
  {{.Model}},chain tier's model (override-aware)
  {{.CWD}},caller project absolute path
  {{.PersonaBody}},loaded persona markdown body (system prompt)
  {{.PersonaToolsCSV}},persona Tools slice joined with commas
  {{.McpConfigPath}},<cwd>/.mcp.json when present, else empty
  {{env "VAR"}},process environment variable lookup
```

Adding a new provider (groq, openrouter, anthropic-vertex, etc.) is a TOML edit only — no Go code change required.

---

## 7. Cross-process slot subsystem

Slots cap concurrent spawns against a `(backend, model)` pair across EVERY sand process on the machine.

### 7.1 Mechanism

`syscall.Flock(fd, LOCK_EX|LOCK_NB)` on per-slot lock files at `/tmp/sand-slots/<backend>/<model-slug>/slot.<N>.lock`. One file per slot index. Kernel-managed: lock auto-releases on process death (SIGKILL-safe; no PID-liveness polling). `/tmp` clears on reboot — stale locks from prior boots vanish.

### 7.2 Public API

```go
// internal/slots/slot.go
type Slot struct {
    Backend string
    Model   string
    Index   int  // 1..N
    // unexported: fd *os.File
}

func (s *Slot) Release()

// AcquireSlot probes slot files 1..N for the given backend+model. Returns the
// acquired Slot or ErrSlotTimeout if all slots remain busy for waitMax.
// slots=0 means unlimited — returns (nil, nil) without touching the filesystem.
func AcquireSlot(backend, model string, slots int, waitMax time.Duration) (*Slot, error)

var ErrSlotTimeout = errors.New("slots: all slots busy after wait_max")
```

### 7.3 Dispatch flow with slots

```
for tier in chain:
  if tier.slots > 0:
    slot, err := AcquireSlot(tier.backend, tier.model, tier.slots, tier.wait_max)
    if errors.Is(err, ErrSlotTimeout):
      record_attempt(tier, "slot_timeout", ...)
      continue
    defer slot.Release()

  result, runErr := backend.Spawn(ctx, req)
  class := ClassifyExitError(result.Stderr, result.ExitCode)
  // ... outcome routing per §9
```

---

## 8. Backend-specific dispatch shapes

### 8.1 claude-native

```
env -u ANTHROPIC_BASE_URL -u ANTHROPIC_AUTH_TOKEN \
  claude -p \
    --bare \
    --model <model> \
    --output-format json \
    --no-session-persistence \
    --append-system-prompt "<PERSONA_BODY + ANTI_RECURSION>" \
    --mcp-config "<CWD>/.mcp.json" \
    --allowedTools "<persona tools csv>" \
    <<< <TASK_PROMPT>
```

ANTHROPIC_BASE_URL + ANTHROPIC_AUTH_TOKEN are STRIPPED to prevent ollama-redirect leakage into the claude-native code path.

### 8.2 ollama-local / ollama-cloud / together-ai

All three route through the claude CLI with `ANTHROPIC_BASE_URL` set to the provider's endpoint and `ANTHROPIC_API_KEY` set via env-var substitution. Same argv shape as claude-native otherwise. The same `claudeNativeBackend` implementation serves all four via the EnvelopeFormat dispatch in `backends.Resolve` (§5.3 envelope_format = "claude_json").

### 8.3 codex-exec

```
CODEX_HOME=<hermetic_dir> codex exec \
  --skip-git-repo-check \
  --ignore-user-config \
  -C <CWD> \
  -m <model> \
  -c approval_policy="never" \
  -c web_search="live" \
  -c project_doc_max_bytes=0 \
  -c skills.bundled.enabled=false \
  -c "mcp_servers.<server>={command=\"...\",args=[...],tools={\"<tool>\"={approval_mode=\"approve\"},...}}" \
  ... (one -c per role-injected MCP server)
  <<< <TASK_PROMPT>
```

CRITICAL:
- Pass `--ignore-user-config` (suppresses project AGENTS.md, skills, ambient-suggestions, memories — the persona body + injected MCP servers are the agent's entire world).
- `--ignore-rules` is NOT used. Per-dispatch rule enforcement happens via the hermetic `CODEX_HOME=<dir>/rules/default.rules` execpolicy file (one `prefix_rule(...,decision="forbidden")` per git-mutation verb + caller-supplied `bash_deny` patterns).
- Hermetic CODEX_HOME (`internal/backends/codex_hermetic.go newHermeticCodexHome`): per-dispatch temp dir containing (a) symlinks to `~/.codex/{auth.json,version.json,installation_id,models_cache.json}` for identity passthrough, (b) `rules/default.rules` forbidding 28 git-mutation verbs (`commit/push/add/reset/rebase/merge/checkout/branch/tag/stash/restore/cherry-pick/am/clean/switch/rm/mv/update-ref/gc/prune/worktree/submodule/init/clone/fetch/pull/remote/apply`) plus any caller `bash_deny` patterns; `defer cleanup()` removes the dir on Spawn return.
- The 4 hermetic `-c` flags are appended unconditionally after the TOML-defined args in `codexExecBackend.renderArgs`:
  - `approval_policy="never"` — required to make `workspace-write` sandbox non-interactive under `--sandbox`; `codex exec` has no `-a` flag.
  - `web_search="live"` — re-enables web search at dispatch (HOME config value is suppressed by `--ignore-user-config`).
  - `project_doc_max_bytes=0` — caps `AGENTS.md` / project-doc instruction budget to zero bytes.
  - `skills.bundled.enabled=false` — disables runtime-bundled codex skills so the agent's world is only persona + injected MCP.
- MCP injection: STATIC role-conditional via `RenderRoleConditionalMCPFlags(role, cwd)` in `internal/backends/mcp_inject.go`. No JSON-RPC `tools/list` probe at dispatch time — the per-tool `approval_mode = "approve"` map is materialized from the backend's declared tool list per role (ta always, hylla/context7/gopls non-build-qa, gopls go-only cwd=, playwright fe-only, build-qa = ta-only).
- Construct the `-c` TOML inline table via Go struct + `strconv.Quote` for key escaping (handles dots in tool names like `hylla.search.vector`). NEVER hand-build the escape-laden string.
- See `~/.claude/codex-mcp-dispatch-tool-conversion.md` for the canonical name-format translation rules.

### 8.4 ANTI_RECURSION constant

Appended to persona body before dispatch — prevents the spawned session from recursively calling `sand.dispatch`:

```
---
DISPATCH CONTEXT: You are the <ROLE> agent, dispatched via sand. Execute the task below directly using YOUR role-appropriate tools (the orchestrator restricts them per the persona's `tools:` allowlist). Do NOT call sand.dispatch. Do NOT use the Agent tool to spawn other roles. Do NOT route the task elsewhere. You ARE the role. The orchestrator coordinates further dispatches.
```

---

## 9. Error classification + auto-fallback

### 9.1 ErrClass enum

```go
// internal/dispatch/errors_class.go
type ErrClass int

const (
    ErrClassSuccess         ErrClass = iota
    ErrClassRateLimit       // HTTP 429, "rate limit exceeded", "quota exhausted"
    ErrClassAuthFailure     // HTTP 401/403, "invalid API key", "auth"
    ErrClassNetwork         // DNS/connection refused, "network is unreachable"
    ErrClassTimeout         // context.DeadlineExceeded, "deadline exceeded"
    ErrClassCrash           // signal-killed without graceful exit
    ErrClassUnknown         // catch-all — still triggers fallback under default policy
)

func ClassifyExitError(stderr []byte, exitCode int) ErrClass
```

### 9.2 Classifier patterns

```toon
classifier-rules{class,stderr-pattern,exit-code,examples}:
  ErrClassRateLimit,"429"|"rate limit"|"quota"|"too many requests",1|125,Anthropic 429|OpenAI 429
  ErrClassAuthFailure,"401"|"403"|"invalid api key"|"unauthorized"|"forbidden",1|125,missing ANTHROPIC_API_KEY
  ErrClassNetwork,"connection refused"|"network unreachable"|"dns",1|2,ollama daemon down
  ErrClassTimeout,"deadline exceeded"|"context canceled"|"timeout",124|125,ctx.DeadlineExceeded
  ErrClassCrash,empty stderr OR "killed"|"signal: ",137|139|143,SIGKILL/SIGSEGV
  ErrClassUnknown,anything else,any non-zero,catch-all
```

### 9.3 Default fallback policy

```toon
outcome-policy{outcome,action}:
  success,record + return Response populated from envelope
  slot_timeout,record + advance to next tier
  rate_limit,record + advance
  auth_failure,record + advance
  network,record + advance
  timeout,record + advance
  unsupported_backend,record + advance (backend not configured or impl missing)
  crash,record + HALT chain (unrecoverable)
  unknown,record + HALT chain
```

Per-tier `retry_on` (§5 tier fields) overrides this: when non-empty, ONLY listed outcomes advance; all others halt with a distinct "retry_on policy" wrapped error (NOT `ErrChainExhausted`).

When every tier records a non-success outcome the dispatch returns `ErrChainExhausted` with the full `fallback_chain` populated.

---

## 10. Envelope parsing

Backends declare their stdout dialect via `envelope_format` in backends.toml. The dispatcher selects the parser by calling `backend.EnvelopeFormat()` after spawn:

```toon
envelope-formats{format,parser,used-by}:
  claude_json,ParseEnvelope (internal/dispatch/envelope.go) — single JSON envelope with iterations array,claude-native + ollama-local + ollama-cloud + together-ai + any provider routed through `claude -p --output-format json`
  codex_stream,ParseCodexEnvelope (internal/dispatch/envelope_codex.go) — line-oriented stream parser for `mcp: server/tool (completed)` markers,codex-exec
```

Both parsers populate:
- `Envelope.Result` — agent's final text
- `Envelope.ToolsUsed` (aggregate map) → `Response.ToolsUsed[]`
- `Envelope.PermissionDenials` (aggregate map) → `Response.PermissionDenials[]`
- `Envelope.ToolCallsOrdered` (per-call slice with Index + Name + IsError) → `Response.ToolCalls[]`

Tool counts and tool-call ordering come from STRUCTURED EVENT RECORDS in the envelope, never from the agent's narrative claims.

---

## 11. Logging

Per-dispatch JSON log files at `/tmp/sand-dispatch/log/<uuid>.json` (drop_007 polish; currently deferred). Captures full envelope + stderr + exit_code + sand-side metadata.

```json
{
  "dispatch_id": "<uuid>",
  "role": "ta-go-planning",
  "started_at": "<RFC3339>",
  "completed_at": "<RFC3339>",
  "served_by": "claude-native:opus",
  "tier": 3,
  "fallback": true,
  "duration_ms": 168793,
  "raw_envelope": "<full Claude Code JSON OR codex raw stream>",
  "tools_used": {...},
  "permission_denials": {...},
  "task_prompt": "<full prompt string>",
  "persona_body_hash": "<sha256 of persona body>",
  "cwd": "/abs/path",
  "exit_code": 0
}
```

Rotation: reboot-volatile (`/tmp`). No retention policy needed. The TOON response to the orchestrator is the SUMMARY of this log; deep inspection = read `log_path`.

---

## 12. Build conventions

- 12.1 `magefile.go` at repo root. Targets: `mage install` (binary + config seeds to `$HOME/.local/bin/sand` + `~/.config/sand/{backends,chains}.toml`), `mage check` (FmtCheck + Vet + Test + Tidy composite), `mage fmt`, `mage fmtCheck`, `mage test`, `mage testPkg <path>`, `mage testFunc <pattern>`, `mage tidy`.
- 12.2 `gofumpt` (auto-installed by `mage fmt` if missing). NEVER raw `gofmt` / `gofumpt` / `go test` / `go vet` from dispatched roles — always route through mage. Orchestrators are the exception.
- 12.3 Module path: `github.com/evanmschultz/sand`.
- 12.4 Go version: 1.22+.
- 12.5 MCP framework: [mark3labs/mcp-go](https://github.com/mark3labs/mcp-go).
- 12.6 Dependencies (minimal): MCP framework, TOML (`BurntSushi/toml`), TOON encoder (sand ships one in `internal/toon`).
- 12.7 CI: `.github/workflows/ci.yml` runs `mage check` on push / PR to main with Go 1.22.

---

## 13. Hard rules — never violate

- 13.1 Sand is TRANSPORT ONLY. No workflow state. No cascade tracking. ta MCP stays the workflow substrate.
- 13.2 Sand RESPONSES are TOON-encoded with structured metadata + tool-usage counts from parsed events, but NEVER thinking blocks, conversation turns, or raw tool args/results.
- 13.3 Sand RESPECTS persona `tools:` allowlists. Pass them as `--allowedTools` to claude / via per-tool `approval_mode = "approve"` for codex. NEVER expand a persona's tool surface from sand.
- 13.4 Sand does NOT edit persona files. Read-only. Mutations go through `mcp__ta__update` on `claude_agents.agent` records.
- 13.5 Sand SHELLS OUT to claude CLI + codex CLI. Does NOT bundle a new Anthropic/OpenAI SDK. Inherits auth from those CLIs.
- 13.6 Sand passes `--skip-git-repo-check` + `--ignore-user-config` to codex. `--ignore-user-config` suppresses project AGENTS.md / skills / ambient-suggestions / memories so the persona body + injected MCP servers are the agent's entire world. `--ignore-rules` is NOT used — per-dispatch rule enforcement is via hermetic `CODEX_HOME=<dir>/rules/default.rules` execpolicy (28 git-mutation verbs + caller `bash_deny`). MCP servers are STATICALLY injected per role via `RenderRoleConditionalMCPFlags`, not via JSON-RPC `tools/list` probe — `--ignore-user-config` does not strip them because they were never read from `~/.codex/config.toml`.
- 13.7 Sand RUNS multiple dispatches in parallel via goroutines. Flock-based slot caps cap any tier with `slots > 0`; `slots = 0` is unlimited.
- 13.8 NO semver phrasing in sand's own docs, commits, or cascade records — work units are named by drop / cascade slug, not by version number.
- 13.9 If a needed capability is missing (e.g. MCP framework gap, codex CLI option absent), REPORT to dev and PAUSE. Do NOT silently work around.

---

## 14. Out of scope

- 14.1 Streaming responses back through MCP (no MCP streaming primitive yet).
- 14.2 Chain config hot-reload (sand re-reads per call, so file edits ARE live; no SIGHUP machinery needed).
- 14.3 Job-id based async dispatch (sand.dispatch is sync per-call; parallelism via concurrent MCP calls).
- 14.4 Workflow tracking (NEVER — ta MCP's job).
- 14.5 Cross-project chain sharing (each project owns its `.claude/sand-chains.toml`; system-default lives at `~/.config/sand/chains.toml`).
- 14.6 Custom MCP framework.
- 14.7 `chains_update` / `backends_update` MCP write tools (defer until TOML-editing friction justifies the API surface).

---

**End of spec.**
