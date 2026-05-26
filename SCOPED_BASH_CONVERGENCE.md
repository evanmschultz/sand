# Scoped Bash — convergence note (from ta, 2026-05-26)

> **Informational drop-in. This file edits nothing else in your repo** — it explains a decision
> proven in `ta` so your session can adopt it when convenient. Canonical spec: `AGENT_SANDBOX_SPEC.md`
> §4 + `HYLLA_BIN.md` §3 (canonical home is hylla/polyglot-foundation; those edits are AHEAD in ta).

## The decision

Agent personas declare **scoped** Bash — `Bash(git diff *)`, `Bash(git log *)`, `Bash(git status)`,
`Bash(mage ...)` — **never bare `Bash`**. And every persona carries a `## Git Discipline — READ-ONLY
(HARD RULE)` section (the orchestrator is the sole committer).

## Why scoped ≥ bare (per channel — this is the load-bearing reasoning)

- **claude-native (Agent-tool) roles** — builder, qa-proof, closeout: Claude Code's `--allowedTools`
  **filters non-allowlisted tools from the model's VISIBLE toolset** (verified at runtime). Scoped Bash
  means the agent literally cannot see `git commit` / `curl` / `rm` — a real second enforcement layer ON
  TOP of the PreToolUse gate hook. Bare `Bash` leans on the hook alone (which fails-open on error).
- **codex roles** — planning, *-falsification: codex **ignores the persona `tools:` line** (it drives
  shell / `apply_patch` directly). For codex that line is DOCUMENTATION, not enforcement — the gate is
  execpolicy (`prefix_rule(forbidden)`) + `--sandbox read-only` + `network:false`. Scoped is
  honest-and-harmless; bare is no better.
- **Net**: scoped is never weaker than bare, strictly stronger for claude-native → it is the standard.

## The git-discipline persona section (verbatim — drop into every persona)

```
## Git Discipline — READ-ONLY (HARD RULE)

Git is **read-only** for you. You MAY run `git diff`, `git status`, `git log`, `git show` to inspect local state. You **MUST NEVER** run any history- or remote-mutating git command — no `git commit`, `git push`, `git add`/staging, `git rebase`, `git merge`, `git reset`, `git checkout -b`, `git branch`, `git tag`, `git stash`, or `git restore`. **Committing and pushing are ORCHESTRATOR-ONLY.** If your task appears to require a commit/push, STOP and return control to the orchestrator with the reason.
```

closeout variant: same, but it MAY draft the commit message — "You PROPOSE the commit message; the
**ORCHESTRATOR runs the actual commit/push**. Never commit yourself."

## Proven where

ta, 2026-05-26: all 13 personas scoped + git-discipline section, synced project↔HOME (identical); E2E —
codex build-qa-falsification under `--sandbox read-only` blocked the edit AND the commit
(execpolicy `Rejected` at CreateProcess), on-disk verified (probe unchanged, HEAD unchanged). git
mutation is ALREADY blocked mechanically in every channel; scoped Bash hardens the claude-native layer.

## Your action (sand — the Go MCP that replaces bin/sh)

You are a config-driven translator (chain + per-role gate TOML → per-channel invocation + gate/sandbox
artifacts). Bake scoped Bash into the translator + enforcement:

1. **claude-native channel**: emit per-role **scoped `Bash(...)` allowlists** (git-read + the project's
   build-runner targets), NEVER bare `Bash`. The `sand gate` PreToolUse hook stays the second layer.
2. **codex channel**: treat the persona `tools:` line as documentation only — the real gate is your
   emitted execpolicy `rules/default.rules` + `--sandbox` + `sandbox_workspace_write.network_access=false`.
3. **Validate**: a persona/role TOML that requests bare `Bash` for a claude-native role should be
   rejected or normalized to scoped, not passed through. Mirror the `AGENT_SANDBOX_SPEC.md` §2 caveat +
   §4 "Bash is SCOPED, never bare" rule (and reconcile any `SAND-SPEC.md` text that still implies bare).
