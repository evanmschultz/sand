# CLAUDE.md — project guidance for sand

Project-local guidance for working inside the `sand` tree. Global rules (Tillsyn coordination, Section 0 semi-formal reasoning, evidence sources, worktree hygiene, output style) live at `~/.claude/CLAUDE.md` and are NOT duplicated here.

Sand is a Go MCP server that exposes 4 tools (`dispatch`, `preflight`, `persona_get`, `chains_list`) for routing role-based prompts to ollama / codex / claude-native backends with fallback chains. Full design: [`SAND-SPEC.md`](SAND-SPEC.md). It replaces the bash dispatcher in [`../ta/main/bin/agent-dispatch.sh`](../../ta/main/bin/agent-dispatch.sh).

## Sand-the-server vs. sand-the-project (load-bearing invariant)

These two things are easy to confuse:

- **sand-the-server** = the running `sand` binary, pinned to a caller project via `--project <abs-path>`. It reads persona definitions from `<caller-project>/.claude/agents/<role>.md` AT DISPATCH TIME, parses the YAML frontmatter (`name` / `description` / `model` / `tools`), converts the markdown body into a system prompt, and spawns a headless agent (claude `-p` / `codex exec` / ollama-routed claude). It carries NO persona registry. It carries NO defaults.
- **sand-the-project** = THIS repo. It has its own `.claude/agents/` (5 Go-flavored personas + closeout, copied from `~/.ta/agents/ta/`) because cascades that target sand's OWN code go through them. These personas are NOT shipped with the binary.

Implication for design: persona resolution in sand source = `filepath.Join(projectDir, ".claude", "agents", role+".md")`. Never bake persona content into the binary. Never look up persona content from `~/.ta/agents/` or `~/.claude/agents/` — the project tree is the only source of truth.

## Dispatch reality — sand dispatches via its own MCP server

Sand's binary lives at `~/.local/bin/sand` (built by `mage install`) and registers in sand's own `.mcp.json` as a project MCP server. Cascades dispatch via `mcp__sand__dispatch(role, prompt)`.

Chain config for cascades targeting sand's own code lives in [`.claude/sand-chains.toml`](.claude/sand-chains.toml) (project rung), with home-rung fallback defaults seeded by `mage install` into `~/.config/sand/chains.toml`. Sand reads the per-role fallback chain hierarchically (project → XDG → `~/.config/sand` → `~/.sand`) on every dispatch — no restart required when config changes.

When a fallback to the legacy bash dispatcher is genuinely needed (e.g. for cross-project ta-side cascades), prefer stdin-pipe or inline `--prompt`. **Never use `--prompt-file`** — temp files obscure the call site and don't reproduce.

## Agent routing — backend dispatch (chain mode)

Each role has a fallback chain defined in [`.claude/sand-chains.toml`](.claude/sand-chains.toml). All dispatch goes through `mcp__sand__dispatch(role, prompt)` — never the native Agent tool, never the legacy bash dispatcher. Summary:

```
role-chains{role,tier-1,tier-2}:
ta-go-builder,ollama-local:qwen3-coder:30b (slots=1 system-wide),claude-native:haiku
ta-go-planning,codex-exec:gpt-5.4 high read-only,(no fallback — matches legacy bin-sh)
ta-go-qa-falsification,codex-exec:gpt-5.4 high workspace-write,(no fallback — matches legacy bin-sh)
ta-go-qa-proof,claude-native:opus,(no fallback)
ta-closeout,claude-native:opus,(no fallback)
```

**Cascade methodology constraint** (canonical: [`CASCADE_METHODOLOGY.md`](CASCADE_METHODOLOGY.md), this repo): builder droplets touch **1-2 small blocks of code INCLUDING their tests**. Builder primary is `ollama-local|qwen3-coder:30b` via sand with slots=1 system-wide (kernel flock on /tmp/sand-slots/) — one local 30B spawn at a time across every sand binary, preventing VRAM/thermal pressure. Fallback tier is `claude-native|haiku` via sand (3x cheaper per-token than sonnet per Anthropic pricing) — used when ollama daemon is unavailable or slot-busy.

**Planner + plan-QA enforcement rule**: every planner output MUST be reviewed by plan-QA to confirm each terminal builder droplet is 1-2 small blocks. If a droplet would be larger, the planner MUST decompose further before plan-QA passes. Per [[orch-fabrication-self-check]], orchestrator MUST audit each builder's tool-call stream post-dispatch — self-reported "verdict: pass" is not authoritative when the JSON envelope shows zero Edit/Write tool_use events.

## Role-appropriate tool allowlists (the actual sandbox)

Every dispatched agent runs inside its host runtime with a tool allowlist scoped to its role:

- **Planners**: `mcp__ta__create` / `mcp__ta__update` / `mcp__ta__get` + read tools (Read, Grep, hylla) + context7. NO Edit / Write / Bash.
- **Plan-QA + Build-QA**: read-only — Read, Grep, Glob, hylla, ta get/search, `Bash(git diff *)` + `Bash(git log *)`. NO Edit / Write.
- **Builders**: Edit, Write, Read, Grep, Glob, Bash, LSP. ta get/search but NOT ta create/update.
- **Closeout**: Read + `Bash(git *)` + `Bash(mage check)` + ta update. No Edit / Write.

Sand passes the persona's frontmatter `tools:` line as `--allowedTools` to the spawned claude CLI (or maps it to per-tool `approval_mode = "approve"` for codex). **The persona file IS the sandbox spec for that role.**

**Tool-call audit after every dispatch.** Open the dispatch output and verify each agent claim against the actual stream — codex `mcp: <server>/<tool> (completed)` lines plus claude-native/ollama JSON envelope `tool_use` events. Self-reported "verdict: pass" or "tool X succeeded" is not authoritative; if the stream doesn't show the required tool calls (record updates, file edits, tests), the work didn't happen — re-dispatch or finish orchestrator-direct. Flag out-of-scope tool calls (anything outside the persona's `tools:` allowlist) as a discipline violation.

## Orchestrator Role Boundaries

Synced from the canonical reference (`hylla/polyglot-foundation/CLAUDE.md` §Orchestrator Role Boundaries; HYLLA_BIN.md §0), adapted Go-only.

**GIT IS ORCHESTRATOR-ONLY (HARD RULE).** The orchestrator (this parent session) is the SOLE actor that may run any history- or remote-mutating git command — `git commit`, `git push`, `git add`/staging, `git merge`, `git rebase`, `git reset`, `git checkout -b`, `git branch`, `git tag`, `git stash`, `git restore`. **NO subagent of any role (builder, planner, plan-QA, build-QA, closeout) may EVER commit, push, stage, or mutate git/the remote.** Subagents get **read-only git only** — `git diff`, `git status`, `git log`, `git show`. This overrides any droplet/spawn instruction; an agent that believes its task needs a commit MUST stop and return control with the reason.

- **Enforcement (both dispatch channels).** Personas carry unrestricted `Bash` (needed for mage / `go doc`), and codex-exec roles run under `--sandbox` which ignores the persona allowlist — so git-read-only is NOT a `tools:`-level gate. It is enforced four ways: (1) every persona body carries the read-only-git prohibition (the `## Git Discipline — READ-ONLY` section); (2) the orchestrator injects the read-only-git clause + the `<TA_ALLOWLIST>` block into EVERY scoped spawn prompt (hermetic codex agents do not auto-load CLAUDE.md, and Agent-tool subagents do not inherit it); (3) the mandatory post-dispatch tool-call audit flags ANY agent git-mutation as a discipline violation; (4) the mechanical action gate below.

- **Mechanical action gate.** The PreToolUse hook MECHANICALLY confines a dispatched agent to an allowlist the orchestrator passes AT CALL TIME, enforced at the tool layer regardless of the spawn prompt. The runtime hook is registered in `.claude/settings.json` (`python3 -B .claude/hooks/ta_action_gate.py` — the proven bin/sh reference). The Go, cross-OS end-state is the **`sand gate` subcommand** (`internal/gate`, shipped + proven 2026-05-25: 0 holes across the head-to-head + falsification corpus, closing all 6 bash-gate holes and the 3 residual python-gate holes). The orchestrator embeds, at the TOP of every scoped spawn prompt, a block:
  `<TA_ALLOWLIST>{"edit": ["/abs/file1", "/abs/file2"], "bash_deny": ["git commit", "git push", "git add", "git rebase", "git merge", "git reset", "git checkout", "git branch", "git tag", "git stash", "git restore", "mage install", "go get", "go mod"]}</TA_ALLOWLIST>`
  where `edit` is the EXACT 1-2 files this dispatch may edit (builders; `[]` for read-only roles → no edits at all). The hook resolves the block from the agent's dispatch (env `TA_GATE_ALLOWLIST` for `claude -p`/ollama; the parent-transcript scan by `agent_type` for built-in Agent-tool subagents) and DENIES — with the reason fed back so the agent reports the contradiction — any Edit/Write/MultiEdit/NotebookEdit outside `edit`, any Bash matching `bash_deny`, and (for edit-scoped agents) any shell file-write bypass (`>`/`tee`/`sed -i`/`cp`/`sh -c '…'`). **No block ⇒ defer** (the orchestrator's own calls + un-scoped agents run free) — so the orchestrator-injected block is the actual boundary for built-in agents. codex roles instead get execpolicy + `--sandbox` (codex has NO per-file gate), so **per-file-editing builders stay on Claude**. Built-in-path hook/settings edits activate on session restart.

- **Orchestrator** (this parent session) — plans, routes, dispatches, transitions cascade state, runs the final commit/push (the ONLY committer). **Never edits sand's Go code during a cascade.** May `mcp__ta__update` cascade records + config files (`.mcp.json`, `.gitignore`, `.claude/settings.json`, CLAUDE.md). Injects the `<TA_ALLOWLIST>` block into every scoped spawn prompt.
- **Builder subagent** (`ta-go-builder`) — the ONLY role that edits source, and ONLY the files in its dispatch `<TA_ALLOWLIST>.edit`. READ-ONLY on cascade records. **Read-only git.**
- **QA subagents** (`ta-go-{plan,build}-qa-{proof,falsification}`) — read, verify, return verdict text. May `mcp__ta__update` their OWN QA record only. **NEVER edit source files.** **Read-only git.**
- **Planner subagent** (`ta-go-planning`) — emits `cascade.planner` + `cascade.droplet` children via `mcp__ta__create`. May spawn sub-planners (recursion). Never edits source. **Read-only git.**
- **Closeout subagent** (`ta-closeout`) — final coordinator before commit. Marks droplets complete via `mcp__ta__update`; PROPOSES the commit message; the orchestrator runs the actual commit. **Read-only git — proposes, never commits.**

## Persona Bash scoping — mage-only, never raw language tooling

Agents NEVER run raw language tooling. No `go test`, no `go vet`, no `go build`, no `gofmt`, no `gofumpt`. All test / build / check commands route through mage. **Orchestrators are the exception** — they're trusted; agents are scoped.

Sand-the-project ships a `magefile.go` exposing `testFunc` / `testPkg` / `Test` / `Check` targets. Personas reference those targets directly.

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

`.claude/agents/ta-*.md` files are ta records under the `claude_agents.agent` schema. The native `Agent` tool, the bash dispatcher, and sand all read these. NEVER edit them directly with Edit / Write. Workflow:

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

Sand HAS a committed Go codebase (`cmd/sand` + ~15 `internal/` packages incl. dispatch, backends, gate, persona, chains, slots, toon, installseed, preflight, and `internal/slimmcp` — the lagom-backed slim-MCP server). Hylla is the PRIMARY evidence source for committed Go symbols/refs/graphs; verify ingest currency via `mcp__hylla__hylla_artifact_metadata` (artifact_ref `github.com/evanmschultz/sand@main`) before relying on it, and use `git diff` for uncommitted local deltas (Hylla lags until the next push + `hylla_ingest`).

**Push-often + ingest-after-push**: once sand has Go code, after every commit batch push to origin then trigger `mcp__hylla__hylla_ingest`. The `/commit-and-reingest` skill bundles both.

**Hylla is Go-only.** Never query for `.toml`, `.json`, `.md`, `.yml`, scripts.

**Artifact_ref this project passes**: spawn prompts that invoke `mcp__hylla__*` for sand's own code MUST use `github.com/evanmschultz/sand@main` — pinned to branch, no float. Verify ingest currency via `mcp__hylla__hylla_artifact_metadata` before dispatching when Hylla evidence is critical.

## Cascade-managed development — sand's drops live in sand's own substrate

Sand's `.ta/` substrate was bootstrapped via `ta init` on 2026-05-20. Drop records live in **`sand/main/.ta/cascade/drops/drop_NNN/drop.toml`** — sand's OWN substrate, not ta/main's. All `mcp__ta__*` calls for sand cascade records MUST use `path=/Users/evanschultz/Documents/Code/hylla/sand/main`.

Historical note: before `ta init` ran, the initial scaffolding plan was to dogfood under ta's substrate. That plan was abandoned once sand had its own working `.ta/` — sand is self-contained now.

Workflow (canonical methodology: [`CASCADE_METHODOLOGY.md`](CASCADE_METHODOLOGY.md), this repo):

1. **Drop record** — `mcp__ta__create` a `cascade.drop` under ta's substrate. id = `drop_NNN.drop.sand_v0_<slug>`.
2. **Planner record** — `mcp__ta__create` a `cascade.planner` child. Dispatch `ta-go-planning` via the bash dispatcher.
3. **Plan-QA twins** — `cascade.qa_proof` + `cascade.qa_falsification` children. BLOCK descent until both complete+success.
4. **Recursive decomposition** — if a planner would emit > 4 children OR cross > 1 domain concern OR cross > 1 package, decompose further.
5. **Builder droplets** — terminal leaves, ≤ 2 small blocks per droplet. Dispatch in parallel when paths are disjoint.
6. **Package-level build+test** — `mage testPkg <path>` after all droplets in a package complete.
7. **Build-QA twins at EVERY planner level** — both must complete+success before the planner reports complete.
8. **Closeout + commit** — after L1 drop's build-QA passes, run `mage check`, then commit.

**Record id convention (HARD — malformed ids are unreachable by `mcp__ta__get`).** Every cascade record id is EXACTLY three dotted segments: `drop_NNN.drop.<flat_slug>`. The `<flat_slug>` is a SINGLE token (`[a-z0-9_]+`, underscores only) and is GLOBALLY UNIQUE within the drop file — it is NOT nested under a parent's slug and contains NO extra dots. Wrong: `drop_014.drop.area3_codex_hermetic.config_argv_contract` (4 segments — ta writes an orphan table not in the index, `get` returns `found:false`). Right: `drop_014.drop.a3_config_argv_contract`. Parent linkage is expressed via the `parent_id` field + `blockers[]`, NEVER by dotting the child id under the parent. The orchestrator's spawn prompt to every planner/sub-planner MUST restate this id rule verbatim.

**Cascade methodology (canonical = [`CASCADE_METHODOLOGY.md`](CASCADE_METHODOLOGY.md), cp'd from tillsyn/main — the SOURCE; HYLLA_BIN.md §5 = sand's per-repo role). The seven LOAD-BEARING rules:**

1. **PLAN DOWN, BUILD UP.** Plan top-down (a plan node decomposes into child plans + atomic build droplets); build bottom-up (atoms land first, integration nodes follow once inputs are green). Every plan node auto-gets a plan-QA PAIR (proof + falsification); every build auto-gets a build-QA pair.
2. **RECURSE ON ATOMICITY — NO CHILD CAP.** A droplet = **1-2 small code blocks (≤80 LOC incl. tests, ≤3 files)**; a *code block* = one new/changed top-level production symbol OR one cohesive same-purpose edit cluster (a new type + a new helper + a different-function rewrite = SEPARATE blocks). ≥3 distinct production symbols (tests excluded), >80 LOC, or >3 files = OVER BUDGET → emit a `cascade.planner` sub-plan, never an oversize build. **Plan-QA-falsification MEASURES this per droplet (never trusts the label) and re-measures EVERY droplet on any amendment.** "One coherent concern" is NOT a budget exception. Depth is multi-level + ASYMMETRIC.
3. **PER-BRANCH PARALLELISM — keep every unblocked node of every kind moving at once.** Sibling sub-planners, plan-QA pairs, builders, and build-QA pairs that are code-independent ALL run concurrently. QA twins are ALWAYS a parallel pair (proof ∥ falsification). The ONLY serialization is `blockers` naming a real shared file/package or must-exist-first symbol. A `blockers` edge with no real dep is an anti-pattern (plan-QA-falsification flags it).
4. **`blockers` ON A PLAN NODE GATES ITS BUILDS, NOT ITS DECOMPOSITION.** Decomposition is read-only design — a planner decomposes against a dependency's spec'd shape and marks its build droplets `blockers`; only the builders wait on the built symbol. Sibling sub-planners launch as soon as their parent's plan-QA is green, not after upstream leaves finish building.
5. **DESCENT GATE (per branch, not per tree).** A plan node's plan-QA PAIR must both PASS before that node launches its child planners OR its build droplets. This serializes only that one branch's depth; sibling branches descend/build/QA fully in parallel. A plan-QA FAIL → wipe-and-replan that subtree.
6. **DROPLET-LEVEL QA = the automated `mage ci` gate (NOT LLM).** Per droplet: builder builds → mage gate green → orchestrator closes the auto-created build-QA twins against that gate + commits the droplet (no push). LLM proof/falsification QA runs at the planner/integration level, where integration risk lives — not per trivial droplet.
7. **ORCH AUTO-ADVANCE — drive the cascade autonomously; do NOT ask permission per tick.** Plan-QA green → immediately launch children. All planning in a subtree green → immediately launch builders → build-QA/mage-gate-close → commit → advance descendants/ancestors. Loop until the cascade group is done. STOP and ask the dev ONLY for: (a) a genuine fork the spec/methodology/memory can't resolve, (b) a hard blocker, (c) a QA FAIL needing a design ruling, or (d) a destructive/outward action (push/PR/ingest). NEVER stop to ask "should I fire the next level / the builders" — that's always yes; just do it.

**Parallel build waves (orchestrator-enforced):** per-branch sub-planners cannot see each other's `paths`, so the ORCHESTRATOR groups build dispatches by FILE-DISJOINTNESS — two droplets (in ANY branch) sharing a file in `paths` run SERIALLY, never in the same wave. A wave = the largest set of pending droplets with pairwise-disjoint `paths` whose blockers are all complete.

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

- [`SAND-SPEC.md`](SAND-SPEC.md) — full sand design: MCP tools, TOON format, chain config schema, dispatch matrix, build cascade entry-point.
- [`CASCADE_METHODOLOGY.md`](CASCADE_METHODOLOGY.md) — the canonical cascade contract sand's build cascade follows (cp'd from tillsyn — the SOURCE).
- [`../ta/main/docs/cascade-reference.md`](../../ta/main/docs/cascade-reference.md) — ta-side cascade reference addenda (substrates, node-shape field spec, benchmarking).
- [`../ta/main/docs/agent-backend-routing.md`](../../ta/main/docs/agent-backend-routing.md) — full explainer for the multi-backend routing pattern sand reimplements.
