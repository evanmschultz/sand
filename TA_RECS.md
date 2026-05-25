# TA Recommendations — Agent Sandboxing & Gatekeeping

ta's opinionated recommendation for the unified system, for the cross-agent (ta / hylla / tillsyn)
+ dev decision. Every claim is **evidence-tagged**: `[real-exec]` = a real `claude`/`codex` run
verified on disk or via the result envelope's `permission_denials`; `[docs]` = vendor doc cited;
`[contradicted]` = another project's claim that ta's own run did NOT reproduce. Companion to the
findings in `TA_SANDBOX_IDEA.md`. Tested 2026-05-24, macOS (Darwin 25), Claude Code 2.1.150,
codex-cli 0.133.0, ollama 0.24.0.

> **Rebuttal protocol (agreed for this 3-way decision).** A recommendation is adopted only if it is
> (1) **source-backed** (vendor doc) AND (2) **reproduced** by at least one project's real run. When
> a project rebuts, we check THEIR cited source AND re-run the probe; a claim that one project's run
> contradicts (e.g. §3.1) is NOT adopted until the discrepancy is reconciled by a shared re-test.
> Self-report ("it works") without an on-disk / `permission_denials` / repo-log artifact does not count.

## 1. ta's recommended best system (cross-OS)

1. **One hook = a compiled Go `gate` subcommand** on the existing project CLI (`ta gate` / `till gate`
   / sand's binary), invoked in **exec form** (`{"type":"command","command":"<binpath>","args":[]}`).
   Reads the PreToolUse JSON on stdin, emits the `permissionDecision` allow/deny envelope.
   **Rationale**: it is the ONLY hook form that runs identically on Windows, Linux, and macOS with
   **zero runtime deps** — `.sh` is Unix-only (Windows hooks run via Git Bash, not guaranteed), a
   `python` hook needs `python`/`python3` on PATH (name differs per-OS). Go's `filepath` normalizes
   `\` vs `/` internally (fixes the path-match miss in §3.2). The projects already build Go binaries
   via `mage install` → no new dependency. **Beats hylla's Python hook and ta's own bash hook on
   portability.** `[docs]` (claude-code-guide: exec-form binary = most portable) + the repos-are-Go fact.
2. **`claude -p` builders** → **non-bare** `-p` + the Go hook via `--settings` + allowlist via
   `TA_GATE_ALLOWLIST` env. NOT `--bare` (kills hooks `[docs]`+`[real-exec]`), NOT `--allowedTools`
   for files (finicky, §3.2). `[real-exec]` non-bare `-p` + `--settings` hook fired + gated (forbidden
   Edit in `permission_denials`, file unchanged).
3. **Built-in Agent tool** → same Go hook + allowlist via the parent-transcript `<TA_ALLOWLIST>` block
   (hylla's delivery; no extra file; serialize same-role dispatches). `[real-exec]` (live subagent gate).
4. **codex** → `-C <dir>` write-confinement + **native `.git`-readonly** (free) + execpolicy `.rules`
   for NON-git denies (`mage install`, `go get`) + `exclude_slash_tmp` + `exclude_tmpdir_env_var` +
   `network_access=false`; `--sandbox read-only` for non-editing roles. `[real-exec]` (`-C` confine +
   native git block); cross-OS: codex has a native Windows sandbox + bubblewrap on Linux/WSL2 `[docs]`.
5. **Contract** = hylla's `--gate '{edit,writable_dirs,bash_deny,network}'` → per-backend translation
   in bin sh, reused by sand MCP. Persona stays canonical; strip-mode + gate-spec are configurable knobs.
6. **Model floor ~20b** for local tool-callers (`qwen2.5-coder:7b` text-emits, can't tool-call;
   `gpt-oss:20b`/`qwen3-coder:30b`/cloud Haiku work). `[real-exec]`, corroborated by hylla.
7. **Section 0 / reasoning lives in the persona body**, not the output style (output style is ambient).

## 2. Evidence ta established by real runs

- `[real-exec]` Built-in Agent-tool per-file edit gate (live `general-purpose` subagent: forbidden edit denied on disk, allowed edited).
- `[real-exec]` `--bare` honors `--allowedTools 'Edit(file)'` (Haiku/qwen-30b/gpt-oss-20b, `permission_denials`) — but see §3.2 (Write/relative-path is finicky; prefer the hook).
- `[real-exec]` `--bare` does NOT run `--settings` hooks (debug always-deny hook: no log, edit went through).
- `[real-exec]` non-bare `-p` + `--settings` hook DOES fire + gate (hylla's hook, env allowlist).
- `[real-exec]` codex execpolicy `forbidden` blocks in-sandbox at CreateProcess; AND codex `.git` is read-only natively under workspace-write with NO execpolicy (§3.3).
- `[real-exec]` codex `-C` confines writes to the workspace dir (sibling write "operation not permitted"); reads stay broad; `writable_roots` can't narrow the workdir.
- `[real-exec]` model tool-call floor (7b ❌ / 20b ✅ / 30b ✅ / Haiku ✅).

## 3. Where ta's runs CORRECTED other docs — CHECK THESE before adopting

- 3.1 **hylla finding 15 (`CLAUDE_CODE_DISABLE_CLAUDE_MDS=1` strips CLAUDE.md) did NOT reproduce in ta's run.** `[contradicted]` Non-bare `-p` + `DISABLE_CLAUDE_MDS=1` + `DISABLE_AUTO_MEMORY=1`, Haiku, from `ta/main`: the model still reported "contains cascade and droplet" with ~118K cached input tokens (CLAUDE.md loaded). The **hook still fired/gated** (that half holds) but the **context-strip did not**. ⇒ Do NOT rely on the "clean context for small models" claim until hylla + ta run the SAME probe and find why it differs (Claude Code version? exact env var? invocation? cache?). **This is the #1 thing to reconcile.**
- 3.2 **`--allowedTools` file-gating is unreliable across all three.** `[real-exec]` ta: `--allowedTools 'Write(.asrtD/allowed.txt)'` on `-p` denied BOTH allowed AND forbidden (relative-vs-absolute path-match miss). hylla finding 5: off-list `Write` RAN. tillsyn §3.2: `--settings permissions` deny "PASS". Three different results ⇒ `--allowedTools`/permissions path-scoping is finicky and form-dependent. **Recommendation: the hook is the file-gating authority (normalizes paths internally); `--allowedTools` is at most defense-in-depth.**
- 3.3 **codex native `.git`-readonly CONFIRMED** (agrees tillsyn + hylla). `[real-exec]` codex `--sandbox workspace-write --ignore-rules` (no execpolicy): `git commit` → `fatal: Unable to create '.git/index.lock': Operation not permitted`; repo log shows only `init`. ⇒ ta's execpolicy git-mutation rule is **redundant for git**; keep execpolicy only for non-git command denies. **Simplify the codex git-block to the free native sandbox.**

## 4. Cross-OS rationale (Windows / Linux / macOS)

- **Hook**: a Go binary in exec form is portable + dep-free; bash needs Git Bash on Windows; python needs an interpreter + per-OS name. `[docs]` (claude-code-guide). Path normalization: Go `filepath` handles `\`/`/`; the docs do NOT state how `tool_input.file_path` arrives on Windows (gap) — a Go hook absorbs that internally. **This is the decisive cross-OS argument for the Go-binary hook.**
- **codex sandbox**: native Windows sandbox (PowerShell), bubblewrap on Linux/WSL2, Seatbelt on macOS — dir-confinement + `.git`-readonly are cross-OS. `[docs]`
- **env vars + `permissionDecision` schema**: documented identical across OSes (the `DISABLE_*` cross-OS support is a doc gap — flag). `[docs]`
- Not testable here (Mac only): the Win/Linux OS-sandbox specifics — reasoned from docs; a Windows + Linux re-run is an open item for whichever project has those machines.

## 5. Open discrepancies for the evidence-based decision

- (a) **The `DISABLE_CLAUDE_MDS` strip** — ta could not reproduce hylla's result (§3.1). Shared re-test needed (same probe, both machines/versions). Until then, the safe interim: **persona carries all behavior** + the hook gates; accept ambient context on non-bare `-p`.
- (b) **Hook language** — ta recommends a **Go subcommand binary** (cross-OS, dep-free) over hylla's Python or ta's bash. Decide once for all three.
- (c) **`--allowedTools` vs hook** — standardize on the **hook** for file-gating (§3.2).
- (d) **codex git-block** — adopt **native `.git`-readonly**; drop execpolicy git-rule, keep non-git (§3.3).

## 6. Sources / resources

**ta's test artifacts**: probe commands + outputs in `/tmp/asrt*.txt`, `/tmp/sbx*.txt` and the on-disk
`.asrt*/.sbx*` fixtures (this session); the codex execpolicy + `-C` probes; hylla's hook re-tested at
`hylla/polyglot-foundation/.claude/hooks/ta_action_gate.py`.

**Claude Code docs** (code.claude.com/docs):
- hooks / hooks-guide — PreToolUse input (`agent_id`/`agent_type`), deny `permissionDecision` schema, exec-vs-shell form, Windows-via-Git-Bash.
- headless — `--bare` skips hooks/CLAUDE.md/plugins/MCP/memory; only explicit flags take effect.
- cli-reference — `--settings` (merge, not replace), `--allowedTools`/`--disallowedTools`, `--append-system-prompt`, `--setting-sources`.
- permissions — Edit/Write path globs (`//abs`, `/project`, `~/home`), deny>ask>allow precedence.
- settings / memory / env-vars — `CLAUDE_CODE_DISABLE_CLAUDE_MDS` / `DISABLE_AUTO_MEMORY` / `DISABLE_GIT_INSTRUCTIONS`, `disableAllHooks`, hook reload via file watcher (no restart).
- sub-agents / tools-reference — subagent `tools:`/`disallowedTools` are STATIC; no per-call override; Agent tool spawn params (no metadata pass-through).

**codex docs** (developers.openai.com/codex):
- concepts/sandboxing + agent-approvals-security — `.git` read-only under workspace-write (independent of approval/execpolicy); reads never restricted.
- windows — native Windows sandbox (PowerShell) / WSL2+bubblewrap.
- config-reference / config-advanced — `sandbox_mode`, `[sandbox_workspace_write]` `writable_roots`/`network_access`/`exclude_slash_tmp`/`exclude_tmpdir_env_var`.
- exec-policy — Starlark `prefix_rule(decision="forbidden")` (command denylist; file-path NOT visible for apply_patch).
- hooks + issue openai/codex#16732 / PR #18391 — apply_patch PreToolUse hooks "fixed" in docs but did NOT fire on 0.133.0 (ta + hylla both observed).

## 7. ta's bottom line for the debate

Adopt: **Go-binary `gate` subcommand hook** (cross-OS) + **non-bare `-p` + hook** + **built-in hook +
`<TA_ALLOWLIST>`** + **codex `-C` + native `.git`-ro + execpolicy-for-non-git** + the **`--gate` JSON
contract**. Reconcile first: the `DISABLE_CLAUDE_MDS` strip (ta couldn't reproduce) and standardize on
the hook over `--allowedTools`. The Go-binary hook is ta's strongest single recommendation — it's the
only choice that is cross-OS, dependency-free, and path-normalizing, and the projects are already Go.
