// Tests for the newCleanRoomHome helper (cleanroom.go).
package backends

import (
	"os"
	"testing"
)

// TestNewCleanRoomHome is table-driven over the invariant properties of
// each newCleanRoomHome call: directory creation and environment slice.
func TestNewCleanRoomHome(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		check func(t *testing.T, dir string, env []string)
	}{
		{
			name: "dir_exists",
			check: func(t *testing.T, dir string, _ []string) {
				fi, err := os.Stat(dir)
				if err != nil {
					t.Fatalf("returned dir %q does not exist: %v", dir, err)
				}
				if !fi.IsDir() {
					t.Fatalf("returned path %q is not a directory", dir)
				}
			},
		},
		{
			name: "env_contains_HOME",
			check: func(t *testing.T, dir string, env []string) {
				if !sliceContains(env, "HOME="+dir) {
					t.Errorf("env missing HOME=%s; got %v", dir, env)
				}
			},
		},
		{
			name: "env_contains_CLAUDE_CONFIG_DIR",
			check: func(t *testing.T, dir string, env []string) {
				if !sliceContains(env, "CLAUDE_CONFIG_DIR="+dir) {
					t.Errorf("env missing CLAUDE_CONFIG_DIR=%s; got %v", dir, env)
				}
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir, env, cleanup, err := newCleanRoomHome()
			if err != nil {
				t.Fatalf("newCleanRoomHome: unexpected error: %v", err)
			}
			t.Cleanup(cleanup)
			tc.check(t, dir, env)
		})
	}
}

// TestNewCleanRoomHome_Cleanup verifies that cleanup removes the dir and
// that calling it a second time is safe (idempotent, no panic).
func TestNewCleanRoomHome_Cleanup(t *testing.T) {
	t.Parallel()

	dir, _, cleanup, err := newCleanRoomHome()
	if err != nil {
		t.Fatalf("newCleanRoomHome: unexpected error: %v", err)
	}

	if _, statErr := os.Stat(dir); statErr != nil {
		t.Fatalf("dir %q should exist before cleanup: %v", dir, statErr)
	}

	cleanup()

	if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
		t.Errorf("dir %q still exists after cleanup", dir)
	}

	cleanup() // second call must not panic
}

// sliceContains is a test-only helper that reports whether want appears in s.
func sliceContains(s []string, want string) bool {
	for _, v := range s {
		if v == want {
			return true
		}
	}
	return false
}
