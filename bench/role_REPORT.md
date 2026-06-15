# sand per-role token-savings benchmark (real upstreams: ta + hylla)

Measured 2026-06-15T07:38:31Z via the real `sand mcp --profile` binary + Anthropic `count_tokens` (model `claude-haiku-4-5-20251001`, baseline=8). Each row = a cascade role's COMBINED tool-surface cost across the upstreams it loads (ta+hylla), full vs slimmed to that role's actual tools. Tool defs are re-sent on EVERY turn, so this is a per-turn-per-agent saving. Raw surfaces + profiles in `bench/raw/`.

| role | full tok (ta+hylla) | slim tok | saved/turn |
|---|---|---|---|
| planner | 9177 | 6625 | 27.8% |
| builder | 9177 | 3112 | 66.1% |
| qa | 9177 | 3222 | 64.9% |
| closeout | 9177 | 4390 | 52.2% |

**Anchor (full surface, per turn):** ta=4790 tok (9 tools), hylla=4387 tok (16 tools); combined = 9177 tok.

**Why it compounds — tool defs are re-sent on EVERY turn of EVERY agent.** Avg tokens saved/turn across roles = 4839. Illustrative cascade (12 agents × 15 turns) → **~871,020 input tokens saved per cascade** (at $3.00/Mtok input ≈ **$2.61/cascade**, before output/caching effects). Adjust TURNS/AGENTS/PRICE in the script for your own model + fan-out. The undeniable version is an A/B run of one real cascade (full vs slim) comparing billed input tokens from the dispatch trace.
