// Package dispatch — error classification for chain-tier fallback.
//
// This file owns the ErrClass enum + ClassifyExitError function that the
// dispatch loop consults after each tier spawn to decide whether to advance
// to the next tier (RateLimit / AuthFailure / Network / Timeout) or to halt
// (Crash / Unknown — though policy lives in the dispatch loop, not here).
//
// Per SAND-SPEC §3, classification is a pure function of the spawned
// subprocess's stderr text + exit code — NEVER the agent's narrative text
// in the JSON envelope's `result` field. The orchestrator memory note
// feedback_always_verify_tool_calls is explicit: trust parsed event streams
// + process exit signals, not self-reported text.
//
// Class precedence when multiple stderr patterns could match:
//
//  1. Success (exitCode == 0 short-circuits — no stderr scan)
//  2. Timeout      — exitCode 124 deterministic; also pattern on "deadline
//     exceeded"|"context canceled"|"timeout"
//  3. Crash        — exitCode 137/139/143 deterministic (SIGKILL / SIGSEGV /
//     SIGTERM); ALSO any non-zero exit with empty stderr
//  4. RateLimit    — stderr contains "429"|"rate limit"|"quota"|"too many
//     requests"
//  5. AuthFailure  — stderr contains "401"|"403"|"invalid api key"|
//     "unauthorized"|"forbidden"
//  6. Network      — stderr contains "connection refused"|"network
//     unreachable"|"dns"
//  7. Unknown      — catch-all non-zero
//
// Timeout + Crash precede the textual classes because their exit codes are
// deterministic operating-system signals (124 from coreutils timeout,
// 128+signo for signals) — text patterns are looser and could spuriously
// match a backend's error message that happens to mention the word "timeout"
// inside a non-timeout failure.
package dispatch

import (
	"bytes"
)

// ErrClass categorises a tier spawn outcome for fallback policy.
//
// Values are stable wire-strings via String() — the dispatch loop emits the
// string into the fallback_chain[N].outcome column of the TOON response,
// and tests + chain config (`retry_on`) reference the same strings.
type ErrClass int

const (
	// ErrClassSuccess — exitCode == 0. No fallback needed.
	ErrClassSuccess ErrClass = iota

	// ErrClassRateLimit — provider rate-limited the call (HTTP 429,
	// "rate limit exceeded", "quota exhausted", "too many requests").
	// Triggers default fallback.
	ErrClassRateLimit

	// ErrClassAuthFailure — authentication or authorization failure
	// (HTTP 401/403, "invalid api key", "unauthorized", "forbidden").
	// Triggers default fallback so a misconfigured tier doesn't halt the
	// whole chain.
	ErrClassAuthFailure

	// ErrClassNetwork — transport-layer failure ("connection refused",
	// "network unreachable", "dns"). Triggers default fallback.
	ErrClassNetwork

	// ErrClassTimeout — deadline exceeded ("deadline exceeded",
	// "context canceled", "timeout", or coreutils-timeout exit 124).
	// Triggers default fallback.
	ErrClassTimeout

	// ErrClassCrash — subprocess died abnormally (SIGKILL=137, SIGSEGV=139,
	// SIGTERM=143, OR any non-zero exit with empty stderr). Does NOT
	// trigger default fallback — surfaces as an unrecoverable dispatch
	// error per SAND-SPEC §3.3.
	ErrClassCrash

	// ErrClassUnknown — non-zero exit that matched no other class. Does
	// NOT trigger default fallback. Catch-all for diagnostic visibility.
	ErrClassUnknown
)

// String returns the stable wire identifier for this class. Used in the
// TOON fallback_chain[N].outcome column AND in chain config's retry_on
// list, so the values are part of sand's public surface — do not rename.
func (c ErrClass) String() string {
	switch c {
	case ErrClassSuccess:
		return "success"
	case ErrClassRateLimit:
		return "rate_limit"
	case ErrClassAuthFailure:
		return "auth_failure"
	case ErrClassNetwork:
		return "network"
	case ErrClassTimeout:
		return "timeout"
	case ErrClassCrash:
		return "crash"
	case ErrClassUnknown:
		return "unknown"
	default:
		return "unknown"
	}
}

// stderr-pattern tables. Each pattern is matched case-insensitively against
// the lowercased stderr. Order within each table doesn't matter — any hit
// wins for that class. CROSS-class precedence is governed by the order of
// checks inside ClassifyExitError.
var (
	rateLimitPatterns = [][]byte{
		[]byte("429"),
		[]byte("rate limit"),
		[]byte("quota"),
		[]byte("too many requests"),
	}
	authFailurePatterns = [][]byte{
		[]byte("401"),
		[]byte("403"),
		[]byte("invalid api key"),
		[]byte("unauthorized"),
		[]byte("forbidden"),
	}
	networkPatterns = [][]byte{
		[]byte("connection refused"),
		[]byte("network unreachable"),
		[]byte("dns"),
	}
	timeoutPatterns = [][]byte{
		[]byte("deadline exceeded"),
		[]byte("context canceled"),
		[]byte("timeout"),
	}
)

// ClassifyExitError categorises a subprocess outcome from its stderr +
// exitCode. See package-level docs for the precedence rules.
//
// stderr may be nil or empty; nil is treated as empty. The function is
// allocation-light: it lowercases stderr once and reuses the buffer for
// every pattern check.
func ClassifyExitError(stderr []byte, exitCode int) ErrClass {
	// 1. Success short-circuits BEFORE any stderr scan. A successful
	//    dispatch may legitimately have noise on stderr (progress
	//    messages, "rate limit headers received" etc.) and we must not
	//    misclassify it.
	if exitCode == 0 {
		return ErrClassSuccess
	}

	// 2. Deterministic exit codes win over text patterns.
	switch exitCode {
	case 124:
		// coreutils timeout(1) convention.
		return ErrClassTimeout
	case 137, 139, 143:
		// SIGKILL (128+9), SIGSEGV (128+11), SIGTERM (128+15).
		return ErrClassCrash
	}

	// 3. Non-zero with empty stderr ⇒ Crash (no diagnostic at all means
	//    the process died before it could write).
	lower := bytes.ToLower(bytes.TrimSpace(stderr))
	if len(lower) == 0 {
		return ErrClassCrash
	}

	// 4. Textual classes in declared precedence: RateLimit, AuthFailure,
	//    Network, Timeout. Provider failures are more common than
	//    transport failures, so we check provider classes first.
	if anyMatch(lower, rateLimitPatterns) {
		return ErrClassRateLimit
	}
	if anyMatch(lower, authFailurePatterns) {
		return ErrClassAuthFailure
	}
	if anyMatch(lower, networkPatterns) {
		return ErrClassNetwork
	}
	if anyMatch(lower, timeoutPatterns) {
		return ErrClassTimeout
	}

	// 5. Catch-all.
	return ErrClassUnknown
}

// anyMatch returns true if any pattern in patterns appears as a substring of
// haystack. haystack is assumed already lowercased; patterns must be
// lowercase literals at declaration site.
func anyMatch(haystack []byte, patterns [][]byte) bool {
	for _, p := range patterns {
		if bytes.Contains(haystack, p) {
			return true
		}
	}
	return false
}
