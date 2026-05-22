#!/bin/sh
# Fake claude CLI that sleeps long enough to outlive a short test context.
# Used by TestRunClaudeNative_ContextCancellation to verify exec.CommandContext
# delivers the cancellation signal and runClaudeNative surfaces it.
set -eu
cat > /dev/null
sleep 30
