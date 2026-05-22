#!/bin/sh
# Fake claude CLI used by TestDispatchFailoverChain.
#
# Reads $FAKE_CLAUDE_FIXTURE_SEQUENCE (comma-separated list, e.g.
# "rate-limit,success") and tracks invocation count via
# $FAKE_CLAUDE_INVOCATION_FILE (a path the script writes/increments).
#
# For each invocation N (1-indexed), the script picks the Nth fixture name
# from the sequence and emits the matching stderr+stdout+exit-code shape:
#
#   rate-limit  -> stderr "Error: HTTP 429 rate limit exceeded", exit 1
#   auth-fail   -> stderr "Error: 401 unauthorized: invalid api key", exit 1
#   timeout     -> stderr "Error: deadline exceeded", exit 1
#   network     -> stderr "Error: connection refused", exit 1
#   crash       -> empty stderr, exit 137
#   success     -> emit canned Claude JSON envelope to stdout, exit 0
#
# stdin is consumed but discarded.

set -eu

# Always drain stdin so the caller's pipe doesn't deadlock.
cat > /dev/null

if [ -z "${FAKE_CLAUDE_FIXTURE_SEQUENCE:-}" ]; then
  echo "fake-claude-sequence: FAKE_CLAUDE_FIXTURE_SEQUENCE unset" >&2
  exit 2
fi
if [ -z "${FAKE_CLAUDE_INVOCATION_FILE:-}" ]; then
  echo "fake-claude-sequence: FAKE_CLAUDE_INVOCATION_FILE unset" >&2
  exit 2
fi

# Atomically increment the invocation counter. We append a single byte per
# invocation; the number of bytes in the file is the invocation index.
printf '.' >> "$FAKE_CLAUDE_INVOCATION_FILE"
N=$(wc -c < "$FAKE_CLAUDE_INVOCATION_FILE" | tr -d ' ')

# Pick the Nth comma-separated entry. cut -d, -f$N handles it portably.
fixture=$(printf '%s' "$FAKE_CLAUDE_FIXTURE_SEQUENCE" | cut -d, -f"$N")
if [ -z "$fixture" ]; then
  echo "fake-claude-sequence: sequence exhausted at invocation $N (seq=$FAKE_CLAUDE_FIXTURE_SEQUENCE)" >&2
  exit 2
fi

case "$fixture" in
  rate-limit)
    echo "Error: HTTP 429 rate limit exceeded" >&2
    exit 1
    ;;
  auth-fail)
    echo "Error: 401 unauthorized: invalid api key" >&2
    exit 1
    ;;
  timeout)
    echo "Error: deadline exceeded" >&2
    exit 1
    ;;
  network)
    echo "Error: connection refused" >&2
    exit 1
    ;;
  crash)
    # Empty stderr + non-zero exit -> ErrClassCrash per errors_class.go.
    exit 137
    ;;
  success)
    cat <<'JSON'
{"result":"failover succeeded","duration_ms":42,"total_cost_usd":0.001,"usage":{"input_tokens":1,"output_tokens":2,"cache_read_input_tokens":3,"cache_creation_input_tokens":4},"iterations":[{"type":"tool_use","name":"Read"}]}
JSON
    exit 0
    ;;
  *)
    echo "fake-claude-sequence: unknown fixture name $fixture" >&2
    exit 2
    ;;
esac
