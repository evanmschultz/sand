# AGENT_SANDBOX_SPEC — locked consensus (2026-05-25)

> **Canonical, build-from spec** for per-dispatch agent sandboxing/gating across ta / hylla /
> tillsyn / sand. Supersedes the round-1/2 scratch docs (`*_RECS.md`, `*_REBUTTAL.md`,
> `*_SANDBOX_IDEA.md`, `AGENT_SANDBOXING_tillsyn.md`) — those are retained in git history only.
> Companion to `AGENT_DISPATCH.md` (the dispatch/routing model). Every mechanism below was
> reproduced on ≥2 independent macOS runs (Darwin 25, `claude 2.1.150`, `codex-cli 0.133.0`); the
> evidence + reproduction commands live in the superseded docs' git history. Windows/Linux is the
> one open axis (§8).

## 0. Two non-negotiable invariants

1. **GIT IS ORCHESTRATOR-ONLY.** No dispatched agent may ever `git commit/push/add/reset/checkout/
   branch/tag/stash/rebase/merge/restore` — enforced at the harness/OS layer, never by prompt.
2. **EDIT-SCOPE.** An agent may write ONLY the file(s) (claude) or directory (codex) granted at
   dispatch — regardless of what its prompt says. Off-scope writes FAIL; the agent reports the
   contradiction. Reads stay broad (read-confinement is not a goal).

## 1. Implementation rule for sand + tillsyn (LOAD-BEARING)

**sand and tillsyn dispatch + gate logic is GO CODE ONLY — never `bin/*.sh`.** sand is a Go MCP
server; tillsyn is Go. They do NOT shell out to `bin/agent-dispatch.sh` (that bash dispatcher is the
legacy bootstrap sand REPLACES, and is the other repos' interim only). Concretely:

- The **PreToolUse gate hook is a Go subcommand of the project's own binary** — `sand gate` / `till
  gate` — registered in **exec form** `{"type":"command","command":"<binpath>","args":["gate"]}`.
  (Go = zero runtime dep, `filepath` normalizes `\`/`/`, the binary already ships via `mage install`.)
- All per-channel dispatch (claude headless / built-in Agent-tool spec / codex exec) is built by Go.
- The ONLY `.sh` permitted is a **user's own hook** they choose to register — never sand's/tillsyn's
  dispatch or gate.
- sand/tillsyn are **config-driven translators**: users declare chains + limits in TOML; sand/tillsyn
  Go code TRANSLATES (`<project>/.claude/agents/<role>.md` persona + chain TOML → the per-channel
  invocation + the gate/sandbox artifacts) and ENFORCES them. No isolation policy hardcoded in Go.

## 2. Per-channel enforcement (consensus, all reproduced)

| Channel | Used for | git-block | edit-scope | clean context |
|---|---|---|---|---|
| **Claude built-in Agent tool** | OAuth/subscription roles (builder, plan-qa-proof, build-qa-proof, closeout) | Go `gate` PreToolUse hook (`bash_deny`) | Go `gate` hook, **per-FILE**, allowlist by `agent_id`/`agent_type` | orchestrator session |
| **`claude -p --bare`** | API-key / ollama tier only (NOT OAuth — `--bare` needs an API key) | `--disallowedTools "Bash(git commit:*)" …` (deny wins) | `--allowedTools "Edit(//abs) Write(//abs) MultiEdit(//abs)"` **per-FILE, `//` double-slash**, + **no bare `Bash`** | `--bare` strips CLAUDE.md/memory/output-style for free |
| **`codex exec`** | planning, plan-qa-falsification, build-qa-falsification (+ dir-scoped editing on mac/Linux) | **execpolicy** `prefix_rule(decision="forbidden")` in hermetic `$CODEX_HOME/rules/default.rules` (NO `--ignore-rules`) | `--sandbox workspace-write -C <dir>` (**per-DIR**) or `read-only`; **`-c approval_policy="never"`** required | hermetic `CODEX_HOME` + `-c project_doc_max_bytes=0` |

**Settled facts (each reproduced ≥2x):**
- claude path rules need the **`//` double-slash absolute form**; single-slash denies everything.
- `claude -p --bare` requires an **API key, not OAuth** → OAuth roles MUST use the built-in Agent tool.
- codex `--sandbox` is **inert in `exec` without `-c approval_policy="never"`**; `-a never` is an invalid flag.
- codex **execpolicy** is the reliable, OS-independent git/command floor. codex's native `.git`-ro is a
  real bonus when `.git` is OUTSIDE the writable set `[workdir/-C, /tmp, $TMPDIR]` (true for real `$HOME`
  project trees with a subdir `-C`) — but is **void for `/tmp`-resident repos** and is writable-roots-
  dependent, so **execpolicy is PRIMARY**, native is defense-in-depth.
- codex PreToolUse hooks are **dead on 0.133.0 exec** — use execpolicy + `-C`, never codex hooks.
- codex MCP servers run **outside** the sandbox → injected `ta`/`hylla` MCP write records even under `read-only`.
- Hook runtime: **Go binary** (Claude Code bundles neither node nor python on Windows; bash is Windows-broken).

## 3. The gate contract (one shape, per-backend translation)

```
gate = {
  "edit":          ["//abs/file.go","//abs/file_test.go"],   # per-FILE (claude); [] = read-only role
  "writable_dirs": ["/abs/droplet-dir"],                      # per-DIR (codex)
  "bash_deny":     ["git commit","git push","git add","git reset","git checkout","git branch",
                    "git tag","git stash","git rebase","git merge","git restore",
                    "mage install","go get","go mod"],
  "network":       false
}
```
sand/tillsyn (Go) translate per channel + **validate mismatch**: a codex role given only `edit` (no
`writable_dirs`) → error "codex is dir-level; provide writable_dirs"; a built-in OAuth role dispatched
as a subprocess → refuse "use the Agent tool". Translation:

| gate field → | built-in Agent tool | `claude -p --bare` | codex exec |
|---|---|---|---|
| `edit` | hook allowlist (`<TA_ALLOWLIST>` by `agent_type`, or gates-file by `agent_id`) | `--allowedTools "Edit(//f) Write(//f) MultiEdit(//f)"` | → use `writable_dirs` (`-C`) |
| `writable_dirs` | (n/a) | (n/a) | `-C <dir0>` + `--add-dir <dirN>` |
| `bash_deny` | hook `bash_deny` | `--disallowedTools "Bash(git commit:*)" …` + no bare `Bash` | execpolicy `rules/default.rules` `prefix_rule(forbidden)` |
| `network:false` | (n/a) | (n/a) | `-c sandbox_workspace_write.network_access=false` |

## 4. Per-role tool / disallowed matrix (the per-role grant)

| Role | hylla | context7 | gopls | playwright | WebSearch | ta/till | edit | git |
|---|---|---|---|---|---|---|---|---|
| planning (go/fe) | **read-only** | ✅ | go only | fe only | ✅ | create+update | ❌ none (`edit:[]`) | ❌ |
| plan-qa-proof / -falsification | **read-only** | ✅ | go only | fe only | ✅ | update (verdict) | ❌ none | ❌ |
| builder (go/fe) | read-only | ✅ | go only | fe only | ✅ | update (comment) | ✅ **the 1–2 droplet files only** | ❌ |
| build-qa-proof / -falsification | ❌ **none** | ✅ | go only | fe only | ✅ | update (verdict) | ❌ none | ❌ |
| closeout | ❌ | ✅ | — | — | ✅ | update | ❌ | ❌ (orch commits) |

- **planning + plan-qa get hylla READ-ONLY** (no `hylla.ingest`/`config.refresh`) + context7 + gopls +
  WebSearch + ta create/update, **no source write**. **build-qa gets NO hylla** (just-shipped code isn't
  in the snapshot — relies on `git diff` + gopls/Read). **No role gets git mutation.**
- Enforcement: codex roles → role-conditional **MCP injection** (hylla only for planning/plan-qa; gopls
  for `*-go-*`; playwright for `*-fe-*`; context7 always; `web_search`) + execpolicy (git/command) +
  `read-only` sandbox for non-editing roles. claude roles → persona `tools:` allowlist + the Go gate hook
  (per-file edit for builder; `edit:[]` for qa/closeout) + `bash_deny`.

## 5. Cross-cutting (all four agree)
- **Veracity audit (HARD):** every channel RETURNS the full tool-call trace (claude `--output-format
  json`; codex exec stream; the sand/tillsyn MCP envelope MUST carry it). Every persona emits a
  `## Tools Used` section. The orchestrator audits claims against the stream — self-report ≠ truth.
- **Section 0 / reasoning lives in the PERSONA body**, never the output style (output style is ambient
  and stripped for small models; the persona always loads).
- **Configurable knobs** (sand/tillsyn own, not hardcoded): system-prompt mode (replace|append), the
  context strip-set, the sandbox mode per role, the gate spec.

## 6. Routing (from AGENT_DISPATCH.md, confirmed by the auth finding)
- OAuth/subscription roles (builder=haiku, *-proof=opus, closeout=opus) → **built-in Agent tool + Go gate hook**.
- planning + *-falsification → **hermetic `codex exec`** (§2).
- API-key / ollama tier → `claude -p --bare` (§2) — dormant; ollama needs a ≥~20b tool-capable model
  (7b text-emits tool calls, unusable).

## 7. sand/tillsyn architecture (the build target)
sand (Go MCP) consumes: the chain TOML + the `[codex.hermetic]`/`[codex.mcp]` config + the per-role
gate spec, and **generates project-local artifacts** (no global state): the `sand gate` hook
registration in `settings.json`, the hermetic `CODEX_HOME/rules/default.rules`, the `--sandbox`/`-C`/
`approval_policy`/MCP-injection codex argv, and the claude `-p`/built-in invocation. It returns the full
tool-call trace. Same for tillsyn on the tillsyn substrate. The gate contract (§3) is the stable
interface; bin/sh is NOT part of sand/tillsyn (§1).

## 8. Open (needs a non-macOS box)
- Go exec-form hook firing on **native Windows + Linux**; codex Windows sandbox (exists, `[windows]
  sandbox`, but can fail to *initialize*, codex #24098 → **execpolicy is the floor that holds regardless**).
- The `claude -p` API-key tier cost vs ollama-≥20b vs built-in-subscription routing (product choice).

## 9. Evidence basis
Reproduced ≥2x on macOS by ta / hylla / tillsyn / sand; commands + on-disk artifacts in the superseded
docs (git history). Vendor sources: Claude Code docs (`permissions` `//abs` + deny>allow; `hooks`
exec-form + no-bundled-node/python; `headless` `--bare`-needs-API-key); codex docs (`exec-policy`
`prefix_rule(forbidden)`; `concepts/sandboxing` writable-roots + `<writable_root>/.git` ro;
`config-reference` `approval_policy`/`[windows] sandbox`); codex issues #16732 (hooks), #24098 (win init).
