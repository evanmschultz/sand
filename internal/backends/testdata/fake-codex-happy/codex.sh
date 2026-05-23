#!/bin/sh
# Fake codex CLI used by TestCodexExecBackend_HappyPath and friends in
# the internal/backends package.
#
# Records the invocation argv to $FAKE_CODEX_ARGV_OUT (NUL-separated, one
# arg per record, starting with $0) and stdin to $FAKE_CODEX_STDIN_OUT,
# records the env passthrough into $FAKE_CODEX_ENV_OUT (newline-separated
# KEY=VALUE pairs), then echoes a canned codex line-oriented stream to
# stdout so the spawn looks like a real `codex exec --ephemeral` invocation.
set -eu

if [ -n "${FAKE_CODEX_ARGV_OUT:-}" ]; then
  : > "$FAKE_CODEX_ARGV_OUT"
  printf '%s\0' "$0" >> "$FAKE_CODEX_ARGV_OUT"
  for a in "$@"; do
    printf '%s\0' "$a" >> "$FAKE_CODEX_ARGV_OUT"
  done
fi

if [ -n "${FAKE_CODEX_STDIN_OUT:-}" ]; then
  cat > "$FAKE_CODEX_STDIN_OUT"
else
  cat > /dev/null
fi

if [ -n "${FAKE_CODEX_ENV_OUT:-}" ]; then
  env > "$FAKE_CODEX_ENV_OUT"
fi

# Emit a codex_stream-shaped sample line so the dispatcher's parser
# (drop_005 sibling envelope-routing droplet) can verify it parses
# tools_used aggregates from `mcp: <server>/<tool> (completed)` lines.
cat <<'STREAM'
mcp: ta/get (completed)
mcp: ta/search (completed)
codex: dispatch complete
STREAM
