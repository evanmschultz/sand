# Agent Sandboxing & Edit-Scope Enforcement — tillsyn findings (2026-05-24)

> **Cross-repo research note.** tillsyn, ta, and hylla are each independently researching +
> smoke-testing the same problem. This file is **tillsyn's empirical findings + proposed shape**.
> It is copied into `ta/main`, `sand/main`, and `hylla/polyglot-foundation` so the three results
> can be compared and a final shape chosen. Everything below was **smoke-tested on 2026-05-24**
> against `codex-cli 0.133.0` and `claude 2.1.150` on macOS (Seatbelt sandbox).

## 1. The problem — per-dispatch agent gatekeeping (two axes)

When the orchestrator dispatches an agent (builder / QA / planner), the agent must be **hard-gated**,
not merely instructed:

1. **No git mutations.** Dispatched agents must NEVER `git add/commit/push/reset/stash/checkout/…`.
   **The orchestrator is the SOLE committer, always.** (Real incident that motivated this: a builder
   ran `git commit` and swept the ENTIRE working tree — its own files + a sibling builder's
   in-progress work + unrelated doc edits — into one commit. Persona instruction alone did NOT
   prevent it.)
2. **Edit-scope.** An agent may edit ONLY the file(s) passed to it at dispatch (the droplet's
   declared `paths` — ideally 1–2 files), **regardless of what its prompt says**. If the prompt
   contradicts the allowed-files list, the edit must FAIL and the agent should REPORT the
   contradiction (good sandboxing AND a built-in smoke test).

Must work across the three dispatch channels:
- **Claude built-in Agent tool** (`Agent(subagent_type=…)`) — in-process subagent.
- **`claude -p --bare`** — subprocess (non-OAuth / ollama path).
- **`codex exec`** — separate OpenAI process (planning / qa-falsification, possibly builders).

## 2. Results matrix (all rows smoke-tested)

| Channel | git-mutation block | edit-scope granularity | mechanism | smoke test |
|---|---|---|---|---|
| Claude built-in Agent tool | ✅ hook | ✅ **file-level** | shared PreToolUse hook (keys on `agent_id`) | **PASS** |
| `claude -p --bare` | ✅ (hook stripped → permissions) | ✅ **file-level** | `--settings` `permissions` allow/deny | **PASS** |
| `codex exec` | ✅ **native** (`.git` read-only) | ⚠️ **directory-level only** | workspace-write sandbox (cwd/`-C`) | **PASS (dir) / FAIL (file)** |

**Bottom line:** Claude (both modes) enforces **exact-file** edit-scope. codex enforces only
**directory-level** writes — its hooks do not fire in 0.133.0 (see §5), and its sandbox is
directory-granular.

## 3. Mechanism per channel

### 3.1 Claude built-in Agent tool — shared PreToolUse hook (file-level)
- A `PreToolUse` hook registered in `~/.claude/settings.json` (matcher `Edit|Write|MultiEdit|Bash`).
- The hook input carries **`agent_id`** ONLY when the call originates from a subagent (absent for the
  main orchestrator session) — so the main session is never constrained, subagents always are.
- The hook (a) denies git-mutation `Bash` commands, (b) checks the edit target file against a per-call
  **allowed-files allowlist** and denies anything outside it with a message telling the agent to
  report the contradiction.
- **Allowlist delivery (the hard part for in-process concurrency):** the in-process subagent shares the
  orchestrator's session env, so a shared env var can't differ per concurrent builder. The hook resolves
  the allowlist in priority order: `$AGENT_GUARD_ALLOWED_FILES` → `~/.claude/edit-scope/<agent_id>.files`
  → `~/.claude/edit-scope/<agent_type>.files`. For concurrent builders the orchestrator writes an
  `agent_id`-keyed scope file at dispatch (fail-closed until present).
- **Deny JSON:** `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"…"}}`.
- **Smoke test (PASS):** subagent prompted to edit `allowed.txt` + `forbidden.txt`; only `allowed.txt`
  permitted; `forbidden.txt` denied; agent reported the prompt-vs-scope contradiction verbatim.

### 3.2 `claude -p --bare` — `--settings` permissions (file-level)
- Per-invocation: `claude -p --bare --settings '{"permissions":{"allow":["Edit(<file>)","Write(<file>)"]}}'`.
- Claude permission rules accept **specific file paths** and apply to all tools; deny/prompt-fail-closed
  blocks unlisted files in non-interactive `-p`. `--bare` **honors `--settings`** (verified).
- Per-process → concurrency-safe (each builder gets its own settings).
- **Smoke test (PASS):** prompt ordered edits to both files; `forbidden.txt` blocked at BOTH the
  Edit-tool layer and the shell-redirection (OS) layer; agent reported it.

### 3.3 `codex exec` — workspace-write sandbox (directory-level)
- **git is blocked natively:** under `--sandbox workspace-write`, `.git` is **read-only**
  (recursive, pointer-file-resolved), **independent of approval policy and execpolicy `--ignore-rules`**.
  So `git commit/add/reset` fail at the OS layer with no extra config.
- **edit-scope is directory-level only:** launch codex with its **workspace (cwd, or `-C <dir>`) set to
  the droplet's directory** under `--sandbox workspace-write`. Writes are confined to that directory;
  sibling dirs and the parent are OS-blocked; **reads of the whole repo still work** (context preserved).
- **Smoke test (PASS, dir-level):** cwd=`droplet/`; `./target.txt` edited; `../other/sibling.txt` and
  `../rootfile.txt` writes "rejected (outside the project)"; `../rootfile.txt` read succeeded.
- **Precision:** the writable scope is codex's **workspace**, set by `-C <dir>` (or the cwd) — NOT the
  directory codex is *invoked* from. To "invoke from dir A but confine writes to dir B", pass `-C B`
  (writes follow the workspace, not the launch dir). The smoke test used **workspace == cwd == `droplet/`**
  and confirmed writes outside it are blocked while reads outside still work; the A≠B variant (e.g. invoke
  at repo root, confine writes to one subpackage) follows the identical rule but was **not separately
  exercised**.
- **File-level is NOT achievable on codex** (see §5).

## 4. The shared hook (Claude + codex share the SAME script + deny JSON)
Both Claude Code and codex implement a **Claude-style PreToolUse hook** with the identical
`permissionDecision`/`hookSpecificOutput` deny shape. One script can serve both, with channel-aware
target extraction:
- Claude `Edit/Write/MultiEdit` → `tool_input.file_path`.
- codex `apply_patch` → parse `*** Add/Update/Delete File: <path>` from `tool_input.command`.
Registered in `~/.claude/settings.json` (Claude) AND `CODEX_HOME/hooks.json` (codex). **Works today for
Claude built-in.** For codex it is **registered but inert** (§5).

## 5. The codex gap (empirically proven)
- codex docs say `apply_patch` PreToolUse hooks were fixed (issue #16732 → PR #18391). **But codex 0.133.0
  in `exec` mode fired ZERO PreToolUse hooks** — a logging probe registered for matcher `.*` captured
  nothing, not even for the `Bash`/shell tool (the longest-supported path). The docs are ahead of the
  released CLI (or exec-mode excludes hooks).
- codex sandbox `writable_roots` / permission profiles are **directory-level** only (no file-level write
  allowlist; permission profiles do read-denial globs, not write-allowlisting).
- ⇒ **codex currently cannot enforce exact-file editing by any means.** Best codex can do is
  directory-level (§3.3) + native `.git` read-only.
- **Open item:** upgrade codex to a version where hooks fire in exec mode, re-run the probe; if hooks
  fire, the shared hook (§4) works for codex with no change → file-level on codex.

## 6. tillsyn's proposed shape (for the 3-way decision)
1. **git:** shared PreToolUse hook for Claude (built-in + claude-p); native `.git`-read-only sandbox for
   codex. Orchestrator is the sole committer everywhere.
2. **edit-scope:**
   - Edit-capable builders that need **exact-file** scope → dispatch on **Claude** (built-in hook, or
     `claude -p --bare` `--settings` permissions).
   - **codex** → use for **non-edit roles** (planning / qa-falsification), where edit-scope is moot; OR,
     if codex must edit, confine its **workspace to the droplet's directory** (dir-level).
3. **Per-call allowlist plumbing:** subprocess channels (claude-p, codex) get the list via process env at
   spawn (clean, concurrency-safe); the in-process built-in Agent tool gets it via an `agent_id`-keyed
   scope file written by the orchestrator at dispatch.
4. **Persona text remains** as defense-in-depth + the source the dispatch translation reads, but is NOT
   the enforcement layer (advisory failed in the real incident).

## 7. Open questions for comparison with ta / hylla
- Did ta/hylla find a working codex file-level mechanism (newer codex, execpolicy `.rules`, or a
  PATH-shadowed `git`/editor shim)?
- Is the `agent_id`-scope-file race (orchestrator writes the file after learning the id at launch) worth
  solving, or do we standardize on subprocess dispatch (claude-p / codex) where per-invocation env is
  clean + concurrency-safe?
- Should builders move OFF the in-process Agent tool entirely (to claude-p / codex subprocess) so
  per-dispatch scoping is always native + concurrency-safe? (Reconcile with the "never claude -p with
  OAuth" rule — `claude -p --bare` ran fine under OAuth in our test.)
- Is directory-level acceptable for codex builders, or do we keep codex to non-edit roles until its
  hooks fire?

## 8. Sources
- Claude Code hooks: https://code.claude.com/docs/en/hooks.md (PreToolUse input incl. `agent_id`/`agent_type`; deny JSON)
- Claude Code permissions: https://code.claude.com/docs/en/permissions.md (Edit/Write path globs; deny precedence)
- Claude Code sandboxing: https://code.claude.com/docs/en/sandboxing.md (filesystem allowWrite)
- codex sandboxing: https://developers.openai.com/codex/concepts/sandboxing + agent-approvals-security (`.git` read-only, independent of approval)
- codex config: https://developers.openai.com/codex/config-reference (`writable_roots` dir-level)
- codex hooks: https://developers.openai.com/codex/hooks ; gap: https://github.com/openai/codex/issues/16732 + PR https://github.com/openai/codex/pull/18391 (docs ahead of 0.133.0)
