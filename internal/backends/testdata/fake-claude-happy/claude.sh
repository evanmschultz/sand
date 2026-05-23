#!/bin/sh
# Fake claude CLI used by TestClaudeNativeBackend_HappyPath and friends in
# the internal/backends package.
#
# Records the invocation argv to $FAKE_CLAUDE_ARGV_OUT (NUL-separated, one
# arg per record, starting with $0) and stdin to $FAKE_CLAUDE_STDIN_OUT,
# records the env passthrough into $FAKE_CLAUDE_ENV_OUT (newline-separated
# KEY=VALUE pairs), then echoes a canned Claude Code JSON envelope to
# stdout so the spawn looks like a real `claude -p --output-format json`
# invocation.
set -eu

if [ -n "${FAKE_CLAUDE_ARGV_OUT:-}" ]; then
  : > "$FAKE_CLAUDE_ARGV_OUT"
  printf '%s\0' "$0" >> "$FAKE_CLAUDE_ARGV_OUT"
  for a in "$@"; do
    printf '%s\0' "$a" >> "$FAKE_CLAUDE_ARGV_OUT"
  done
fi

if [ -n "${FAKE_CLAUDE_STDIN_OUT:-}" ]; then
  cat > "$FAKE_CLAUDE_STDIN_OUT"
else
  cat > /dev/null
fi

if [ -n "${FAKE_CLAUDE_ENV_OUT:-}" ]; then
  env > "$FAKE_CLAUDE_ENV_OUT"
fi

cat <<'JSON'
{"type":"result","result":"droplet complete","duration_ms":1234,"cost_usd":0.0123,"usage":{"input_tokens":10,"output_tokens":20}}
JSON
