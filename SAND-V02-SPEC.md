# SAND-V02-SPEC — Design doc for sand v0.2+

> Extends [`SAND-SPEC.md`](SAND-SPEC.md). Captures the v0.2+ feature set: cross-project slot subsystem, hierarchical chains config, error classification with auto-fallback, user-configurable backend templates, ollama + codex backends, and extended TOON response shape.

## 0. TL;DR For The v0.2 Build

```toon
v02-features{feature,drop,user-impact}:
  Cross-project slot subsystem via flock,drop_008,1 ollama-local + 3 ollama-cloud globally enforceable across all projects
  Hierarchical chains config,drop_008,project override + ~/.config/sand/chains.toml system default
  Error classification + auto-fallback,drop_008,sand catches 429/401/timeout and advances chain automatically
  Extended TOON response (fallback_chain + tool_calls),drop_008,calling agent gets full audit trail per dispatch
  User-configurable backend templates,drop_011,add new providers (together.ai/openrouter/etc.) via ~/.config/sand/backends.toml — zero Go code
  Ollama backend (local + cloud),drop_004,routes through claude CLI with ANTHROPIC_BASE_URL pointing at ollama daemon/cloud API
  Codex backend + per-MCP tool translation,drop_005,routes through codex exec --ephemeral; injects MCP servers with conversion-doc-compliant tool names
  Polish (per-dispatch JSON log files + cooldown state + dry-run polish),drop_007,full audit trail at /tmp/sand-dispatch/log/<uuid>.json
```

## 1. Cross-Project Slot Subsystem (drop_008)

### 1.1 Mechanism: `flock(2)` on per-slot files

Slot directory: `/tmp/sand-slots/<backend>/<model-slug>/slot.<N>.lock`. One file per slot index. Acquire via `syscall.Flock(fd, LOCK_EX|LOCK_NB)`. Release via `fd.Close()`.

```toon
slot-properties{property,implication}:
  Kernel-managed lock,Auto-released on process death — SIGKILL safe, no PID-liveness polling needed
  Atomic acquire,Multiple sand processes race-free even with simultaneous mkdir
  Cross-platform,macOS BSD flock + Linux flock both work via syscall.Flock — no Windows support (sand is dev-mac/linux only)
  /tmp clears on reboot,Stale locks from previous boot auto-vanish — no explicit cleanup needed
  Slot file payload,Optional metadata (pid|acquired_at|dispatch_id) for `sand status` debug; NOT load-bearing for correctness
```

### 1.2 Public API

```go
// internal/slots/slot.go
package slots

type Slot struct {
    Backend  string
    Model    string
    Index    int  // 1..N
    // unexported: fd *os.File
}

func (s *Slot) Release()

// AcquireSlot probes slot files 1..N for the given backend+model. Returns
// the acquired Slot or ErrSlotTimeout if all slots remain busy for waitMax.
// slots=0 means unlimited — function returns nil, nil (no slot tracked).
func AcquireSlot(backend, model string, slots int, waitMax time.Duration) (*Slot, error)

var ErrSlotTimeout = errors.New("all slots busy after wait_max")
```

### 1.3 Chain config integration

Chains.toml tier records gain `slots` + `wait_max` semantics:

```toml
"ta-go-builder" = [
  { backend = "ollama-cloud", model = "qwen3-coder-cloud-235b", slots = 3, wait_max = 10, opts = "" },
  { backend = "ollama-local", model = "qwen3-coder:30b",        slots = 1, wait_max = 30, opts = "" },
  { backend = "codex-exec",   model = "gpt-5.5",                slots = 0, wait_max = 0,  opts = "--sandbox workspace-write" },
  { backend = "claude-native", model = "haiku",                 slots = 0, wait_max = 0,  opts = "" },
]
```

`slots = 0` → unlimited (skip the AcquireSlot call entirely). Provider rate-limits handled reactively via error classification (see §3).

### 1.4 Dispatch flow with slots

```
for tier in chain:
  if tier.slots > 0:
    slot, err := AcquireSlot(tier.backend, tier.model, tier.slots, tier.wait_max)
    if errors.Is(err, ErrSlotTimeout):
      record_attempt(tier, "slot_timeout", "all slots busy for <wait_max>s")
      continue
    defer slot.Release()
  
  err := spawn(tier)
  errClass := ClassifyExitError(stderr, exitCode)
  
  switch errClass {
  case ErrClassSuccess:
    record_attempt(tier, "success", "")
    return response
  case ErrClassRateLimit, ErrClassNetwork, ErrClassTimeout, ErrClassAuthFailure:
    record_attempt(tier, errClass.String(), errClass.Reason(stderr))
    continue
  default:
    record_attempt(tier, "unknown_error", err.Error())
    return err  // unrecoverable for this dispatch
  }
}
return ErrChainExhausted
```

## 2. Hierarchical Chains Config (drop_008)

### 2.1 Resolution order

```toon
resolution-order{rung,path,when-it-wins}:
  1,$XDG_CONFIG_HOME/sand/chains.toml,XDG_CONFIG_HOME env var is set
  2,$HOME/.config/sand/chains.toml,default XDG location (Linux + macOS modern apps)
  3,$HOME/.sand/chains.toml,dotfile fallback for users who prefer it
  4,<projectDir>/.claude/sand-chains.toml,PROJECT OVERRIDE — wins if present
```

**Project override wins.** Resolution checks project first; if absent, falls through to system defaults in order 1-3.

If none of the 4 exist, sand returns `ErrChainConfigNotFound` with a list of paths checked — never invents implicit defaults.

### 2.2 Merge or replace?

**REPLACE.** Project config (when present) entirely supersedes system defaults. No deep merge. Predictable + simple.

If a user wants project-level overrides for ONE role while keeping system defaults for others, they should copy the system file as a starting point. Future work could add a `[extends]` directive, but v0.2 is replace-only.

### 2.3 `mage install` seeds `~/.config/sand/chains.toml`

On first install, mage Install creates `~/.config/sand/chains.toml` from a packaged template if the file is missing. Template = current `.claude/sand-chains.toml` content. Like ta seeds `~/.ta/schema.toml`.

Never overwrite existing system config.

## 3. Error Classification + Auto-Fallback (drop_008)

### 3.1 ErrClass enum

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
    ErrClassUnknown         // catch-all — still triggers fallback
)

func ClassifyExitError(stderr []byte, exitCode int) ErrClass
```

### 3.2 Classifier patterns

Pattern matching on stderr text + exit codes per provider:

```toon
classifier-rules{class,stderr-pattern,exit-code,examples}:
  ErrClassRateLimit,"429"|"rate limit"|"quota"|"too many requests",1|125,Anthropic 429|OpenAI 429
  ErrClassAuthFailure,"401"|"403"|"invalid api key"|"unauthorized"|"forbidden",1|125,missing ANTHROPIC_API_KEY
  ErrClassNetwork,"connection refused"|"network unreachable"|"dns",1|2,ollama daemon down
  ErrClassTimeout,"deadline exceeded"|"context canceled"|"timeout",124|125,ctx.DeadlineExceeded
  ErrClassCrash,empty stderr OR "killed"|"signal: ",137|139|143,SIGKILL/SIGSEGV
  ErrClassUnknown,anything else,any non-zero,catch-all
```

### 3.3 Fallback policy (per chain tier)

Tier records gain optional `retry_on` field listing ErrClass values that trigger advancing to the next tier. Empty list = use defaults: `[RateLimit, Network, Timeout, AuthFailure]` advance; `Crash` + `Unknown` halt.

```toml
{ backend = "ollama-cloud", model = "qwen3-coder-cloud-235b", slots = 3, wait_max = 10, retry_on = ["rate_limit", "timeout", "network"] }
```

## 4. Extended TOON Response Shape (drop_008)

Building on SAND-SPEC §3.1:

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

`fallback_chain[N]` is the new audit field. Always populated even on success (will have a single row with outcome=success).

`tool_calls[N]` is the new per-call ordered breakdown (drop_007 adds the full log_path file write).

## 5. User-Configurable Backend Templates (drop_011)

### 5.1 New file: `~/.config/sand/backends.toml`

```toml
# Per-backend spawn templates. Each entry defines how to build the
# os/exec command for a backend KIND. Chains.toml references these
# entries by name in the `backend = "X"` field.
#
# Template substitution: {{model}}, {{cwd}}, {{persona_body}},
# {{persona_tools_csv}}, {{mcp_config_path}}, {{env:VAR}}, {{prompt}}
# (via stdin pipe by default — listed here for completeness).

[backends.claude-native]
command = "claude"
args = ["-p", "--bare", "--model", "{{model}}", "--output-format", "json", "--no-session-persistence", "--append-system-prompt", "{{persona_body}}"]
mcp_config_arg = "--mcp-config"             # appended with mcp.json path when project has .mcp.json
allowed_tools_arg = "--allowedTools"
allowed_tools_csv_template = "{{persona_tools_csv}}"
slots_default = 0                            # provider rate-limits
envelope_format = "claude_json"              # parser hint — see §5.3
stdin_prompt = true                          # prompt goes to subprocess stdin

[backends.ollama-local]
command = "claude"                           # routes through Claude Code CLI with ollama endpoint
env = [
  "ANTHROPIC_BASE_URL=http://localhost:11434",
  "ANTHROPIC_API_KEY=ollama",                # any non-empty value
]
args = ["-p", "--bare", "--model", "{{model}}", "--output-format", "json", "--no-session-persistence", "--append-system-prompt", "{{persona_body}}"]
mcp_config_arg = "--mcp-config"
allowed_tools_arg = "--allowedTools"
allowed_tools_csv_template = "{{persona_tools_csv}}"
slots_default = 1                            # SINGLE local model at a time
envelope_format = "claude_json"
stdin_prompt = true

[backends.ollama-cloud]
command = "claude"
env = [
  "ANTHROPIC_BASE_URL=https://ollama.com/api",
  "ANTHROPIC_API_KEY={{env:OLLAMA_API_KEY}}",
]
args = ["-p", "--bare", "--model", "{{model}}", "--output-format", "json", "--no-session-persistence", "--append-system-prompt", "{{persona_body}}"]
mcp_config_arg = "--mcp-config"
allowed_tools_arg = "--allowedTools"
allowed_tools_csv_template = "{{persona_tools_csv}}"
slots_default = 3
envelope_format = "claude_json"
stdin_prompt = true

[backends.codex-exec]
command = "codex"
args = ["exec", "--ephemeral", "--ignore-rules", "--skip-git-repo-check", "-C", "{{cwd}}", "-m", "{{model}}"]
mcp_injection = "codex_inline_toml"           # special-case per ~/.claude/codex-mcp-dispatch-tool-conversion.md
slots_default = 0                             # codex API rate-limits
envelope_format = "codex_stream"              # parser hint — different from claude
stdin_prompt = true

# Add new providers by adding [backends.NAME] blocks. No Go code change.
[backends.together-ai]
command = "claude"
env = [
  "ANTHROPIC_BASE_URL=https://api.together.xyz/v1",
  "ANTHROPIC_API_KEY={{env:TOGETHER_API_KEY}}",
]
args = ["-p", "--bare", "--model", "{{model}}", "--output-format", "json", "--no-session-persistence", "--append-system-prompt", "{{persona_body}}"]
mcp_config_arg = "--mcp-config"
allowed_tools_arg = "--allowedTools"
slots_default = 0
envelope_format = "claude_json"
stdin_prompt = true
```

### 5.2 Resolution order for backends.toml

Same hierarchy as chains.toml (§2.1):
1. `$XDG_CONFIG_HOME/sand/backends.toml`
2. `$HOME/.config/sand/backends.toml`
3. `$HOME/.sand/backends.toml`
4. `<projectDir>/.claude/sand-backends.toml`

Project override wins. `mage install` seeds the system file with the 3 baseline backends (claude-native + ollama-* + codex-exec).

### 5.3 Envelope format hint

`envelope_format` tells the dispatcher how to parse the spawned CLI's output:
- `claude_json`: `claude -p --output-format json` envelope (already implemented in `internal/dispatch/envelope.go`).
- `codex_stream`: codex exec output is line-oriented stream, not a single JSON object. drop_005 adds parser.
- Future: `together_chat`, `openrouter_completion`, etc. can be added as plugin parsers.

### 5.4 Templating engine

Use Go `text/template` for substitution. Custom funcs:
- `{{model}}` → tier's model
- `{{cwd}}` → caller project dir
- `{{persona_body}}` → loaded persona's Body
- `{{persona_tools_csv}}` → persona's Tools joined with `,`
- `{{mcp_config_path}}` → `<cwd>/.mcp.json` if it exists, else empty
- `{{env:VAR}}` → expanded from process environment
- `{{prompt}}` → the dispatch prompt (rarely used; usually goes to stdin)

## 6. Ollama Backend (drop_004)

Once §5 backend templates land, ollama-local + ollama-cloud are just two `[backends.X]` entries (already shown in §5.1).

The dispatcher uses the template — no special-case code for ollama. The only ollama-specific logic is in `internal/preflight` (already shipped: HTTP GET `/api/version` + `ollama list` parse) which is REPURPOSED for ollama health checks per tier.

## 7. Codex Backend (drop_005)

### 7.1 Spawn command per template

See §5.1 `[backends.codex-exec]`. Standard `codex exec --ephemeral` invocation.

### 7.2 MCP injection (special-case per conversion doc)

Codex needs `-c "mcp_servers.<server>={...}"` inline TOML overrides per `~/.claude/codex-mcp-dispatch-tool-conversion.md`. For each MCP server declared in the caller's `.mcp.json`, sand:

1. Spawns the MCP server briefly to probe its `tools/list` via JSON-RPC.
2. Captures the canonical tool names from the response.
3. Builds the inline TOML: `mcp_servers.<server>={command="...", args=[...], tools={"<tool1>"={approval_mode="approve"}, ...}}`.
4. Appends as `-c` flags to the codex command.

For tools with dots (e.g. `hylla.search.vector`), keys are quoted. For bare snake_case (e.g. `get`), unquoted. Per the doc.

`approval_mode = "approve"` per-tool — empirically the only form that pre-approves under `--ephemeral`.

### 7.3 Tool-call audit

Codex emits `mcp: <server>/<tool> (completed)` lines in its stream output. The codex_stream envelope parser extracts these into the `tools_used[N]{name,count}` aggregate + `tool_calls[N]` ordered list.

## 8. Drop_007 Polish

- 8.1 Per-dispatch JSON log files at `/tmp/sand-dispatch/log/<uuid>.json` (full envelope + stderr + exit_code + sand-side metadata).
- 8.2 Cooldown state: when a tier returns `ErrClassRateLimit`, sand marks it cooling-down (default 60s, override via `Retry-After` header if present) — subsequent dispatches in the same sand process skip without probing. In-process state; lost on restart.
- 8.3 Dry-run polish: render the full would-be argv + env + stdin shape as TOON, not just placeholder text.

## 9. Build Sequencing

```toon
build-sequencing{drop,blocks-on,can-parallel-with}:
  drop_008,nothing (drop_003 already done),—
  drop_011,drop_008,—
  drop_004,drop_008+drop_011,drop_005
  drop_005,drop_008+drop_011,drop_004
  drop_007,drops 003-005+011,—
```

drop_008 ships first. drop_011 ships next (foundational refactor — drops 004/005 plug into it). Then drops 004 + 005 in parallel (different backends, disjoint file scopes). drop_007 last.

## 10. Cascade Methodology Reminder

Per the project's cascade methodology (sand inherits from ta — see `../ta/main/docs/cascade-methodology.md`):

- Every L1 drop → L2 planner → ≥1 L3 planners (per concern) → terminal builder droplets (1-2 small blocks + tests).
- Plan-QA twins (proof + falsification) at every planner level.
- Build-QA twins per builder droplet.
- Closeout + commit after L1 drop's `mage check` passes.

For v0.2 drops, prefer dispatch via `mcp__sand__dispatch` (claude-native primary) for builder + qa-proof + closeout. Use bash dispatcher OR Agent tool subagent_type for planning + qa-falsification (codex-only chains until drop_005 lights up codex in sand).
