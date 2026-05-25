#!/usr/bin/env bash
# .claude/agent-chains.sh — per-role fallback chains.
# Sourced by bin/agent-dispatch.sh at dispatch time.
#
# ROUTING POLICY (hylla/ta/valv bin/sh — THIS system, hard rule):
#   * Anthropic models (haiku/sonnet/opus) → Claude Code's BUILT-IN Agent tool,
#     dispatched orchestrator-DIRECT (NOT this .sh). The claude-native rows below
#     are MODEL HINTS the orchestrator reads; bin/agent-dispatch.sh REFUSES to run
#     them (dispatch_claude_native) — so no `claude -p` ever fires here.
#   * codex models (gpt-5.5) → `codex exec` via bin/agent-dispatch.sh (hermetic).
#   * `claude -p` is NOT used ANYWHERE in this system — no OAuth `-p`, NEVER an
#     ANTHROPIC_API_KEY. The `-p`/ollama path (dispatch_ollama) is dormant here and
#     exists only as a sand/tillsyn USER-CONFIG option (their Go MCP, not this .sh).
#   * On agent-call failure the dispatcher SIGNALS the orchestrator
#     (`CODEX_EXHAUSTED` on stderr) so it re-dispatches via the Agent tool.
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
    ta-go-builder|ta-fe-builder)                                 chain_builder ;;
    ta-go-planning|ta-fe-planning)                               chain_planning ;;
    # Proof QA splits by axis: plan-QA proof → opus, build-QA proof → sonnet.
    ta-go-plan-qa-proof|ta-fe-plan-qa-proof)                     chain_plan_qa_proof ;;
    ta-go-build-qa-proof|ta-fe-build-qa-proof)                   chain_build_qa_proof ;;
    # Falsification QA → codex gpt-5.5. Plan-axis = effort=high (higher stakes);
    # build-axis = effort=low. FE roles get the Playwright MCP injected
    # (dispatch_codex), Go roles get gopls; so the mandatory FE-Playwright gate
    # is satisfied on codex, not only claude-native.
    ta-go-plan-qa-falsification|ta-fe-plan-qa-falsification)     chain_plan_qa_falsification ;;
    ta-go-build-qa-falsification|ta-fe-build-qa-falsification)   chain_build_qa_falsification ;;
    ta-closeout)                                                 chain_closeout ;;
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
# Single tier: codex gpt-5.5 with LOW reasoning effort (decomposition is
# cheaper-stakes than adversarial plan-QA). Sandbox read-only — planners never
# edit source. No claude-native fallback row. On codex failure (rate-limit or
# otherwise), dispatcher exits with CODEX_EXHAUSTED and the orchestrator
# re-dispatches via the native Agent tool with `subagent_type=<role>
# model=sonnet` — sonnet is the *equal-tier* Anthropic substitute for planning,
# NOT an upgrade. Orchestrator may escalate to opus only on REPEATED failures
# (judgment call, not automatic). The dispatcher adds `-c approval_policy=never`
# (codex --sandbox is inert in exec without it), so opts stay clean here.
chain_planning() {
  cat <<'EOF'
codex-exec|gpt-5.5|--sandbox read-only -c model_reasoning_effort=low||
EOF
}

# --- QA Falsification (split by axis) --------------------------------------
# Both axes: codex gpt-5.5, single tier, sandbox READ-ONLY. QA NEVER edits
# source — it reads, verifies, and reports its verdict via the ta MCP (which
# runs OUTSIDE the codex sandbox, so verdict comments still post under
# read-only). codex does NOT honor the persona `tools:` allowlist (it uses
# shell/apply_patch, and its PreToolUse hooks are dead on 0.133.0), so the
# SANDBOX is the ONLY mechanical source-edit gate for a codex role —
# `workspace-write` would let a codex QA write source. read-only = zero writes
# = QA cannot edit. The dispatcher adds `-c approval_policy=never` (codex
# --sandbox is inert in exec without it). On codex failure the dispatcher exits
# CODEX_EXHAUSTED and the orchestrator re-dispatches via the Agent tool
# (model=sonnet, opus on REPEATED plan-axis failures — judgment call).
#
# Plan-axis falsification = effort=HIGH (adversarial plan attack is higher
# stakes); build-axis falsification = effort=LOW.
chain_plan_qa_falsification() {
  cat <<'EOF'
codex-exec|gpt-5.5|--sandbox read-only -c model_reasoning_effort=high||
EOF
}

chain_build_qa_falsification() {
  cat <<'EOF'
codex-exec|gpt-5.5|--sandbox read-only -c model_reasoning_effort=low||
EOF
}

# --- QA Proof (split by axis) ----------------------------------------------
# Plan-axis proof → Agent-tool OPUS (plan reasoning is the quality floor).
# Build-axis proof → Agent-tool SONNET (build-axis proof is lower-stakes; sonnet
# is the deliberate cost-aware floor). No codex fallback row — proof routes via
# agent-tool dispatch (never invokes the bash dispatcher in practice); the row
# is the orchestrator's model hint.
chain_plan_qa_proof() {
  cat <<'EOF'
claude-native|opus||||
EOF
}

chain_build_qa_proof() {
  cat <<'EOF'
claude-native|sonnet||||
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
