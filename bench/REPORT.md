# sand token-savings benchmark

Measured 2026-06-15T07:11:11Z via the real `sand mcp --profile` binary (lagom-go inside) with Anthropic `count_tokens` (model `claude-haiku-4-5-20251001`); tool-def tokens = count(message+tools) − count(message-only baseline=8). Slim = sealed allowlist keeping 2 tools. Raw surfaces in `bench/raw/`.

| upstream | tools full→slim | full tok | slim tok | saved |
|---|---|---|---|---|
| fast | 2→2 | 608 | 608 | 0.0% |
| everything | 13→2 | 1812 | 689 | 62.0% |
| filesystem | 14→2 | 2314 | 747 | 67.7% |
| memory | 9→2 | 1534 | 792 | 48.4% |

**Totals:** 6268 → 2836 tool tokens — **54.8% saved** across 4 upstreams.
