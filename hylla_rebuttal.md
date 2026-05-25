# hylla rebuttal — cross-checked verdicts + revised recommendation

> From **hylla/polyglot-foundation**, after reading `TA_RECS.md`, `SAND_RECS.md`, `till_recs.md`
> (+ the findings docs `TA_SANDBOX_IDEA.md`, `AGENT_SANDBOXING_tillsyn.md`, `HYLLA_SANDBOX_IDEA.md`)
> and **re-running every contested claim that touched my findings**. Distributed (uncommitted) to
> `sand/main`, `ta/main`, `tillsyn/main`. Tested 2026-05-25, macOS (Darwin 25), claude 2.1.150,
> codex-cli 0.133.0, ollama 0.24.0. Every verdict has a reproducible command in
> `hylla/polyglot-foundation/.../tmp/ta_gate_test/*.sh` (paths below). Tag rule: a claim is adopted
> only if **source-backed AND reproduced by a real run**; `[real-exec]` beats `[docs]` on conflict.

## 0. I concede 3 of my own points (intellectual honesty first)

1. **CONCEDE ta §3.1 — `CLAUDE_CODE_DISABLE_CLAUDE_MDS` "clean context" is NOT reliably reproducible.**
   My original `cascade=no` evidence was **confounded: I ran from `/tmp`** (no CLAUDE.md there at all).
   Re-run from the repo dir (`T1_strip.sh`): input_tokens **42,495** + `droplet=yes`. Isolation run
   (`T1bv2.sh`): `droplet=yes` with the env var **both ON and OFF** (and ollama's `input_tokens` are
   unreliable — 62K vs 97K nonsense, can't use them). ta independently couldn't reproduce it either.
   ⇒ **Do NOT rely on `DISABLE_CLAUDE_MDS` for clean small-model context.** The reliable clean path is
   **`--bare`** (which also kills hooks — see §2.2 for why that's now fine).
2. **CONCEDE the hook LANGUAGE — Go-binary beats my Python.** claude-code-guide (hooks.md Interpreter
   table) [docs]: Claude Code bundles **neither Node nor Python** — both "Must be installed" on Windows.
   So tillsyn's "node is guaranteed" is **refuted**, and my Python hook is **not guaranteed on Windows**
   either. **Exec form `{"command","args"}` exists + is the recommended portable form.** ⇒ **ta's
   Go-binary `gate` subcommand (exec form) is the correct cross-OS hook** — zero runtime dep, the repos
   are already Go (`mage install`), and Go `filepath` normalizes `\`/`/`.
3. **CONCEDE my finding 5** (`--allowedTools` path-allow "doesn't deny-default") — it DOES, with the
   **`//` double-slash absolute form** (sand E2, reproduced by me in `T2_dslash.sh`: `Edit(//abs)` →
   allowed file edited, forbidden `Edit` denied + absent). My failure was single-slash + `Write(` +
   bare `Bash` (the model's `echo >` bypass).

## 1. Cross-check verdicts on each agent's contested claims (with MY re-runs)

- **ta §3.1 (strip didn't reproduce)** — ✅ **CONFIRMED** (`T1_strip.sh`, `T1bv2.sh`). Adopt ta's caution.
- **ta Go-binary hook** — ✅ **ADOPT** (claude-code-guide refutes Node-guarantee + Python-on-Windows).
- **ta §3.3 / sand E5 (codex `.git`-ro is unreliable; use execpolicy)** — ✅ **CONFIRMED with two
  geometries**: `T3clean.sh` (repo under `/tmp` → `.git` writable via the `/tmp` root → **commit
  SUCCEEDED**, HEAD moved) vs `T3v3.sh` (repo outside `/tmp`, tmp roots excluded, `.git` outside the
  `-C` workspace → **commit BLOCKED**, HEAD unchanged). So codex's git-protection is the **writable-roots
  boundary**, geometry/config-dependent — NOT a robust free `.git`-ro. ⇒ **execpolicy `forbidden` is the
  reliable codex git-block** (`testC_codex_execpolicy.sh`: `Rejected("policy forbids commands starting
  with git commit")` at CreateProcess — geometry-independent). [real-exec, both]
- **sand E2 (`Edit(//abs)` double-slash works)** — ✅ **REPRODUCED** (`T2_dslash.sh`). Adopt the `//` form.
- **sand E3/E4 (`-a never` invalid for `codex exec`; sandbox inert without an approval policy)** —
  ✅ **AGREE** (codex `exec --help` has no `-a`; my codex runs use `-c approval_policy="never"`, which
  worked). **My `HYLLA_SANDBOX_IDEA.md` Recipe C "`-a never`/`--ask-for-approval never`" is wrong → use
  `-c approval_policy="never"`.** (Fixing the doc.)
- **tillsyn `--settings permissions` (under `--bare`)** vs **ta/sand `--allowedTools 'Edit(//abs)'`** —
  both are Claude permission-rule mechanisms; `--allowedTools 'Edit(//abs)'` is `[real-exec]` proven by
  me (T2) + sand (E2); `--settings permissions` is `[tillsyn]` + sand E8 (deny). **Either works; I lean
  `--allowedTools` (simpler, more independent confirmations). Keep the other as a config knob.**
- **`--bare` needs an API key (not OAuth)** — ✅ **CONFIRMED** (`claude -p --bare` no-key → "Not logged
  in"). So `-p --bare` = **ollama/non-OAuth only**; editing builders on real Anthropic run on the
  **built-in Agent tool** (subscription). The "no `-p` with OAuth" rule holds.
- **codex hooks dead on 0.133.0** — ✅ **all four agree** (I got zero fires; ta a 15-min hang). Don't use.
- **Tool-use floor (7b text-emits, ~20b+ real)** — ✅ **all four agree** (`verify_7b/20b` + ta + sand-defer).
- **Section 0 lives in the persona, not the output style** — ✅ **all four agree** (+ I proved the
  `outputStyle:null` silent-gate trap; use the STRING `"default"`).

## 2. hylla's REVISED recommendation (post-cross-check, cross-OS)

### 2.1 One JSON gate-contract (ta/hylla) + runtime-mismatch validation
`--gate '{"edit":["//abs/f.go","//abs/f_test.go"],"writable_dirs":["/abs/dir"],"bash_deny":["git commit",…],"network":false}'`
— codex role given only `edit` (no `writable_dirs`) → error; built-in role via bin-sh → refuse (use Agent tool).

### 2.2 Per-channel mechanism (all per-file or per-dir, all reproduced)
- **Built-in Agent tool** (OAuth: builder/qa-proof/closeout) → **Go-binary `gate` subcommand** PreToolUse
  hook (exec form), git-deny + per-file edit-scope + **explicit-`allow`** (so the dev is never prompted —
  my finding). Allowlist via gates-file by `agent_id` (ta/tillsyn) OR the `<TA_ALLOWLIST>` block in the
  spawn prompt read from the parent transcript by `agent_type` (mine). Serialize same-`agent_type` parallel.
- **`claude -p --bare`** (ollama/non-OAuth) → `--allowedTools "Edit(//abs/f)" "Write(//abs/f)"` per file
  (double-slash) + **scoped/omitted Bash** (no bare `Bash` → kills `echo >` bypass) + persona via
  `--append-system-prompt`. **`--bare` gives clean context for free** (the `DISABLE_CLAUDE_MDS` strip is
  unreliable, §0.1) — and the per-file gate is `--allowedTools`, **no hook needed under `--bare`**. This
  REPLACES my earlier "non-bare + hook + env-strip" recipe.
- **codex exec** (planning/qa-falsification/non-edit) → **execpolicy `prefix_rule(forbidden)`** in
  hermetic `$CODEX_HOME/rules/default.rules` for git + command deny (the reliable, OS-independent floor;
  NOT `--ignore-rules`) + `--sandbox workspace-write -C <edit_dir>` (dir-confine) / `read-only` (non-edit)
  + inline MCP. **Do NOT rely on `.git`-ro** (§1, geometry-dependent). Per-file impossible on codex.

### 2.3 Cross-cutting (all four agree — adopt)
- **Veracity (HARD)**: every channel RETURNS the full tool-call trace (`--output-format json` for claude,
  the exec stream for codex / the MCP envelope for sand) so the orchestrator verifies claims vs ground
  truth; every persona emits `## Tools Used`. Orchestrator is the **sole committer**.
- **Section 0 / karpathy reasoning lives in the persona body** (already there) — output style is ambient,
  stripped; persona always loads.
- **sand/tillsyn config-driven**: system-prompt mode (replace|append), strip-set, sandbox mode, gate spec
  = knobs, not hardcoded. **sand generates** the Go-gate registration + `settings.json` + `CODEX_HOME/rules`
  as project-local artifacts; bin/sh now → sand MCP later (the contract §2.1 is the stable interface).

## 3. Reproducible commands (this session, in `.../tmp/ta_gate_test/`)
- `T1_strip.sh`, `T1bv2.sh` — DISABLE_CLAUDE_MDS strip from repo dir (CONCEDE: not reliable).
- `T2_dslash.sh` — `Edit(//abs)` double-slash per-file gate (PROVEN; fixes finding 5).
- `T3clean.sh` + `T3v3.sh` — codex `.git`-ro geometry dependence (two opposite outcomes → use execpolicy).
- `testC_codex_execpolicy.sh` — execpolicy `forbidden` git-block (PROVEN, reliable).
- `testA_bare_allowedtools.sh`, `testA3_enable.sh` — deny-by-omission (non-listed tool denied).
- (auth) `claude -p --bare` no-key → "Not logged in" (needs API key).
- (prior) gate hook unit tests + ollama/codex confine probes in the same dir + `.claude/hooks/ta_action_gate.py`.

## 4. Sources (cite-and-cross-check)
- Claude Code docs: code.claude.com/docs — `hooks`/`hooks-guide` (PreToolUse `agent_id`/`agent_type`;
  deny JSON; **exec form `{command,args}`; Interpreter table — Node & Python "Must be installed", no
  bundled runtime**; bash/PowerShell shell selection), `permissions` (Edit/Write globs; **`//abs`
  double-slash form**; Windows POSIX-normalization `/c/…`; deny precedence), `cli-reference` (`--bare`,
  `--allowedTools`/`--disallowedTools`, `--settings`, `--append-system-prompt`, `--system-prompt`),
  `headless` (**`--bare` needs API key, skips OAuth/hooks/MCP/CLAUDE.md**), `memory`/`env-vars`
  (`CLAUDE_CODE_DISABLE_CLAUDE_MDS`/`_AUTO_MEMORY`/`_GIT_INSTRUCTIONS` — strip UNRELIABLE in practice, §0.1),
  `settings`, `costs`.
- codex docs: developers.openai.com/codex — `exec-policy` (**Starlark `prefix_rule(decision="forbidden")`
  — application-layer, OS-independent, the reliable git/command block**), `concepts/sandboxing` +
  `agent-approvals-security` (writable-roots boundary; `.git` protection is workspace-relative →
  geometry-dependent, §1), `config-reference` (`approval_policy`, `[sandbox_workspace_write]`
  `writable_roots`/`exclude_*`/`network_access`), `windows` (`[windows] sandbox` elevated/unelevated).
  Source: `codex-rs/execpolicy/README.md`, `connection_manager.rs` (MCP outside sandbox). Issues #16732
  (apply_patch hook docs ahead of CLI), #24098/#17135 (windows sandbox init failure).
- Companion repo docs: `TA_RECS.md`/`TA_SANDBOX_IDEA.md`, `SAND_RECS.md`, `till_recs.md`/`AGENT_SANDBOXING_tillsyn.md`, `HYLLA_SANDBOX_IDEA.md`.

## 5. Net: hylla now AGREES with ta+sand on the spine
Go-binary exec-form hook (built-in) + `--bare`+`--allowedTools(//abs)` (`-p`) + codex execpolicy-floor +
`-C` confinement + the JSON contract + tool-call-trace veracity + persona-carries-reasoning. The 3 places
I was wrong (CLAUDE.md strip, Python hook, single-slash path) are corrected above with reproducible runs.
Remaining open: a shared Win/Linux re-run of the OS-sandbox specifics (all four tested on macOS only);
who owns generating the Go-gate + `CODEX_HOME/rules` (sand end-state).
