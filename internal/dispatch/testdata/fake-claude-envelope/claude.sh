#!/bin/sh
# Fake claude CLI used by TestDispatchHappyPath.
#
# Reads $FAKE_CLAUDE_ENVELOPE_FILE (path to a JSON file in testdata/) and
# echoes its contents verbatim to stdout so the wet-run Dispatch path receives
# a realistic claude -p --output-format=json envelope. The argv/stdin recorder
# pattern from fake-claude-happy is preserved so happy-path argv checks remain
# possible.
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

if [ -n "${FAKE_CLAUDE_ENVELOPE_FILE:-}" ]; then
  cat "$FAKE_CLAUDE_ENVELOPE_FILE"
else
  echo '{"result":"missing envelope fixture"}'
fi
