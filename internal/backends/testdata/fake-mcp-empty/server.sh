#!/bin/sh
# Fake MCP server — completes the handshake but reports an empty tools
# list. Used by TestProbeMCPServer_EmptyToolsList to verify the renderer
# emits `tools={}` and the result is round-trippable.
set -eu

# Step 1: initialize.
IFS= read -r _ || exit 0
printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05","capabilities":{"tools":{}},"serverInfo":{"name":"fake-mcp-empty","version":"0.0.1"}}}'

# Step 2: initialized notification.
IFS= read -r _ || exit 0

# Step 3: tools/list — empty tools array.
IFS= read -r _ || exit 0
printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{"tools":[]}}'

sleep 0.05
