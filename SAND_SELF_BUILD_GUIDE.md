# SAND self-build guide — how sand must build its dispatch + sandbox system

> Authored from `hylla/polyglot-foundation` (the cross-repo coordination root for this work),
> 2026-05-25, after a 4-repo audit (hylla / ta / valv / sand) + a bin/sh proof-of-concept that is
> **E2E-smoke-tested** across all three dispatch channels. This is sand's **build-from** brief.
> sand is meant to be **flexible + config-driven**, but the project owner's system must produce the
> exact chains in `.claude/sand-chains.toml` (§3). Read the companion docs before building (§0).

## 0. Read these first (authority order)

1. **`AGENT_SANDBOX_SPEC.md`** (this repo, "locked consensus 2026-05-25") — the per-channel
   enforcement table, the gate contract, the per-role tool matrix. **CANONICAL.**
2. **`AGENT_DISPATCH.md`** (this repo) — routing + the TOML sand generates.
3. **The empirically-proven bin/sh reference — COPY FROM THESE EXACT hylla paths** (the known-good
   source, E2E-proven 2026-05-25; identical copies were synced to ta/valv/sand):
   - `/Users/evanschultz/Documents/Code/hylla/hylla/polyglot-foundation/bin/agent-dispatch.sh`
     (codex hermetic + execpolicy git-block, `--gate` translation, `--sandbox`/`-C`, role-conditional
     MCP injection, **per-run audit capture** to `.claude/agent-runs/<run>.{out,err,meta.json}`)
   - `/Users/evanschultz/Documents/Code/hylla/hylla/polyglot-foundation/.claude/agent-chains.sh`
     (the per-axis chain spec §3)
   - `/Users/evanschultz/Documents/Code/hylla/hylla/polyglot-foundation/.claude/hooks/ta_action_gate.py`
     (PreToolUse gate; per-file edit-scope + **git-block hardened past global flags** — `git -C dir
     commit`, `git -c k=v commit`, abs-path, env-prefix, `--git-dir=`, `&&`-chained all DENY — **plus
     shell-write-bypass block**: an edit-scoped agent's `cat>`/`>>`/`python -c`/`node -e`/`sed -i`/
     `tee`/`cp`/`mv`/`dd of=` are all DENIED so the per-file scope can't be circumvented via Bash)
   - `/Users/evanschultz/Documents/Code/hylla/hylla/polyglot-foundation/.claude/settings.json`
     (the PreToolUse hook registration, matcher `Edit|Write|MultiEdit|NotebookEdit|Bash`)
   **Proven (reproducible):** gate logic 14/14 (allow in-scope / deny off-scope+git incl. evasions /
   allow read-only-git + `mage check` / no false-positive on typo-verbs); codex read-only sandbox +
   execpolicy block `git commit` + writes under live `gpt-5.5`; `-p`/ollama per-file `Edit(//abs)` +
   `--disallowedTools` block off-scope write + git; full tool-call trace persisted per run.
4. Superseded scratch (git history only): `*_RECS.md`, `*_REBUTTAL.md`, `*_SANDBOX_IDEA.md`,
   `AGENT_SANDBOXING_tillsyn.md`, `SAND_E2E_PROOF.md`. Evidence + reproduction live there.

**Authority rule:** where `SAND-SPEC.md` §8.3 / §13.6 conflict with `AGENT_SANDBOX_SPEC.md`, the
**consensus spec wins** (it is newer + empirically reproduced). See §2.1.

## 1. What sand is

A Go MCP server that is a **config-driven translator + enforcer**: users declare chains + per-role
limits in TOML; sand's Go code TRANSLATES (`<project>/.claude/agents/<role>.md` persona + chain TOML →
the per-channel invocation + the gate/sandbox artifacts) and ENFORCES them. **No isolation policy is
hardcoded in Go** — it is read from config. sand and tillsyn dispatch + gate logic is **GO CODE ONLY,
never `bin/*.sh`**; the bash dispatcher in this repo is the legacy bootstrap sand REPLACES, kept only
as the proven reference to translate from. The ONLY `.sh` permitted is a user's own hook.

## 2. Gaps the audit found that sand MUST close

The bin/sh layer is correct + proven; **sand's Go path implements none of it yet**. Close these:

- 2.1 **Codex hermeticity contradiction (RESOLVE FIRST).** `SAND-SPEC.md` §8.3/§13.6 say "do NOT
  `--ignore-user-config`; use `--ignore-rules`". The consensus + the proven bin/sh do the **opposite**:
  pass `--ignore-user-config`, **remove `--ignore-rules`**, and write an OWN hermetic
  `$CODEX_HOME/rules/default.rules` execpolicy. The consensus is authoritative — `--ignore-rules`
  would disable the execpolicy that is the reliable, OS-independent git/command block. **Update
  SAND-SPEC.md to match `AGENT_SANDBOX_SPEC.md` §2.**
- 2.2 **Go path has zero sandboxing.** `internal/backends/{codex_exec,claude_native}.go` build argv
  only from `BackendConfig.Args`/`Env`. They must implement (codex): hermetic `CODEX_HOME` (symlink
  only `auth.json/version.json/installation_id/models_cache.json`), `-c approval_policy="never"`,
  `-c project_doc_max_bytes=0`, the execpolicy `rules/default.rules` (git mutations always forbidden +
  the gate's non-git `bash_deny`), `--sandbox read-only|workspace-write -C <dir>`, and **role-
  conditional MCP injection** (§4). claude-native: built-in Agent-tool spec (no `-p` against OAuth) or
  `claude -p --bare --allowedTools "Edit(//abs)"` for the API-key/ollama tier.
- 2.3 **No `backends.toml`.** `backends.Resolve` errors `ErrBackendsConfigNotFound`, so `sand.dispatch`
  cannot spawn at all today. Seed + ship the backend templates the chains reference (claude_native,
  codex_exec, ollama).
- 2.4 **No Go `sand gate` subcommand.** `AGENT_SANDBOX_SPEC.md` §1 mandates a PreToolUse gate as a Go
  subcommand registered in **exec form** `{"type":"command","command":"<binpath>","args":["gate"]}`.
  Implement `sand gate` (the proven `bin/agent-dispatch.sh` `--gate` contract + `ta_action_gate.py`
  logic are the reference): per-FILE edit-scope, `edit:[]` ⇒ deny all (QA never edits), `bash_deny`
  git/command block, explicit-`allow` so the dev is never prompted, fail-closed for scoped agents,
  defer (allow) for the main orchestrator session.
- 2.5 **Gate hook is untracked.** `bin/agent_gate.sh` is gitignored — not in a clean checkout. The
  interim hook standardized across the four repos is the **Python** `ta_action_gate.py` (now in
  `.claude/hooks/`, tracked). sand's production gate is the Go `sand gate` (§2.4); retire the bash one.
- 2.6 **Role-name inconsistency.** `sand-chains.toml` previously used combined `ta-go-plan-qa` /
  `ta-go-build-qa`; personas + `agent-chains.sh` use the SPLIT `-proof` / `-falsification` names.
  **Standardize on the split names** (now done in `.claude/sand-chains.toml`, §3).
- 2.7 **Go engine is FE-blind.** `codex_exec.go renderMCPInjectionFlags` probes whatever `.mcp.json`
  declares — no `*-fe-*`/`*-go-*` branching. It must implement the role-conditional matrix (§4) so FE
  roles get Playwright and Go roles get gopls. See `SAND_FE_AGENT_GUIDE.md`.
- 2.8 **Per-run audit trace (veracity + sand corpus).** The bin/sh reference persists every dispatch's
  full stdout (response + codex tool stream), stderr (execpolicy `Rejected(...)` lines, diagnostics),
  and a `meta.json` (role/backend/model/served_by/gate/cwd/ts) under `.claude/agent-runs/` (gitignored).
  This is how an orchestrator audits that an agent's self-report matches what actually ran, and the
  reference corpus sand learns from. **sand's MCP MUST return AND persist the equivalent full trace +
  metadata** — proven in the bin/sh; replicate it in Go.

## 3. The project owner's canonical chains (sand must reproduce exactly)

Source of truth: `.claude/sand-chains.toml` (this repo, rewritten 2026-05-25) — **sand's OWN agents are
Go-only** (`ta-go-*` + closeout). The per-role spec below is identical for the FE family; `ta-fe-*`
rows are NOT in sand's own config but are what sand's MCP must GENERATE for consuming projects that
have FE (hylla/poly, ta) — see `SAND_FE_AGENT_GUIDE.md`. Per-role spec:

| Role | backend | model | effort / sandbox |
|---|---|---|---|
| planning | codex-exec | gpt-5.5 | effort=low, read-only |
| plan-qa-proof | claude-native | opus | — |
| plan-qa-falsification | codex-exec | gpt-5.5 | effort=high, read-only |
| builder | claude-native | haiku (sonnet fallback) | — |
| build-qa-proof | claude-native | sonnet | — |
| build-qa-falsification | codex-exec | gpt-5.5 | effort=low, read-only |
| closeout | claude-native | opus | — |

- **Builders are claude built-in haiku, NOT ollama.** sand stays flexible enough to support an ollama
  tier (commented example in the TOML), but the owner's chains use claude-native. The dispatcher adds
  `-c approval_policy="never"` for codex (sandbox is inert without it), so `opts` carry only
  `--sandbox <mode>` + `model_reasoning_effort`.
- Every codex role is **read-only** (planning + all QA-falsification are NON-EDITING; QA never edits).

## 4. Per-role tool / disallowed matrix (role-name-driven injection)

| Role | hylla | context7 | gopls | playwright | ta | edit | git |
|---|---|---|---|---|---|---|---|
| planning (go/fe) | read-only | ✅ | go only | **fe only** | create+update | none | ❌ |
| plan-qa proof+falsif | read-only | ✅ | go only | **fe only** | update | none | ❌ |
| builder (go/fe) | read-only | ✅ | go only | **fe only** | comment | **1–2 droplet files** | ❌ |
| build-qa proof+falsif | **none** | ✅ | go only | **fe only** | update | none | ❌ |
| closeout | read-only | ✅ | — | — | update+create | none | ❌ |

- **planning + plan-qa get hylla READ-ONLY; build-qa gets NO hylla** (just-shipped code isn't in the
  snapshot). **ALL FE roles get Playwright MCP** (`SAND_FE_AGENT_GUIDE.md`). **No role gets git
  mutation — the orchestrator is the sole committer.**

## 5. Cross-cutting invariants (never config-overridable)

- **GIT IS ORCHESTRATOR-ONLY.** No dispatched agent may `git commit/push/add/reset/checkout/branch/
  tag/stash/rebase/merge/restore` — enforced at the harness/OS layer (gate `bash_deny` + codex
  execpolicy), never by prompt.
- **EDIT-SCOPE.** An agent writes ONLY its granted file(s) (claude, per-FILE) or directory (codex,
  per-DIR `-C`), regardless of prompt. Off-scope writes FAIL; the agent reports the contradiction.
- **Veracity audit.** Every channel RETURNS the full tool-call trace (claude `--output-format json`;
  codex exec stream; the sand MCP envelope MUST carry it). Self-report ≠ truth. The bin/sh reference
  also **PERSISTS** the trace per dispatch to `.claude/agent-runs/<run>.tier<N>.<backend>.{out,err}` +
  `<run>.meta.json` (gitignored) so an orchestrator can audit after the fact + sand has a reference
  corpus. **sand's MCP MUST do the equivalent: return AND persist every tool call, stdout, stderr, and
  run metadata** — that is how an orchestrator knows nothing happened that shouldn't have.
- **Reasoning lives in the persona body**, never the output style (output style is ambient + stripped
  for small models; the persona always loads).
