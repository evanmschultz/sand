// Package chains defines the configuration data shapes for sand's per-role
// backend fallback chains.
//
// A Config is loaded from `.claude/sand-chains.toml` at sand startup. The
// canonical layout for that file lives in SAND-SPEC.md §5: each role owns an
// ordered slice of Tier entries that walked in order until one succeeds.
//
// This file declares the types and the strict Parse entry point. Per-role
// lookup helpers land in a sibling droplet in this package.
package chains

import (
	"errors"
	"fmt"
	"io"

	"github.com/BurntSushi/toml"
)

// ErrUnknownRole is returned (wrapped) by Config.Chain when the requested role
// is not present in the chain configuration. Callers should match it via
// errors.Is so that wrapping with %w remains transparent.
var ErrUnknownRole = errors.New("chains: unknown role")

// Config is the top-level chain configuration loaded from
// `.claude/sand-chains.toml`. Roles maps each role name (e.g.
// "ta-go-builder") to its ordered fallback chain of Tier entries.
//
// The TOML key is `chains` per SAND-SPEC §5
// (`[[chains."<role>".tiers]]`); the Go field is `Roles` per the
// drop_002 planner contract.
type Config struct {
	Roles map[string][]Tier `toml:"chains"`
}

// Tier is one entry in a role's fallback chain. Tiers are walked in order;
// the first tier whose backend dispatch succeeds wins.
//
// Field semantics (from SAND-SPEC §5):
//   - Backend identifies the dispatch backend: one of "ollama-local",
//     "codex-exec", or "claude-native". Validated at startup.
//   - Model is the backend-specific model identifier (e.g.
//     "qwen2.5-coder:7b", "gpt-5.5", "opus").
//   - Opts is an opaque CLI-flags string forwarded to the backend's
//     command line (e.g. codex's "--sandbox workspace-write ..."). Empty
//     for backends that take no extra flags.
//   - WaitMax is the ollama-local slot-wait ceiling in seconds; ignored
//     for codex-exec and claude-native tiers.
//   - Slots is the ollama-local concurrency cap (0 = unlimited); ignored
//     for codex-exec and claude-native tiers.
type Tier struct {
	Backend string `toml:"backend"`
	Model   string `toml:"model"`
	Opts    string `toml:"opts"`
	WaitMax int    `toml:"wait_max"`
	Slots   int    `toml:"slots"`
}

// Parse decodes a sand chains TOML document from r into a Config.
//
// Parse is strict: any TOML key that does not map to a Config or Tier field
// causes Parse to return an error. Strictness is enforced by inspecting
// MetaData.Undecoded() after decode; an empty Undecoded slice means every
// input key was consumed by the destination types.
//
// Errors are wrapped with %w so callers may use errors.Is / errors.As against
// the underlying toml package errors.
func Parse(r io.Reader) (Config, error) {
	var cfg Config

	data, err := io.ReadAll(r)
	if err != nil {
		return Config{}, fmt.Errorf("chains: read config: %w", err)
	}

	meta, err := toml.Decode(string(data), &cfg)
	if err != nil {
		return Config{}, fmt.Errorf("chains: decode toml: %w", err)
	}

	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, 0, len(undecoded))
		for _, k := range undecoded {
			keys = append(keys, k.String())
		}
		return Config{}, fmt.Errorf("chains: unknown fields in config: %v", keys)
	}

	return cfg, nil
}

// Chain returns the ordered fallback tier slice configured for role.
//
// If role is not present in c.Roles, Chain returns a nil tier slice and an
// error that satisfies errors.Is(err, ErrUnknownRole); the role string is
// included in the wrapped error message for diagnostic context.
//
// The returned slice aliases the underlying Config storage; callers that
// intend to mutate tiers should copy first.
func (c Config) Chain(role string) ([]Tier, error) {
	tiers, ok := c.Roles[role]
	if !ok {
		return nil, fmt.Errorf("chains: role %q: %w", role, ErrUnknownRole)
	}
	return tiers, nil
}
