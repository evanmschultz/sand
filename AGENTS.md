# AGENTS.md — project guidance for agent runners

Project-local guidance for agent runners (Codex, etc.) working inside the `sand` tree. Mirrors [`CLAUDE.md`](CLAUDE.md) — the two files MUST stay in lockstep.

## Sand-the-server vs. sand-the-project — load-bearing invariant

- **sand-the-server**: the running `sand` binary, pinned to a caller project via `--project <abs-path>`. Reads persona definitions from `<caller-project>/.claude/agents/<role>.md` at dispatch time. NO baked-in persona registry. NO defaults from `~/.ta/agents/` or `~/.claude/agents/` — the caller project's tree is the only source of truth.
- **sand-the-project**: this repo. Has its own `.claude/agents/` for cascades that target sand's OWN code (5 Go personas + closeout). NOT shipped with the binary.

## Bootstrap reality

Sand's binary now exists and dispatches go through `mcp__sand__dispatch` from sand's own `.mcp.json`. Historical bootstrap (when sand had no binary) shelled out to ta's bash dispatcher; sand's chains are now ported to TOML and live in [`.claude/sand-chains.toml`](.claude/sand-chains.toml) (project rung) plus the seeded home-rung defaults.

## Subagent spawn defaults — background-first

Spawn agents with background-mode by default (Codex's equivalent of Claude Code's `run_in_background: true`). Foreground mode lets agents bypass their declared `tools:` allowlist; background mode enforces it.

## Mage-only Bash discipline

Agents NEVER run raw language tooling. No `go test` / `go vet` / `go build` / `gofmt` / `gofumpt`. All test / build / check commands route through mage.

If a capability is missing, add a mage target — do NOT broaden the persona's Bash scope.

## Cascade isolation — agents test ONLY their slice

- Below-package: `mage testFunc <pattern>` (or `mage testFunc 'TestA|TestB|TestC'` for several, pipe-joined regex), with optional `TA_TEST_PKG=./pkg/path` scoping.
- Package-level: `mage testPkg ./pkg/path`.
- Orchestrator-level (commit gate): `mage check`.

Never invoke `go test` / `go vet` / `gofmt` / `gofumpt` directly.

## Cascade methodology — canonical reference

The cascade methodology that sand's build cascade follows lives at [`../ta/main/docs/cascade-methodology.md`](../../ta/main/docs/cascade-methodology.md). When orchestrating sand's build cascade — read that first, then [`SAND-SPEC.md`](SAND-SPEC.md) for sand-specific slicing.

## ta CLI usage

- All `ta <read-command>` invocations from agents MUST pass `--json`. ANSI laslig output is for humans.
- Read commands that accept `--json`: `ta get`, `ta list-sections`, `ta schema`, `ta search`.
- Mutating commands (`create` / `update` / `delete` / `schema --action=create|update|delete`) return a concise laslig success notice. Use `--verbose` to echo the post-mutation record.
- Bare `ta` without a TTY is the MCP server — no explicit subcommand needed when registering in `.mcp.json` / `.codex/config.toml`.
