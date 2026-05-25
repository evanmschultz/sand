# SAND recommendations — empirical sandbox/gate findings (2026-05-24)

> **Origin: `sand/main`.** Distributed (uncommitted) to `ta/main`, `tillsyn/main`,
> `hylla/polyglot-foundation` for the 4-way decision on how to runtime-confine dispatched
> agents. This is **sand's empirically-tested rebuttal + synthesis** of `AGENT_SANDBOXING_tillsyn.md`,
> `HYLLA_SANDBOX_IDEA.md`, and `TA_SANDBOX_IDEA.md`. sand is the tool that will ultimately
> generate the per-backend dispatch config, so getting the ground truth right matters most here.
>
> **Environment tested:** macOS (Darwin 25, Seatbelt), `codex-cli 0.133.0`, `claude 2.1.150`.
> Every ✅ below is **on-disk-verified this session** (file contents / git HEAD movement / the
> CLI's own structured envelope or report). ❌ = empirically disproven. 📄 = doc/reasoned, not run here.
>
> **Cross-check discipline (per dev directive):** when you rebut a finding here, cite the
> command you ran + the on-disk evidence, and check it against the cited vendor docs AND the
> other repos' results. Self-report ("it worked") is not evidence; a file's contents / HEAD / a
> `permission_denials` array is. Several claims in the three prior docs were **macOS-only and
> partly wrong** — re-run before trusting.

## 0. TL;DR — three doc-correcting truths

1. **claude `--allowedTools "Edit(//abs)"` per-file gate WORKS — but the `//` double-slash
   absolute-path syntax is MANDATORY.** Single-slash `Edit(/abs)` denied *everything, including the
   allow-listed file*. This reconciles ta (✅ deny-by-omission works) vs hylla (finding 5 "path-allow
   doesn't deny-default") — **hylla's failure was the wrong path form (or `Write(` vs `Edit(`)**.
2. **codex `--sandbox` does NOTHING in `exec` mode without `-c approval_policy="never"`.** Bare
   `--sandbox workspace-write` ran a sibling-dir write AND `git commit` unsandboxed (codex's own
   report: *"blocked by sandbox: none"*). **`-a never` (hylla Recipe C) is an INVALID flag for
   `codex exec`** → exit 2, nothing runs (a false "blocked"). The exec-mode knob is the config
   override `-c approval_policy="never"`.
3. **codex "`.git` is read-only for free" is UNRELIABLE.** With `workspace-write -C <droplet>` +
   `approval_policy=never`, sibling writes were correctly blocked — but **`git commit` SUCCEEDED**
   because `.git` lived in the PARENT of the `-C` workspace. tillsyn §3.1 / hylla's "git blocked
   natively" only holds when the repo root *is* the workspace. The **reliable** codex git-block is
   **execpolicy** (next section), not the sandbox.

## 1. Experiments + on-disk evidence (this box, 2026-05-24)

| # | Command (abridged) | Result (on-disk) | Verdict |
|---|---|---|---|
| E1 | `claude -p --bare --allowedTools "Edit(/abs/allowed)"` + prompt to edit allowed+forbidden | BOTH denied (incl. allowed) — `permission_denials` shows both | ❌ single-slash syntax → deny-all |
| E2 | same but `Edit(//abs/allowed)` | allowed.txt→`ALLOWED-EDITED`; forbidden.txt UNCHANGED + denied | ✅ **per-file deny-by-omission works (`//` required)** |
| E3 | `codex exec --sandbox workspace-write -C droplet` (no approval flag) | sibling write SUCCEEDED, `git commit` landed (`d8b21fb`); codex: "blocked by sandbox: none" | ❌ **sandbox not enforced without approval policy** |
| E4 | E3 + `-a never` | `error: unexpected argument '-a'`, exit 2, nothing ran | ❌ **`-a never` invalid for `codex exec`** |
| E5 | E3 + `-c approval_policy="never"` | in-workspace write OK; sibling write **"operation not permitted"**; read outside OK; **`git commit` SUCCEEDED** (`f8dc0a8`) | ✅ dir-confine + broad-read; ❌ **`.git`-ro fails (subdir geometry)** |
| E6 | hermetic `CODEX_HOME/rules/default.rules` `prefix_rule(["git","commit"],"forbidden")`, NO `--ignore-rules`, `-c approval_policy=never` | `git commit` → **"policy forbids commands starting with git commit"** at CreateProcess; `git diff` ran; HEAD unchanged | ✅ **execpolicy = reliable codex git-block (geometry/mode-independent)** |
| E7 | `codex exec --sandbox read-only -c approval_policy=never` | file write blocked; read OK; `git add` ran, commit no-op'd ("nothing to commit") | ⚠️ read-only blocks writes but **does NOT directly block git** (incidental only) |
| E8 | `claude -p --bare --disallowedTools "Bash(git commit:*)"` + prompt to commit | denied ("Bash tool denied permission"); HEAD unchanged | ✅ **claude git-block via `--disallowedTools` (deny wins over allow)** |

## 2. Corrected per-channel matrix

| Capability | Claude built-in Agent tool | `claude -p` | `codex exec` |
|---|---|---|---|
| **No git mutation** | PreToolUse hook (bash_deny) — ✅ | `--disallowedTools "Bash(git commit:*)" …` ✅ (E8) **or** hook (no-`--bare`) | **execpolicy `prefix_rule forbidden`** ✅ (E6) — NOT sandbox `.git`-ro (E5 ❌) |
| **Per-FILE edit-scope** | PreToolUse hook by `agent_id`/`agent_type` ✅ (ta/hylla proven) | `--allowedTools "Edit(//abs)"` ✅ (E2) **or** no-`--bare`+hook | ❌ not achievable (dir-level only) |
| **Per-DIR write-confine** | (n/a) | (n/a) | `--sandbox workspace-write -C <dir>` + **`-c approval_policy="never"`** ✅ (E5) — macOS/Linux only |
| **Broad reads** | ✅ | ✅ (reads not gated by `--allowedTools`) | ✅ (E5/E7) |
| **Enforcement is OS-agnostic?** | ✅ (Claude permission engine / hook) | ✅ | git-block ✅ (execpolicy = config); dir-confine ❌ (Seatbelt/bwrap = macOS/Linux only) |

## 3. The no-commit invariant — cross-OS-robust recipe (RECOMMENDED)

Every channel's git-block is **config / permission-engine based, NOT OS-sandbox dependent** →
the orchestrator-only-commit guarantee is **fully portable (Win/Lin/Mac)**:

- **codex (every role):** hermetic `CODEX_HOME` containing only auth/identity symlinks + **our own**
  `rules/default.rules` forbidding `git commit/push/add/reset/checkout/branch/tag/stash/restore`
  (+ `mage install`, `go get`, `go mod` if desired). **Do NOT pass `--ignore-rules`** (it disables
  execpolicy); the hermetic `CODEX_HOME` is what excludes the dev's global rules. (E6 ✅.) This
  **replaces** AGENT_DISPATCH.md's `--ignore-rules`.
- **claude built-in:** python3 PreToolUse hook denying git-mutation `Bash` (OS-agnostic if the hook
  script is python3, not bash).
- **claude `-p`:** `--disallowedTools "Bash(git commit:*)" "Bash(git push:*)" …` (E8 ✅; deny wins).

## 4. Edit-scope — per channel + cross-OS

- **File-level** (the goal) = **claude only**: built-in hook (allowlist by `agent_id`/`agent_type`)
  or `-p --allowedTools "Edit(//abs)"` (E2). OS-agnostic engine; path **syntax** is per-OS
  (Windows `C:\…` → the generator emits the right form).
- **codex = directory-level only**, and **only on macOS/Linux** (Seatbelt/bwrap). `-C <dir>` +
  `workspace-write` + `-c approval_policy="never"` (E5). **Windows has no codex OS sandbox** →
  dir-confine won't enforce there.
- ⇒ **Edit-scoped builders → claude** (portable file-level). **codex → non-editing roles**
  (planning, qa-falsification) where edit-scope is moot + execpolicy handles git. On Windows keep
  codex to non-editing regardless.

## 5. Where sand AGREES / DISAGREES with the three docs (rebuttal-checked)

- **AGREE (all three):** orchestrator is sole committer; two axes (git + edit-scope); codex hooks
  don't fire in 0.133 (don't rely on them — ta saw a 15-min hang, hylla/tillsyn saw silence; either
  way unusable); reads stay broad; persona is defense-in-depth not the gate; tool-call-trace +
  `## Tools Used` veracity audit is mandatory.
- **AGREE w/ hylla, strongly:** **Section 0 + thinking directives belong in the PERSONA body, not
  the output style** (output style is ambient, stripped for small models). And **sand/tillsyn must
  be config-driven/flexible** (system-prompt mode, strip-set, gate spec as knobs) — NOT hardcoded.
- **DISAGREE / CORRECT tillsyn §3.1 + hylla finding "git blocked natively":** ❌ `.git` read-only is
  **geometry-dependent and failed** when the writable workspace is a subdir (E5 — commit landed).
  Use **execpolicy** (E6), not the sandbox, for the codex git guarantee.
- **CORRECT hylla Recipe C:** `-a never` is **invalid** for `codex exec` (E4); use
  `-c approval_policy="never"`. Without an approval policy the sandbox is **inert** in exec mode (E3).
- **CORRECT hylla finding 5 (`--allowedTools` path-allow "doesn't deny-default"):** it DOES, with
  the **`//` double-slash** absolute form (E2). ta's deny-by-omission claim is the right one.
- **OPEN (not yet re-run by sand):** hylla's no-`--bare` + injected-hook per-file path on `-p`
  (sand proved the simpler `--allowedTools(//abs)` instead — both viable); env-strip
  (`CLAUDE_CODE_DISABLE_CLAUDE_MDS`) composing with hooks; the model tool-floor (7b text-emits,
  ~20b+ real tool-calls — ta+hylla agree, sand didn't re-run); Linux bwrap + Windows behavior
  (sand can only execute on macOS).

## 6. Recommended system for sand to build

sand = **config-driven gate translator** (not a hardcoded recipe). It consumes ONE gate contract:
```
--gate '{"edit":["//abs/f.go","//abs/f_test.go"],"writable_dirs":["/abs/dir"],
         "bash_deny":["git commit","git push","git add","git reset","mage install","go get","go mod"],
         "network":false}'
```
plus per-project/role **policy knobs** (system-prompt mode replace|append, strip-set, sandbox mode),
and emits the per-channel form from §2/§3/§4. Validation: a **codex** role given only `edit` (no
`writable_dirs`) → error ("codex gates per-directory"); a **built-in** role called via subprocess →
refuse ("use the Agent tool"). sand also **generates** the python3 hook + `settings.json` +
`CODEX_HOME/rules/default.rules` as project-local artifacts (no global state), and its response
envelope **always returns the full tool-call trace** (veracity audit).

## 7. Sources

- **sand's experiments (E1–E8 above):** live `claude`/`codex` runs on macOS, 2026-05-24; evidence
  = on-disk file contents, `git rev-parse HEAD` movement, `permission_denials` arrays, and the CLIs'
  own structured reports. Reproducible from the commands in §1.
- **Cross-repo docs compared:** `AGENT_SANDBOXING_tillsyn.md`, `HYLLA_SANDBOX_IDEA.md`,
  `TA_SANDBOX_IDEA.md` (all 2026-05-24, macOS).
- **Claude Code docs:** code.claude.com/docs — `cli-reference` (`--bare`, `--allowedTools`/
  `--disallowedTools`, `--settings`, `--append-system-prompt`), `permissions` (tool-rule path globs;
  `//abs` form; deny-precedence), `hooks` (PreToolUse input incl. `agent_id`/`agent_type`; deny JSON;
  "PermissionRequest hooks don't fire in `-p`, use PreToolUse"), `memory`/`env-vars`
  (`CLAUDE_CODE_DISABLE_CLAUDE_MDS`, `_AUTO_MEMORY`), `sandboxing`.
- **codex docs:** developers.openai.com/codex — `config-reference` (`approval_policy`,
  `[sandbox_workspace_write]` `writable_roots`/`exclude_*`/`network_access`), `concepts/sandboxing`
  (Seatbelt mac / bwrap+seccomp+Landlock linux; reads never restricted), `agent-approvals-security`,
  `exec-policy` (Starlark `prefix_rule(decision="forbidden")`). Issue `#16732` / PR `#18391` (hooks
  docs ahead of 0.133.0); `connection_manager.rs` (MCP servers spawn OUTSIDE the sandbox).
- **codex CLI ground truth:** `codex exec --help` on 0.133.0 (confirms `--ignore-user-config`,
  `--sandbox`, `-c`; NO `-a` flag in exec).

## 8. Open items for the 4-way discussion

- Settle the `-p` per-file mechanism: `--bare`+`--allowedTools(//abs)` (sand-proven, simple) vs
  no-`--bare`+env-strip+hook (hylla-proven, also strips CLAUDE.md). Both work; pick the default,
  keep the other as a config knob.
- Confirm execpolicy on Linux + whether codex on Windows has any sandbox/execpolicy at all.
- Decide codex builders: dir-confine (mac/linux) or keep codex non-editing everywhere for portability.
- Who owns generating the python3 hook + `CODEX_HOME/rules` — sand (preferred end-state) vs bin/sh now.
- Re-run the model tool-floor + env-strip×hook composition to close the 📄 items.
