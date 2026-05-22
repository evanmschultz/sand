// Package dispatch — backend selection guard.
//
// This file owns the fail-fast backend selection seam for drop_003. The
// `ErrUnsupportedBackend` sentinel itself lives in dispatch.go (owned by
// drop_003.drop.droplet_dispatch_core_contract); this file imports the
// sentinel through the package and never redeclares it.
//
// drop_003 implements claude-native only. Any tier whose backend names
// "ollama-local" or "codex-exec" must surface as `ErrUnsupportedBackend`
// BEFORE any spawn is attempted — drops 004 (ollama) and 005 (codex) will
// narrow this guard's scope as those backends land.
package dispatch

import (
	"context"
	"fmt"

	"github.com/evanmschultz/sand/internal/chains"
	"github.com/evanmschultz/sand/internal/persona"
)

// claudeNativeBackend is the only backend identifier accepted by drop_003.
// Pulled into a named constant so the guard, error message, and tests
// reference exactly one string.
const claudeNativeBackend = "claude-native"

// spawnFunc is the dispatch-internal seam between the backend guard and the
// concrete claude-native spawn implementation. Production callers leave it
// nil and runTier substitutes runClaudeNative; tests inject a counter or
// recorder to assert that the guard does not invoke the spawn for
// unsupported backends.
type spawnFunc func(
	ctx context.Context,
	params Params,
	p persona.Persona,
	tier chains.Tier,
	mcpConfigPath string,
) (claudeResult, error)

// runTier runs one chain tier under the backend guard. If tier.Backend is
// anything other than "claude-native" runTier returns ErrUnsupportedBackend
// (wrapped with the offending backend string) WITHOUT invoking spawn — that
// is the load-bearing property the L4 acceptance criteria demand.
//
// When spawn is nil runTier falls back to runClaudeNative so production
// callers can omit the seam argument; tests pass a counter or recorder.
func runTier(
	ctx context.Context,
	params Params,
	p persona.Persona,
	tier chains.Tier,
	mcpConfigPath string,
	spawn spawnFunc,
) (claudeResult, error) {
	if tier.Backend != claudeNativeBackend {
		return claudeResult{}, fmt.Errorf(
			"dispatch: tier backend %q: %w", tier.Backend, ErrUnsupportedBackend,
		)
	}
	if spawn == nil {
		spawn = runClaudeNative
	}
	return spawn(ctx, params, p, tier, mcpConfigPath)
}
