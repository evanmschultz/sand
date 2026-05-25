# SAND FE-agent guide — Playwright-first frontend agents

> Companion to `SAND_SELF_BUILD_GUIDE.md` + `AGENT_SANDBOX_SPEC.md`. Authored from
> `hylla/polyglot-foundation`, 2026-05-25. **sand itself is a Go-only project — it runs NO FE agents.**
> This guide is the spec for the **FE support sand must BUILD INTO ITS MCP for consuming projects**
> that DO have FE (hylla/poly, ta, tillsyn). sand's Go engine is currently FE-blind (it probes whatever
> `.mcp.json` declares, with no `*-fe-*` branching) — this doc is what that FE support must satisfy. The
> proven reference (E2E 2026-05-25) is the `*-fe-*` branch of
> `/Users/evanschultz/Documents/Code/hylla/hylla/polyglot-foundation/bin/agent-dispatch.sh` — copy/model from there.

## 1. The headline rule

**EVERY FE agent — every role in the `ta-fe-*` family — gets the Playwright MCP.** Planning,
plan-qa-proof, plan-qa-falsification, builder, build-qa-proof, build-qa-falsification: all six. This
is non-negotiable; FE work is not verifiable without driving a real browser against the live backend.

## 2. The FE role set (for CONSUMING projects — sand generates these, doesn't run them)

sand's own agents are Go-only (`.claude/sand-chains.toml` = `ta-go-*` + closeout). A consuming project
with FE (hylla, ta) needs the parallel 6 `ta-fe-*` roles, same backends/models/effort as Go — and
sand's MCP must be able to GENERATE them for that project:

| FE role | backend | model | effort/sandbox | Playwright |
|---|---|---|---|---|
| ta-fe-planning | codex-exec | gpt-5.5 | low, read-only | ✅ inject |
| ta-fe-plan-qa-proof | claude-native | opus | — | ✅ (plugin/MCP) |
| ta-fe-plan-qa-falsification | codex-exec | gpt-5.5 | high, read-only | ✅ inject |
| ta-fe-builder | claude-native | haiku (sonnet fb) | — | ✅ (plugin/MCP) |
| ta-fe-build-qa-proof | claude-native | sonnet | — | ✅ (plugin/MCP) |
| ta-fe-build-qa-falsification | codex-exec | gpt-5.5 | low, read-only | ✅ inject |

FE personas carry **hylla read** (planning + plan-qa only; build-qa gets none), **context7**, **ta**,
**Playwright `browser_*`**, and `Bash` for `pnpm test:e2e`. FE roles do NOT get gopls (that is Go-only).

## 3. How Playwright reaches each channel

- **codex-exec FE roles** — the dispatcher/Go engine injects Playwright as a codex MCP server (the
  reference: `bin/agent-dispatch.sh` `*-fe-*` branch injects
  `mcp_servers.playwright={command="<playwright-mcp>",args=["--headless","--isolated"],tools={browser_*}}`).
  The MCP runs as a codex SUBPROCESS, OUTSIDE the `--sandbox`, so it launches a headless browser +
  reaches the live backend regardless of read-only/workspace-write. `--isolated` keeps each dispatch's
  browser profile ephemeral so parallel FE dispatches don't contend.
- **claude-native FE roles** (built-in Agent tool) — the Playwright plugin/MCP is available to the
  subagent; the persona `tools:` allowlist must include the `browser_*` set.

## 4. The live-backend URL (LOAD-BEARING — orchestrator must pass it)

FE personas are GENERIC — they never hardcode a port; they drive **the URL the orchestrator provides**.
For hylla's Wails app the canonical target is **`http://localhost:34917`** (the Wails AssetServer via
`mage uiDev`, with `window.go.main.App.*` IPC bindings against the live Go backend). **NEVER**
`http://localhost:4348` (the bare Astro standalone dev server — binding-less, silently produces a
false-PASS empty state). For a `web/`-style viewer, pass that binary's URL instead. Each consuming
project declares its own endpoint; sand passes whatever the chain/orchestrator supplies.

## 5. FE QA discipline

- FE QA (plan + build, proof + falsification) MUST run `pnpm test:e2e` (or the Playwright MCP browser
  tools) on every gate touching FE artifacts. A verdict cannot be `pass` without a Playwright run
  reported in the QA record.
- SolidJS `createResource` swallows thrown errors silently — FE QA MUST check `[role="alert"],
  [data-tone="error"]` element counts beyond `console.error`.
- Rendering-engine caveat: Playwright bundled Chromium ≠ macOS WKWebView in production — layered
  defense is manual macOS visual-smoke post-build (no automated WKWebView driver exists for macOS).

## 6. What sand's Go engine must add for FE

- Role-name branching in MCP injection: `*-fe-*` → Playwright server; `*-go-*` → gopls; context7 + ta
  always; hylla read-only for planning/plan-qa (skip build-qa). Today `renderMCPInjectionFlags` is
  generic — add the conditional matrix (`SAND_SELF_BUILD_GUIDE.md` §2.7, §4).
- A config knob for the live-backend URL per project (passed into Playwright-driving FE dispatches).
- The ability to GENERATE the `ta-fe-*` persona set (6) with the §2 tool grants for any consuming
  project that declares FE — sand does NOT carry these for itself (it is Go-only).
