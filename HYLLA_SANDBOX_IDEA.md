# Hylla Sandbox Idea — Per-Dispatch Agent Confinement

> Origin: **hylla/polyglot-foundation** (2026-05-24). Distributed (uncommitted) to
> `sand/main`, `ta/main`, `tillsyn/main` for discussion. Empirical findings +
> design for mechanically confining a dispatched agent to exactly the actions the
> orchestrator grants AT CALL TIME — "hyper-localized edit + full sandbox" —
> across all three execution providers, **with a clean (no-CLAUDE.md) context for
> small models**. Every claim below is tagged ✅ PROVEN (ground-truth evidence in
> hand), 📄 DOC (vendor-doc-cited, not yet locally run), or ❌ DISPROVEN.

## TL;DR

- **The model**: every dispatch carries an **allowlist of permitted actions**
  (the 1–2 files it may edit + forbidden commands). The enforcement layer is the
  **sole authority**: permitted actions run **without prompting the dev**;
  forbidden actions are **silently denied** and the agent **fails + reports the
  contradiction**; if it needs more, the **orchestrator revises the call shape**.
  The dev manages nothing per-action; the agent cannot widen its own grant.
- **Per-provider gate (proven matrix)**:
  - **Claude built-in (Agent tool, OAuth)** → PreToolUse hook (`ta_action_gate.py`), allowlist via the **parent transcript keyed on `agent_type`**. ✅ Per-file.
  - **`claude -p` (non-OAuth / ollama)** → **drop `--bare`**, inject the hook via `--settings`, deliver the allowlist via the **`TA_GATE_ALLOWLIST` env var**, and strip CLAUDE.md/memory via env vars. ✅ Per-file (no-`--bare` hook proven; env-strip 📄+in-flight).
  - **codex exec (hermetic)** → the **OS sandbox**: `--sandbox read-only`, or `--sandbox workspace-write -C <dir> --add-dir <dir>` + tmp-exclude. ✅ Per-**directory** (codex hooks unusable on 0.133.0 — below).
- **The CLAUDE.md-bloat problem is solved without `--bare` or an OS sandbox**: `CLAUDE_CODE_DISABLE_CLAUDE_MDS=1` + `CLAUDE_CODE_DISABLE_AUTO_MEMORY=1` strip CLAUDE.md + memory **while hooks keep firing** (since `--bare` is what disables hooks, and we're not using it).

## Provider Matrix (status-tagged)

| Capability | Built-in (hook) | `-p` (no `--bare`) | codex (`--sandbox`) |
|---|---|---|---|
| Per-FILE edit gate | ✅ hook denies off-list Edit/Write | ✅ hook (env-allowlist) | ❌ per-file (codex hooks didn't fire) |
| Per-DIR write confinement | (n/a; per-file) | (n/a; per-file) | ✅ `-C <dir>` + tmp-exclude, OS-enforced |
| Read-only-git (no commit/push) | ✅ hook `bash_deny` | ✅ hook `bash_deny` | ✅ sandbox (`.git` always read-only) |
| Permitted action runs w/o dev prompt | ✅ explicit `allow` | ✅ explicit `allow` | ✅ sandbox auto-allows |
| Forbidden denied w/o dev prompt | ✅ explicit `deny` | ✅ explicit `deny` | ✅ sandbox denies (approval=never) |
| CLEAN context (no CLAUDE.md) for tiny models | (orchestrator session) | ✅ `CLAUDE_CODE_DISABLE_CLAUDE_MDS=1` + `DISABLE_AUTO_MEMORY=1` (no `--bare` needed) | ✅ hermetic `CODEX_HOME` + `project_doc_max_bytes=0` |
| Shell-write bypass (`echo > f`) | ⚠️ tool-layer; persona + `git diff --stat` audit backstop | ⚠️ same | ✅ blocked at syscall (read-only) / confined (`-C`) |
| Reads restricted to dirs | ❌ (not a goal) | ❌ | ❌ codex sandbox governs writes/exec/network only, never reads |
| MCP writes under sandbox (ta create/update) | n/a | n/a | ✅ MCP servers spawn OUTSIDE the sandbox (codex source-confirmed) |

## Findings (empirical, dated 2026-05-24) — incl. corrections

1. ✅ **PreToolUse fires for built-in subagents**; payload carries `agent_id` + `agent_type` (subagent-only). Deny schema: `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny|allow","permissionDecisionReason":"..."}}`.
2. ✅ **A subagent's `transcript_path` is the PARENT transcript**, not its own — subagents have **no** separate transcript file (verified: only the parent jsonl exists). So the hook resolves the allowlist by matching `agent_type` → the most-recent `Agent` tool_use (with its `<TA_ALLOWLIST>` prompt) in the parent transcript. **Constraint: serialize same-role dispatches.**
3. ✅ **Defer ≠ silent.** A hook that `exit 0`s for permitted actions hands back to the normal flow, which **prompts the dev** for any non-allowlisted command (e.g. `git status`). Fix: emit an explicit `allow` for permitted scoped-agent actions → no prompt. (This was the cause of the dev seeing a `git status` approval prompt.)
4. ✅ **`--bare` disables hooks**; `--settings`-injecting a hook does NOT re-enable them (ground-truth: an off-list Write + a `git commit` ran ungated under `--bare`, no hook log, a stray empty commit landed and was `git reset --soft`'d). No override flag exists. 📄 (cli-reference: `--bare` "skip auto-discovery of hooks…").
5. ✅ **`--allowedTools "Write(/abs/file)"` does NOT deny-default** on `-p`: an off-list Write still ran. Path-scoped allow entries don't create "only-this-file-writable" (allow/deny precedence: deny wins, can't allow-a-subset).
6. ✅ **`--disallowedTools "Bash(git commit:*)"` works** on `-p` (commit blocked). Read-only-git IS enforceable via `--disallowedTools`; per-file edit is not (under `--bare`).
7. ✅ **`claude -p` WITHOUT `--bare` + `--settings` hook + `TA_GATE_ALLOWLIST` env → per-file gate WORKS** (ground-truth log: `agent_id:""` confirms the env-var path; `allowed.txt` allowed + created, `forbidden.txt` denied + absent, `git commit` denied — across multiple model retries). Routes to ollama via `ANTHROPIC_BASE_URL` (served-by not rigorously captured; gate is backend-independent).
8. 📄 **`CLAUDE_CODE_DISABLE_CLAUDE_MDS=1` + `CLAUDE_CODE_DISABLE_AUTO_MEMORY=1` strip CLAUDE.md (user/project/local/rules) + auto-memory WITHOUT `--bare`** → hooks still fire (env-vars.md / memory.md). These suppressions are individually toggleable; `--bare` is the all-or-nothing bundle. *(Local composition-with-hook check in progress; the no-`--bare`-hook half is already PROVEN in finding 7.)*
9. ❌ **codex per-file hooks: NOT usable on installed codex 0.133.0.** `codex features list` shows `hooks stable true`; `--strict-config` accepted `[[hooks.PreToolUse]]`; `--dangerously-bypass-hook-trust` is recognized — yet a configured PreToolUse command hook **did not fire** in 2 attempts (the `echo` ran ungated, hook log empty). So codex hooks are present-but-not-demonstrably-working with the config I tried; **do not rely on them.** Use the sandbox.
10. ✅ **codex `--sandbox read-only`** blocks ALL writes + git at the OS layer (`/tmp` write, repo write, `git commit` → "operation not permitted"); reads ran.
11. ✅ **codex `--sandbox workspace-write -C <dir>` + `exclude_slash_tmp` + `exclude_tmpdir_env_var`** confines writes to exactly that one dir: write to cwd RAN; writes to `/tmp` and to a sibling repo path BLOCKED (files absent); read of `go.mod` (outside cwd) RAN. **Per-directory, OS-enforced; reads unrestricted; `writable_roots`/`--add-dir` are additive (cwd is always writable, can't be subtracted).**
12. 📄 **codex MCP servers run OUTSIDE the `--sandbox`** (source: `codex-rs/codex-mcp/src/connection_manager.rs`) — an injected `ta`/`tillsyn` MCP can create/update records even under `--sandbox read-only`. (Matches the hermetic dispatch's reliance.)
13. ❌ **`--dangerously-bypass-hook-trust` is a non-answer**: it only skips codex's hook-trust safety check; relying on a "dangerously" flag for routine gating is a smell, and it's moot since the codex hook didn't fire anyway.
14. ✅ **Ollama tool-use capability floor: 7b fails, ~20b works.** `qwen2.5-coder:7b` via the ollama Anthropic-shim does NOT make real tool calls — it emits `{"name":"write",...}` as TEXT (confirmed twice; hook debug log empty; no files written). `gpt-oss:20b` and `qwen3-coder:30b` DO make real `tool_use` calls and are correctly gated. So **ollama builders need a ≥~20b tool-capable model**; below that the agent can't edit/run at all (and there's nothing to gate).
15. ✅ **`CLAUDE_CODE_DISABLE_CLAUDE_MDS=1` strips project + global CLAUDE.md** (the `cascade`/`droplet` terms vanished from the model's self-reported context) and composes with the hook (gate still fires). `CLAUDE_CODE_DISABLE_AUTO_MEMORY=1` strips auto-memory. Both work WITHOUT `--bare` → hooks survive.
16. ✅❌ **The Section 0 leak was the OUTPUT STYLE, and `outputStyle:"default"` (STRING) strips it — but `outputStyle:null` SILENTLY BREAKS THE HOOK.** With CLAUDE.md already disabled, `section0` flipped `yes`→`no` only when an `outputStyle` override was applied → the output style (not the persona, not CLAUDE.md) was the Section 0 source. **DANGER**: `--settings '{"outputStyle":null,"hooks":{…}}'` → `null` is invalid → the whole `--settings` was mangled → **the hook silently did not fire and an off-list write went through** (caught via empty debug log + `forbidden.txt` present). `--settings '{"outputStyle":"default","hooks":{…}}'` → gate fires (log-proven) AND `section0=no`. **Always use the string `"default"`; always confirm the gate via the debug log, never trust that a strip+gate combo composed.**
17. 📄 **Full non-`--bare` `-p` context accounting** (claude-code-guide, doc-cited): suppress git-instructions `CLAUDE_CODE_DISABLE_GIT_INSTRUCTIONS=1`; skills/slash-commands `--disable-slash-commands`; env-info `--exclude-dynamic-system-prompt-sections` (default-prompt only); auto-MCP `--mcp-config <role.json> --strict-mcp-config` (+ `ENABLE_TOOL_SEARCH=true` defers schemas); base system prompt only removable via `--system-prompt` (full replace — drops tool-use guidance + safety, risky for small models). Only **hooks** are irreducible without `--bare` — which is what we want.

## The Mechanisms (proven recipes)

### A. Built-in Agent tool → `ta_action_gate.py` (PreToolUse hook)
- Registered in `.claude/settings.json` `hooks.PreToolUse` (matcher `Edit|Write|MultiEdit|NotebookEdit|Bash`; widen to `*` to also gate MCP tools).
- Resolution order: (1) `TA_GATE_ALLOWLIST` env [subprocess paths]; (2) else if `agent_id` present, scan the PARENT transcript for the most-recent `Agent`/`Task` tool_use with `input.subagent_type == agent_type` carrying a `<TA_ALLOWLIST>` block; (3) else **defer** (orchestrator never gated).
- Decision: Edit/Write target ∈ `edit` → explicit `allow`, else `deny`; Bash matching `bash_deny` → `deny`, else explicit `allow`; the deny reason is fed back so the agent reports the contradiction. The agent cannot forge/remove the block (it's orchestrator-authored).

### B. `claude -p` (non-OAuth / ollama) → no-`--bare` hook + clean context [RECOMMENDED, PROVEN]
```bash
ANTHROPIC_BASE_URL=http://localhost:11434 ANTHROPIC_API_KEY=ollama \
CLAUDE_CODE_DISABLE_CLAUDE_MDS=1 CLAUDE_CODE_DISABLE_AUTO_MEMORY=1 \
CLAUDE_CODE_DISABLE_GIT_INSTRUCTIONS=1 ENABLE_TOOL_SEARCH=true \
TA_GATE_ALLOWLIST='{"edit":["/abs/f"],"bash_deny":["git commit","git push",...]}' \
claude -p --model <m> --output-format json --no-session-persistence \
  --settings '{"outputStyle":"default","hooks":{"PreToolUse":[{"matcher":"*","hooks":[{"type":"command","command":"python3 \"/abs/.claude/hooks/ta_action_gate.py\""}]}]}}' \
  --append-system-prompt '<persona body INCL. Section 0 / semi-formal-reasoning directive>' \
  --mcp-config '<role-mcp.json>' --strict-mcp-config --disable-slash-commands \
  --allowedTools 'Read Edit Write Bash ...' \
  <<<'<task>'
```
- ✅ **PROVEN end-to-end on gpt-oss:20b**: `cascade=no droplet=no section0=no` (CLAUDE.md + output-style stripped) AND the per-file gate fired (off-list Write denied + absent, on-list allowed — debug-log-proven).
- NO `--bare` ⇒ hook fires. `DISABLE_CLAUDE_MDS`/`DISABLE_AUTO_MEMORY`/`DISABLE_GIT_INSTRUCTIONS` + `outputStyle:"default"` + `--disable-slash-commands` + `--strict-mcp-config` ⇒ the model's context is just the base tool-guidance + persona + task + the role's MCP tool names. Persona (incl. **Section 0**) is the sole behavioral payload. Allowlist via env var (`-p` has no `agent_id`).
- ⚠️ **TRAP (proven)**: `outputStyle:null` is INVALID — it silently breaks the `--settings` hook (gate fails open, off-list write goes through). Use the STRING `"default"`. ALWAYS confirm the gate fired via the debug log + file existence; never assume a strip+gate combo composed.
- **Tool-use floor**: needs a ≥~20b tool-capable model (7b emits tool calls as text and can't drive tools at all).
- ⚠️ `--system-prompt` (full replace, "ONLY persona") drops Claude Code's tool-use guidance + safety → risky for small models that are already shaky at tool-calling; prefer `--append-system-prompt` (persona + base guidance = "persona + necessary context").

### C. codex exec → OS sandbox (per-dir) [PROVEN]
- Non-editing roles → `--sandbox read-only` (zero writes; `ta` MCP create/update still works).
- Editing/mage roles → `--sandbox workspace-write -C <writable-dir> [--add-dir <more>] -c sandbox_workspace_write.exclude_slash_tmp=true -c sandbox_workspace_write.exclude_tmpdir_env_var=true -c sandbox_workspace_write.network_access=false -a never`.
- Command-deny: `.git` is auto read-only (commit blocked free); for other commands use exec-policy `.rules` (`prefix_rule(pattern=["mage","install"], decision="forbidden")`) — file-based, not `-c`.
- Per-file is NOT available (sandbox is per-dir); keep editing builders on Claude.

## The CLAUDE.md-context-strip options (your question) + recommendation

- **Option 1 [RECOMMENDED]** — no `--bare` + `CLAUDE_CODE_DISABLE_CLAUDE_MDS=1` + `CLAUDE_CODE_DISABLE_AUTO_MEMORY=1` + hook + `--append-system-prompt`. Clean context **and** per-file hook gate; simplest; no OS-sandbox fragility. (Recipe B.)
- **Option 2** — `--bare` (clean, no hooks) wrapped in an OS sandbox (`sandbox-exec`/`bwrap`) for write-confinement. Pros: `--bare` clean context, OS-enforced, catches subprocess writes a hook can't see. Cons: **dir-granular not per-file**; `sandbox-exec` deprecated + version-drifts; Node breaks without `(allow mach-host)`/`sysctl` allows; must allow `~/.claude`+`$TMPDIR` (relocate `CLAUDE_CONFIG_DIR` to a throwaway dir so the agent can't touch real creds). Keep as an optional **coarse outer ring**, not the primary gate.
- **Option 3** — `--bare` + `--disallowedTools` only. Clean context, command-deny works, but **no per-file edit gate**. Only for non-editing roles.
- **Verdict**: Option 1. The in-app hook is the authoritative per-file gate (matches the requirement, robust, `--bare`-independent); OS sandbox is defense-in-depth if we later want subprocess-write containment.

### Universal principle: behavior lives in the persona, not ambient context
The agent should be directed ONLY by: (a) its **persona** (passed as the system prompt), (b) the **task prompt**, (c) the role's **MCP tools + base tool-guidance** (necessary context). Everything else — CLAUDE.md, auto-memory, the custom output style, git-instructions, skills/slash-commands, env-info, auto-discovered MCP — is **ambient bloat to strip** (it doesn't survive small-model context windows and isn't the agent's job). Crucially, **semi-formal reasoning (Section 0) must live in the persona body**, NOT the output style: the output style is ambient (stripped for small models), but the persona is the agent's identity and always loads. The personas already carry a Section 0 directive — keep it there. This makes semi-formal reasoning **universal** (every backend, every model size) and decoupled from the output-style (which the orchestrator keeps for ITS own responses, but small dispatched agents strip). Per-backend mechanism differs (built-in: persona-as-system-prompt; `-p`: Recipe B; codex: hermetic `CODEX_HOME` + `project_doc_max_bytes=0`), but the principle is one: **persona + prompt + necessary tools, nothing ambient.**

**hylla bin/sh policy (this repo): the persona IS the sole system prompt.** Strip EVERYTHING — even on the dev's own machine, not just CI — and pass the persona via `--system-prompt` (full **REPLACE**, not `--append`) so the persona is the only system-prompt text. Because replacing drops Claude Code's base tool-use-guidance + safety prose, **every persona MUST self-contain**: (1) tool-use / process guidance (which tools, when; mage-only; Hylla-first; read-only-git; the dispatch-allowlist contract); (2) the **semi-formal reasoning (Section 0)** directive; (3) a **karpathy-style thinking** directive (first-principles, think-before-acting). The tool *schemas* still arrive via `--allowedTools`/`--mcp-config` (tool-calling capability is preserved); only the prose guidance moves into the persona. **Caveat (verify before relying on it for builders): confirm a ~20b model still tool-calls correctly under full `--system-prompt` replace** — the base prose may have been helping; if it regresses, fall back to `--append-system-prompt` for the editing roles.

**tillsyn + sand: keep it FLEXIBLE / configurable.** The sand-generated dispatch + tillsyn must expose, per project/role: the system-prompt MODE (replace vs append), the strip set (which ambient sources to suppress), and the gate spec — as knobs, NOT hardcoded. hylla's "strip everything + persona-only" is ONE profile; other consumers may want base-guidance retained or a different reasoning style. The generator/MCP owns the policy knobs so each project chooses.

## The `<TA_ALLOWLIST>` contract + the `--gate` JSON arg (for sand MCP)

Orchestrator embeds at the top of every scoped spawn prompt (built-in path) / the dispatcher extracts it (`-p`):
```
<TA_ALLOWLIST>
{"edit": ["/abs/fileA.go","/abs/fileA_test.go"],
 "writable_dirs": ["/abs/dir"],
 "bash_deny": ["git commit","git push","git add","git rebase","git merge","git reset","git checkout","git branch","git tag","git stash","git restore","mage install","go get","go mod"],
 "network": false}
</TA_ALLOWLIST>
```
Proposed dispatcher / sand-MCP arg shape (single JSON gate spec, easy to generate):
```
agent-dispatch --role <persona> --gate '{"edit":[...],"writable_dirs":[...],"bash_deny":[...],"network":false}' --prompt '<task>'
```
Per-backend translation the converter (bin/sh now, sand MCP later) must emit:

| gate field → | Built-in Agent tool | `-p` (no `--bare`) | codex exec |
|---|---|---|---|
| `edit` (per-file) | `<TA_ALLOWLIST>` block in prompt → hook | `TA_GATE_ALLOWLIST` env + `--settings` hook | not per-file → use `writable_dirs` |
| `writable_dirs` | (n/a) | (n/a) | `-C <dir0>` + `--add-dir <dirN>` + exclude-tmp |
| `bash_deny` | hook `bash_deny` | hook `bash_deny` | `.git` ro (free) + exec-policy `.rules` |
| `network:false` | (n/a) | (n/a) | `-c sandbox_workspace_write.network_access=false` |
| clean context | (orch session) | `DISABLE_CLAUDE_MDS=1` + `DISABLE_AUTO_MEMORY=1` + `--append-system-prompt` | hermetic `CODEX_HOME` + `project_doc_max_bytes=0` |
| delivery | parent transcript by `agent_type` | env var (no `agent_id`) | sandbox flags |

**Persona conversion**: keep `tools:` as the tool-existence allowlist; attach a default `bash_deny` per role family (git mutation + `mage install` + `go get`/`go mod`); set the codex `--sandbox` mode per role (read-only for non-editing, workspace-write+`-C` for editing); mark editing roles (need `edit`/`writable_dirs`) vs read-only (`edit:[]`).

## OS-sandbox appendix (Option 2 — optional outer ring)

macOS Seatbelt (`sandbox-exec`, deprecated-but-works on Darwin 25; SBPL `(deny default)` + targeted `(allow ...)`, deny-always-wins, `subpath` dir-granular). Working write-confine profile must also allow the Node capability block or the CLI silently breaks (`os.cpus()` → `[]`): `process-exec/fork`, `sysctl-read`, **`mach-host`**, `mach-lookup` (opendirectoryd/cfprefsd), `ipc-posix-sem`, `pseudo-tty`, `/dev/null`, `file-read* (subpath "/")`, and `file-write*` for the edit dir + relocated `CLAUDE_CONFIG_DIR` + `$TMPDIR`. Invoke `sandbox-exec -D … -p "$(cat profile.sb)" claude -p --bare …`. Linux: `bwrap --ro-bind / / --bind <writedir> <writedir> --bind $CLAUDE_CONFIG_DIR … --share-net --die-with-parent claude -p --bare …` (first-class, better-supported). **Relocate `CLAUDE_CONFIG_DIR` to a throwaway dir** so the sandboxed agent never touches real `~/.claude` (sessions/creds/tillsyn-auth). Caveat: temp-then-rename + per-file granularity gaps make this coarser than the hook.

## Tool-call return + veracity audit (HARD REQUIREMENT — all backends)

The orchestrator must verify an agent's claims against the **actual tool-call stream** — self-reported "I did X" / "verdict: pass" is NEVER authoritative. So every dispatch channel MUST return the full tool-call trace, and every persona MUST emit a resources/Tools-Used audit:

- **bin/sh `claude -p`**: `--output-format json` (or `stream-json`) returns the message stream incl. every `tool_use` event → the orchestrator parses it to confirm the required Edit/Write/Bash/MCP calls actually happened and flags any out-of-scope call. The gate's `.claude/hooks/ta_gate_debug.log` is a second authoritative record of allow/deny decisions.
- **codex exec**: the stream emits `exec` shell lines + `mcp: <server>/<tool>` lines → the orchestrator audits those.
- **sand MCP / tillsyn**: MUST surface the SAME — the full tool-call trace returned to the caller (orchestrator/principal) so veracity is checkable. Non-negotiable for both; the MCP response envelope must carry the trace, not just a summary.
- **Persona requirement**: every persona's output MUST include a `## Tools Used` (resources-used) section — every distinct tool/MCP call + key Bash + the evidence sources consulted (Hylla refs, Context7 libs, files read). Empty = methodology violation. The orchestrator cross-checks this list against the actual stream.

This closes the agent-fabrication hole: a builder/QA cannot claim a mage gate passed, a file was edited, or a record updated unless the stream shows the call.

## Repo pointers (where the pieces live — hylla/polyglot-foundation)

- **Gate hook**: `.claude/hooks/ta_action_gate.py` — PreToolUse; resolves the allowlist from `TA_GATE_ALLOWLIST` env (subprocess paths) OR the parent-transcript `<TA_ALLOWLIST>` block by `agent_type` (built-in subagents); emits explicit allow/deny; logs to `.claude/hooks/ta_gate_debug.log`.
- **Hook registration**: `.claude/settings.json` → `hooks.PreToolUse` (matcher `Edit|Write|MultiEdit|NotebookEdit|Bash`; widen to `*` to also gate MCP tools).
- **Dispatcher**: `bin/agent-dispatch.sh` — `dispatch_codex` (hermetic `CODEX_HOME` + per-role `--sandbox`/`-C` from chain opts + inline MCP injection); `dispatch_ollama` (the `claude -p` path; carries the documented no-`--bare` gate notes).
- **Chains / per-role sandbox opts**: `.claude/agent-chains.sh` (planning → `--sandbox read-only`; qa-falsification → `--sandbox workspace-write`).
- **Personas**: `.claude/agents/ta-{go,fe}-{planning,builder,plan-qa-proof,plan-qa-falsification,build-qa-proof,build-qa-falsification}.md` + `ta-closeout.md`.
- **Orchestrator rules**: `CLAUDE.md` § "Orchestrator Role Boundaries" (`<TA_ALLOWLIST>` injection contract + GIT-ORCHESTRATOR-ONLY).
- **Test harnesses from this investigation**: `/tmp/ta_gate_test/*.sh` (gate unit tests + ollama/`-p` + codex-confine probes) and `/tmp/ck/*` (codex hook probe).

## Roadmap: bin/sh → sand MCP → tillsyn

- **Now (bin/sh)**: hook + `.claude/settings.json` registration; per-role `--sandbox`/`-C` in codex dispatch; `-p` uses Option 1. Orchestrator injects the `<TA_ALLOWLIST>` block (built-in) / dispatcher sets env + `--settings` (`-p`).
- **sand (MCP, replaces bin/sh)**: sand generates persona defs + the `--gate '<json>'` call shape, emitting the per-backend translation automatically (table above), and generates the hook + settings as project-local artifacts (no global state). sand owns def→runtime conversion.
- **tillsyn**: same model, tillsyn substrate (`mcp__tillsyn__*`). Gate layers on top of tillsyn's existing FE-Playwright + hermetic-codex dispatch. tillsyn ↔ sand share the def→codex translation logic (first to confirm hands off).

## Resources / citations

- Claude Code: `cli-reference` (`--bare`, `--settings`, `--allowedTools`/`--disallowedTools`, `--append-system-prompt`), `settings` (precedence; `autoMemoryEnabled`, `claudeMdExcludes`, `disableAllHooks`), `memory` (CLAUDE.md load order; `CLAUDE_CODE_DISABLE_CLAUDE_MDS`, `CLAUDE_CODE_DISABLE_AUTO_MEMORY`), `env-vars`, `hooks` / `hooks-guide` (PreToolUse input/deny schema; "PermissionRequest hooks do not fire in -p, use PreToolUse"), `subagents` (filesystem agents load at startup → restart to reload), `claude-directory` (`CLAUDE_CONFIG_DIR`). All at code.claude.com/docs.
- codex: developers.openai.com `config-reference` / `config-advanced` (`sandbox_mode`, `[sandbox_workspace_write]` `writable_roots`/`network_access`/`exclude_*`, `[[hooks.PreToolUse]]`, `approval_policy`, `[tools]`), `concepts/sandboxing` (Seatbelt/bwrap+seccomp), `agent-approvals-security` (read-only/workspace-write semantics; reads never restricted), `exec-policy` (Starlark `prefix_rule(decision="forbidden")`). Source: `openai/codex` `codex-rs/codex-mcp/src/connection_manager.rs` (MCP spawned outside sandbox), `codex-rs/sandboxing/src/seatbelt_base_policy.sbpl` + `seatbelt.rs`, issue `#11210` (`mach-host`/`os.cpus()` Node breakage). Installed: **codex-cli 0.133.0** (`hooks` feature `stable true`, but PreToolUse hook did not fire in testing).
- OS sandbox write-ups: jmmv.dev `macos-sandbox-exec`; 7402.org `macos-sandboxing-of-folder`; zameermanji.com `sandboxing-subprocesses-in-macos`; apple/containerization `#737` (sandbox-exec deprecation); containers/bubblewrap + Arch Wiki Bubblewrap/Examples.

## Fixes applied (hylla, this round)
- `.claude/hooks/ta_action_gate.py` — the gate (unit-tested 19/19; live-verified: built-in builder + read-only QA + `-p` no-`--bare`). Env-var + parent-transcript delivery; explicit allow/deny (no dev prompt).
- `.claude/settings.json` — `hooks.PreToolUse` registration.
- `CLAUDE.md` "Orchestrator Role Boundaries" — `GIT IS ORCHESTRATOR-ONLY` + mechanical-gate + `<TA_ALLOWLIST>` injection spec.
- All 13 personas — read-only-git block.
- `bin/agent-dispatch.sh` `dispatch_ollama` — documented the `-p` gate path (drop `--bare`; reverted the broken `--settings`-under-`--bare` injection).

## Open items / decisions
- Build the unified `--gate '<json>'` converter into `bin/agent-dispatch.sh` (and have sand generate it).
- Widen the built-in hook matcher to `*` + add a `tool_deny` list (gate MCP tools too, e.g. QA may not `mcp__ta__create`/`delete`/`hylla.ingest`). Needs a restart.
- Shell-write-bypass policy on Claude (denylist `>`/`tee`/`sed -i` in `bash_deny` vs persona + `git diff --stat` audit). True syscall confinement on Claude = the OS-sandbox outer ring (Option 2).
- `.claude/hooks/*` + `settings.json` are gitignored (`.claude` whitelist tracks only `ta-*.md`) — decide whether to whitelist them or have sand regenerate.
- Finish the local composition check for finding 8 (env-var strip × hook firing together); the no-`--bare`-hook half is already proven.
