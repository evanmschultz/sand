# ta Sandbox & Gatekeeping — Findings

The **ta** data point for the cross-project (ta / hylla / tillsyn) comparison on how to
runtime-confine dispatched agents — not by trusting their prompt. Every claim below is tagged
with how it was established: **[real-exec]** (a real `claude`/`codex` run, verified on disk or via
the harness result envelope), **[docs]** (official Claude Code / codex docs), or **[unit]** (hook
logic driven with mock inputs). Tested 2026-05-24 on macOS, Claude Code 2.1.150, codex 0.133.0,
ollama 0.24.0.

> Shared by cp into `sand/main`, `hylla/poly`, `tillsyn/main` as ta's findings for the 3-way
> compare. Per-project adaptation (substrate `mcp__ta__*` vs `mcp__tillsyn__till_*`, the build
> runner, and the future sand MCP that replaces bin sh) is discussed before adoption.

## 1. Goal & threat model

Agents are NOT trusted to honor their prompt. Enforcement must be harness/OS-level, prompt-proof:
- **No git mutation** — read-only git (`diff`/`log`/`status`) only; never `commit`/`push`/`reset`/etc.
- **Scoped shell** — only an explicit allowlist; never arbitrary `Bash`, never raw language tooling.
- **Bounded edits** — agents edit ONLY a call-time-specified set: **files** (claude) or a **dir** (codex).
- **Reads stay broad** — confining reads is NOT cleanly available and is accepted (agents need context).
- The **orchestrator is the only actor that commits/pushes.**

## 2. The capability contract (uniform call shape)

The **persona** (`.claude/agents/<role>.md`) is the canonical source of tools/bash. The dispatcher
CONVERTS the persona to the runtime form. The per-call contract adds only the dynamic bits:

```json
{ "role": "ta-go-builder",
  "edit_files": ["internal/install/install.go","internal/install/registrations.go"],
  "edit_dir":   "internal/install",
  "extra_bash": [] }
```
- `edit_files` (file gate) → consumed for **claude** runtimes; `edit_dir` (dir gate) → **codex**.
- Same JSON shape regardless of runtime; the dispatcher resolves the role's runtime from the chain
  def and **validates**: a **codex** role given `edit_files` (no `edit_dir`) → **error to orch**
  ("codex gates per-directory; provide edit_dir"). A **built-in** role → refused (use Agent tool).
- `extra_bash` is ONLY a rare one-off, dev-approved command not already in the persona. Persona is canonical.

## 3. Per-runtime enforcement (confirmed)

### 3.1 Built-in Agent tool (claude-native: builder / qa-proof / closeout)
- **No per-call `allowedTools`** exists on the Agent/Task tool — subagent tools are STATIC frontmatter. **[docs]**
- So per-call file gating uses a **PreToolUse hook** keyed on **agent identity** + a gates file the
  orchestrator writes pre-dispatch. **[real-exec]** A `general-purpose` subagent edited its allowed
  file but its write to a forbidden file was **denied** (reason surfaced; forbidden file unchanged on disk).
- Hook input carries `agent_id` (unique per spawn) + `agent_type` (= subagent_type); **absent for the
  main thread** → the orchestrator is never gated. **[docs]** Hooks **reload mid-session** via the file
  watcher — **no restart needed**. **[docs]**
- LIMIT: orchestrator can't pre-know/set `agent_id`, and there's no spawn metadata pass-through, so
  **parallel spawns of the SAME `agent_type` with DIFFERENT file lists can't be distinguished** — use
  serial dispatch, distinct agent_types, or the `claude -p` subprocess path for those. **[docs]**

### 3.2 `claude -p --bare` (subprocess; e.g. ollama / non-OAuth)
- **`--bare` CANNOT run hooks — period.** It skips hook auto-discovery, and `--settings` is a
  settings-key merge (permissions/env/model) — hooks are NOT a `--settings` feature and load in a
  phase `--bare` skips. **[docs]** + **[real-exec]** (a `--settings` hook did not fire; a debug
  always-deny hook produced no log).
- So file gating under `--bare` uses **`--allowedTools 'Edit(file1)' 'Edit(file2)'`** (deny-by-omission;
  a CLI flag honored under `--bare`). **TRIPLE-CONFIRMED [real-exec]**: with cloud Haiku, qwen3-coder:30b,
  and gpt-oss:20b — allowed file edited, forbidden write **permission-denied** (in the result envelope's
  `permission_denials`), forbidden file unchanged on disk. Prompt-proof (the prompt ordered editing the
  forbidden file; the harness blocked it).
- **READS are NOT gated** by `--allowedTools Read(path)` — a `--bare` agent read a forbidden file's
  secret despite only `Read(allowed)` being listed. **[real-exec]** Reads are broad (accepted).
- `--append-system-prompt` (persona) and `--mcp-config` ARE honored under `--bare`. **[docs]**

### 3.3 codex exec (planning / qa-falsification)
- **Git-mutation block**: execpolicy `prefix_rule(pattern=["git","<sub>"], decision="forbidden")` in a
  hermetic `$CODEX_HOME/rules/default.rules` blocks the command **in-sandbox at CreateProcess** (not just
  escalation). **[real-exec]** (`git commit` → `Rejected`; `git diff` ran; commit never landed.) Requires
  NOT passing `--ignore-rules` (it disables execpolicy); hermetic CODEX_HOME keeps other rules out.
- **Dir write confinement**: `--sandbox workspace-write` makes writable = **`-C` workspace dir + /tmp +
  $TMPDIR**. Setting `-C` to the allowed dir confines writes there; a write to a sibling dir was
  **"operation not permitted"**. **[real-exec]**
- `writable_roots` only ADDS roots — it can NOT narrow the workdir; so `-C` MUST be the allowed dir
  (can't keep `-C` at project root and restrict to a subdir). **[real-exec]**
- **Reads stay broad** under workspace-write (cross-dir read succeeded). **[real-exec]**
- **Hooks do NOT work for codex** file-editing: a PreToolUse `apply_patch` hook hung ~15 min on 0.133.0
  (matches openai/codex#16732). So codex uses execpolicy + `-C`, not hooks. **[real-exec]**
- Per-tool MCP `approval_mode="approve"` injection controls which MCP tools are exposed (ta / hylla-ro /
  context7 / gopls / playwright) — see AGENT_DISPATCH.md.

### 3.4 Anti-misroute
- If the orch calls bin sh for a chain-def **claude-native (built-in)** role, the dispatcher REFUSES with
  "dispatch via the built-in Agent tool; file-gating there is the PreToolUse hook + gates file." (Exists as
  `dispatch_claude_native` REFUSED.) This keeps OAuth roles on the subscription + the correct gating path.

## 4. The dual-mode hook (`bin/enforce_editable_paths.sh`)

One script, auto-selecting from hook input:
- **Mode A — Agent-tool subagent** (`agent_id` present): gate by `agent_type` against the gates file
  `{ "<agent_type>": ["/abs/path", ...] }` (default `$HOME/.ta-edit-gates.json`, override `TA_EDIT_GATES_FILE`).
- **Mode B — `claude -p`** (`agent_id` absent, `TA_EDITABLE_PATHS` set): gate against the colon-separated env (isolated per subprocess; parallel-safe). NOTE: under `--bare`, hooks don't fire (3.2) so Mode B applies only to NON-bare `-p`; `--bare` uses `--allowedTools` instead.
- **Mode C — orchestrator main thread** (neither): defer (never gated).
Fail-open on internal error. **[unit]** all three modes verified with mock inputs; Mode A verified **[real-exec]** live.

## 5. Reads vs writes — the consistent model

Across ALL runtimes: **writes are confined** (files for claude/Agent-tool via `--allowedTools`/hook; a dir
for codex via `-C`), **reads are broad**. Read-confinement is not cleanly available via `--allowedTools`
(claude) or workspace-write (codex); it would need OS-level sandboxing. Accepted — build agents need read
context, and the danger is writes/commits, which ARE all confined.

## 6. Conversion guide (contract → dispatch args)

- **Built-in Agent tool**: persona `tools:` (static) + orchestrator writes `edit_files` into the gates file
  under `agent_type` + the PreToolUse hook enforces. git-mutation blocked by no `Bash(git commit*)` in persona.
- **`claude -p --bare`**: `--append-system-prompt <persona body>` + `--allowedTools "<persona bash>,Edit(<edit_files>)"` (deny-by-omission gates files) + `--mcp-config`. No hook (`--bare` can't). 
- **codex exec**: hermetic `CODEX_HOME` with `rules/default.rules` (forbid git-mutation) + `--sandbox workspace-write -C <edit_dir>` (or `read-only` for non-editing roles) + inline MCP injection. No `--ignore-rules`.

## 7. bin sh now / sand MCP future

- **Now**: `bin/agent-dispatch.sh` (ta = canonical, synced to sand/hylla/valv). Codex execpolicy git-mutation
  guard is WIRED + smoke-verified (execpolicy check + dry-run). `bin/enforce_editable_paths.sh` shipped.
- **Future**: the sand MCP replaces bin sh; it consumes the §2 contract and emits the §6 args. The contract +
  adapters are the stable interface; only the orchestration host changes.

## 8. Model tool-calling viability (through Claude Code + ollama)

The gate is harness-level + model-independent, but the model must emit REAL tool calls to do work / exercise it.
- `qwen2.5-coder:7b` (7.6B) → ❌ **text-emits** tool calls. Root cause: its tool-template requires
  `<tool_call>…</tool_call>` (no backticks), but the model emits ` ```json {…}` instead → ollama can't
  parse → passed as text → no `tool_use` (num_turns:1, files unchanged). Has `tools` capability but is too
  weak to follow the format; not template-fixable. **[real-exec]**
- `gpt-oss:20b` (20.9B) → ✅ real tool calls + gate holds (`permission_denials` recorded), but **slow** (~10 min/task; thinking model). **[real-exec]**
- `qwen3-coder:30b` (30.5B) → ✅ real tool calls + gate holds. **[real-exec]**
- cloud Haiku → ✅ fast + reliable + gate holds. **[real-exec]**
- **Smallest viable LOCAL tool-caller tested = `gpt-oss:20b`**; `qwen2.5-coder:7b` is unusable. For speed, qwen-30b or cloud Haiku.

## 9. Status

- CONFIRMED [real-exec]: Agent-tool agent-identity edit-gate (live, on-disk); `--bare` `--allowedTools` file
  edit-gate (Haiku + qwen-30b + gpt-oss-20b, `permission_denials`); `--bare` cannot run hooks; reads broad
  (claude + codex); codex git-mutation execpolicy block; codex `-C` dir confinement; codex apply_patch hook
  non-viable; model viability spectrum.
- BUILT: codex execpolicy guard wired into `dispatch_codex` (+ smoke); `bin/enforce_editable_paths.sh` (dual-mode).
- REMAINING: wire the JSON-contract → args conversion + runtime-mismatch validation into bin sh; wire codex
  `-C` per-droplet; full bin sh dry-run + real smoke of the conversion; the parallel-same-type Agent-tool case.

## 10. Notes

- Read-only git + scoped Bash close the shell-write bypass (an agent with general shell could `echo > file`
  to evade Edit-gating; builders' Bash is scoped to git-read + mage only).
- ollama-routed costs in result envelopes are Claude Code estimates, not real bills (served by local GPU = $0).
