#!/bin/sh
# Fake codex CLI that emits a stderr diagnostic and exits non-zero.
# Used by TestCodexExecBackend_NonZeroExitNotAnError to verify Spawn
# surfaces ExitCode + Stderr without returning a Go error.
set -eu
cat > /dev/null
echo "fake-codex: intentional failure" >&2
exit 9
