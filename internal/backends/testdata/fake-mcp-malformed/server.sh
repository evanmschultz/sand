#!/bin/sh
# Fake MCP server — emits non-JSON garbage as the tools/list response so
# the probe's parser must classify it as a non-fatal SkipReason. Used by
# TestProbeMCPServer_MalformedResponse.
set -eu

IFS= read -r _ || exit 0
printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05","capabilities":{"tools":{}},"serverInfo":{"name":"fake-mcp-malformed","version":"0.0.1"}}}'

IFS= read -r _ || exit 0

IFS= read -r _ || exit 0
printf '%s\n' 'this is not JSON at all'

sleep 0.05
