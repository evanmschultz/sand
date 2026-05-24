#!/usr/bin/env bash
# .claude/agent-chains.sh — per-role fallback chains.
# Sourced by bin/agent-dispatch.sh at dispatch time.
#
# Each role maps to a function that emits a pipe-delimited tier table on stdout:
#
#   backend | model | opts | wait_max | slots
#
# Fields:
#   backend   codex-exec | claude-native
#             (ollama-local removed 2026-05-21 — see chain_builder rationale)
#   model     model tag (gpt-5.5, opus, sonnet, haiku)
#   opts      backend-specific flags (codex: --sandbox / -c effort=...) or empty
#   wait_max  unused now that ollama is removed
#   slots     unused now that ollama is removed
#
# Fallback policy (2026-05-21 hardening): claude-native rows are present
# ONLY for agent-tool roles (chain_builder / chain_qa_proof / chain_closeout)
# where the orchestrator reads them as model hints and dispatches via
# Claude Code's native Agent tool (subscription-billed, no claude -p
# subprocess). For bash-dispatched roles (chain_planning + chain_qa_falsif)
# there is NO claude-native fallback row in the chain — codex exhaustion
# exits the dispatcher with a non-zero code + CODEX_EXHAUSTED marker on
# stderr telling the orchestrator to re-dispatch via the Agent tool.
# This guarantees no `claude -p` subprocess (which could pick up
# ANTHROPIC_API_KEY billing in mis-configured envs) ever fires through
# the bash dispatcher's automatic fallback path.
#
# Lock policy: codex-exec dispatches directly — the external API returns
# 429/401 on overload or auth, and any non-zero exit advances the
# dispatcher to the next tier (or exits with CODEX_EXHAUSTED for
# bash-dispatched roles).
#
# Chain principle: cheap-first → escalate ON FAILURE. Tier 1 = cheapest tier
# that clears the role's quality floor for its TYPICAL work. Higher tiers
# are backend redundancy when tier 1 is unavailable, NOT graceful degradation.

emit_chain_for_role() {
  case "$1" in
    ta-go-builder|ta-fe-builder)                     chain_builder ;;
    ta-go-planning|ta-fe-planning)                   chain_planning ;;
    # Proof QA (plan + build, fe + go) → claude-native opus via Agent tool.
    ta-go-plan-qa-proof|ta-fe-plan-qa-proof|ta-go-build-qa-proof|ta-fe-build-qa-proof) \
                                                     chain_qa_proof ;;
    # Falsification QA (plan + build, fe + go) → codex. FE roles get the
    # Playwright MCP injected (dispatch_codex), Go roles get gopls; so the
    # mandatory FE-Playwright gate is satisfied on codex, not only claude-native.
    ta-go-plan-qa-falsification|ta-fe-plan-qa-falsification|ta-go-build-qa-falsification|ta-fe-build-qa-falsification) \
                                                     chain_qa_falsification ;;
    ta-closeout)                                     chain_closeout ;;
    *)  echo "" ;;
  esac
}

# --- Builder ---------------------------------------------------------------
# Atomic 1-2 small blocks per cascade methodology. Claude Haiku is fast +
# cheap (3x cheaper than Sonnet per Anthropic API pricing). Local Ollama
# (qwen3-coder:30b) was dropped 2026-05-21 — running many 30B agents in
# parallel pressured VRAM/thermal budget and slowed iteration loops.
# Cheap-first: haiku primary, sonnet fallback if haiku fails.
chain_builder() {
  cat <<'EOF'
claude-native|haiku||||
claude-native|sonnet||||
EOF
}

# --- Planning --------------------------------------------------------------
# Single tier: codex 5.4 with high reasoning. No claude-native fallback row.
# On codex failure (rate-limit or otherwise), dispatcher exits with
# CODEX_EXHAUSTED and the orchestrator re-dispatches via the native Agent
# tool with `subagent_type=<role> model=sonnet` — sonnet is the *equal-tier*
# Anthropic substitute for planning, NOT an upgrade. Orchestrator may
# escalate to opus only on REPEATED failures (judgment call, not automatic).
chain_planning() {
  cat <<'EOF'
codex-exec|gpt-5.4|--sandbox read-only -c model_reasoning_effort=high||
EOF
}

# --- QA Falsification ------------------------------------------------------
# Single tier: codex 5.4 high — adversarial reasoning is high-stakes
# enough that we don't want to bother with a cheaper try-first. No
# claude-native fallback row. On codex failure, dispatcher exits with
# CODEX_EXHAUSTED and the orchestrator re-dispatches via the native Agent
# tool with `model=sonnet` — sonnet is the equal-tier Anthropic substitute
# for build-QA-falsif (the typical case). For plan-QA-falsif slices (rarer,
# higher stakes), the orchestrator may escalate to opus on REPEATED
# sonnet failures — judgment call, not automatic.
#
# Sandbox: workspace-write so the QA agent can run `mage check` / `mage
# testPkg` (which writes to go build cache + pnpm node_modules + /tmp).
# The persona's tools: line is the actual write-discipline; codex's
# sandbox is defense-in-depth, not the primary gate.
chain_qa_falsification() {
  cat <<'EOF'
codex-exec|gpt-5.4|--sandbox workspace-write -c model_reasoning_effort=high||
EOF
}

# --- QA Proof --------------------------------------------------------------
# Single tier: Agent-tool opus. Quality floor IS opus — sonnet would
# underqualify for proof reasoning. No codex fallback row in this chain
# because qa_proof routes via agent-tool dispatch (never invokes the
# bash dispatcher in practice); the row is the orchestrator's model hint.
chain_qa_proof() {
  cat <<'EOF'
claude-native|opus||||
EOF
}

# --- Closeout -------------------------------------------------------------
# Single tier: Agent-tool opus. Final coordinator before commit; quality
# floor IS opus. Routes via agent-tool dispatch; the row is the
# orchestrator's model hint.
chain_closeout() {
  cat <<'EOF'
claude-native|opus||||
EOF
}
