#!/bin/sh
# Fake claude CLI that emits a stderr diagnostic and exits non-zero.
# Used by TestClaudeNativeBackend_NonZeroExitNotAnError to verify Spawn
# surfaces ExitCode + Stderr without returning a Go error.
set -eu
cat > /dev/null
echo "fake-claude: intentional failure" >&2
exit 7
