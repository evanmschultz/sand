# CLAUDE.md — project guidance for sand

Project-local guidance for working inside the `sand` tree. Global rules (Tillsyn coordination, Section 0 semi-formal reasoning, evidence sources, worktree hygiene, output style) live at `~/.claude/CLAUDE.md` and are NOT duplicated here.

Sand is a Go MCP server that exposes 4 tools (`dispatch`, `preflight`, `persona_get`, `chains_list`) for routing role-based prompts to ollama / codex / claude-native backends with fallback chains. Full design: [`SAND-SPEC.md`](SAND-SPEC.md). It replaces the bash dispatcher in [`../ta/main/bin/agent-dispatch.sh`](../../ta/main/bin/agent-dispatch.sh).

## Sand-the-server vs. sand-the-project (load-bearing invariant)

These two things are easy to confuse:

- **sand-the-server** = the running `sand` binary, pinned to a caller project via `--project <abs-path>`. It reads persona definitions from `<caller-project>/.claude/agents/<role>.md` AT DISPATCH TIME, parses the YAML frontmatter (`name` / `description` / `model` / `tools`), converts the markdown body into a system prompt, and spawns a headless agent (claude `-p` / `codex exec` / ollama-routed claude). It carries NO persona registry. It carries NO defaults.
- **sand-the-project** = THIS repo. It has its own `.claude/agents/` (5 Go-flavored personas + closeout, copied from `~/.ta/agents/ta/`) because cascades that target sand's OWN code go through them. These personas are NOT shipped with the binary.

Implication for design: persona resolution in sand source = `filepath.Join(projectDir, ".claude", "agents", role+".md")`. Never bake persona content into the binary. Never look up persona content from `~/.ta/agents/` or `~/.claude/agents/` — the project tree is the only source of truth.

## Bootstrap reality — sand builds via ta's dispatcher

Sand does not exist yet as a binary. During the v0.1 build cascade, all dispatches go through ta's existing bash dispatcher in `../ta/main`:

```
echo "<task prompt>" | /Users/evanschultz/Documents/Code/hylla/ta/main/bin/agent-dispatch.sh --role ta-go-builder --cwd /Users/evanschultz/Documents/Code/hylla/sand/main
```

(Stdin pipe shown above; `--prompt "..."` inline is the alternative. **Never use `--prompt-file`** — temp files obscure the call site and don't reproduce.)

Chain config: [`../ta/main/.claude/agent-chains.sh`](../../ta/main/.claude/agent-chains.sh) — sand reuses ta's chains until sand-the-server is functional and ports the chains to TOML.

After sand v0.1 ships:
1. Sand binary lives at `~/.local/bin/sand` (built by `mage install`).
2. Sand registers in sand's own `.mcp.json` as a project MCP server.
3. Future cascades dispatch via `mcp__sand__dispatch(role, prompt)` instead of `Bash(./bin/agent-dispatch.sh)`.

## Agent routing — backend dispatch (chain mode)

Each role has a fallback chain (primary + ordered fallbacks). See [`../ta/main/.claude/agent-chains.sh`](../../ta/main/.claude/agent-chains.sh) for the current per-role chain table. Summary:

```
role-primaries{role,backend,model,dispatch}:
ta-go-builder,ollama-local,qwen2.5-coder:7b,bash-dispatcher
ta-go-planning,codex-exec,gpt-5.5+low,bash-dispatcher
ta-go-qa-falsification,codex-exec,gpt-5.5+xhigh,bash-dispatcher
ta-go-qa-proof,claude-native,opus,agent-tool
ta-closeout,claude-native,opus,agent-tool
```

**Cascade methodology constraint** ([`../ta/main/docs/cascade-methodology.md`](../../ta/main/docs/cascade-methodology.md)): builder droplets touch **1-2 small blocks of code INCLUDING their tests**. The 7B coder handles atomic edits fine. Only ONE local model is in the chains — `qwen2.5-coder:7b`. Larger local models melt the machine and aren't in any chain.

**Planner + plan-QA enforcement rule**: every planner output MUST be reviewed by plan-QA to confirm each terminal builder droplet is 1-2 small blocks. If a droplet would be larger, the planner MUST decompose further before plan-QA passes. The 7B builder backend will FAIL LOUDLY on under-decomposed droplets — that is the desired feedback signal.

## Role-appropriate tool allowlists (the actual sandbox)

Every dispatched agent runs inside its host runtime with a tool allowlist scoped to its role:

- **Planners**: `mcp__ta__create` / `mcp__ta__update` / `mcp__ta__get` + read tools (Read, Grep, hylla) + context7. NO Edit / Write / Bash.
- **Plan-QA + Build-QA**: read-only — Read, Grep, Glob, hylla, ta get/search, `Bash(git diff *)` + `Bash(git log *)`. NO Edit / Write.
- **Builders**: Edit, Write, Read, Grep, Glob, Bash, LSP. ta get/search but NOT ta create/update.
- **Closeout**: Read + `Bash(git *)` + `Bash(mage check)` + ta update. No Edit / Write.

The dispatcher passes the persona's frontmatter `tools:` line as `--allowedTools` to Claude Code (or maps it for codex). **The persona file IS the sandbox spec for that role.**

**Tool-call audit after every dispatch.** Open the dispatch output and verify each agent claim against the actual stream — codex `mcp: <server>/<tool> (completed)` lines plus claude-native/ollama JSON envelope `tool_use` events. Self-reported "verdict: pass" or "tool X succeeded" is not authoritative; if the stream doesn't show the required tool calls (record updates, file edits, tests), the work didn't happen — re-dispatch or finish orchestrator-direct. Flag out-of-scope tool calls (anything outside the persona's `tools:` allowlist) as a discipline violation.

## Persona Bash scoping — mage-only, never raw language tooling

Agents NEVER run raw language tooling. No `go test`, no `go vet`, no `go build`, no `gofmt`, no `gofumpt`. All test / build / check commands route through mage. **Orchestrators are the exception** — they're trusted; agents are scoped.

Sand-the-project does NOT have `magefile.go` yet — it lands during the v0.1 build cascade. Until then, mage targets referenced in personas are NOT runnable in sand and dispatched agents will fail loudly if they try. That is correct — sand needs the magefile before QA personas can run their gates.

Per-role scoped Bash allowlist (verified in ta — `--allowedTools` filters tools from the model's visible toolset). TOON form (the project's adopted encoding — see SAND-SPEC §4):

```toon
bash-allowlist{role,allowed,denied}:
  builder,Bash(mage testFunc *)|Bash(mage testPkg *)|Bash(git diff *)|Bash(git log *)|Bash(git status),go *|gofmt|gofumpt|pnpm *|npm *|node *|npx *|git commit/push/reset
  planner,,all
  qa-proof,Bash(mage testFunc *)|Bash(mage testPkg *)|Bash(mage check)|Bash(git diff *)|Bash(git log *)|Bash(git status),raw lang tooling|git commit/push/reset
  qa-falsif,Bash(mage testFunc *)|Bash(mage testPkg *)|Bash(mage check)|Bash(git diff *)|Bash(git log *)|Bash(git status),raw lang tooling|git commit/push/reset
  closeout,Bash(mage check)|Bash(git diff *)|Bash(git log *)|Bash(git status),raw lang tooling|git commit/push/reset
```

(Pipe `|` separates list items inside a single TOON field; comma `,` separates fields. Planner's `allowed` cell is empty because planners get no `Bash(...)` patterns — they author plans, not run commands.)

**If a capability is missing, add a mage target — do NOT broaden the persona's Bash scope.**

## Editing role personas

`.claude/agents/ta-*.md` files are ta records under the `claude_agents.agent` schema. Both the native `Agent` tool AND the bash dispatcher (and, post-v0.1, sand) read these. NEVER edit them directly with Edit / Write. Workflow:

1. `mcp__ta__update` on the agent record id (e.g. `ta-go-builder`) with the desired field overlay.
2. `ta template save --kind=agent --path=./.claude/agents/<file>.md --group=ta --overwrite` — pushes the updated persona into `~/.ta/agents/ta/<file>.md`.
3. Verify both files match (project + HOME) before commit.

Direct edits of `~/.claude/agents/*-agent.md` or `~/.ta/agents/*/*.md` bypass ta's substrate tracking and create drift.

## Hylla discipline — defer until sand has committed Go

Evidence order for Go work, when sand has committed Go code:
1. Hylla (`mcp__hylla__*`) — primary for committed symbols / refs / graphs.
2. `git diff` — uncommitted local deltas.
3. Read / Grep / Glob — non-Go, and post-edit pre-push Go.
4. Context7 + `go doc` + LSP — external semantics.

Sand has NO Go source committed yet. Hylla queries will return nothing until the first build cascade lands `go.mod` + `main.go` + `magefile.go` + initial package skeleton. Until then: rely on Context7 + `go doc` + the SAND-SPEC.md, and use `git diff` after every droplet.

**Push-often + ingest-after-push**: once sand has Go code, after every commit batch push to origin then trigger `mcp__hylla__hylla_ingest`. The `/commit-and-reingest` skill bundles both.

**Hylla is Go-only.** Never query for `.toml`, `.json`, `.md`, `.yml`, scripts.

**Artifact_ref this project passes**: spawn prompts that invoke `mcp__hylla__*` for sand's own code MUST use `github.com/evanmschultz/sand@main` — pinned to branch, no float. Verify ingest currency via `mcp__hylla__hylla_artifact_metadata` before dispatching when Hylla evidence is critical.

## Cascade-managed development — sand's drops live in sand's own substrate

Sand's `.ta/` substrate was bootstrapped via `ta init` on 2026-05-20. Drop records live in **`sand/main/.ta/cascade/drops/drop_NNN/drop.toml`** — sand's OWN substrate, not ta/main's. All `mcp__ta__*` calls for sand cascade records MUST use `path=/Users/evanschultz/Documents/Code/hylla/sand/main`.

Historical note: before `ta init` ran, the initial scaffolding plan was to dogfood under ta's substrate. That plan was abandoned once sand had its own working `.ta/` — sand is self-contained now.

Workflow per [`../ta/main/docs/cascade-methodology.md`](../../ta/main/docs/cascade-methodology.md):

1. **Drop record** — `mcp__ta__create` a `cascade.drop` under ta's substrate. id = `drop_NNN.drop.sand_v0_<slug>`.
2. **Planner record** — `mcp__ta__create` a `cascade.planner` child. Dispatch `ta-go-planning` via the bash dispatcher.
3. **Plan-QA twins** — `cascade.qa_proof` + `cascade.qa_falsification` children. BLOCK descent until both complete+success.
4. **Recursive decomposition** — if a planner would emit > 4 children OR cross > 1 domain concern OR cross > 1 package, decompose further.
5. **Builder droplets** — terminal leaves, ≤ 2 small blocks per droplet. Dispatch in parallel when paths are disjoint.
6. **Package-level build+test** — `mage testPkg <path>` after all droplets in a package complete.
7. **Build-QA twins at EVERY planner level** — both must complete+success before the planner reports complete.
8. **Closeout + commit** — after L1 drop's build-QA passes, run `mage check`, then commit.

**State machine**: `todo` → `in_progress` → `complete | failed`. `outcome = success | failure | blocked`. Always `mcp__ta__update` to record transitions.

**Dogfood discipline (MCP-first)**: cascade record CRUD goes through MCP. If `mcp__ta__*` fails, REPORT and PAUSE — don't silently fall back to `./bin/ta` CLI.

## Cascade isolation — test only your slice

A dispatched role operating below strict package level MUST run only its slice's tests:

- **Below-package**: `mage testFunc TestMyThing` or `mage testFunc 'TestA|TestB|TestC'`. Package narrowing: `TA_TEST_PKG=./internal/<pkg> mage testFunc TestMyThing`.
- **Package-level**: `mage testPkg ./internal/<pkg>`.
- **Module-level**: `mage Test` (or `mage Check`). Orchestrator-level QA + commit gate only.

## MCP server — pinning the project directory

ta's MCP server is one project per process. Sand's `.mcp.json` pins ta to `sand/main`:

```json
{"mcpServers":{"ta":{"command":"ta","args":["--project","/Users/evanschultz/Documents/Code/hylla/sand/main"]}}}
```

Launch Claude Code FROM `sand/main` to pick up cwd-inheritance for other MCP servers. The `--project` arg wins over cwd when both are present.

## ta CLI usage

- All `ta <read-command>` invocations from dispatched roles MUST pass `--json`. ANSI laslig output is for humans.
- `--json` accepted on: `ta get`, `ta list-sections`, `ta schema`, `ta search`.
- Mutating commands (`create` / `update` / `delete` / `schema --action=...`) return a concise success notice; use `--verbose` for the post-mutation record.
- Bare `ta` without a TTY is the MCP server.
- **NEVER invoke raw `go test` / `go vet` / `go build` / `gofmt` / `gofumpt`.** Always route through mage (once mage targets exist).

## Project-specific docs

- [`SAND-SPEC.md`](SAND-SPEC.md) — full sand v0.1 design: MCP tools, TOON format, chain config schema, dispatch matrix, build cascade entry-point.
- [`../ta/main/docs/cascade-methodology.md`](../../ta/main/docs/cascade-methodology.md) — the canonical cascade contract sand's build cascade follows.
- [`../ta/main/docs/agent-backend-routing.md`](../../ta/main/docs/agent-backend-routing.md) — full explainer for the multi-backend routing pattern sand reimplements.
