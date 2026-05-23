#!/bin/sh
# Fake claude CLI that sleeps long enough to outlive a short test context.
# Used by TestClaudeNativeBackend_ContextCancellation to verify
# exec.CommandContext delivers the cancellation signal and Spawn surfaces
# it as a wrapped ctx.Err().
set -eu
cat > /dev/null
sleep 30
