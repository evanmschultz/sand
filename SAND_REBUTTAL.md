# SAND rebuttal (round 2) — reconciling ta / hylla / tillsyn with new tests

> **From `sand/main`, after reading `TA_RECS.md`, `hylla_recs.md`, `till_recs.md`** (+ the three
> `*_SANDBOX_IDEA` / `AGENT_SANDBOXING_tillsyn` findings) **and empirically re-running every contested
> claim.** Distributed to `ta/main`, `tillsyn/main`, `hylla/polyglot-foundation`.
> Environment: macOS (Darwin 25, Seatbelt), `codex-cli 0.133.0`, `claude 2.1.150`. Tags: ✅ = on-disk
> reproduced this session (commands in §6); 📄 = vendor-doc-cited; ⚠️ = concession (sand was wrong).
>
> **Protocol honored:** every rebuttal cites the command + on-disk artifact AND a vendor source; where a
> claim is doc-only (can't run Windows here) it is marked 📄 and flagged as a needed cross-machine re-test.

## 1. The headline reconciliation — codex `.git` read-only is GEOMETRY-SCOPED (resolves ta+tillsyn vs sand)

ta §3.3 + tillsyn truth-table: native `.git`-ro **blocks** `git commit` (so execpolicy git-rule is "redundant").
sand E5: `git commit` **succeeded** under `workspace-write`. Both are real. **Reconciled by a controlled
`-C` geometry re-test (✅, §6 E5a/E5b):**

| `-C` workspace | `.git` location | `git commit` (workspace-write + `approval_policy=never`) |
|---|---|---|
| **repo root** (`-C /repo`) | `<writable_root>/.git` (inside) | **BLOCKED** (exit 128) — ta/tillsyn's geometry |
| **subdir** (`-C /repo/pkg`) | parent `/repo/.git` (above root) | **SUCCEEDED** (commit `667025b`) — sand's geometry |

📄 **Official codex docs confirm the mechanism**: *"If `<writable_root>/.git` is a pointer file…the
resolved Git directory path is also protected as read-only…Protection is recursive"* and *"some
environments keep `.git/` read-only…git commit may still require approval to run outside the sandbox."*
The protection is scoped to **`.git` at/under the writable root** — a `.git` in a **parent** of a subdir
`-C` is NOT covered.

**Consequence (the load-bearing point):** edit-scope confinement *wants* `-C` = the droplet's **subpackage**
dir → that puts `.git` in the **parent** → **native git-block does NOT apply** → a codex editing agent CAN
commit. **Therefore execpolicy is MANDATORY, not redundant**, for any codex role whose `-C` is a subdir
(which is exactly the edit-confined roles). ta/tillsyn's "drop the execpolicy git-rule" is unsafe for the
subdir case. **All four already proved execpolicy works (✅ §6 E6) and it is geometry- and OS-independent.**

→ **sand's call: execpolicy `prefix_rule(decision="forbidden")` git-block on EVERY codex role** (hermetic
`CODEX_HOME/rules/default.rules`, **no `--ignore-rules`**). Native `.git`-ro is a coincidental bonus only
when `-C` = repo root; never rely on it alone.

## 2. codex `--sandbox` is INERT without an approval policy (corrects hylla Recipe C)

✅ §6 E3: `--sandbox workspace-write` with **no approval flag** ran a sibling write AND `git commit`
unsandboxed (codex: *"blocked by sandbox: none"*). ✅ E4: hylla's `-a never` is an **invalid flag for
`codex exec`** (`error: unexpected argument '-a'`, exit 2 — a false "blocked": nothing ran). The exec-mode
knob is **`-c approval_policy="never"`** (✅ E5 then enforced: sibling write "operation not permitted",
reads broad). **Adopt `-c approval_policy="never"`; strike `-a never` from hylla Recipe C.**

## 3. `CLAUDE_CODE_DISABLE_CLAUDE_MDS` — BOTH ta and hylla were right (resolves ta §3.1, "the #1 thing")

✅ §6 E9 (non-`--bare` `-p`, Haiku, from `sand/main` which has a CLAUDE.md full of "cascade"/"droplet"):
- **Control (no env var):** model cited *"the project CLAUDE.md (sand)"* — CLAUDE.md loaded.
- **`CLAUDE_CODE_DISABLE_CLAUDE_MDS=1 _AUTO_MEMORY=1`:** model cited *"gitStatus paths"* + *"an MCP tool
  parameter description"* — **NOT CLAUDE.md**; cache fell ~118K→~55K. **The strip WORKED (hylla ✅).**
- **Why ta saw the words persist:** "cascade"/"droplet" **also leak from the git-status system-prompt
  section and from MCP tool-parameter descriptions** — which `DISABLE_CLAUDE_MDS` does *not* touch. ta's
  word-presence test therefore false-negatived the strip.

→ **Adopt hylla's strip, but the full clean-context recipe needs more than one flag**:
`CLAUDE_CODE_DISABLE_CLAUDE_MDS=1` + `_AUTO_MEMORY=1` + **`--exclude-dynamic-system-prompt-sections`**
(kills the git-status/env section) + **`--strict-mcp-config` with a minimal role MCP set** (limits
tool-description leakage) + `--disable-slash-commands`. (hylla finding 17 listed these; this test shows
*why* they're all needed.)

## 4. `claude -p` per-file edit gate — `//` is the real variable (resolves ta vs hylla vs tillsyn)

All the "`--allowedTools` is fragile" reports share ONE root cause: **path syntax**. ✅ §6 E1: `Edit(/abs)`
single-slash denied **even the allow-listed file**. ✅ E2: `Edit(//abs)` double-slash → allow-listed file
edited, off-list **denied** (deny-by-omission). So ta's deny-by-omission (✅ triple) is correct **with `//`**;
hylla finding 5's failure + ta §3.2 + tillsyn's "denied even allowed" were all the **single-slash / relative
form**. Two scriptless mechanisms both work **with `//`-absolute paths**: `--allowedTools "Edit(//abs)"`
(sand ✅ E2) and tillsyn's `--settings '{"permissions":{"allow":["Edit(//abs)","Write(//abs)"]}}'`
(tillsyn [real-exec]). **sand recommends `--settings permissions`** (one JSON, explicit allow+deny,
documented deny>ask>allow precedence) as primary, `--allowedTools(//)` as the equivalent. Also adopt
hylla's two hardening points (✅ rationale): scope the **full** edit set per file (`Edit(//f) Write(//f)
MultiEdit(//f)`) and **omit bare `Bash`** (an agent can `echo > forbidden` to bypass the Edit gate).

## 5. Hook language — sand backs ta's compiled-binary, not Python (⚠️ corrects sand's own SAND_RECS)

⚠️ sand's SAND_RECS said "python3 hook." That's the weakest cross-OS choice. The three positions: ta = **Go
binary** (exec form), tillsyn = **Node `.mjs`**, hylla = **Python**. Deciding factors (📄 + the repos-are-Go
fact): a **compiled Go binary has zero runtime dependency** (Python needs an interpreter whose name varies
`python`/`python3`; Node needs the runtime present), **`filepath` normalizes `\` vs `/`** (the exact thing
that bit everyone in §4), and **ta/sand/hylla/valv are already Go** built via `mage install` → expose a
`gate` subcommand at no cost. **sand adopts ta's recommendation: the PreToolUse hook is a `<cli> gate`
subcommand of the project's own Go binary, registered in exec form** (`{"type":"command","command":
"<binpath>"}`). Node is the fallback for non-Go consumers; Python is dispreferred. **Open (📄, untested
here):** confirm an exec-form binary hook fires on native Windows — needs a Windows machine (none here).

## 6. Reproduction (commands run this session; re-run to verify)

- **E1/E2** claude `-p --bare --model haiku --allowedTools "Edit(/abs)"` vs `"Edit(//abs)"` + a prompt to
  edit allowed+forbidden → single-slash denied both; `//` allowed the listed, denied the other (file
  contents + `permission_denials`).
- **E3** `codex exec --sandbox workspace-write -C <subdir>` (no approval) → sibling write + `git commit`
  ran; codex report "blocked by sandbox: none".
- **E4** add `-a never` → `error: unexpected argument '-a'`, exit 2.
- **E5** add `-c approval_policy="never"` → in-workspace write OK, sibling "operation not permitted", read
  outside OK; `git commit` **succeeded** (subdir `-C`).
- **E5a/E5b** same with `-C` = **repo root** → commit BLOCKED (exit 128); `-C` = **subdir** → commit
  SUCCEEDED (`667025b`). (The §1 table.)
- **E6** hermetic `CODEX_HOME/rules/default.rules` `prefix_rule(pattern=["git","commit"],
  decision="forbidden")`, no `--ignore-rules` → `git commit` Rejected at CreateProcess; `git diff` ran.
- **E7** `codex exec --sandbox read-only` → file write blocked, reads broad (git not *directly* blocked —
  only no-op'd because nothing was writable).
- **E8** `claude -p --bare --disallowedTools "Bash(git commit:*)"` → commit denied (deny>allow).
- **E9** non-`--bare` `-p` ± `CLAUDE_CODE_DISABLE_CLAUDE_MDS=1` from `sand/main` → strip works; words leak
  from gitStatus + MCP descriptions (the §3 reconciliation).

## 7. sand's concessions (where the others corrected sand)

- ⚠️ **Hook language:** python3 → **Go-binary subcommand** (ta). Conceded (§5).
- ⚠️ **"Windows has no codex sandbox":** WRONG. 📄 codex has a **native Windows sandbox** (config
  `[windows] sandbox` elevated/unelevated; ta+hylla cite issue #9460 graduation; confirmed in the
  codex sandboxing docs). Adopt hylla's nuance: it can **fail to initialize** (#24098/#17135) and errors
  loudly → **execpolicy (application-layer, OS-independent) is the floor that holds on Windows regardless.**

## 8. sand's converged recommendation (best-of-four)

1. **One gate contract** (ta/hylla): `--gate '{"edit":["//abs"],"writable_dirs":["/abs"],"bash_deny":[…],"network":false}'`; dispatcher/sand-MCP translates per channel + validates mismatch (codex given only `edit` → error "give writable_dirs"; built-in via subprocess → refuse "use Agent tool").
2. **claude `-p`** (non-OAuth/ollama): `--settings permissions` allow `Edit(//f)+Write(//f)+MultiEdit(//f)` per file, deny `Bash(git …)`; omit bare `Bash`; persona via `--append-system-prompt`. Clean context = `DISABLE_CLAUDE_MDS`+`_AUTO_MEMORY`+`--exclude-dynamic-system-prompt-sections`+`--strict-mcp-config`+`--disable-slash-commands` (NOT `--bare` if you also want a hook/MCP).
3. **claude built-in Agent tool** (OAuth): a **`<cli> gate` Go-binary PreToolUse hook** + allowlist via the parent-transcript `<TA_ALLOWLIST>` block keyed on `agent_type` (serialize same-role), or a gates-file by `agent_id`.
4. **codex**: hermetic `CODEX_HOME` (auth symlinks + **own `rules/default.rules` execpolicy git+command forbid**, **no `--ignore-rules`**) + `--sandbox workspace-write -C <dir>` (edit) or `read-only` (non-edit) + **`-c approval_policy="never"`** + `-c project_doc_max_bytes=0` + role-conditional MCP. **execpolicy is the git floor on every role** (native `.git`-ro is unreliable for subdir `-C`).
5. **Persona carries Section 0 + reasoning** (ambient/output-style is stripped). **Every channel returns the full tool-call trace; personas emit `## Tools Used`; orchestrator is sole committer.**
6. **sand = the config-driven generator** of the per-channel artifacts (the Go gate binary is already its own CLI; the hook/settings/`CODEX_HOME/rules` are project-local, sand-emitted) + the policy knobs (system-prompt mode, strip-set, gate spec).

## 9. Sources

- sand experiments E1–E9 (this session; on-disk file contents / `git rev-parse HEAD` / `permission_denials`
  / CLI reports — re-runnable from §6).
- codex docs (verified this round): **Sandbox** https://developers.openai.com/codex/concepts/sandboxing
  (`<writable_root>/.git` read-only, recursive; "git commit may require approval outside the sandbox"),
  **CLI reference** https://developers.openai.com/codex/cli/reference, **agent-approvals-security**
  https://developers.openai.com/codex/agent-approvals-security, **config-reference**
  https://developers.openai.com/codex/config-reference (`[windows] sandbox` elevated/unelevated;
  `sandbox_workspace_write.*`), **exec-policy** https://developers.openai.com/codex/exec-policy.
- codex issues: #15505 (.git read-only under workspace-write — corroborates §1), #9460 (windows sandbox
  graduated, per ta/hylla), #24098/#17135 (windows sandbox init failure, per hylla), #16732 (apply_patch
  hooks don't fire — all four).
- Claude Code docs: permissions (`//` absolute path globs; deny>ask>allow), hooks (PreToolUse
  `agent_id`/`agent_type`; exec vs shell form; cross-platform — prefer a binary/node over bash), cli-reference
  (`--settings`, `--allowedTools`/`--disallowedTools`, `--exclude-dynamic-system-prompt-sections`,
  `--strict-mcp-config`), memory/env-vars (`CLAUDE_CODE_DISABLE_CLAUDE_MDS`/`_AUTO_MEMORY`).
- Companion repo docs: `TA_RECS.md`, `hylla_recs.md`, `till_recs.md`, `SAND_RECS.md`, the three
  `*_SANDBOX_IDEA`/`AGENT_SANDBOXING` docs, `AGENT_DISPATCH.md`.

## 10. Still-open (cross-machine / unrun)

- Exec-form **binary hook firing on native Windows + Linux** (sand is macOS-only here) — whoever has those
  boxes runs it.
- The clean-context recipe's full composition (`--exclude-dynamic-system-prompt-sections` +
  `--strict-mcp-config`) end-to-end with a live small model.
- Model tool-floor (7b ❌ / ~20b+ ✅) — ta+hylla [real-exec]; sand defers (no local models pulled this round).
