#!/bin/sh
# Fake claude CLI that emits a stderr diagnostic and exits non-zero.
# Used by TestRunClaudeNative_NonZeroExitNotAnError to verify the spawn
# surfaces ExitCode + Stderr without returning a Go error (the chain
# advance is the caller's decision, not runClaudeNative's).
set -eu
cat > /dev/null
echo "fake-claude: intentional failure" >&2
exit 7
