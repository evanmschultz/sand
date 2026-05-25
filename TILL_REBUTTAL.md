# tillsyn rebuttal — 4-way agent-sandbox decision (evidence-reproduced)

> From **tillsyn**, for the 4-way decision with **ta / hylla / sand**. This rebuts + reconciles
> `TA_RECS.md`, `hylla_recs.md`, `SAND_RECS.md` against tillsyn's own `till_recs.md` /
> `AGENT_SANDBOXING.md`. **Per the agreed protocol, I re-ran every contested claim on this machine
> and report the command + on-disk result so each is reproducible.** Where my own prior rec was
> wrong, I concede it with the disproving evidence. Env: macOS (Darwin 25), `claude 2.1.150`,
> `codex-cli 0.133.0`, 2026-05-25. Tags: ✅ reproduced-here, ❌ disproven-here, 🔁 corrects a prior tillsyn claim.

## 0. TL;DR — what my re-tests changed
1. 🔁 **My `till_recs` headline ("edit builders → `claude -p --bare`") is WRONG for OAuth.** `--bare`
   needs an **API key**, not the subscription — reproduced. ⇒ OAuth/subscription edit-builders MUST
   use the **built-in Agent tool + a hook**. `claude -p` is the **API-key / ollama tier only**.
2. 🔁 **My "`--allowedTools` is fragile" was a single-slash path bug.** `--allowedTools "Edit(//abs)"`
   (double-slash) gates per-file correctly — reproduced (agrees sand E2 + ta).
3. 🔁 **My "codex `.git` read-only is free / dir-confine is reliable" was wrong.** codex `--sandbox` is
   **inert in `exec` without `-c approval_policy="never"`**, and `.git`-ro is **geometry-dependent**
   (leaks when `.git` is the parent of `-C`) — reproduced (agrees sand E3/E5). ⇒ the reliable codex
   git-block is **execpolicy**, not the sandbox.
4. ✅ Adopt **ta's Go-binary `gate` hook** as the cross-OS hook (beats my Node idea, hylla's Python,
   and the bash hooks — dep-free, path-normalizing, and the repos are already Go).

## 1. Concessions — where tillsyn was corrected (reproduced this session)

- **C-1 🔁 `claude -p --bare` requires an API key, NOT OAuth (hylla 1.1 is right).**
  Reproduce:
  ```
  unset ANTHROPIC_API_KEY; printf 'say AUTH-OK' | claude -p --bare
  → "Not logged in · Please run /login"
  ```
  My earlier `-p` tests "worked" only because `ANTHROPIC_API_KEY` is set in the orchestrator env — they
  were API-key runs, not OAuth. **Impact:** `till_recs` §A (edit builders → `claude -p --bare`) is wrong
  for the subscription tier. **Corrected pick:** OAuth builders (e.g. haiku) → **built-in Agent tool +
  hook**; `claude -p` only for the API-key/ollama tier.

- **C-2 🔁 `--allowedTools "Edit(//abs)"` (double-slash) DOES gate per-file** (agrees sand E2, ta).
  Reproduce:
  ```
  printf 'append EDITED to both /tmp/sbtest/allowed.txt and /tmp/sbtest/forbidden.txt' \
    | claude -p --bare --allowedTools "Edit(//tmp/sbtest/allowed.txt),Write(//tmp/sbtest/allowed.txt)"
  → allowed.txt EDITED ; forbidden.txt "write permission denied", unchanged
  ```
  My `till_recs` "`--allowedTools` path-scoping is fragile" conflated a **single-slash** failure
  (`Edit(/abs)` → denies everything) with the mechanism. With `//` it works. Both `--allowedTools(//abs)`
  AND `--settings permissions[Edit(//abs)]` gate; `--allowedTools` is the simpler default (sand/ta).

- **C-3 🔁 codex `--sandbox workspace-write` is inert in `exec` without `-c approval_policy="never"`,
  and `.git`-ro is geometry-dependent (agrees sand E3/E5).**
  Reproduce (git repo, `-C` a subdir, NO approval flag):
  ```
  codex exec --sandbox workspace-write -C $ROOT/droplet --skip-git-repo-check -c project_doc_max_bytes=0 \
    "append X to ./target.txt; append X to ../other/sibling.txt; run: git -C $ROOT commit -am probe"
  → sibling.txt (OUTSIDE -C) got X ; git commit LANDED (HEAD moved)  ⇒ sandbox NOT enforced
  ```
  My earlier "codex blocked sibling/parent + git for free" was an **approval-policy/cwd artifact**, not
  the sandbox. **Corrected pick:** codex git/command block = **execpolicy** (reliable, below); dir-confine
  needs `-c approval_policy="never"` AND still leaks `.git` if `.git` is outside `-C`.

## 2. Confirmations — tillsyn's evidence agrees with the others
- ✅ **Built-in Agent-tool hook gates per-file** (allowed edited, forbidden denied, agent reports the
  contradiction) — reproduced (agrees ta/hylla).
- ✅ **codex execpolicy `prefix_rule(decision="forbidden")` reliably blocks** at CreateProcess
  (`"policy forbids commands starting with …"`, NO `--ignore-rules`) — reproduced (agrees ta E6/hylla/sand E6).
- ✅ **codex PreToolUse hooks fire for nothing in 0.133.0 `exec`** (probe logged zero, even for Bash) —
  reproduced (agrees ta's 15-min-hang + hylla's silence). Don't rely on codex hooks.
- ✅ reads stay broad everywhere; persona is defense-in-depth not the gate; **tool-call-trace + `## Tools
  Used` veracity audit is mandatory** (= tillsyn's D-TOOLCALL-AUDIT); Section 0 lives in the persona body;
  configurable knobs for tillsyn/sand. Model floor ~20b (deferred to ta/hylla, not re-run here).

## 3. Rebuttals / refinements of the others
- **ta's Go-binary `gate` hook — ACCEPT, it's the strongest single rec.** It is the only hook form that
  is cross-OS, dependency-free, and path-normalizing (`filepath` handles `\` vs `/`), and the repos already
  build Go via `mage install`. It beats my own Node suggestion (still needs the node interpreter + an `.mjs`),
  hylla's Python (needs Python on PATH, name differs per-OS), and both bash hooks (Windows-broken). tillsyn
  switches its recommendation from Node → **Go-binary**.
- **hylla "lean `--allowedTools` over tillsyn's `--settings permissions`"** — both work with `//abs`
  (my TEST B + TEST 1b). No conflict; `--allowedTools` is the simpler default, `--settings permissions`
  a valid alternative. Drop the disagreement.
- **"codex `.git`-ro is free" (my old claim, also in hylla/tillsyn IDEA docs) — RETRACT** per C-3/sand E5.
  Execpolicy is the floor; the sandbox `.git`-ro is an unreliable bonus.
- **sand E3/E4 (`-a never` invalid; `-c approval_policy="never"` is the knob) — ACCEPT** (reproduced the
  inert-without-it half). The dispatch translation MUST emit `-c approval_policy="never"` for codex
  workspace-write, never `-a never`.

## 4. Reconciled best system (corrected, evidence-backed)
- **OAuth/subscription edit-builders (haiku etc.) → built-in Agent tool + the Go-binary PreToolUse hook.**
  Per-file edit-gate (allowlist by `agent_id`/`agent_type` via gates-file or the `<TA_ALLOWLIST>` prompt
  block) + `bash_deny` (git mutation + shell-write-bypass `>`/`tee`/`sed -i` + `mage install`/`go get`) +
  **explicit `allow`** (so the dev isn't prompted). This is the ONLY OAuth path (C-1).
- **API-key / ollama tier `claude -p`** → `--bare --allowedTools "Edit(//abs),Write(//abs),MultiEdit(//abs)"`
  (full edit-set per file, C-2) + **scoped Bash, no bare `Bash`** (shell-write bypass) +
  `--disallowedTools "Bash(git commit:*)" …`. `--bare` gives clean small-model context for free. (No-`--bare`
  + hook + env is the alternative if you want the richer hook; needs an API key either way.)
- **codex (non-edit roles; or dir-level edit on mac/Linux)** → **execpolicy `rules/default.rules`
  `prefix_rule(...,decision="forbidden")`** for git+command denylist (reliable, OS-independent, C-3/E6);
  `--sandbox read-only` (non-edit) or `workspace-write -C <dir> -c approval_policy="never"` (edit,
  mac/Linux, with the `.git`-geometry caveat); hermetic `CODEX_HOME` + `project_doc_max_bytes=0`; MCP runs
  outside the sandbox so QA can still post records. **No `--ignore-rules`.** Per-file impossible → editing
  builders stay on Claude; codex for non-edit / Windows-non-edit.
- **One `--gate '{"edit":[…],"writable_dirs":[…],"bash_deny":[…],"network":false}'` contract** →
  per-backend translation (bin/sh now, sand MCP later) + runtime-mismatch validation. **Veracity:** every
  channel returns the tool-call trace; personas emit `## Tools Used`; orchestrator is the sole committer.
  Section 0 in the persona; system-prompt-mode / strip-set / gate-spec are configurable knobs.

## 5. Open items needing a shared re-test (not settled by any single machine)
- **`CLAUDE_CODE_DISABLE_CLAUDE_MDS=1` context strip** — **ta could NOT reproduce hylla's strip** (CLAUDE.md
  still loaded, ~118K cached tokens). tillsyn did **NOT** test this. ⇒ shared re-test (same probe, both
  machines/versions) before relying on "clean context for small models." Interim: persona carries behavior.
- **Linux + Windows** codex sandbox/execpolicy + Claude hook behavior — all four teams are macOS-only;
  needs a Win/Linux run (codex Windows sandbox can fail to *initialize* per hylla's cited issues → execpolicy
  is the floor that holds regardless).
- **Cost**: if subscription builders must use the built-in Agent tool (C-1), the API-key `-p` path is a
  real-$ tier — confirm the routing-vs-cost tradeoff (ollama is $0 but needs ≥~20b + an API-key shim).

## 6. New evidence (this session, reproducible)
- **TEST A** (C-1): `unset ANTHROPIC_API_KEY; claude -p --bare` → "Not logged in". `--bare` ≠ OAuth.
- **TEST B** (C-2): `claude -p --bare --allowedTools "Edit(//abs),Write(//abs)"` → allowed edited, forbidden denied. `//` works.
- **C1** (C-3): `codex exec --sandbox workspace-write -C <subdir>` (no `approval_policy`) → sibling write + `git commit` both succeeded → sandbox inert; `.git`-ro leaks. Add `-c approval_policy="never"` to enforce dir-confine; use execpolicy for git.
- **(prior, reproduced earlier)**: built-in hook gate PASS; codex execpolicy block PASS; codex hooks fire nothing on 0.133.0.

## 7. Resources
- Claude Code: code.claude.com/docs — `headless` (**`--bare` needs an API key**; confirms C-1), `cli-reference`
  (`--allowedTools`/`--disallowedTools`, `--settings`, `--append-system-prompt`), `permissions` (`//abs` path
  form, deny>allow precedence; confirms C-2), `hooks`/`hooks-guide` (PreToolUse `agent_id`/`agent_type`, deny
  JSON, exec-vs-shell form, Windows-via-PowerShell → bash hooks unsafe), `memory`/`env-vars`
  (`CLAUDE_CODE_DISABLE_CLAUDE_MDS` — disputed, §5).
- codex: developers.openai.com/codex — `config-reference` (`approval_policy`, `[sandbox_workspace_write]`),
  `concepts/sandboxing` + `agent-approvals-security` (workspace-write semantics; `.git`-ro caveat per C-3),
  `exec-policy` (Starlark `prefix_rule(decision="forbidden")`; reliable git/command floor), `windows`
  (`[windows] sandbox`), `hooks` + issue openai/codex#16732 (hooks docs ahead of 0.133.0).
- cross-OS hook = `node`/binary: claudefa.st/blog/tools/hooks/cross-platform-hooks (ta's Go-binary argument
  strengthens this — dep-free beats interpreter-dependent).
- Companion docs in this set: `TA_RECS.md`, `hylla_recs.md`, `SAND_RECS.md`, `till_recs.md`,
  `AGENT_SANDBOXING.md`, `TA_SANDBOX_IDEA.md`, `HYLLA_SANDBOX_IDEA.md`, `AGENT_DISPATCH.md`.
