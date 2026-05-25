# hylla bin/sh sandbox-gate POC — implementation pointer (for sand / ta / tillsyn)

> From **hylla/polyglot-foundation**. This is hylla's **working bin/sh proof-of-concept** of the
> 4-way-consensus per-dispatch gate (see `hylla_rebuttal.md` + the four `*_RECS`/`*_REBUTTAL` docs).
> **It is E2E-smoke-tested (results in §4) before sharing** (per dev directive). Distributed
> (uncommitted) to `sand/main`, `ta/main`, `tillsyn/main`.
>
> **Scope / ownership**: hylla uses **bin/sh** as the interim proof-of-concept; the gate hook is
> **Python/sh** (NOT the Go-binary — the Go-binary `gate` is sand's cross-OS end-state). **sand and
> tillsyn dispatch in Go code, never bin/sh** — sand will generate the MCP that replaces this bin/sh
> system; this POC is the reference implementation + the `--gate` contract sand should consume.
> Tested macOS only (Darwin 25, codex 0.133.0, claude 2.1.150, ollama 0.24.0); Windows/Linux unverified.

## 1. The files (pointers into this repo)

- **Gate hook** — `.claude/hooks/ta_action_gate.py` (Python; the bin/sh-era hook). PreToolUse.
  Resolves the per-dispatch allowlist from (a) `TA_GATE_ALLOWLIST` env [subprocess] or (b) the
  `<TA_ALLOWLIST>{json}</TA_ALLOWLIST>` block in the agent's first user message read from the parent
  transcript, keyed on `agent_type` [built-in subagents]. Emits **explicit `allow`** for permitted
  (so the dev is never prompted) and **`deny` + reason** for off-list Edit/Write or `bash_deny`
  Bash. No block ⇒ no-op (orchestrator unconstrained). Debug log: `.claude/hooks/ta_gate_debug.log`.
- **Hook registration** — `.claude/settings.json` → `hooks.PreToolUse` (matcher
  `Edit|Write|MultiEdit|NotebookEdit|Bash`).
- **Dispatcher** — `bin/agent-dispatch.sh` — takes `--gate '<json>'` and translates per backend
  (§3). codex: writes `$CODEX_HOME/rules/default.rules` execpolicy + `-C` + `approval_policy=never`
  (NO `--ignore-rules`). `claude -p --bare`: `--allowedTools "Edit(//abs)"` + `--disallowedTools`.
- **Chains / per-role sandbox** — `.claude/agent-chains.sh` (planning → `--sandbox read-only`,
  qa-falsification → `--sandbox workspace-write`).
- **Personas (tool-matrix)** — `.claude/agents/ta-{go,fe}-{planning,builder,plan-qa-proof,
  plan-qa-falsification,build-qa-proof,build-qa-falsification}.md` + `ta-closeout.md`. Each `tools:`
  line encodes which tools EXIST per role (§2); the gate adds per-call limits.
- **Orchestrator rules** — `CLAUDE.md` § "Orchestrator Role Boundaries" (the `<TA_ALLOWLIST>`
  injection contract + GIT-ORCHESTRATOR-ONLY).

## 2. Tool-matrix (enforced two ways: persona `tools:` = which tools exist; gate = per-call limits)

**NO role gets git commit/push/add/reset/etc. — orchestrator only** (gate `bash_deny` + persona
read-only-git rule + codex execpolicy). Reads stay broad.

| Role | source Edit/Write | hylla | context7 | websearch | gopls(go)/playwright(fe) | ta | mage |
|---|---|---|---|---|---|---|---|
| planning | ✗ | READ | ✓ | ✓ | ✓ | create+update | ✗ (read-only Bash) |
| plan-qa-proof / -falsification | ✗ | READ | ✓ | ✓ | ✓ | update(verdict) | ✗ |
| builder | ✓ (per-file gated) | READ | ✓ | – | ✓ | comment | ✓ (scoped) |
| build-qa-proof / -falsification | ✗ | **none** | ✓ | ✓ | ✓ | update(verdict) | ✓ (re-run) |
| closeout | ✗ | READ | ✓ | – | (LSP) | **update+create** | ✓ (final gate) |

(Matches your directive: planning + plan-qa twins get hylla-READ + context7 + websearch + gopls, no
source-write; build-qa excluded from hylla; closeout `update`+`create` gap fixed this round.)

## 3. The `--gate` contract → per-backend translation

`--gate '{"edit":["//abs/f.go","//abs/f_test.go"],"writable_dirs":["/abs/dir"],"bash_deny":["git commit","mage install","go get","go mod"],"network":false}'`

- **Built-in Agent tool (OAuth: builder/qa-proof/closeout)** — orchestrator injects the
  `<TA_ALLOWLIST>{edit,bash_deny}` block atop the spawn prompt; the Python hook enforces (per-file
  Edit/Write + bash_deny + explicit-allow). Serialize same-`agent_type` parallel dispatches.
- **`claude -p --bare` (ollama / non-OAuth ONLY — `--bare` needs an API key, NOT OAuth)** — dispatcher
  emits `--allowedTools "Edit(//abs) Write(//abs) MultiEdit(//abs)"` per `edit` file (**`//`
  double-slash mandatory**), the persona's MCP tools, a scoped Bash set (no bare `Bash`), and
  `--disallowedTools "Bash(<bash_deny>:*)"`. `--bare` = clean small-model context for free.
- **codex exec (planning + qa-falsification — both NON-EDITING)** — dispatcher writes hermetic
  `$CODEX_HOME/rules/default.rules` with `prefix_rule(...,decision="forbidden")` for git mutations
  (always) + the gate's non-git `bash_deny`; **`--sandbox read-only`** for these roles + `-c
  approval_policy="never"` (the sandbox is inert without it; `-a` is NOT a valid `codex exec` flag).
  **NO `--ignore-rules`** (it would disable our execpolicy). **QA NEVER edits source** — codex
  ignores the persona `tools:` and its hooks are dead, so `--sandbox read-only` (zero writes) is the
  ONLY mechanical source-edit gate for a codex role; `workspace-write` would let codex QA write source
  and is therefore wrong here (corrected 2026-05-25). QA reports its verdict via the ta MCP, which
  runs OUTSIDE the sandbox so it still posts under read-only. mage re-runs are the claude-native
  build-qa-PROOF twin's job + the orchestrator's drop-end gate. (`workspace-write -C <dir>` is only
  for a hypothetical codex *builder*, which doesn't exist — editing builders are claude-native;
  per-file is not available on codex anyway.)

## 4. E2E smoke results (this session, reproducible — `.../tmp/ta_gate_test/*.sh`)

- ✅ **codex execpolicy via the dispatcher + `--gate`** (`b9pf1aitl`): the probe ran three
  *commands* (not edits) — `git commit` **FORBIDDEN** (execpolicy git rule), `mage install`
  **FORBIDDEN** (`Rejected("policy forbids commands starting with 'mage install'")` — the
  gate→execpolicy translation), `git diff --stat` **RAN**. **The `--gate` → execpolicy wiring works
  end-to-end.**
- ✅ **codex QA is read-only → CANNOT edit source** (`bgezng0hj`, the corrected
  `chain_qa_falsification`): `ta-go-build-qa-falsification` probe → write a source file **BLOCKED**
  (read-only sandbox), `git commit` **BLOCKED**, `head go.mod` **RAN**, `SAND_QA_EDIT_PROBE.txt`
  **absent**. **QA never edits, enforced on both channels** (built-in: no `Edit`/`Write` in `tools:`;
  codex: `--sandbox read-only`).
- ✅ **built-in hook** — `ta_action_gate.py` unit-tested 19/19 + live: a `ta-go-builder` subagent's
  off-list Write **denied** (file absent, debug log `decision:deny`), on-list Write ran, `git
  commit`/`push` denied, read-only git explicit-allowed (no dev prompt); `ta-go-build-qa-proof`
  (read-only) edit-blocked + git-blocked.
- ✅ **`Edit(//abs)` per-file gate** (`T2_dslash.sh`, the mechanism the `-p` path emits): allowed
  edited, off-list `Edit` denied (`permission_denials`), file absent.
- ⚠️ **`-p` dispatcher path** — wired + `bash -n` syntax-clean, but **ollama is dormant (no chain
  row)** so its full dispatcher-E2E is deferred; the underlying `--allowedTools(//abs)` mechanism is
  T2-proven. (claude `-p --bare` needs an API key — verified.)
- ✅ **codex `.git`-ro is geometry-dependent → execpolicy is primary** (`T3clean.sh` repo-under-/tmp
  commit succeeded vs `T3v3.sh` repo-under-$HOME blocked) — why the dispatcher uses execpolicy, not
  the native `.git`-ro, for the codex git-block.

## 5. For sand (the Go MCP that replaces this)

The stable interface is the **`--gate '<json>'` contract (§3)** + the per-backend translation table.
sand generates: the gate hook (Go-binary `<cli> gate` exec-form for cross-OS — beats this POC's
Python on Windows), `settings.json` registration, and `$CODEX_HOME/rules/default.rules`, all
project-local, from the contract + policy knobs (system-prompt mode, strip-set, sandbox mode). The
veracity rule holds in every form: **return the full tool-call trace; personas emit `## Tools Used`;
orchestrator is the sole committer.** This bin/sh POC is the working reference for that generation.

## 6. Sources / companions
`hylla_rebuttal.md`, `HYLLA_SANDBOX_IDEA.md`, `hylla_recs.md` (hylla); `TA_RECS.md`/`TA_REBUTTAL.md`/
`TA_SANDBOX_IDEA.md`; `SAND_RECS.md`/`SAND_REBUTTAL.md`; `till_recs.md`/`TILL_REBUTTAL.md`/
`AGENT_SANDBOXING_tillsyn.md`. Vendor docs cited throughout those (Claude Code `hooks`/`permissions`/
`cli-reference`/`headless`/`memory`; codex `exec-policy`/`concepts/sandboxing`/`config-reference`/
`windows`; issues #16732, #9460, #24098/#17135, #15505).
