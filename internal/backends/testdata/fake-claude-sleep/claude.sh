#!/bin/sh
# Fake claude CLI that sleeps long enough to outlive a short test context.
# Used by TestClaudeNativeBackend_ContextCancellation to verify
# exec.CommandContext delivers the cancellation signal and Spawn surfaces
# it as a wrapped ctx.Err().
#
# `exec sleep 30` is load-bearing: without exec, the kernel-delivered SIGKILL
# from exec.CommandContext hits the bash parent only — bash on Linux does not
# forward the signal to its sleep child, so the child runs to completion and
# the test hangs for the full 30s. exec replaces the shell image with sleep,
# so SIGKILL terminates the process directly. macOS happens to terminate the
# group on shell exit, masking the bug locally — Linux CI surfaces it.
set -eu
cat > /dev/null
exec sleep 30
