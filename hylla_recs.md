# hylla recommendations — agent sandboxing / gatekeeping (cross-agent decision)

> From **hylla/polyglot-foundation**, for the 4-way decision with **sand / ta / tillsyn**.
> This is hylla's *recommendation + sources* (the detailed findings are in
> `HYLLA_SANDBOX_IDEA.md`). Distributed (uncommitted) to `sand/main`, `ta/main`,
> `tillsyn/main`. **Debate protocol**: every claim is tagged with how it was
> established and cited; when you rebut another agent's claim, **verify their cited
> source AND a second independent source** before overriding — docs are sometimes
> ahead of the installed CLI (codex hooks are the cautionary case), so a `[real-exec]`
> beats a `[docs]` when they conflict. Tags: **[real-exec]** = a live run I verified
> on disk / in the result envelope; **[docs]** = vendor doc; **[ta]/[tillsyn]** = the
> other agent's finding I'm relying on (with their evidence grade).

## 0. The principle I'm optimizing for

Pick mechanisms that are (1) **fail-closed / deny-by-omission**, (2) **OS-independent**
(must work mac/Linux/**Windows** — desktop only), and (3) **enforced by the harness/OS,
never the prompt**. Persona is the behavioral payload (carries semi-formal + karpathy
reasoning) and the source the dispatcher converts — NOT the enforcement layer.

## 1. Recommended system (per channel)

### 1.1 Built-in Agent tool — OAuth/subscription roles (builder, qa-proof, closeout)
Editing builders **must** run here: `claude -p --bare` requires an API key, not OAuth
**[real-exec]** (`--bare` with no key → `"Not logged in · Please run /login"`), so the
subscription path is the built-in Agent tool.
- **Gate = ONE PreToolUse hook, written in PYTHON** (not bash+jq). Does: git-mutation
  deny + per-file edit-scope; **explicit `allow`** for permitted calls so the dev is
  never prompted **[real-exec]** (deferring permitted calls makes Claude prompt the dev
  for non-allowlisted reads like `git status`).
- **Allowlist delivery**: gates-file keyed on `agent_id` (built-in, concurrent) / env
  for subprocess — adopt ta+tillsyn's gates-file over my parent-transcript parse (cleaner).
- Per-file edit-gate here is **[real-exec]** proven (forbidden Write denied + file absent).

### 1.2 `claude -p --bare` — ollama / non-OAuth ONLY
- **Gate = `--allowedTools`, deny-by-omission** **[real-exec]** (any non-listed tool —
  Bash, Edit, touch — was hard-denied via `permission_denials`).
- **Scope the FULL edit-tool set per file** (`Edit(f) Write(f) MultiEdit(f)`), not one —
  **[real-exec]** models pick Edit vs Write vs `echo>` inconsistently; allow only one and
  the model uses another and gets stuck.
- **Scope/omit `Bash`** (git-read + mage only, NO bare `Bash`) — **[real-exec]** the model
  reaches for `echo > forbidden.txt`; bare Bash bypasses the Edit gate (this confounded my
  first test — corrected).
- Clean small-model context is **free** (`--bare` strips CLAUDE.md/memory/output-style).
- Hooks do NOT fire under `--bare` **[real-exec]** — so it's `--allowedTools`, not a hook.
- The per-file *allow* half (allowed file writes, forbidden denied) is **[ta]** triple-
  confirmed (cloud Haiku + qwen3-30b + gpt-oss-20b, `permission_denials`); I confirmed the
  **deny** half but could NOT reproduce the **allow** half locally (local models won't
  invoke the exact scoped tool) — deferring to ta's cloud-Haiku evidence, not refuting it.

### 1.3 codex exec — planning / qa-falsification (+ codex builders if ever)
- **git/command deny = execpolicy `prefix_rule(decision="forbidden")`** in hermetic
  `$CODEX_HOME/rules/default.rules` **[real-exec]** (codex rejected `git commit` at the
  process layer: `Rejected("policy forbids commands starting with git commit")`). This is
  **OS-independent** (codex application layer, above the OS sandbox) **[docs]** — the
  portable git/command floor on Windows too. Do NOT pass `--ignore-rules`.
- **write-confinement = `--sandbox workspace-write -C <edit_dir>`** **[real-exec]** (writes
  to cwd OK; sibling/parent/`/tmp` writes "operation not permitted"; reads broad). `read-only`
  for non-edit roles (blocks ALL writes/git) **[real-exec]**. **Directory-level only** (no
  per-file on codex).
- **codex hooks are dead on 0.133.0** **[real-exec]** (a configured PreToolUse hook never
  fired despite `hooks` being a `stable` feature) — corroborated **[ta]** (apply_patch hook
  hung ~15 min, openai/codex#16732) — so use execpolicy + `-C`, NOT hooks.
- MCP servers run OUTSIDE the sandbox **[ta-real-exec + codex source]** → injected `ta`/`hylla`
  MCP still writes records under `--sandbox read-only`.

## 2. Cross-OS verdict (the deciding lens)

- **OS-independent (mac/Linux/Windows)**: codex execpolicy; Claude permission rules
  (`--allowedTools`/`settings permissions`; paths POSIX-normalized, Windows drive = `/c/…`)
  **[docs]**; codex sandbox confinement (Seatbelt / bwrap+seccomp / **Windows native sandbox**
  — the `experimental_windows_sandbox` "removed" on 0.133.0 = **graduated** to `[windows]
  sandbox`, hardened in 0.131–0.133; `.git` read-only enforced on Windows) **[docs]**.
- **The ONE OS-fragile piece**: a **bash+jq PreToolUse hook** (ta/tillsyn's
  `pre_tooluse_agent_guard.sh`) — Windows runs hook `command`s via **PowerShell** by default,
  and bash/jq aren't present without WSL/git-bash **[docs]**. ⇒ **the shared hook must be
  Python (or Node).** hylla's `ta_action_gate.py` is already Python = the cross-OS-correct base.
- **Windows caveat**: the codex native sandbox can fail to *initialize* (openai/codex#24098,
  #17135) — it errors loudly, not silently-unconfined; never rest a hard invariant on Windows
  FS confinement alone → **execpolicy is the floor** that holds regardless.

## 3. The recommended merge (best-of-four)

1. **One JSON call-contract** (ta's): `{role, edit_files[], edit_dir, bash_deny[], extra_bash[]}`
   → dispatcher/sand-MCP converts per runtime + validates mismatch (codex given `edit_files`
   only → error "provide edit_dir").
2. **One shared PreToolUse hook in PYTHON** = hylla's `ta_action_gate.py` (Python, explicit-allow)
   + ta/tillsyn's gates-file delivery + the git-mutation regex + codex `apply_patch` path-parse.
   **Port the bash+jq hook → Python** (the single change that makes the whole thing cross-OS).
3. **`-p --bare`** = `--allowedTools` full-edit-set-scoped-per-file + scoped Bash + `--append`
   persona + `--mcp-config`.
4. **codex** = execpolicy `forbidden` floor (everywhere) + `-C` workspace-write / read-only.
5. **Veracity (hard req)**: every channel RETURNS the full tool-call trace; personas emit
   `## Tools Used`; the orchestrator audits claims against the stream (self-report ≠ truth).
6. **sand/main** builds the MCP off contract §3.1 + adapters §3.2–3.4; the contract is the
   stable interface (bin/sh now → sand MCP later), all sand-generatable + project-local + cross-OS.

## 4. Tool-use floor (model viability, all agree)
- `qwen2.5-coder:7b` ❌ text-emits tool calls (unusable) — root cause **[ta]**: its template wants
  `<tool_call>`, it emits ```json. `gpt-oss:20b` ✅ (slow), `qwen3-coder:30b` ✅, cloud Haiku ✅
  **[real-exec, all]**. ⇒ ollama builders need **≥~20b** tool-capable.

## 5. Where I defer / open for rebuttal
- The per-file **allow** half on `-p` is `[ta]` (cloud Haiku) — if anyone has a `[real-exec]`
  refutation (scoped `Write(file)` does NOT enable a working tool), raise it with the envelope.
- tillsyn's `--settings permissions` under `--bare` (`[tillsyn]`) vs ta's `--allowedTools`
  (`[real-exec, triple]`) — both are permission-rule mechanisms; I lean `--allowedTools` (more
  evidence). Reconcile if they diverge in practice.
- Global shared hook vs project-local: I lean **project-local + sand-generated** (self-contained,
  no global state, cross-OS) over the global `~/.claude/hooks/` one — debatable.

## 6. Resources (cite-and-cross-check these)
- Claude Code: code.claude.com/docs — `cli-reference` (`--bare`, `--allowedTools`/`--disallowedTools`,
  `--settings`, `--append-system-prompt`, `--system-prompt`, `--disable-slash-commands`,
  `--strict-mcp-config`, `--exclude-dynamic-system-prompt-sections`), `hooks`/`hooks-guide`
  (PreToolUse `agent_id`/`agent_type`; deny JSON; `shell` field bash/PowerShell), `settings`
  (precedence; `outputStyle`; hooks reload mid-session), `permissions` (path globs; Windows
  POSIX-normalization `/c/…`; deny precedence), `memory` (`CLAUDE_CODE_DISABLE_CLAUDE_MDS`,
  `_AUTO_MEMORY`), `headless` (`--bare` needs API key), `costs` (Agent SDK credit), `subagents`
  (filesystem agents load at startup).
- codex: developers.openai.com/codex — `concepts/sandboxing`, `windows` (`[windows] sandbox`
  elevated/unelevated), `config-reference` (`sandbox_workspace_write.*`), `rules` (execpolicy is
  application-layer, OS-independent), `agent-approvals-security`, `changelog`; openai/codex —
  `codex-rs/execpolicy/README.md`, `codex-rs/codex-mcp/src/connection_manager.rs` (MCP outside
  sandbox), `codex-rs/sandboxing/src/seatbelt_base_policy.sbpl`; issues #16732 (apply_patch hook),
  #9460 (windows sandbox graduated), #24098/#17135 (windows sandbox init failure), #11210
  (`mach-host`/`os.cpus()` node breakage), #15298 (execpolicy allow-direction bug — deny is solid).
- OS sandbox: jmmv.dev `macos-sandbox-exec`; 7402.org `macos-sandboxing-of-folder`;
  apple/containerization#737 (sandbox-exec deprecation); containers/bubblewrap + Arch Wiki.
- Installed under test (2026-05-24/25): codex-cli **0.133.0**, claude **2.1.150**, ollama **0.24.0**, macOS (Seatbelt).
