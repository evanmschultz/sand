#!/bin/sh
# Fake MCP server — emits a fatal stderr diagnostic then exits non-zero
# BEFORE responding to the JSON-RPC initialize. Used by
# TestProbeMCPServer_StderrCapture to verify A3 — the stderr text must
# surface in the ProbeResult.SkipReason.
set -eu
IFS= read -r _ || true
echo "FATAL: schema migration required" >&2
exit 9
