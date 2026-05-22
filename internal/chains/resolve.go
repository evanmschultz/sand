package chains

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrChainConfigNotFound is returned by Resolve when none of the 4 candidate
// chain-config paths exist on disk. The wrapped error message lists every
// candidate path that was checked, in resolution order.
//
// Callers should match it via errors.Is so wrapping with %w stays transparent.
var ErrChainConfigNotFound = errors.New("chains: no chain config found")

// Resolve walks the v0.2 hierarchical chain-config resolution order and returns
// the absolute path of the first existing chains.toml, plus a source label
// identifying which rung won.
//
// Resolution order (per SAND-V02-SPEC §2.1, project-first override semantics):
//
//  1. <projectDir>/.claude/sand-chains.toml          source = "project"
//  2. $XDG_CONFIG_HOME/sand/chains.toml              source = "xdg"
//     (skipped entirely when XDG_CONFIG_HOME is unset or empty)
//  3. $HOME/.config/sand/chains.toml                 source = "home-config"
//  4. $HOME/.sand/chains.toml                        source = "home-dotfile"
//
// If none of the candidate paths exist, Resolve returns an error that
// satisfies errors.Is(err, ErrChainConfigNotFound); the error message includes
// the candidate paths that were probed.
//
// Resolve never invents implicit defaults — REPLACE semantics are enforced
// structurally by returning a single winning path (no merge possible at API).
func Resolve(projectDir string) (path string, source string, err error) {
	type candidate struct {
		source string
		path   string
	}

	candidates := make([]candidate, 0, 4)

	// Rung 1: project override.
	candidates = append(candidates, candidate{
		source: "project",
		path:   filepath.Join(projectDir, ".claude", "sand-chains.toml"),
	})

	// Rung 2: XDG. SKIPPED ENTIRELY when env var unset/empty — never call
	// filepath.Join on an empty XDG root, which would yield a relative
	// "sand/chains.toml" probe against cwd.
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		candidates = append(candidates, candidate{
			source: "xdg",
			path:   filepath.Join(xdg, "sand", "chains.toml"),
		})
	}

	// Rungs 3-4: $HOME-anchored. If HOME is unset we still record the
	// would-be paths as candidates for the not-found error message, but
	// filepath.Join("", ...) yields a relative path that Stat will not
	// match a real file — the not-found path still terminates cleanly.
	home := os.Getenv("HOME")
	candidates = append(
		candidates,
		candidate{
			source: "home-config",
			path:   filepath.Join(home, ".config", "sand", "chains.toml"),
		},
		candidate{
			source: "home-dotfile",
			path:   filepath.Join(home, ".sand", "chains.toml"),
		},
	)

	for _, c := range candidates {
		if _, statErr := os.Stat(c.path); statErr == nil {
			return c.path, c.source, nil
		}
	}

	paths := make([]string, 0, len(candidates))
	for _, c := range candidates {
		paths = append(paths, c.path)
	}
	return "", "", fmt.Errorf("%w; checked: %v", ErrChainConfigNotFound, paths)
}
