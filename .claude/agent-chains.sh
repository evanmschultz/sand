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
# Lock policy: codex-exec and claude-native dispatch directly — the external
# API returns 429/401 on overload or auth, and any non-zero exit advances
# the dispatcher to the next tier.
#
# Chain principle: cheap-first → escalate ON FAILURE. Tier 1 = cheapest tier
# that clears the role's quality floor for its TYPICAL work. Higher tiers
# are backend redundancy when tier 1 is unavailable, NOT graceful degradation.

emit_chain_for_role() {
  case "$1" in
    ta-go-builder|ta-fe-builder)                     chain_builder ;;
    ta-go-planning|ta-fe-planning)                   chain_planning ;;
    ta-go-qa-falsification|ta-fe-qa-falsification)   chain_qa_falsification ;;
    ta-go-qa-proof|ta-fe-qa-proof)                   chain_qa_proof ;;
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
# Cheap-first: codex 5.5 low primary (cheapest sufficient for decomposition);
# codex 5.5 medium tier-2 if low fails; claude sonnet + opus as cross-backend
# redundancy.
chain_planning() {
  cat <<'EOF'
codex-exec|gpt-5.5|--sandbox read-only -c model_reasoning_effort=low||
codex-exec|gpt-5.5|--sandbox read-only -c model_reasoning_effort=medium||
claude-native|sonnet||||
claude-native|opus||||
EOF
}

# --- QA Falsification ------------------------------------------------------
# Adversarial reasoning. Cheap-first: medium handles build-QA-falsif (the
# typical case where the plan was already vetted in plan-QA); high tier-2
# handles plan-QA-falsif via escalation when medium is insufficient.
# Claude opus is cross-backend redundancy.
#
# Sandbox: workspace-write so the QA agent can run `mage check` / `mage testPkg`
# (which writes to go build cache + pnpm node_modules + /tmp). The persona's
# tools: line is the actual write-discipline (no Edit/Write/Bash(go *)/etc.);
# codex's sandbox is defense-in-depth, not the primary gate.
chain_qa_falsification() {
  cat <<'EOF'
codex-exec|gpt-5.5|--sandbox workspace-write -c model_reasoning_effort=medium||
codex-exec|gpt-5.5|--sandbox workspace-write -c model_reasoning_effort=high||
claude-native|opus||||
EOF
}

# --- QA Proof --------------------------------------------------------------
# Evidence verification. Quality floor IS opus — no graceful-degradation
# tier to sonnet (if opus fails, fall to codex; sonnet would underqualify
# for proof reasoning). Sandbox: workspace-write so the QA agent can run
# `mage check`. Persona tools: line gates source-write discipline.
chain_qa_proof() {
  cat <<'EOF'
claude-native|opus||||
codex-exec|gpt-5.5|--sandbox workspace-write -c model_reasoning_effort=medium||
EOF
}

# --- Closeout -------------------------------------------------------------
# Final coordinator before commit. Quality floor IS opus. Sandbox:
# workspace-write so closeout can run `mage check`. Persona tools: line
# gates source-write discipline (no Edit/Write tools).
chain_closeout() {
  cat <<'EOF'
claude-native|opus||||
codex-exec|gpt-5.5|--sandbox workspace-write -c model_reasoning_effort=medium||
EOF
}
