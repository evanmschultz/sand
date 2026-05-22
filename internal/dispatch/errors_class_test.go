package dispatch

import (
	"testing"
)

// TestErrClass_String pins the wire identifiers for every enum value. These
// strings appear in chain config retry_on lists AND in TOON
// fallback_chain[N].outcome columns, so renaming silently breaks both
// orchestrator parsing and user configs.
func TestErrClass_String(t *testing.T) {
	tests := []struct {
		class ErrClass
		want  string
	}{
		{ErrClassSuccess, "success"},
		{ErrClassRateLimit, "rate_limit"},
		{ErrClassAuthFailure, "auth_failure"},
		{ErrClassNetwork, "network"},
		{ErrClassTimeout, "timeout"},
		{ErrClassCrash, "crash"},
		{ErrClassUnknown, "unknown"},
		{ErrClass(999), "unknown"}, // out-of-range falls back to "unknown"
	}
	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			if got := tc.class.String(); got != tc.want {
				t.Fatalf("ErrClass(%d).String() = %q, want %q", tc.class, got, tc.want)
			}
		})
	}
}

// TestClassifyExitError exercises the full classification table across the
// three providers sand routes to (claude / codex / ollama) plus the
// success-short-circuit guard. Each class has at least two provider-flavored
// examples to defend against pattern regressions when a backend changes its
// error wording.
func TestClassifyExitError(t *testing.T) {
	tests := []struct {
		name     string
		stderr   string
		exitCode int
		want     ErrClass
	}{
		// --- Success short-circuit. Stderr noise must NOT misclassify. ---
		{
			name:     "success_clean_exit_no_stderr",
			stderr:   "",
			exitCode: 0,
			want:     ErrClassSuccess,
		},
		{
			name:     "success_with_rate_word_in_stderr_progress",
			stderr:   "info: rate limit headers received from upstream (ok)",
			exitCode: 0,
			want:     ErrClassSuccess,
		},
		{
			name:     "success_with_timeout_word_in_stderr_progress",
			stderr:   "debug: configured request timeout = 60s\n",
			exitCode: 0,
			want:     ErrClassSuccess,
		},
		{
			name:     "success_with_401_substring_in_log_line",
			stderr:   "trace: request id 401abc completed in 12ms",
			exitCode: 0,
			want:     ErrClassSuccess,
		},

		// --- RateLimit (claude / codex / ollama provider markers). ---
		{
			name:     "rate_limit_claude_429_text",
			stderr:   "Error: HTTP 429 Too Many Requests from anthropic api",
			exitCode: 1,
			want:     ErrClassRateLimit,
		},
		{
			name:     "rate_limit_codex_rate_limit_exceeded",
			stderr:   "codex: openai api returned rate limit exceeded, retry in 60s",
			exitCode: 1,
			want:     ErrClassRateLimit,
		},
		{
			name:     "rate_limit_ollama_quota_exhausted",
			stderr:   "ollama-cloud: monthly quota exhausted for model qwen3-coder",
			exitCode: 1,
			want:     ErrClassRateLimit,
		},
		{
			name:     "rate_limit_too_many_requests_exit_125",
			stderr:   "TOO MANY REQUESTS: throttled by provider",
			exitCode: 125,
			want:     ErrClassRateLimit,
		},

		// --- AuthFailure (claude / codex / ollama auth markers). ---
		{
			name:     "auth_failure_claude_401",
			stderr:   "Error: HTTP 401 Unauthorized — check ANTHROPIC_API_KEY",
			exitCode: 1,
			want:     ErrClassAuthFailure,
		},
		{
			name:     "auth_failure_codex_invalid_api_key",
			stderr:   "codex exec: invalid api key (OPENAI_API_KEY rejected)",
			exitCode: 1,
			want:     ErrClassAuthFailure,
		},
		{
			name:     "auth_failure_ollama_403_forbidden",
			stderr:   "ollama: 403 forbidden — model gated for current account",
			exitCode: 1,
			want:     ErrClassAuthFailure,
		},
		{
			name:     "auth_failure_unauthorized_exit_125",
			stderr:   "UNAUTHORIZED: token expired",
			exitCode: 125,
			want:     ErrClassAuthFailure,
		},

		// --- Network (transport-layer markers). ---
		{
			name:     "network_ollama_daemon_connection_refused",
			stderr:   "dial tcp 127.0.0.1:11434: connection refused",
			exitCode: 1,
			want:     ErrClassNetwork,
		},
		{
			name:     "network_claude_dns_failure",
			stderr:   "claude: dns lookup failed for api.anthropic.com",
			exitCode: 2,
			want:     ErrClassNetwork,
		},
		{
			name:     "network_codex_network_unreachable",
			stderr:   "codex: network unreachable from container",
			exitCode: 1,
			want:     ErrClassNetwork,
		},

		// --- Timeout (deadline + coreutils-timeout markers). ---
		{
			name:     "timeout_exit_124_deterministic",
			stderr:   "", // coreutils timeout writes nothing
			exitCode: 124,
			want:     ErrClassTimeout,
		},
		{
			name:     "timeout_claude_context_deadline_exceeded",
			stderr:   "context deadline exceeded after 30s",
			exitCode: 1,
			want:     ErrClassTimeout,
		},
		{
			name:     "timeout_codex_context_canceled",
			stderr:   "codex: context canceled while awaiting response",
			exitCode: 125,
			want:     ErrClassTimeout,
		},
		{
			name:     "timeout_ollama_explicit_timeout_word",
			stderr:   "ollama: request timeout after 120000ms",
			exitCode: 1,
			want:     ErrClassTimeout,
		},

		// --- Crash (signal exits + non-zero with empty stderr). ---
		{
			name:     "crash_sigkill_exit_137",
			stderr:   "",
			exitCode: 137,
			want:     ErrClassCrash,
		},
		{
			name:     "crash_sigsegv_exit_139",
			stderr:   "fatal error: SIGSEGV: segmentation violation",
			exitCode: 139,
			want:     ErrClassCrash,
		},
		{
			name:     "crash_sigterm_exit_143",
			stderr:   "killed by signal: terminated",
			exitCode: 143,
			want:     ErrClassCrash,
		},
		{
			name:     "crash_empty_stderr_nonzero_exit",
			stderr:   "",
			exitCode: 1,
			want:     ErrClassCrash,
		},
		{
			name:     "crash_whitespace_only_stderr_nonzero_exit",
			stderr:   "   \n  \t",
			exitCode: 1,
			want:     ErrClassCrash,
		},

		// --- Unknown (catch-all non-zero with no matching pattern). ---
		{
			name:     "unknown_unhandled_error_text",
			stderr:   "claude: unexpected error parsing local config file",
			exitCode: 1,
			want:     ErrClassUnknown,
		},
		{
			name:     "unknown_codex_internal_failure",
			stderr:   "codex internal failure: state machine inconsistent",
			exitCode: 2,
			want:     ErrClassUnknown,
		},
		{
			name:     "unknown_ollama_model_not_found",
			stderr:   "ollama: model qwen3-coder:nonexistent not found locally",
			exitCode: 1,
			want:     ErrClassUnknown,
		},

		// --- Precedence guards: deterministic exit codes win over text. ---
		{
			name:     "timeout_exit_124_overrides_rate_limit_text",
			stderr:   "warning: hit rate limit before timeout fired",
			exitCode: 124,
			want:     ErrClassTimeout,
		},
		{
			name:     "crash_exit_137_overrides_rate_limit_text",
			stderr:   "rate limit reached before kill",
			exitCode: 137,
			want:     ErrClassCrash,
		},
		// Provider-class precedence: when stderr matches BOTH rate_limit
		// and auth_failure patterns the spec table lists RateLimit first.
		{
			name:     "rate_limit_wins_over_auth_when_both_match",
			stderr:   "Error: HTTP 429 rate limit exceeded; also 401 hint",
			exitCode: 1,
			want:     ErrClassRateLimit,
		},
		// AuthFailure precedes Network when both match.
		{
			name:     "auth_wins_over_network_when_both_match",
			stderr:   "Error: 401 unauthorized after connection refused retry",
			exitCode: 1,
			want:     ErrClassAuthFailure,
		},
		// Network precedes Timeout when both match.
		{
			name:     "network_wins_over_timeout_when_both_match",
			stderr:   "dns lookup failed: request timeout after 5s",
			exitCode: 1,
			want:     ErrClassNetwork,
		},

		// --- Nil stderr safety. ---
		{
			name:     "nil_stderr_nonzero_exit_is_crash",
			stderr:   "",
			exitCode: 2,
			want:     ErrClassCrash,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ClassifyExitError([]byte(tc.stderr), tc.exitCode)
			if got != tc.want {
				t.Fatalf(
					"ClassifyExitError(stderr=%q, exitCode=%d) = %s, want %s",
					tc.stderr, tc.exitCode, got, tc.want,
				)
			}
		})
	}
}

// TestClassifyExitError_NilStderr separately asserts the nil-byte-slice path
// since the table above only uses string literals (always non-nil byte slice
// after conversion). A nil slice must not panic and must be treated as
// empty.
func TestClassifyExitError_NilStderr(t *testing.T) {
	if got := ClassifyExitError(nil, 0); got != ErrClassSuccess {
		t.Fatalf("ClassifyExitError(nil, 0) = %s, want success", got)
	}
	if got := ClassifyExitError(nil, 1); got != ErrClassCrash {
		t.Fatalf("ClassifyExitError(nil, 1) = %s, want crash (empty stderr + non-zero)", got)
	}
	if got := ClassifyExitError(nil, 124); got != ErrClassTimeout {
		t.Fatalf("ClassifyExitError(nil, 124) = %s, want timeout (deterministic exit)", got)
	}
}

// TestClassifyExitError_CaseInsensitive asserts that stderr matching is
// case-insensitive across all textual classes — providers vary in casing
// (e.g. "UNAUTHORIZED" from one stack, "Unauthorized" from another).
func TestClassifyExitError_CaseInsensitive(t *testing.T) {
	cases := []struct {
		name   string
		stderr string
		want   ErrClass
	}{
		{"rate_limit_uppercase", "RATE LIMIT EXCEEDED", ErrClassRateLimit},
		{"auth_mixed_case", "Invalid API Key for provider", ErrClassAuthFailure},
		{"network_uppercase", "CONNECTION REFUSED", ErrClassNetwork},
		{"timeout_mixed_case", "Context Canceled by caller", ErrClassTimeout},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ClassifyExitError([]byte(tc.stderr), 1); got != tc.want {
				t.Fatalf("got %s, want %s for stderr=%q", got, tc.want, tc.stderr)
			}
		})
	}
}
