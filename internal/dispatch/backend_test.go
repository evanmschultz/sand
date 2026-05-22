// Tests for the dispatch package backend guard. The guard must surface
// ErrUnsupportedBackend (errors.Is-matchable) for any tier whose backend is
// not "claude-native" and must NOT invoke the spawn for those tiers; the
// claude-native pass-through must invoke the spawn exactly once.
package dispatch

import (
	"context"
	"errors"
	"testing"

	"github.com/evanmschultz/sand/internal/chains"
	"github.com/evanmschultz/sand/internal/persona"
)

// TestUnsupportedBackendFailFast exercises every documented backend value
// against runTier. ollama-local and codex-exec tiers (and the empty / bogus
// fallbacks) must return ErrUnsupportedBackend without touching the spawn
// counter; claude-native must call the spawn exactly once and propagate its
// return values verbatim.
func TestUnsupportedBackendFailFast(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		backend   string
		wantSpawn int
		wantErr   error
	}{
		{"ollama-local rejected", "ollama-local", 0, ErrUnsupportedBackend},
		{"codex-exec rejected", "codex-exec", 0, ErrUnsupportedBackend},
		{"empty backend rejected", "", 0, ErrUnsupportedBackend},
		{"unknown backend rejected", "bogus", 0, ErrUnsupportedBackend},
		{"claude-native pass through", "claude-native", 1, nil},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			var spawnCalls int
			spawn := func(
				_ context.Context,
				_ Params,
				_ persona.Persona,
				_ chains.Tier,
				_ string,
			) (claudeResult, error) {
				spawnCalls++
				return claudeResult{ExitCode: 0, DurationMs: 42}, nil
			}

			tier := chains.Tier{Backend: c.backend, Model: "opus"}
			res, err := runTier(
				context.Background(),
				Params{Role: "ta-go-builder", Prompt: "x"},
				persona.Persona{},
				tier,
				"",
				spawn,
			)

			if c.wantErr != nil {
				if !errors.Is(err, c.wantErr) {
					t.Errorf("runTier(%q) err = %v; want errors.Is(_, %v)", c.backend, err, c.wantErr)
				}
				if res.Stdout != nil || res.Stderr != nil || res.ExitCode != 0 || res.DurationMs != 0 {
					t.Errorf("runTier(%q) result = %#v; want zero claudeResult on guard failure", c.backend, res)
				}
			} else {
				if err != nil {
					t.Errorf("runTier(%q) unexpected err: %v", c.backend, err)
				}
				if res.DurationMs != 42 {
					t.Errorf("runTier(%q) result.DurationMs = %d; want spawn return propagated (42)", c.backend, res.DurationMs)
				}
			}

			if spawnCalls != c.wantSpawn {
				t.Errorf("runTier(%q) spawn invocations = %d; want %d", c.backend, spawnCalls, c.wantSpawn)
			}
		})
	}
}

// TestUnsupportedBackendErrorMessage pins the diagnostic surface area: the
// wrapped error must include the offending backend string verbatim so the
// dispatch log and TOON response can surface "which tier was rejected"
// without a separate field. errors.Is must still match the sentinel.
func TestUnsupportedBackendErrorMessage(t *testing.T) {
	t.Parallel()

	tier := chains.Tier{Backend: "ollama-local", Model: "qwen3-coder:30b"}
	_, err := runTier(
		context.Background(),
		Params{},
		persona.Persona{},
		tier,
		"",
		nil, // no spawn — guard must short-circuit before nil-spawn fallback
	)

	if err == nil {
		t.Fatalf("runTier with unsupported backend returned nil error")
	}
	if !errors.Is(err, ErrUnsupportedBackend) {
		t.Errorf("errors.Is(err, ErrUnsupportedBackend) = false; err = %v", err)
	}
	if msg := err.Error(); !contains(msg, "ollama-local") {
		t.Errorf("err message = %q; want it to include offending backend %q", msg, "ollama-local")
	}
}

// contains is a tiny local helper to avoid pulling in strings just for this.
func contains(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
