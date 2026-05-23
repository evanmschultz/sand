#!/bin/sh
# Fake MCP server — hangs past the probe deadline. Writes its own PID to
# $FAKE_MCP_PID_OUT (when set) so tests can verify A2 (subprocess reaped
# after probe failure). Reads the first JSON-RPC message from stdin then
# sleeps far longer than DefaultProbeTimeout so ProbeMCPServer must trip
# the timeout path.
set -eu

if [ -n "${FAKE_MCP_PID_OUT:-}" ]; then
  printf '%s' "$$" > "$FAKE_MCP_PID_OUT"
fi

IFS= read -r _ || exit 0
# 10s is enough to outlast a ~250ms probe deadline by 40x while staying
# bounded so a buggy defer chain that fails to kill the child fully
# exits naturally within an acceptable test wallclock.
sleep 10
