#!/bin/sh
# Fake codex CLI that sleeps long enough to outlive a short test context.
# Used by TestCodexExecBackend_ContextCancellation to verify
# exec.CommandContext delivers the cancellation signal and Spawn surfaces
# it as a wrapped ctx.Err().
#
# `exec sleep 30` is load-bearing: see the matching note in
# fake-claude-sleep/claude.sh — without exec the bash parent absorbs SIGKILL
# but leaves sleep alive on Linux, hanging the test for 30s. macOS masks the
# bug; Ubuntu CI surfaces it.
set -eu
cat > /dev/null
exec sleep 30
