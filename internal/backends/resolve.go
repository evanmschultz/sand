// Package backends owns sand's user-configurable backend template system.
// This file implements the hierarchical backends.toml loader — path-only,
// not a parser. Parsing of the resolved file into BackendConfig records
// is the sibling factory droplet's responsibility.
package backends

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrBackendsConfigNotFound is returned by ResolveBackendsConfig when none
// of the 4 candidate backends-config paths exist on disk. The wrapped error
// message lists every candidate path that was checked, in resolution order.
//
// Callers should match it via errors.Is so wrapping with %w stays
// transparent.
//
// This sentinel is deliberately distinct from the sibling backend-factory's
// ErrUnknownBackend (which signals a missing config ENTRY, not a missing
// config FILE).
var ErrBackendsConfigNotFound = errors.New("backends: no backends config found")

// ResolveBackendsConfig walks the v0.2 hierarchical backends-config
// resolution order and returns the absolute path of the first existing
// backends.toml, plus a source label identifying which rung won.
//
// Resolution order (per SAND-V02-SPEC §5.2, project-first override semantics
// mirroring chains.Resolve from drop_008):
//
//  1. <projectDir>/.claude/sand-backends.toml        source = "project"
//  2. $XDG_CONFIG_HOME/sand/backends.toml            source = "xdg"
//     (skipped entirely when XDG_CONFIG_HOME is unset or empty)
//  3. $HOME/.config/sand/backends.toml               source = "home-config"
//  4. $HOME/.sand/backends.toml                      source = "home-dotfile"
//
// If none of the candidate paths exist, ResolveBackendsConfig returns an
// error that satisfies errors.Is(err, ErrBackendsConfigNotFound); the error
// message includes the candidate paths that were probed.
//
// ResolveBackendsConfig never invents implicit defaults — REPLACE semantics
// are enforced structurally by returning a single winning path (no merge
// possible at API level).
func ResolveBackendsConfig(projectDir string) (path string, source string, err error) {
	type candidate struct {
		source string
		path   string
	}

	candidates := make([]candidate, 0, 4)

	// Rung 1: project override.
	candidates = append(candidates, candidate{
		source: "project",
		path:   filepath.Join(projectDir, ".claude", "sand-backends.toml"),
	})

	// Rung 2: XDG. SKIPPED ENTIRELY when env var unset/empty — never call
	// filepath.Join on an empty XDG root, which would yield a relative
	// "sand/backends.toml" probe against cwd.
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		candidates = append(candidates, candidate{
			source: "xdg",
			path:   filepath.Join(xdg, "sand", "backends.toml"),
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
			path:   filepath.Join(home, ".config", "sand", "backends.toml"),
		},
		candidate{
			source: "home-dotfile",
			path:   filepath.Join(home, ".sand", "backends.toml"),
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
	return "", "", fmt.Errorf("%w; checked: %v", ErrBackendsConfigNotFound, paths)
}
