#!/bin/sh
# Fake MCP server — handshakes cleanly but returns a JSON-RPC error
# object in place of tools/list result. Used by
# TestProbeMCPServer_ToolsListError to verify the parser surfaces the
# error code + message in the SkipReason.
set -eu

IFS= read -r _ || exit 0
printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05","capabilities":{"tools":{}},"serverInfo":{"name":"fake-mcp-error","version":"0.0.1"}}}'

IFS= read -r _ || exit 0

IFS= read -r _ || exit 0
printf '%s\n' '{"jsonrpc":"2.0","id":2,"error":{"code":-32601,"message":"method not found"}}'

sleep 0.05
