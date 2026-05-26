# HYLLA_BIN.md — multi-provider agent dispatch + sandbox reference

> **Single canonical reference** for the bin/sh + hook agent-sandboxing system, living in
> `hylla/polyglot-foundation`. The sibling projects (sand, tillsyn, ta, valv) **point here** for the
> proven implementation instead of each carrying their own copy. Sectioned by project (§5). Everything
> below is **E2E-proven on macOS** (Darwin 25, `claude 2.1.150`, `codex-cli 0.133.0`, ollama 0.24.0),
> 2026-05-25; the cross-agent debate + evidence is in the round docs (§6). Windows/Linux is the one
> open axis — the execpolicy + Claude permission engine are the OS-independent floors.

## 0. The proven files in THIS repo (refer to these)

| File | What it is |
|---|---|
| `bin/agent-dispatch.sh` | The chain dispatcher. `--gate '<json>'` contract; `dispatch_codex` (hermetic `CODEX_HOME` + execpolicy + `--sandbox` + `-c approval_policy=never` + `project_doc_max_bytes=0` + `skills.bundled.enabled=false` + role-conditional MCP inject); `dispatch_claude_native` **REFUSES `-p`** (Anthropic → built-in Agent tool); per-run audit capture to `.claude/agent-runs/`. |
| `.claude/agent-chains.sh` | Per-role chains + the **ROUTING POLICY** header (Anthropic→built-in, codex→exec, no `-p`, `CODEX_EXHAUSTED` failure-signal). |
| `.claude/hooks/ta_action_gate.py` | PreToolUse gate (run `python3 -B`): per-file edit-scope, git-block past global flags, shell-write-bypass block, `bash_deny`, explicit-allow, `<TA_ALLOWLIST>`/env allowlist resolution. |
| `.claude/settings.json` | Hook registration (`python3 -B …ta_action_gate.py`, matcher `Edit\|Write\|MultiEdit\|NotebookEdit\|Bash`). |
| `CLAUDE.md` §"Orchestrator Role Boundaries" | `<TA_ALLOWLIST>` injection contract + GIT-ORCHESTRATOR-ONLY + the Agent Bindings table. |
| `AGENT_SANDBOX_SPEC.md` + the rebuttals (§6) | The consensus + the empirical debate. **The 4 `*_REBUTTAL.md` are the authority** (last-agreed, tested); `AGENT_SANDBOX_SPEC.md` is sand's summary. |

## 1. The model (per channel)

- **Anthropic models (haiku/sonnet/opus) → Claude Code BUILT-IN Agent tool**, dispatched orchestrator-
  direct. Gated by the **PreToolUse hook** (`ta_action_gate.py`): per-file edits, no git, no shell-write.
  **NO `claude -p`, NEVER an `ANTHROPIC_API_KEY`** in this system. *Limitation (official `sub-agents.md`
  §"What loads at startup"): a custom built-in subagent ALWAYS loads `~/.claude/CLAUDE.md` + project
  CLAUDE.md + memory — only Explore/Plan skip; no setting changes it. So built-in agents are gated on
  ACTIONS but inherit ambient CONTEXT — accepted for these large models (persona carries Section 0 +
  behavior; hook is the security boundary).*
- **codex models (gpt-5.5) → `codex exec`**, hermetic + persona-only (PROVEN: AGENTS.md/HOME/skills all
  absent). git-block = execpolicy `prefix_rule(forbidden)` (no `--ignore-rules`); writes = `--sandbox
  read-only` (or `-C <dir>` for dir-edit, mac/Linux); `-c approval_policy="never"`; role-conditional MCP.
  **Per-file impossible on codex (hooks dead 0.133.0) → codex is NEVER an editing builder.**
- **`claude -p`** = the API-key/ollama tier. **Not used in hylla/ta/valv.** It is the **sand/tillsyn
  user-config** path; its clean-context recipe (env strips + flags) is in §6 (HYLLA_SANDBOX_IDEA f17).
- **Failure signal**: on a tier failure the dispatcher prints `CODEX_EXHAUSTED role=<role>` so the orch
  re-dispatches the chain's next tier (e.g. built-in). sand/tillsyn make this a configurable "run-what-
  you-can" flow.

## 2. The per-role chain spec (FE + Go identical)

| Role | backend | model | effort / sandbox |
|---|---|---|---|
| planning | codex-exec | gpt-5.5 | low, read-only |
| plan-qa-proof | claude-native (built-in) | opus | — |
| plan-qa-falsification | codex-exec | gpt-5.5 | high, read-only |
| builder | claude-native (built-in) | haiku (sonnet fallback) | — |
| build-qa-proof | claude-native (built-in) | sonnet | — |
| build-qa-falsification | codex-exec | gpt-5.5 | low, read-only |
| closeout | claude-native (built-in) | opus | — |

## 3. The `--gate` contract + per-role tool matrix

`--gate '{"edit":["//abs/f"],"writable_dirs":["/abs/dir"],"bash_deny":["git commit",…],"network":false}'`

- **Tools per type**: planning + plan-qa → hylla(read) + context7 + gopls(go)/playwright(fe) + ta; **build-qa
  codex → ta ONLY** (hylla + context7 + gopls all stripped — build-qa is reading-based, and the heavy/network
  MCP intermittently hung codex startup; 2026-05-26 fix); builder → per-file edit + the above (read); ALL FE
  roles → **Playwright**; ALL Go roles → gopls; **no role gets git mutation** (orchestrator is sole committer).
- **codex MCP-init reliability (2026-05-26, LOAD-BEARING)**: every injected MCP server carries
  `startup_timeout_sec=15`. codex's first turn awaits ALL servers' `initialize`+`tools/list` (codex bug
  [#19556]/[#21318]; default 30s, hung past it on macOS `exec` → 600s SIGTERM); a slow gopls-index / context7-HTTP
  stalled the first turn. With the bound, codex drops a laggard after 15s + proceeds (graceful degradation).
  **Proven 2/2** on concurrent full-MCP plan-qa-falsif. Pair with the build-qa ta-only strip above.

## 4. Veracity (hard rule, all channels)

Every dispatch RETURNS the full tool-call trace AND the dispatcher PERSISTS it to
`.claude/agent-runs/<run>.{out,err,meta.json}` (gitignored). The orchestrator audits agent self-reports
against the stream; every persona emits `## Tools Used`. Self-report ≠ truth.

## 5. Per-project guidance

### 5.1 sand — build the Go MCP that replaces this bin/sh (FULL why/how)
sand is a Go MCP server + **config-driven translator**: users declare chains + per-role gate limits in
**TOML**; sand TRANSLATES (persona + chain TOML → per-channel invocation + gate/sandbox artifacts) and
**ENFORCES every hard rule the user's TOML declares** (not advisory). sand is **Go-only for its own
agents** (no FE agents itself) but must be **FE-AWARE** so its TOML can express FE flows for consuming
projects (hylla/ta). Implement: the Go `sand gate` PreToolUse subcommand (exec form — the cross-OS
end-state replacing this Python hook), hermetic codex argv (§1), role-conditional MCP injection, the
`--gate` contract (§3), per-run trace persistence (§4). Reconcile `SAND-SPEC.md` §8.3/§13.6 (which say
`--ignore-rules`) to the consensus (NO `--ignore-rules`; own execpolicy). Research/why: §6 docs + the
rebuttals. Study the proven files in §0.

### 5.2 tillsyn — like sand, AND actually uses FE (FULL why/how)
Same as sand (Go, TOML-configurable, `till gate` subcommand, all of §1–§4), on the **tillsyn substrate**
(`mcp__tillsyn__till_*` instead of `mcp__ta__*`). tillsyn **does run FE agents** (Playwright, live-backend
URL, the `ta-fe-*` role set). Needs all implementation details + how to do its own research — see §6.

### 5.3 ta — like hylla (FULL, fe + go)
ta needs it ALL (fe + go agents, full enforcement) and uses **this bin/sh now** (before sand ships).
ta's dispatcher/chains/hook are synced from §0. Same multi-provider model, same gate contract, same
veracity. ta is the cascade substrate (`mcp__ta__*`).

### 5.4 valv — USE the bin/sh Go agents only (MINIMAL)
valv only needs to **use** its own bin/sh agents — **Go agents only, NO FE**. Dispatch via
`bin/agent-dispatch.sh` (codex roles) + the built-in Agent tool (Anthropic roles); the gate hook + chains
are synced from §0. valv does **not** need the why/how internals or FE. Keep its builder read-only on
cascade records and its closeout with `mcp__ta__update`.

## 6. Evidence + research (citations)

- **Round docs (the authority)**: `*_REBUTTAL.md` (ta/sand/tillsyn/hylla — final agreed), `*_RECS.md`,
  `*_SANDBOX_IDEA.md` / `AGENT_SANDBOXING_tillsyn.md`, `AGENT_SANDBOX_SPEC.md`. Key: HYLLA_SANDBOX_IDEA
  finding 17 (the `claude -p` clean-context flag set: `CLAUDE_CODE_DISABLE_CLAUDE_MDS`/`_AUTO_MEMORY`/
  `_GIT_INSTRUCTIONS` + `--exclude-dynamic-system-prompt-sections` + `--strict-mcp-config` +
  `--disable-slash-commands` + `outputStyle:"default"`).
- **E2E proven 2026-05-25** (this repo): built-in gate (in-scope-allow / off-scope-deny / git-deny /
  shell-write-deny, logged); codex hermetic (AGENTS/HOME/skills absent) + execpolicy git-block + runtime
  MCP injection per type; Playwright both channels vs live 34917; `python3 -B` no-pycache; a **full FE
  cascade unit** (`drop_009.drop.droplet_fe_hero_a11y`) ran the whole chain green (built-in builder
  gated to 1 file → codex Playwright QA PASS → droplet complete).
- **Vendor docs**: Claude Code `sub-agents` (custom subagents always load CLAUDE.md; only Explore/Plan
  skip; `skills`/`disallowedTools`/`includeGitInstructions`), `hooks`, `permissions` (`//abs` form),
  `headless` (`--bare` needs API key), `memory`/`env-vars`. codex `exec-policy` (`prefix_rule(forbidden)`),
  `concepts/sandboxing` (`.git`-ro geometry-dependent), `config-reference` (`approval_policy`,
  `sandbox_workspace_write.*`, `project_doc_max_bytes`); `skills.bundled.enabled=false` (disables bundled
  codex skills — verified). codex issues #16732 (hooks dead in exec), #24098 (windows sandbox init).
