# LAGOM CONSUME — SAND HANDOFF

Drives sand actually consuming lagom (Go dep) to slim each agent's MCP, then FULLY e2e battle-testing it — in this project AND as a vehicle for others. lagom is now READY as a Go lib. This is drop_016 (lagom consumption), unblocked.

Source of this handoff: the dev's "PROMPT FOR SAND ORCH — consume + e2e battle-test lagom" (embedded §3/§5/§6 below) + sand's current state + the lagom/main docs I read (POC_FINDINGS.md especially).

---

## 0. MANDATORY FIRST READS (followup agent: read ALL before touching code)

lagom repo (`../lagom/main/`):
- `docs/HANDOFF.md` — read first.
- `docs/SAND_ORCH_PROMPT.md` — design brief (sand-facing).
- `docs/POC_FINDINGS.md` — REAL-agent findings; the codex-vs-claude vehicle truth (§2 here summarizes, but read the source).
- `docs/SAND_LAGOM_HANDOFF.md` — the §8 contract (sand↔lagom seam).
- `go/examples/branded.go` + `branded_test.go` — the EXACT mcp-go pattern `sand mcp` copies (proven e2e).
- `bin/lagom-codex-poc.sh` — the proven headless e2e harness (codex vehicle).
- `bench/` — token-savings measurement pattern.

sand memories (recall): `handoff_session_2026-06-14_lagom_consume` (newest), `project_sand_consumes_lagom`, `project_sand_per_persona_scoped_dispatch`, `project_sand_dynamic_agent_minting`, `project_sand_enforced_streaming`, `project_sand_v02_harness_research`, `feedback_cascade_builder_discipline`, `feedback_sand_redeploy_after_dispatch_changes`, `feedback_always_verify_tool_calls`, `project_sand_gate_edit_scope_and_audit_gaps`. (§9 below = the losslessness ledger: resolved decisions, open questions, deferred, docs TODO, W-status.)

---

## 1. SAND STATE (2026-06-14)

DONE: v0.1 (4 MCP tools) + drop_014 (codex hermetic CODEX_HOME, gate threading, role-conditional MCP, audit capture) + drop_015 W5 clean-room tmp HOME (`61eda01`) + W4 trace/enforcement (`982ae49`, `ebbd2a4`, `ed69f38`):
- claude `--output-format stream-json --verbose` + codex `--json` trace, CODE-ENFORCED (BackendConfig.StructuredOutput knob), num_turns + permission_denials + is_error-correlated tool_calls — PROVEN LIVE both backends + research-confirmed + regression-pinned with real fixtures. **The trace is trustworthy — use it to verify every dispatch.**
- sand REJECTS builder-role dispatches ("use the builtin Agent tool, not sand").
Working tree clean at `ed69f38`. `mage ci` green.

LEFT (drop_015 sandbox, sand's OWN side): W1 `--settings` (DECOMPOSED: w1_spike_claude_settings_bare → w1_spawnrequest_persona_settings_path → w1_renderargs_settings_flag → w1_settings_authority_allowedtools + QA pair), W2 visibility (`--strict-mcp-config` + pruned `--mcp-config`), W6 per-agent hooks/CLAUDE.md/context-budget, W3 codex+ollama parity, W7 `sand mcp --profile` slim-server (5 sub-segments planned). + the e2e proof.

NOW: lagom is ready → build W7 DIRECTLY on lagom (skip the stub projector that the original W7 plan envisioned — lagom drops in NOW). drop_016 = this handoff.

---

## 2. DESIGN-CHANGING POC FINDINGS (`../lagom/main/docs/POC_FINDINGS.md`) — HONOR THESE

1. **E2E VEHICLE = `codex exec` (sync MCP).** A one-shot `claude -p` fires the model request (~112-254ms) BEFORE its async-loaded MCP connects (~250ms) → the slim tools miss that turn. So headless one-shot `claude -p` is the WRONG vehicle for confined MCP. Confined **claude** agents must run where MCP is already up: the **built-in Agent tool (in-session)** or a warmed/persistent session. Confined **headless** = codex. (This VALIDATES sand's design.)
2. **`claude -p --settings deny` is NOT enforced in `-p`** (bin-sh relied on a PreToolUse hook). So W1's settings-deny only confines the in-session/built-in path, not raw headless `-p`.
3. **Built-in tools PERSIST in context; deny blocks EXECUTION, not PRESENCE.** `ToolSearch`/`Agent`/`Workflow` are escape vectors → **DENY ALL built-ins** (Bash/Read/Write/Edit/Grep/Glob/Task/Agent/Workflow/ToolSearch/…) for a confined agent.
4. **Clean tmp HOME needs `ANTHROPIC_API_KEY`** (OAuth lives in the macOS Keychain, not a file; symlinking `.claude.json` did NOT help). W5 clean-room + the existing `requireAPIKey` already align.
5. **`claude -p` LEAKS its MCP children.** After exit, the spawned MCP + upstream stay ALIVE. **The dispatcher MUST reap the process tree itself** (`pkill -f "$WORKDIR"`). NEW sand requirement — do not trust the harness to reap.
6. **GREEN proven via `bin/lagom-codex-poc.sh`:** a real headless codex agent confined to a lagom-slimmed MCP — `secret` dropped, `echo.token` pinned — ALL proofs green (slim-only tool list, dropped→"not available", pin injected downstream, ephemeral per-agent, clean teardown).

---

## 3. LAGOM CONSUMPTION — EXACT STEPS (dev's prompt)

```
gh auth setup-git                              # one-time git auth (private repo)
export GOPRIVATE='github.com/hylla-io/*'
go get github.com/hylla-io/lagom/go@main       # or pin @<commit>; no release tag on purpose
# pure Go, no cgo (CGO_ENABLED=0 ok); only transitive dep = wazero
```
NOTE: sand agents must NEVER run raw `go get`/`go mod` — the ORCHESTRATOR adds the dep (it's a git/mod mutation), then `mage install`. Read the API via local `go doc github.com/hylla-io/lagom/go [Symbol]` (pkg.go.dev is private). Every exported symbol is doc-commented.

API:
```go
import lagom "github.com/hylla-io/lagom/go"
slim, err := lagom.Project(ctx, upstreamDefsJSON, policyJSON)   // narrow tools/list
up,   err := lagom.Rewrite(ctx, callJSON, policyJSON)           // inject pins / reject
pol,  err := lagom.Merge(ctx, baseJSON, overlayJSON)            // narrow-only compose
err  = lagom.Validate(ctx, policyJSON, upstreamDefsJSON)        // drift -> error
g, err := lagom.NewGuard(ctx, upstreamDefsJSON, policyJSON)     // the helper `sand mcp` uses
toolsList := g.SlimDefs()                                       // register as mcp-go tools/list
upCall, err := g.Gate(ctx, callJSON)                            // gate each tools/call (err = rejected)
pol, _ := lagom.SealedPolicyBuilder().Keep("search").Rename("search","find").
            Pin("search","artifact","hylla").ConstrainEnum("search","query",[]any{"a","b"}).Build()
rec, _ := lagom.Mint(ctx, "agent-7", baseJSON, dynamicJSON, upstreamJSON)  // ephemeral
resolved, _ := lagom.Refire(ctx, rec)
```
**#1 GOTCHA (bites every consumer): lagom `ToolDef` uses `input_schema` (snake); MCP emits `inputSchema` (camel). Map per tool — `{"name","description","input_schema": <the inputSchema>}` — BEFORE Project/NewGuard. Copy the one-liner `lagomDef` from `go/examples/branded.go`.**

---

## 4. W7 = `sand mcp --profile <ephemeral.json>` (build ON lagom)

An mcp-go server whose `tools/list` = `g.SlimDefs()` and whose every handler runs `g.Gate(ctx, call)` FIRST (reject on err) before calling the real upstream. Copy `go/examples/branded.go` + `branded_test.go`. BRAND it: sand picks the server name + tool names (rename) + short docs; lagom never appears (only in `go.mod`). Map `inputSchema`→`input_schema` per tool (§3 gotcha). Thread the two-field policy (EXERCISE + GRANT/BUDGET) + `lagom.Mint`/`Refire` for the dynamic-mint MCP tool per `project_sand_dynamic_agent_minting` (narrow-only via `Merge`).

Per-dispatch flow (from `SAND_ORCH_PROMPT.md` §4 + POC): compute policy for role → mint ephemeral `/tmp/agent-ID/profile.json` → write agent `.mcp.json` pointing at `sand mcp --profile <that>` → spawn the confined agent (codex headless / built-in Agent in-session) → **reap the process tree on exit** (§2.5).

---

## 5. E2E BATTLE TEST — vehicle = codex (dev's §5 + POC GREEN)

```
codex exec --ephemeral --ignore-user-config -C <cwd> \
  -c approval_policy="never" -c skills.bundled.enabled=false -c project_doc_max_bytes=0 \
  -c 'mcp_servers.guarded={command="sand",args=["mcp","--profile","/tmp/agent-ID/profile.json"],startup_timeout_sec=25,tools={<kept>={approval_mode="approve"}}}'
```
Deny ALL escape tools; clean env; dispatcher kills the process tree on exit (`pkill -f <workdir>`). Reference harness: `../lagom/main/bin/lagom-codex-poc.sh`.

ASSERTIONS (all must pass): agent's tool list = ONLY the slim set (dropped tools ABSENT) · a dropped tool → "not available" · a pinned arg is injected downstream (upstream sees the pinned value the agent never set) · after exit, NO `sand mcp`/upstream procs remain.

---

## 6. TOKEN NUMBERS (required)

Measure real savings with Anthropic `count_tokens`: full tools/list vs slim tools/list per a REAL upstream (e.g. ta, hylla). Log jsonl + csv + a report — copy `../lagom/main/bench/`. lagom's first result was ~52% avg tool-surface token cut; produce sand's OWN numbers.

---

## 7. DISCIPLINE (carry from sand memories — non-negotiable)

- BUILDERS = builtin Agent tool (atomic 1-droplet, PARALLEL file-disjoint waves, small model). PLANNERS + qa-falsification = sand (codex-exec). qa-proof/closeout = sand (claude-native). Sand REJECTS builder dispatches.
- VERIFY EVERY dispatch ground-truth: the trace now works (use it) AND independently (`git diff`, `mage testPkg`, `mcp__ta__get`, Read). PostToolUse audit shows builtin-agent tool calls (out_of_scope/forbidden) — check it.
- mage ONLY (no raw `go test/build/vet`, no `gofmt`/`gofumpt`). ORCHESTRATOR-ONLY git. `mage ci` green before commit; watch cross-package consumers (`renderArgs` changes broke a dispatch golden this session).
- After ANY dispatch-surface change: ORCHESTRATOR runs `mage install` + the dev RESTARTS the MCP server before trusting live behavior (stale binary masquerades as defects). The `gate` is a STRUCTURED arg.
- The orchestrator (not agents) adds the lagom dep (`go get`) — it's a git/mod mutation.
- Do NOT run two builders in parallel that both `mage format`/git the shared tree without worktree isolation (a near-miss this session).

---

## 8. SEQUENCING (recommended)

1. ORCH: `go get` lagom (GOPRIVATE + gh auth) → `mage install` → confirm it compiles (CGO_ENABLED=0). Read `go doc` + `branded.go`.
2. Build **W7 `sand mcp --profile` on lagom** (the slim server, branded, inputSchema-mapped) — the long pole. Sub-plan its segments (profile schema w/ exercise+grant · projector=lagom Guard · mcp-server · cmd wiring · e2e).
3. Build the **confinement bits** the e2e needs: deny-ALL-built-ins in the dispatch (§2.3), `--strict-mcp-config` (W2), and the **process-tree reaper** (§2.5).
4. **E2E battle test via codex** (§5) until all assertions green; capture the real codex run.
5. **Token numbers** (§6).
6. Round out W1 (settings, in-session/built-in path per §2.2) / W6 (hooks) / W3 (parity) for completeness.
7. Then the cross-project story: `.sand/config.toml` source-resolution + the dynamic-mint MCP tool + adoption ergonomics, so other repos consume the same confined-dispatch.

NO versioning / GH-release until the dev says everything is done AND the full e2e + token numbers are proven.

---

## 9. LOSSLESSNESS — RESOLVED DECISIONS · OPEN QUESTIONS · DEFERRED · DOCS · W-STATUS

**RESOLVED (do NOT re-litigate):**
- Capability delegation = TWO policy fields — **EXERCISE** (tools the agent itself invokes) + **GRANT/BUDGET** (what it may delegate when minting). Mint narrows the BUDGET only via lagom `Merge` (never widen). Default budget = exercise; **planner = budget ⊋ exercise** (read-only itself, grants write to X), capped by its own minter. **GRANT/BUDGET is a FIRST-CLASS separate field** (decided 2026-06-14). See `project_sand_dynamic_agent_minting`.
- `.sand/config.toml` agent-def-dir precedence = **orch-arg > repo `.sand/config.toml` > global `~/.config/sand` > default `.claude/`** (REPO-over-global, per-key merge, top-wins). The configurable dir is **OPT-IN**; DEFAULT = standard `.claude/`. **Standard-location agents get the FULL mcp; getting the SLIMMED mcp REQUIRES opt-in dispatch through sand from a tmp profile dir.** See `project_sand_v02_harness_research` + `project_sand_dynamic_agent_minting`.
- Structured-streaming flag is **sand-ENFORCED in code** (knob `auto|stream-json|json|off`, default `auto`). SHIPPED `ebbd2a4`. See `project_sand_enforced_streaming`.
- **Configurability + openness FIRST.** The narrow-only-budget gate ships as a CONFIGURABLE policy with a SAFE DEFAULT (budget = exercise); README states we considered stricter security but chose openness, inviting community input on balancing.
- Backend role-split: builders = builtin Agent tool; planner + qa-falsification = sand (codex); qa-proof/closeout = sand (claude-native); sand REJECTS builder dispatch.

**OPEN QUESTIONS / NOT-YET-TESTED (carry forward — from POC_FINDINGS §"NOT yet tested" + this session):**
- codex with **OAuth** auth tier — confirm it confines the same as the API-key tier the POC used.
- **claude -p ollama tier** (local, free) end-to-end with the slim MCP.
- Folding the working **codex vehicle into sand's actual dispatch** (vs the standalone `bin/lagom-codex-poc.sh`).
- **LIVE-WATCH** ("look in while it's running"): streaming fills the `.out` live (tailable) — but full **live-stream-of-events-to-the-orchestrator** (so dev/orch can watch a run NOT go off the rails in real time) is a DESIRED enhancement, not built.
- Confined **claude-native** path: POC says always route via the built-in Agent tool (in-session, MCP already up) or a warmed/persistent session — confirm at scale; decide if sand offers a warmed-session mode.
- Cross-project **adoption ergonomics**: sensible-defaults / copy-into-project / store agent defs in global sand config; install `sand gate` per project.
- Edit-scope gap ([[project_sand_gate_edit_scope_and_audit_gaps]]): bare Edit/Write in persona frontmatter defeats per-file confinement until W1 `--settings` (+ deny-all-builtins) lands.

**DEFERRED (beyond drop_016 MVP):**
- **v0.2 multi-harness** — add order **opencode → omp → continue → cline**, then cursor; defer pi/zed/aider/windsurf. Axis-2 (MCP VISIBILITY scoping) is the discriminator; 5/9 share sand's md+frontmatter def-format. See `project_sand_v02_harness_research`.
- lagom per-tool richness at scale (pin/constrain/enum/rename across real upstreams beyond the e2e proof).
- Versioning + GH-repo polish.

**DOCS TODO (dev: "ALL needs to be clear in the docs"):**
- SAND-SPEC: the enforced-streaming knob + the consequence of `off`; the box+slim+config design; which flags are sand-enforced givens + how to override; the **codex-is-the-confined-headless-vehicle** finding; the **deny-ALL-built-ins** + **process-tree-reaper** requirements; the `.sand/config.toml` shape + precedence; the **exercise/grant** dynamic-mint model; that standard-location = full mcp, slimming = opt-in.

**CLEANUPS (small):** lint modernizations (`interface{}`→`any`, WriteString, forvar) in test files; the synthetic codex fixture (a real one now sits alongside it).

**FULL drop_015 W-STATUS:** W5 DONE (`61eda01`) · W4 DONE (`982ae49`/`ebbd2a4`/`ed69f38`) · **W1 DECOMPOSED** (4 droplets `w1_spike_claude_settings_bare`→`w1_spawnrequest_persona_settings_path`→`w1_renderargs_settings_flag`→`w1_settings_authority_allowedtools` + QA pair; NOT built) · **W2/W6/W3 NOT decomposed** · **W7** 5 sub-segments planned (`w7_profile_schema`/`w7_projector_seam`/`w7_mcp_server_core`/`w7_cmd_wiring`/`w7_slim_e2e_docs`) — build on lagom now. drop_015 plan-tree records live in `.ta/cascade/drops/drop_015/`.
