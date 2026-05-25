# till_recs — tillsyn's recommendation for agent edit/git sandboxing

> **From tillsyn, for the cross-repo decision (tillsyn / ta / hylla / sand).** After reading ta's
> (`TA_SANDBOX_IDEA.md`) and hylla's (`HYLLA_SANDBOX_IDEA.md`) findings AND **empirically
> re-testing every contested claim** (not trusting any doc), this is what tillsyn thinks is best —
> cross-OS (Windows / Linux / macOS), with reasoning + resources. Tested 2026-05-25 on macOS,
> `claude 2.1.150`, `codex-cli 0.133.0`.

## TL;DR — the recommendation

1. **Edit-capable builders → `claude -p --bare --settings permissions`.** Per-file edit gate via
   `permissions.allow:["Edit(//abs)","Write(//abs)"]` + git/dangerous-bash block via
   `permissions.deny` / `--disallowedTools`. **Empirically proven**, works **under `--bare`**, is
   **fully cross-OS (CLI flags + JSON, no hook script)**, concurrency-safe (own process), and runs on
   the OAuth subscription. This is the primary mechanism.
2. **`--allowedTools "Edit(/path)"` path-scoping is NOT reliable — do not use it for gating.** In our
   test it denied **even the allowed file**. Use `--settings permissions` (precedence-clear, proven).
3. **codex → directory-level only.** `--sandbox workspace-write -C <dir>` (edit roles) or
   `--sandbox read-only` (non-edit) for write confinement; **execpolicy `rules/default.rules`** for the
   git/command denylist (proven). Per-file is **impossible on codex 0.133.0** (its PreToolUse hooks fire
   for nothing in `exec` mode). ⇒ keep **editing builders on Claude**; use codex for non-edit roles or
   where dir-level is acceptable.
4. **Any unavoidable hook (Claude built-in Agent tool) → write it in Node.js**, not bash or Python.
   Claude Code guarantees `node` on every OS; bash hooks fail on native Windows, Python needs Python
   everywhere.
5. **Prefer CLI/JSON enforcement over hook scripts** wherever possible — it is inherently portable and
   has no per-OS shell dependency. Reserve OS-level sandboxes (`sandbox-exec`/`bwrap`) as an optional
   coarse outer ring only (per-OS, fragile, `sandbox-exec` is deprecated on macOS).

## Empirical truth table (re-tested this session — verdicts, not doc claims)

| Claim under test | Result | Verdict |
|---|---|---|
| claude `-p --bare --settings permissions.allow[Edit(//abs)]` gates per-file | allowed file edited; forbidden **blocked**; fail-closed in non-interactive `-p` | ✅ **USE THIS** |
| claude `-p --bare --allowedTools "Edit(/abs)"` gates per-file | **denied even the allowed file** (path-scope didn't grant) | ❌ fragile — avoid |
| Built-in Agent-tool PreToolUse hook gates a subagent's edit | allowed edited, forbidden denied, agent reported contradiction | ✅ works (needs hook script) |
| codex `--sandbox workspace-write -C <dir>` confines writes | writes inside dir OK; sibling + parent **blocked**; outside **reads OK** | ✅ dir-level only |
| codex per-file edit gate via PreToolUse hook (0.133.0 exec) | **zero hooks fired** (probe logged nothing, even for Bash) | ❌ not available |
| codex execpolicy `prefix_rule(decision="forbidden")` blocks a command | command **rejected at CreateProcess** ("policy forbids…") | ✅ works (no `--ignore-rules`) |
| codex `.git` read-only under workspace-write | git mutation blocked at OS layer, independent of approval/rules | ✅ free |

## Recommended mechanism per channel (cross-OS)

### A. `claude -p --bare` — edit builders (PRIMARY, proven, scriptless, cross-OS)
```bash
claude -p --bare --permission-mode default \
  --settings '{"permissions":{
     "allow":["Edit(//abs/file1)","Write(//abs/file1)","Edit(//abs/file2)","Write(//abs/file2)"],
     "deny":["Bash(git commit:*)","Bash(git push:*)","Bash(git add:*)","Bash(git reset:*)",
             "Bash(git checkout:*)","Bash(git stash:*)","Bash(git rebase:*)","Bash(git merge:*)"]}}' \
  <<<'<task prompt>'
```
- File-level edit gate: only the listed files are writable; off-list edits fail-closed (no prompt in
  `-p`). **Use `//`-prefixed absolute paths** (single-slash `/abs` did NOT match in testing).
- git block: `permissions.deny` (or `--disallowedTools "Bash(git commit:*)" …`).
- Clean context for small/cheap models: `--bare` strips CLAUDE.md/plugins/hooks. (If you need MCP
  servers or the richer hook, instead of `--bare` use `CLAUDE_CODE_DISABLE_CLAUDE_MDS=1` +
  `CLAUDE_CODE_DISABLE_AUTO_MEMORY=1` — hylla's recipe — which keeps hooks alive.)
- Cross-OS: pure flags + JSON; identical on Win/Linux/Mac; no shell-specific script.
- Concurrency-safe: each builder is its own process with its own `--settings`.
- Section 0 / reasoning directive belongs in the **persona** passed via `--append-system-prompt`
  (not the output style — the output style is ambient and gets stripped).

### B. Claude built-in Agent tool — non-edit roles / fallback (needs a Node hook)
- No per-dispatch `--settings` exists for the Agent tool (session-wide only), so per-call gating needs a
  **PreToolUse hook** (matcher `Edit|Write|MultiEdit|Bash`).
- **Write the hook in Node.js** (`node hook.mjs`) for cross-OS. Resolve the allowlist from the
  orchestrator-embedded `<TA_ALLOWLIST>{json}</TA_ALLOWLIST>` block in the spawn prompt, read from the
  parent transcript keyed on `agent_type` (hylla's approach — no scope-file, no `agent_id` race). Emit
  **explicit `allow`** for in-list actions (plain `exit 0` defer prompts the dev for non-listed cmds).
- Limit: concurrent dispatches of the **same** `agent_type` with different lists can't be distinguished
  (orchestrator can't pre-set `agent_id`). ⇒ serialize same-role, or route those to channel A.

### C. codex exec — non-edit roles / dir-level edit
- Non-edit (planning / qa-falsification): `--sandbox read-only` (zero writes; injected MCP still works —
  MCP runs outside the sandbox).
- Edit (if used): `--sandbox workspace-write -C <droplet-dir>` (+ `exclude_slash_tmp`,
  `exclude_tmpdir_env_var`, `network_access=false`). Writes confined to that dir; **per-file not possible**.
- git + command denylist: hermetic `CODEX_HOME` with `rules/default.rules`
  (`prefix_rule(pattern=["git","commit"],decision="forbidden")`, also `mage install` / `go get`),
  **without `--ignore-rules`**; `.git` is read-only for free.
- Clean context: hermetic `CODEX_HOME` + `-c project_doc_max_bytes=0`.
- Cross-OS: codex's own sandbox per OS (Seatbelt / Landlock+seccomp / Windows-native) — we only pass flags.

## Cross-OS reasoning (Win / Linux / Mac)

- **CLI/JSON enforcement is the portable core.** `--settings permissions` (Claude) and codex
  `--sandbox`/execpolicy are flags+files, identical everywhere → no per-OS branching. This is why
  channel A is the primary recommendation.
- **Hook scripts are the portability liability.** bash hooks (ta's `.sh`, tillsyn's first draft) **fail
  on native Windows**; Python hooks (hylla's `.py`) need Python on every machine. **Node is the only
  runtime Claude Code guarantees on all three OSes** → any required hook must be a `.mjs` invoked as
  `node`, using `node:path`/`node:os` (never hardcode `$HOME` / `%USERPROFILE%`).
- **Avoid OS-level sandboxes as the gate.** `sandbox-exec` (macOS) is deprecated and version-drifts;
  `bwrap` is Linux-only; Windows differs. Keep them — if at all — as a coarse optional outer ring.

## Unified shape for the bin-sh redo → sand MCP

- **One gate contract** (hylla's): `--gate '{"edit":[…],"writable_dirs":[…],"bash_deny":[…],"network":false}'`
  + ta's runtime-mismatch validation (codex given `edit` only → error "codex is dir-level; give writable_dirs";
  built-in role via bin-sh → refuse "use the Agent tool").
- **Per-backend translation** the converter (bin-sh now, sand MCP later) emits:
  - claude `-p` → `--settings permissions` (allow `Edit(//file)`+`Write(//file)`, deny `bash_deny`) [primary].
  - built-in Agent tool → `<TA_ALLOWLIST>` in prompt + **Node** PreToolUse hook [fallback].
  - codex → `-C <writable_dir>` / `--sandbox read-only` + execpolicy `rules/default.rules` + MCP inject.
- **Default routing**: edit builders → channel A (`claude -p`); codex → non-edit roles; built-in Node hook → fallback.
- **Veracity (all backends)**: return the full tool-call trace (`--output-format json` for claude `-p`;
  the exec stream for codex) so the orchestrator verifies claims against ground truth; every persona
  emits a `## Tools Used` section. The orchestrator is the **sole committer**, always.
- **Configurable knobs** (tillsyn + sand own these, not hardcoded): system-prompt mode (replace vs
  append), the context strip-set, and the gate spec — hylla's "strip-everything persona-only" is one profile.

## Scorecard vs ta / hylla (empirical)

- **ta** — execpolicy ✅ (confirmed by us); JSON contract + validation ✅ (adopt); model-viability data ✅.
  But its claude `-p` gate (`--allowedTools 'Edit(file)'`) is **fragile** in our test → replace with
  `--settings permissions`.
- **hylla** — most complete; transcript-delivered allowlist + explicit-allow + clean-context env-strips +
  MCP-outside-sandbox + the `outputStyle:null` trap = all adopt. Two refinements: (1) you do **not** need
  to drop `--bare` for file-gating (`--settings permissions` gates under `--bare`); drop it only for the
  richer hook/MCP. (2) its hook is Python → prefer **Node** for Windows.
- **tillsyn** — `--settings permissions` is the proven winner; we add the **Node cross-OS** point and the
  **execpolicy** confirmation.

## Resources

- Claude Code hooks (PreToolUse input `agent_id`/`agent_type`, deny JSON): https://code.claude.com/docs/en/hooks.md
- Claude Code hooks cross-platform — **use `node`**: https://claudefa.st/blog/tools/hooks/cross-platform-hooks
- Claude Code permissions (Edit/Write path globs, `//` absolute, deny precedence): https://code.claude.com/docs/en/permissions.md
- Claude Code sandboxing / settings / CLI reference (`--bare`, `--settings`, `--disallowedTools`, `--append-system-prompt`): https://code.claude.com/docs/en/sandboxing.md , .../settings.md , .../cli-reference.md
- Claude context strips (`CLAUDE_CODE_DISABLE_CLAUDE_MDS`, `_AUTO_MEMORY`, `_GIT_INSTRUCTIONS`): https://code.claude.com/docs/en/memory.md , .../env-vars.md
- codex sandboxing (Seatbelt / Landlock+seccomp; `.git` read-only): https://developers.openai.com/codex/concepts/sandboxing , .../agent-approvals-security
- codex config (`sandbox_workspace_write.writable_roots`/`exclude_*`/`network_access`): https://developers.openai.com/codex/config-reference
- codex exec-policy (Starlark `prefix_rule(decision="forbidden")`): https://developers.openai.com/codex/exec-policy
- codex hooks (present but did NOT fire in 0.133.0 exec): https://developers.openai.com/codex/hooks ; gap https://github.com/openai/codex/issues/16732
- Companion docs in this repo set: `AGENT_SANDBOXING.md` (tillsyn findings), `TA_SANDBOX_IDEA.md`, `HYLLA_SANDBOX_IDEA.md`, `AGENT_DISPATCH.md`.
