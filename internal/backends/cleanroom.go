// Package backends — claude clean-room HOME helper.
//
// HELPER ONLY. Wiring into claudeNativeBackend.Spawn lives in the sibling
// droplet w5_spawn_wiring — do not call it from here.
package backends

import (
	"fmt"
	"os"
)

// newCleanRoomHome returns an isolated tmp directory for a spawned claude
// agent and the env overrides that point the agent at it (HOME + CLAUDE_CONFIG_DIR).
//
// CLAUDE_CONFIG_DIR is the authoritative override for where `claude` reads
// its config dir; confirmed from binary strings (see drop_015 w5_spike_envvars).
// HOME isolation prevents XDG fallbacks reaching the real user tree.
// cleanup is idempotent (os.RemoveAll is a no-op on a missing path);
// callers MUST defer it immediately after checking err.
func newCleanRoomHome() (dir string, env []string, cleanup func(), err error) {
	dir, err = os.MkdirTemp("", "claude-cleanroom.*")
	if err != nil {
		return "", nil, func() {}, fmt.Errorf("backends: create claude clean-room home: %w", err)
	}
	return dir, []string{
		"HOME=" + dir,
		"CLAUDE_CONFIG_DIR=" + dir,
	}, func() { _ = os.RemoveAll(dir) }, nil
}
