# Agent Dispatch Model (sand keystone)

> **Status (2026-05-24):** This is the CANONICAL spec for the cascade agent-dispatch
> model. Today it is implemented by `bin/agent-dispatch.sh` + `.claude/agent-chains.sh`
> (the "bin/sh system"). **sand's job is to replace that bin/sh system**: when sand is
> working, the sand agent generates per-project agent-dispatch **TOML config** encoding
> this model, and sets it up in `hylla/polyglot-foundation`, `valv/main`, and `ta/main`.
> The ta-family repos (hylla, ta, valv, sand) use the **ta** cascade substrate; tillsyn
> uses the **tillsyn** substrate — see `tillsyn/main/AGENT_DISPATCH.md`, which mirrors this
> doc on the tillsyn side. Whoever builds + confirms the skills/hooks→codex translation
> logic FIRST hands it off to the other.

## 1. Agent shape

Per language family, six roles + one shared closeout:

- `planning`, `plan-qa-proof`, `plan-qa-falsification`, `builder`, `build-qa-proof`, `build-qa-falsification`
- plus one shared `closeout`.

QA is **always** a proof/falsification **pair**, for **both** the plan axis and the build
axis, **specialized** per language (`fe-*` and `go-*`).

- **FE+Go repos** (hylla, ta) = **13**: closeout + 6 `fe-*` + 6 `go-*`.
- **Go-only repos** (valv, sand) = **7**: closeout + 6 `go-*`.
- In `~/.ta` the home-library copies are UNprefixed (`fe-build-qa-proof.md`); in a project
  install they are flat-prefixed (`ta-fe-build-qa-proof.md`) and the `name:` frontmatter
  matches the filename stem.

## 2. Per-persona tool matrix

| Role | hylla(ro) | context7 | gopls | playwright | WebSearch | ta |
|---|---|---|---|---|---|---|
| planning (fe/go) | ✅ read | ✅ | go only | fe only | ✅ | create+update |
| plan-qa-proof / -falsification (fe/go) | ✅ read | ✅ | go only | fe only | ✅ | update (verdict) |
| builder (fe/go) | ✅ read | ✅ | go only | fe only | ✅ | update (comment) |
| build-qa-proof / -falsification (fe/go) | ❌ **none** | ✅ | go only | fe only | ✅ | update (verdict) |
| closeout | ❌ | ✅ | — | — | ✅ | update |

- **planning + plan-QA ALWAYS get hylla READ-ONLY.** **build-QA gets NO hylla** (just-shipped
  code isn't in the Hylla snapshot yet — build-QA relies on `git diff` + LSP/Read).
- `gopls` = the `LSP` tool for claude-native roles; an injected `gopls mcp` server for codex.
- `playwright` = the `@playwright/mcp` browser tools; FE roles only.

## 3. Dual-path dispatch (LOAD-BEARING)

Pick the dispatch channel by the tier's auth model — never mix:

- **OAuth / subscription claude** (the default claude-native roles: builder=haiku,
  qa-proof=opus, closeout=opus) → **built-in Agent tool ONLY** (`Agent(subagent_type=<role>)`).
  **NEVER `claude -p` with OAuth.** Isolation = the persona `tools:` allowlist (the subagent
  keeps the orchestrator's loaded plugins like Playwright, but can only CALL allowlisted
  tools). The chain row is a model hint for the orchestrator's Agent-tool dispatch.
- **codex-exec** (planning, qa-falsification) → hermetic `codex exec` (Section 4).
- **Non-OAuth claude** (ollama via `ANTHROPIC_BASE_URL`, or an explicit API key) →
  `claude -p --bare` + `--mcp-config` injecting the role's tools (`--bare` strips plugins +
  LSP, so playwright/gopls/context7/ta/hylla must be injected as MCP servers) + `--allowedTools`.
  *(Ollama is currently removed from the chains; this path is dormant but specified.)*

`claude -p` is reserved for non-OAuth endpoints. A dispatcher that receives an OAuth
claude-native role must **refuse** and tell the caller to use the Agent tool.

## 4. Hermetic codex requirements

`codex exec` must load NOTHING from `~/.codex` except auth + identity:

- `--ignore-user-config` (skips `$CODEX_HOME/config.toml`).
- `-c project_doc_max_bytes=0` (suppresses ALL `AGENTS.md`, global + project — there is no
  disable flag; codex #14316).
- `--ignore-rules` (skips `.rules` execpolicy).
- **Hermetic `CODEX_HOME`**: run codex with `CODEX_HOME=<throwaway tmp dir>` containing only
  `auth.json` / `version.json` / `installation_id` / `models_cache.json` symlinked from the
  real `~/.codex`. This removes global `skills/`, `hooks`, `plugins/`, `memories/`, `rules/`.
  *(Caveat: codex BUNDLED skills — imagegen / openai-docs / plugin-creator / skill-creator /
  skill-installer — ship with the install, not under `~/.codex`, so they remain visible but
  INERT; no clean disable yet, codex #14316.)*
- `-c web_search="live"` (re-enable web search lost with the config) or `--search`.
- **Role-conditional inline MCP injection** via repeated `-c "mcp_servers.<name>={…}"`, with
  per-tool `approval_mode="approve"` (server-level auto-approve is NOT honored — codex #16501):
  - `ta` — always (stdio: `ta --project <cwd>`).
  - `hylla` — planning + plan-qa ONLY, read-only tool set (stdio: `hylla mcp`); **skip for `*build-qa*`**.
  - `context7` — always (HTTP: `url="https://mcp.context7.com/mcp"`, header→`CONTEXT7_API_KEY` env var).
  - `gopls` — `*-go-*` roles (stdio: `gopls mcp`, `cwd=<dispatch dir>`, 6 approve-moded tools).
  - `playwright` — `*-fe-*` roles (stdio: `playwright-mcp --headless --isolated`).

Verified live: context7 + Playwright both work under this hermetic config; the global
`~/.codex/skills/playwright` no longer loads (the injected MCP is used instead); the global
Tillsyn `AGENTS.md` no longer leaks.

## 5. Capabilities come from MCP injection, NOT skills

Codex skills are the WRONG mechanism here: project-local skill discovery (`.agents/skills`)
is unreliable (codex #21907), and our agents are persona-complete (the persona body is the
full instruction set). The Playwright **capability** is the injected `@playwright/mcp` MCP,
NOT the `playwright` SKILL.md. **Nothing needs to be copied into `.agents/skills` or `.codex/`.**
`@playwright/mcp` should be a GLOBAL npm install (`npm i -g @playwright/mcp@latest`) so the
daily `npm update -g` maintains it; the Playwright browsers live in `~/Library/Caches/ms-playwright`.

## 6. Personas are generic; the orchestrator passes the endpoint

Personas NEVER hardcode a live-backend URL. They say "the URL the orchestrator provides in
your spawn prompt." Each project's CLAUDE.md is the source of truth for its endpoint, and the
orchestrator reads it and passes it into the spawn prompt when dispatching any FE agent that
drives Playwright. Per-project endpoints to encode:

- hylla/polyglot-foundation: Wails `http://localhost:34917`; web viewer `4347`; bare Astro `4348` (binding-less, never target).
- ta/main: web viewer (confirm port in ta CLAUDE.md).
- tillsyn/main: Wails `34115`; bare Astro `51428`.
- valv/main, sand/main: Go-only (no FE endpoint).

## 7. Skills/hooks → codex translation logic (sand + tillsyn)

The end-state is **project-local** skills/hooks/MCP so hermetic headless codex ignores ALL
global state and still has what it needs. sand (and tillsyn, on its side) need logic to
TRANSLATE Claude Code skills/hooks into codex-compatible forms (or, preferably, into the MCP
injections of Section 4). sand and `tillsyn/main/AGENT_DISPATCH.md` cross-reference each other:
**whoever builds + confirms this translation logic first hands it off to the other.** `ta init`
(TUI select) is the intended surface for initializing a project's agent-dispatch config.

## 8. Worked examples (the TOML sand generates)

### 8a. FE+Go project (e.g. hylla, ta) — 13 agents
```toml
[project]
agents = ["closeout","fe-planning","fe-plan-qa-proof","fe-plan-qa-falsification","fe-builder","fe-build-qa-proof","fe-build-qa-falsification","go-planning","go-plan-qa-proof","go-plan-qa-falsification","go-builder","go-build-qa-proof","go-build-qa-falsification"]
live_backend_url = "http://localhost:34917"   # orch passes this into FE spawn prompts

[routing]                                      # role -> channel + tier
planning             = { channel = "codex",        model = "gpt-5.4", effort = "high", sandbox = "read-only" }
"plan-qa-proof"      = { channel = "agent-tool",   model = "opus" }                  # OAuth, built-in
"plan-qa-falsification" = { channel = "codex",     model = "gpt-5.4", effort = "high", sandbox = "workspace-write" }
builder              = { channel = "agent-tool",   model = "haiku" }                 # OAuth, built-in
"build-qa-proof"     = { channel = "agent-tool",   model = "opus" }                  # OAuth, built-in
"build-qa-falsification" = { channel = "codex",    model = "gpt-5.4", effort = "high", sandbox = "workspace-write" }
closeout             = { channel = "agent-tool",   model = "opus" }

[codex.hermetic]                               # Section 4
ignore_user_config = true
project_doc_max_bytes = 0
ignore_rules = true
codex_home = "ephemeral-symlink-auth-only"
web_search = "live"

[codex.mcp]                                    # role-conditional injection
ta        = { scope = "all" }
hylla     = { scope = "planning,plan-qa", mode = "read-only" }
context7  = { scope = "all", transport = "http" }
gopls     = { scope = "*-go-*" }
playwright = { scope = "*-fe-*", command = "playwright-mcp", args = ["--headless","--isolated"] }
```

### 8b. Go-only project (e.g. valv, sand) — 7 agents
Same as 8a but: `agents = ["closeout", 6× go-*]`, no `live_backend_url`, no `playwright` MCP,
no `fe-*` routing.

## 9. How sand sets up the projects

When working, the sand agent (via `ta init` / TUI select) generates the above TOML per
project and replaces `bin/agent-dispatch.sh` + `.claude/agent-chains.sh`:
`hylla/polyglot-foundation` (13, FE+Go, endpoint 34917), `ta/main` (13, FE+Go), `valv/main`
(7, Go-only), `sand/main` (7, Go-only). Until then, the bin/sh system at each repo IS this
model and is kept in sync from `ta/main` (the canonical copy).
