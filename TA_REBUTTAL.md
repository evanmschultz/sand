# TA Rebuttal — 4-way agent-sandboxing decision (ta's evidence-backed response, rev 2)

ta's rebuttal after reading `SAND_RECS.md`, `hylla_recs.md`, `till_recs.md` + the findings docs
(`AGENT_SANDBOXING_tillsyn.md`, `HYLLA_SANDBOX_IDEA.md`, `TA_SANDBOX_IDEA.md`, `TA_RECS.md`).
Per the shared protocol I **re-ran every contested claim**; each verdict cites the command + on-disk
evidence and is reproducible. **rev 2** corrects a claim I made in rev 1 after catching a `/tmp` test
artifact (see §0.1). macOS, Claude Code 2.1.150, codex 0.133.0, ollama 0.24.0.

## 0. New evidence ta ran (reproducible, on-disk + `permission_denials` + `git log` verified)

| # | Test | Setup | Result | Verdict |
|---|---|---|---|---|
| R1 | `--allowedTools` per-file, **`//` double-slash** | `claude -p --bare --allowedTools "Edit(//abs/allowed),Read"` + prompt to edit allowed+forbidden | allowed→`PROBE`; forbidden UNCHANGED + in `permission_denials` | ✅ **`//abs` works** (sand E2) |
| R2 | `--settings permissions`, `//abs` | `--settings '{"permissions":{"allow":["Edit(//abs)","Write(//abs)"]}}'` | allowed→`PROBE`; forbidden denied | ✅ **also works** (tillsyn) |
| R4 | codex `-C`=subdir confinement, **repo under `$HOME`** | `-C <repo>/subA`, write to subA / sibling subB / repo-root / outside-repo | **only subA written**; subB + repo-root + outside ALL "operation not permitted" (unchanged on disk) | ✅ **`-C` = REAL per-dir confinement** |
| R5 | codex `git commit`, `-C`=subdir, **repo under `$HOME`** | `-C <repo>/sub` + `git commit --allow-empty`, NO execpolicy | **BLOCKED**: `Unable to create .git/index.lock: Operation not permitted`; log = init only | ✅ **git blocked natively under `-C` (subdir too)** |
| R3 | same as R5 but **repo under `/tmp`** | `-C /tmp/…/sub` + `git commit` | commit SUCCEEDED | ⚠️ **VOID — `/tmp` is an always-writable root (confound), not a real bypass** |

### 0.1 The `/tmp` trap (cost me a wrong concession in rev 1 — flag for everyone)
codex workspace-write writable set is **`[workdir, /tmp, $TMPDIR]`**. Any test whose repo/fixtures live
under `/tmp` is **confounded** — writes "succeed" because of `/tmp`, not the geometry under test. R3 (and,
I suspect, **sand's E5** + hylla's `/tmp` confinement notes) hit this. **Always run confinement tests under
`$HOME` or the real project tree, never `/tmp`.** Re-run anything that concluded from a `/tmp`-resident repo.

## 1. Where ta CONCEDES / CORRECTS

- 1.1 **CONCEDE (sand/tillsyn): `--allowedTools` is fine with `//` double-slash.** My `TA_RECS §3.2` ("finicky/abandon") was wrong — the cause was the path FORM (single-slash/relative fails; `//abs` works, R1). Both `--allowedTools "Edit(//abs)"` (R1) and `--settings permissions ["Edit(//abs)","Write(//abs)"]` (R2) gate per-file under `--bare`. The `//` form is the reconciliation across all of us.
- 1.2 **RETRACT my rev-1 concession that "codex native `.git`-readonly is geometry-dependent → execpolicy necessary."** That rested on R3, which was `/tmp`-confounded. **Truth (R4+R5, under `$HOME`):** codex `-C` confines writes to exactly that dir; `.git` (at the repo root) is OUTSIDE a subdir workspace, so `git commit` is **blocked natively for the subdir geometry too**. So native confinement blocks git in BOTH geometries under realistic fs. **execpolicy is still my recommended PRIMARY git-block — but for robustness reasons (explicit "policy forbids" error, geometry/fs-independent, also denies non-git mutations), NOT because the native sandbox fails.**

## 2. Where ta's findings STAND

- 2.1 **codex execpolicy `prefix_rule(forbidden)` blocks at CreateProcess** (mine + sand E6 + hylla + tillsyn) — explicit, fs/geometry-independent; the recommended primary git/command floor (`--ignore-rules` must NOT be passed; hermetic `CODEX_HOME` excludes the dev's rules).
- 2.2 **codex `-C` per-dir write confinement is REAL** (R4) — sibling-subdir / repo-root / outside-repo writes all blocked under `$HOME`. So codex CAN safely host dir-scoped editing roles (writes can't escape the dir); per-file is still not available (dir-level only).
- 2.3 **Non-bare `-p` + `--settings` hook fires + gates** (mine, hylla) — the alternative when MCP/hook richness is wanted. `--bare` does NOT run hooks (all agree).
- 2.4 **Model tool-call floor**: 7b text-emits (unusable; root cause = emits ` ```json` not `<tool_call>`), ~20b+ works — all four agree.

## 3. The one open DIFFERENCE — hook runtime (Go binary vs Node vs Python)

- 3.1 All agree **never bash** (Windows runs hook `command`s via Git Bash/PowerShell). Split: hylla/sand → Python, tillsyn → **Node** ("Claude Code guarantees node"), ta → a compiled **Go `gate` subcommand**.
- 3.2 ta's case: zero interpreter dep, reuses the **already-installed project CLI** (`ta gate`/`till gate` — all Go projects with `mage install` binaries), Go `filepath`/`os` normalize Win `\`/POSIX `/`, exec-form needs no shell. tillsyn's Node point is strong (node ships with Claude Code) and **beats Python**. **Proposed resolution: Go-subcommand primary; Node fallback for any consumer without an installed project binary; never bash, never Python.**

## 4. Resolved open item (was §4 in rev 1)

- 4.1 **codex `-C`=subdir DOES confine to the subdir** (R4) — my rev-1 worry that codex "expands writable to the repo root" was the `/tmp` artifact. Sibling-subdir, repo-root, and outside-repo writes are all OS-blocked under `$HOME`. So dir-scoped codex editing is safe; the only true limit is per-file (codex is dir-granular).

## 5. ta's consolidated best-of-4 recommendation

1. **Routing** (hylla's `--bare`-needs-API-key `[real-exec]`): OAuth editing builders → **built-in Agent tool + gate hook**; ollama/API-key builders → **`claude -p --bare`**; codex → planning/qa-falsif (+ dir-scoped editing now that R4 proves `-C` confines).
2. **`-p` per-file** → `--settings '{"permissions":{"allow":["Edit(//abs)","Write(//abs)"],"deny":["Bash(git commit:*)",…]}}'` (R2) or `--allowedTools "Edit(//abs)"` (R1) — **always `//` double-slash absolute**; full edit-set per file (Edit+Write+MultiEdit, per hylla); no bare `Bash`.
3. **Built-in gate** → one **Go `gate` subcommand** hook (§3) + allowlist via `<TA_ALLOWLIST>` block (hylla's parent-transcript delivery) or gates-file by `agent_type`; explicit `allow`; serialize same-`agent_type`.
4. **codex** → **execpolicy `rules/default.rules` `prefix_rule(forbidden)`** (primary git/command block, no `--ignore-rules`) + `--sandbox workspace-write -C <dir>` (REAL per-dir confinement, R4) / `read-only`; `exclude_slash_tmp`/`exclude_tmpdir`/`network=false`; ensure **approval=never** (`--ephemeral` or `-c approval_policy="never"` — bare `--sandbox` is inert otherwise, sand E3).
5. **One `--gate '<json>'` contract** (`{edit[],writable_dirs[],bash_deny[],network}`) + ta's runtime-mismatch validation → per-backend translation; bin sh now, sand MCP later; configurable knobs.
6. **Section 0 in persona**; **veracity audit** (return full tool-call trace + `## Tools Used`); **orchestrator is sole committer**.
7. **Clean small-model context** = `--bare` (free; no hooks → pairs with `--allowedTools`/`--settings`). The `CLAUDE_CODE_DISABLE_CLAUDE_MDS` no-`--bare` path is a knob — **ta could NOT reproduce the strip** (model still saw "cascade/droplet"); verify per-machine before relying.

## 6. Net agreements / disagreements

- **AGREE (4/4)**: orchestrator-sole-committer; codex hooks dead on 0.133.0; execpolicy = reliable codex git/command block; reads broad; per-file only on Claude; persona-not-prompt + Section-0-in-persona; ~20b model floor; one JSON gate contract; veracity audit; bash hooks OS-fragile.
- **RESOLVED**: `//abs` makes both `--allowedTools` + `--settings permissions` work (R1/R2); codex `-C` is real per-dir confinement (R4) and blocks git natively for both geometries under realistic fs (R5) — the "subdir bypass" was a `/tmp` artifact (R3).
- **OPEN**: hook runtime (Go-subcommand vs Node — ta: Go primary/Node fallback); the `DISABLE_CLAUDE_MDS` strip (ta couldn't reproduce — re-test per machine); re-verify sand E5 / any `/tmp`-resident codex result.

## 7. Sources / resources
- **ta's R1/R2/R4/R5 (rev 2)**: live `claude -p --bare` (Haiku, real API) + `codex exec` runs; evidence = on-disk file contents (fixtures `.ra1/.ra2/$HOME/cxconf2-*/$HOME/gitgeo3-*`), `permission_denials` arrays, `git log` HEAD. The `/tmp` trap (§0.1) voids R3 and any `/tmp`-resident confinement claim.
- Cross-repo docs: `SAND_RECS.md`, `hylla_recs.md`, `till_recs.md`, `AGENT_SANDBOXING_tillsyn.md`, `HYLLA_SANDBOX_IDEA.md`, `TA_SANDBOX_IDEA.md`, `TA_RECS.md`.
- Claude Code docs: code.claude.com/docs — `permissions` (**`//` absolute path form**, deny>ask>allow), `cli-reference` (`--bare`, `--allowedTools`/`--disallowedTools`, `--settings`, `--append-system-prompt`), `hooks`/`hooks-guide` (PreToolUse `agent_id`/`agent_type`, deny JSON, exec-vs-shell, Windows-via-Git-Bash/PowerShell), `headless` (`--bare` needs API key — hylla `[real-exec]`), `memory`/`env-vars` (`CLAUDE_CODE_DISABLE_CLAUDE_MDS`/`_AUTO_MEMORY`).
- codex docs: developers.openai.com/codex — `concepts/sandboxing` (writable = workdir+`/tmp`+`$TMPDIR`; `.git` read-only), `windows` (native sandbox graduated), `config-reference` (`approval_policy`, `sandbox_workspace_write.*`/`exclude_*`/`network_access`), `exec-policy` (`prefix_rule(decision="forbidden")`), issue #16732 (apply_patch hook dead). `-a` is NOT a valid `codex exec` flag (sand E4) → `-c approval_policy="never"`.

## 8. ta's bottom line (rev 2)
The four converge tightly. Settled by evidence: (1) `-p` per-file = **`//abs`** with `--settings permissions` (allow+deny) or `--allowedTools`; (2) codex `-C` is **real per-dir confinement** and **blocks git natively** for both geometries under realistic fs — the "subdir bypass" was a `/tmp` artifact; (3) **execpolicy** is still the recommended-primary codex git-block (explicit + fs-independent + non-git), defense-in-depth over the native sandbox. Remaining decision: **hook runtime** — ta recommends a **Go `gate` subcommand** primary, **Node** fallback, never bash/Python. And re-run any `/tmp`-resident codex test (the §0.1 trap).
