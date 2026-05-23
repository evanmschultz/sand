#!/bin/sh
# Fake MCP server — happy path. Implements the three-step JSON-RPC stdio
# handshake (initialize → notifications/initialized → tools/list) and
# returns a tools/list response containing mixed dotted + snake_case
# canonical tool names. Used by TestProbeMCPServer_HappyPath.
#
# Records the invocation argv to $FAKE_MCP_ARGV_OUT (NUL-separated) and
# stdin to $FAKE_MCP_STDIN_OUT for assertions about what the probe sent.
set -eu

if [ -n "${FAKE_MCP_ARGV_OUT:-}" ]; then
  : > "$FAKE_MCP_ARGV_OUT"
  printf '%s\0' "$0" >> "$FAKE_MCP_ARGV_OUT"
  for a in "$@"; do
    printf '%s\0' "$a" >> "$FAKE_MCP_ARGV_OUT"
  done
fi

# Capture stdin to a tee file while still reading line-by-line.
: > "${FAKE_MCP_STDIN_OUT:-/dev/null}"

# Read three lines from stdin: initialize, initialized notification,
# tools/list. After EACH read, emit the appropriate response when one is
# expected (initialize + tools/list; the initialized notification has no
# response).
read_line() {
  IFS= read -r line || return 1
  if [ -n "${FAKE_MCP_STDIN_OUT:-}" ]; then
    printf '%s\n' "$line" >> "$FAKE_MCP_STDIN_OUT"
  fi
  printf '%s' "$line"
}

# Step 1: initialize. Respond with a minimal server-capabilities envelope.
_=$(read_line) || exit 0
printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05","capabilities":{"tools":{}},"serverInfo":{"name":"fake-mcp-happy","version":"0.0.1"}}}'

# Step 2: initialized notification (no response).
_=$(read_line) || exit 0

# Step 3: tools/list. Respond with mixed dotted + snake_case canonical tool names.
_=$(read_line) || exit 0
printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"get"},{"name":"hylla.search.vector"},{"name":"my-tool"},{"name":"Update"}]}}'

# Keep the process alive briefly so the probe's deferred cleanup is what
# kills it, not a premature exit that would race with stdout draining.
sleep 0.05
