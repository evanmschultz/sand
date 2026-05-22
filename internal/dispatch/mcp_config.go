// Package dispatch implements the sand.dispatch flow: resolving caller
// inputs (MCP config, persona, chain), spawning the selected backend, and
// returning a TOON-encoded response.
//
// This file owns one piece of that flow: resolving the caller project's
// `.mcp.json` path. SAND-SPEC §3.1 + drop_003 L1 require dispatch to read
// the CALLER project's `<cwd>/.mcp.json` and pass it to claude via
// `--mcp-config <abs-path>` ONLY when the file is present. Sand bundles no
// MCP servers and must not synthesize a config when none exists.
package dispatch

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// mcpConfigFilename is the canonical caller-project MCP config basename.
// Sand reads `<cwd>/.mcp.json` from the caller project on every dispatch
// (see SAND-SPEC §3.1 and project memory `project_sand_mcp_per_project_configurable`).
const mcpConfigFilename = ".mcp.json"

// resolveMCPConfig computes the absolute path to the caller project's
// `.mcp.json` and reports whether the file is currently present.
//
// The returned path is always the absolute `<cwd>/.mcp.json` regardless of
// existence so callers can render diagnostics or dry-run output that names
// the would-be config location even when the file is missing. cwd may be
// relative or absolute; relative paths are normalized via filepath.Abs
// against the process working directory.
//
// Missing `.mcp.json` is an OPTIONAL state, not a dispatch failure: in that
// case exists is false, path is still populated, and err is nil. The
// dispatch flow uses exists to decide whether to add `--mcp-config <path>`
// to the spawned claude command (drop_003 L1 acceptance criteria).
//
// A non-nil error is returned only for unexpected filesystem failures
// (e.g. cwd cannot be resolved to an absolute path, or stat fails with
// something other than ErrNotExist — typically a permission error). Such
// errors wrap the underlying cause via fmt.Errorf %w so callers can use
// errors.Is for classification.
func resolveMCPConfig(cwd string) (path string, exists bool, err error) {
	absCwd, err := filepath.Abs(cwd)
	if err != nil {
		return "", false, fmt.Errorf("dispatch: resolve mcp config cwd %q: %w", cwd, err)
	}

	path = filepath.Join(absCwd, mcpConfigFilename)

	info, statErr := os.Stat(path)
	switch {
	case statErr == nil:
		// Stat succeeded. Treat directories at this path as "not present"
		// rather than as a usable config: claude expects a regular file at
		// --mcp-config, and a directory would fail downstream in a less
		// obvious way. Reporting exists=false here matches the
		// missing-is-optional semantics without surfacing a hard error.
		if info.IsDir() {
			return path, false, nil
		}
		return path, true, nil
	case errors.Is(statErr, fs.ErrNotExist):
		return path, false, nil
	default:
		return path, false, fmt.Errorf("dispatch: stat mcp config %q: %w", path, statErr)
	}
}
