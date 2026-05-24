# sand

Go MCP server for headless agent dispatch. Routes role-based prompts to
ollama / codex / claude-native backends with per-role fallback chains, a
kernel-flock cross-process slot subsystem, and a token-oriented (TOON)
response envelope.

Sand replaces the shell-script agent dispatchers that orchestrators
previously shelled out to (e.g. `ta/bin/agent-dispatch.sh`).

## What sand does

For each MCP `dispatch` call sand:

1. Loads the caller project's role persona from
   `<cwd>/.claude/agents/<role>.md` (YAML frontmatter + system-prompt body).
2. Resolves the role's fallback chain from `sand-chains.toml`
   (project rung → XDG → `~/.config/sand` → `~/.sand`, first hit wins).
3. Walks the chain tier-by-tier:
   - Optionally acquires a cross-process slot via `syscall.Flock` on
     `/tmp/sand-slots/<backend>/<model>/slot.<N>.lock` when `tier.slots > 0`.
   - Resolves the backend via `backends.toml` and `Spawn`s the configured CLI.
   - Classifies the outcome (success, rate_limit, network, timeout, auth,
     crash, unknown) and either advances or halts per the chain policy.
4. Parses the spawned CLI's stdout envelope (`claude_json` or `codex_stream`)
   into a typed `Response`, encodes it as TOON, and returns to the MCP caller.

Every dispatch attempt — successful or not — appears as one row in the
returned `fallback_chain` table for full audit visibility.

## Install

Requires Go 1.22+ and `mage`.

```bash
git clone https://github.com/evanmschultz/sand.git
cd sand
mage install      # builds ~/.local/bin/sand + seeds ~/.config/sand/{backends,chains}.toml
```

`mage install` is idempotent and NEVER overwrites existing config files —
your customised `backends.toml` and `chains.toml` are preserved byte-for-byte.

## MCP wiring

Add sand to your project's `.mcp.json` and pin it to that project's root:

```json
{
  "mcpServers": {
    "sand": {
      "command": "sand",
      "args": ["--project", "/abs/path/to/your/project"]
    }
  }
}
```

The `--project` argument tells sand-the-server where to read personas and
project-local `sand-chains.toml` / `sand-backends.toml` overrides from.

## The 4 MCP tools

```toon
sand-tools{tool,purpose,arguments}:
  dispatch,Run a role's chain end-to-end and return the agent's TOON envelope,role|prompt|cwd?|model_override?|dry_run?
  preflight,Render the would-be argv + env + stdin for a role without spawning,role|prompt?|cwd?|model_override?
  persona_get,Return one role's parsed persona (frontmatter + body) for inspection,role|cwd?
  chains_list,Return every configured role's full fallback chain as TOON,role? (omit for all)
```

All 4 tools are READ-ONLY relative to your config. To change chains or
backends today you edit the TOML files directly — sand re-reads them on
every dispatch, so no restart is needed for config changes to take effect.

## Config files

```toon
config-locations{file,scope,purpose,hierarchical-resolution}:
  sand-chains.toml,per-role fallback chains,which backend+model serves each role,project<cwd>/.claude/ → $XDG_CONFIG_HOME/sand → ~/.config/sand → ~/.sand
  sand-backends.toml,per-backend spawn templates,how to invoke each backend CLI,same hierarchy as chains
  <cwd>/.claude/agents/<role>.md,role persona,YAML frontmatter (name/description/model/tools) + markdown body used as system prompt,per-project only
  <cwd>/.mcp.json,caller's declared MCP servers,handed to spawned agents as their MCP context,per-project only
```

First-hit-wins resolution means a project-rung file fully overrides the
home-rung file (no merge). Drop a `~/.config/sand/chains.toml` for your
system default; drop `.claude/sand-chains.toml` in a project only when that
project needs a different chain.

### sand-chains.toml shape

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
  backend,must match a [backends.NAME] entry,required
  model,backend-specific model identifier,required
  slots,cross-process concurrency cap (0 = unlimited),0
  wait_max,seconds to wait for a slot before advancing,0
  opts,opaque extra CLI flags forwarded to the backend command,empty
```

### sand-backends.toml shape

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

Template substitutions inside string values:

```toon
template-vars{var,resolved-to}:
  {{.Model}},the chain tier's model (override-aware)
  {{.Cwd}},caller project absolute path
  {{.PersonaBody}},loaded persona markdown body (system prompt)
  {{.PersonaToolsCSV}},persona Tools slice joined with commas
  {{.McpConfigPath}},<cwd>/.mcp.json when present, else empty
  {{env "VAR"}},process environment variable lookup
```

Adding a new provider (e.g. groq, openrouter, anthropic-vertex) is a TOML
edit only — no Go code change required. The seeded `~/.config/sand/backends.toml`
ships with 4 commented example blocks (ollama-local, ollama-cloud,
codex-exec, together-ai) you can uncomment-and-go.

## Persona format

Personas live in `<project>/.claude/agents/<role>.md`. YAML frontmatter
declares the role's tool allowlist; the markdown body becomes the spawned
agent's system prompt.

```markdown
---
name: ta-go-builder
description: Build Go code with TDD, idiomatic error handling...
model: haiku
tools: Read, Edit, Write, Grep, Glob, Bash(mage testFunc *), Bash(mage testPkg *), Bash(git diff *), LSP
---

You are a Go builder. ...
```

The `tools:` line is passed as `--allowedTools` to the spawned claude CLI
(or translated to per-tool overrides for codex). The persona file IS the
sandbox spec for that role.

## Slot subsystem

Slots cap concurrent spawns against a `(backend, model)` pair across
EVERY sand process on the machine. Useful when you want hard ceilings on
expensive or rate-limited backends:

- `slots = 0` — unlimited (skips the filesystem entirely).
- `slots = 1` — at most one spawn for this `(backend, model)` system-wide.
- `slots = 3` — at most three spawns concurrent.

Implementation: `syscall.Flock(LOCK_EX|LOCK_NB)` on
`/tmp/sand-slots/<backend>/<model-slug>/slot.<N>.lock`. Locks auto-release
when the holding process exits (kernel-managed; SIGKILL-safe). On
acquisition failure sand polls every 100ms until `wait_max` seconds, then
advances to the next tier with `outcome = "slot_timeout"`.

## Fallback semantics

Per-tier outcome classification routes the chain walk:

```toon
outcome-policy{outcome,action}:
  success,record + return Response populated from envelope
  slot_timeout,record + advance to next tier
  rate_limit,record + advance
  auth_failure,record + advance
  network,record + advance
  timeout,record + advance
  unsupported_backend,record + advance (backend not configured or not impl yet)
  crash,record + HALT chain (unrecoverable)
  unknown,record + HALT chain
```

When every tier records a non-success outcome the dispatch returns
`ErrChainExhausted` with the full `fallback_chain` populated.

## Backends shipped

```toon
backends{name,kind,cli,envelope,activation}:
  claude-native,baseline,claude -p --bare --output-format json,claude_json,active by default in seeded backends.toml
  ollama-local,claude CLI pointed at local ollama daemon (http://localhost:11434),claude --model qwen3-coder:30b,claude_json,uncomment in backends.toml; needs ollama daemon running
  ollama-cloud,claude CLI pointed at ollama.com API,claude --model qwen3-coder-cloud-235b,claude_json,uncomment in backends.toml; needs OLLAMA_API_KEY
  codex-exec,codex exec --ephemeral with per-MCP inline TOML injection,codex exec -m gpt-5.4,codex_stream,uncomment in backends.toml; needs codex CLI + ~/.codex/auth.json
  together-ai,claude CLI pointed at together.xyz endpoint,claude --model <together-model>,claude_json,uncomment in backends.toml; needs TOGETHER_API_KEY
```

The codex backend probes each declared MCP server's `tools/list` over JSON-RPC
at dispatch time (5s timeout per server) and injects the discovered tools
as inline TOML `-c` overrides with `approval_mode = "approve"` so the
spawned codex pre-approves them. See
`~/.claude/codex-mcp-dispatch-tool-conversion.md` for the canonical
translation rules.

## Troubleshooting

```toon
common-issues{symptom,cause,fix}:
  ErrChainExhausted on every dispatch,no backend in chain resolves (all unknown or unsupported),check sand-backends.toml has [backends.NAME] entries matching every tier's backend field
  ErrUnknownBackend wrapping a tier,tier names a backend NOT defined in backends.toml,add the [backends.NAME] block or correct the tier name
  ErrUnsupportedEnvelopeFormat,envelope_format value has no Backend impl,supported values: claude_json, codex_stream — typo or future format
  slot_timeout on every attempt,another process holding the lock + wait_max too short,raise wait_max or check `lsof /tmp/sand-slots/<backend>/<model>/slot.*.lock`
  auth_failure on ollama-cloud,OLLAMA_API_KEY missing or invalid,export OLLAMA_API_KEY; restart MCP server is NOT needed (re-read per call)
  codex tier hangs at dispatch,MCP server probe timing out,check sand stderr for `probe %s: deadline exceeded`; raise probe timeout in codex_mcp_probe.go if a legitimate slow server
  Response.tool_calls always empty for codex tier,known gap — codex_stream parser populates aggregate maps only,drop_007 polish wires ordered breakdown
```

## License

MIT — see [LICENSE](LICENSE).

## Design docs

- [SAND-SPEC.md](SAND-SPEC.md) — canonical design: 10 sections covering MCP tools, TOON envelope, chain config schema, backend templates, slot subsystem, error classification, dispatch matrix.
- [CLAUDE.md](CLAUDE.md) — project guidance for orchestrators working on sand itself.
