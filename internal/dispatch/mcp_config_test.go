package dispatch

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestMCPConfigPath exercises resolveMCPConfig across the cases enumerated
// in drop_003.drop.droplet_dispatch_caller_config acceptance criteria:
// present file, missing file, relative cwd normalization, and an
// unexpected filesystem error (a directory occupying the .mcp.json path
// stands in for "not a usable regular file" — exists must be false without
// raising a hard error; a permission-denied stat is also covered when the
// platform supports it).
func TestMCPConfigPath(t *testing.T) {
	// presentDir contains a real .mcp.json file.
	presentDir := t.TempDir()
	cfgInPresent := filepath.Join(presentDir, ".mcp.json")
	if err := os.WriteFile(cfgInPresent, []byte("{}"), 0o644); err != nil {
		t.Fatalf("setup: write %s: %v", cfgInPresent, err)
	}

	// missingDir intentionally has no .mcp.json.
	missingDir := t.TempDir()

	// dirAtPathDir has a DIRECTORY named .mcp.json (not a regular file);
	// resolveMCPConfig must treat that as not-present and not return an
	// error.
	dirAtPathDir := t.TempDir()
	cfgAsDir := filepath.Join(dirAtPathDir, ".mcp.json")
	if err := os.Mkdir(cfgAsDir, 0o755); err != nil {
		t.Fatalf("setup: mkdir %s: %v", cfgAsDir, err)
	}

	// relCwd: relative path that resolves to presentDir via os.Chdir.
	// Using t.Chdir keeps the change scoped to this sub-test.

	cases := []struct {
		name       string
		cwd        string
		wantExists bool
		wantPath   string // empty means "compute as filepath.Join(absCwd, .mcp.json)" via helper
		wantErr    bool
	}{
		{
			name:       "present absolute cwd",
			cwd:        presentDir,
			wantExists: true,
			wantPath:   cfgInPresent,
		},
		{
			name:       "missing absolute cwd",
			cwd:        missingDir,
			wantExists: false,
			wantPath:   filepath.Join(missingDir, ".mcp.json"),
		},
		{
			name:       "directory at config path treated as missing",
			cwd:        dirAtPathDir,
			wantExists: false,
			wantPath:   cfgAsDir,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotPath, gotExists, err := resolveMCPConfig(tc.cwd)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("resolveMCPConfig(%q) err = nil, want non-nil", tc.cwd)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveMCPConfig(%q) err = %v, want nil", tc.cwd, err)
			}
			if gotExists != tc.wantExists {
				t.Errorf("exists = %v, want %v", gotExists, tc.wantExists)
			}
			if gotPath != tc.wantPath {
				t.Errorf("path = %q, want %q", gotPath, tc.wantPath)
			}
			if !filepath.IsAbs(gotPath) {
				t.Errorf("path %q is not absolute; resolveMCPConfig must always return an absolute path", gotPath)
			}
		})
	}

	// Relative cwd normalization: switch into a temp parent dir, then
	// resolve "child" — the returned path must be absolute and equal to
	// <parent>/child/.mcp.json. We create the file so we also verify
	// exists=true survives the relative-to-absolute round trip.
	t.Run("relative cwd is normalized to absolute", func(t *testing.T) {
		parent := t.TempDir()
		child := filepath.Join(parent, "child")
		if err := os.Mkdir(child, 0o755); err != nil {
			t.Fatalf("setup: mkdir %s: %v", child, err)
		}
		childCfg := filepath.Join(child, ".mcp.json")
		if err := os.WriteFile(childCfg, []byte("{}"), 0o644); err != nil {
			t.Fatalf("setup: write %s: %v", childCfg, err)
		}

		t.Chdir(parent)

		gotPath, gotExists, err := resolveMCPConfig("child")
		if err != nil {
			t.Fatalf("resolveMCPConfig(%q) err = %v, want nil", "child", err)
		}
		if !gotExists {
			t.Errorf("exists = false, want true (relative cwd should resolve to a real file)")
		}
		if !filepath.IsAbs(gotPath) {
			t.Errorf("path %q is not absolute after relative-cwd normalization", gotPath)
		}
		// On macOS /tmp is a symlink to /private/tmp; compare via EvalSymlinks
		// so a symlinked parent doesn't fail the equality check.
		gotEval, err := filepath.EvalSymlinks(gotPath)
		if err != nil {
			t.Fatalf("EvalSymlinks(%q): %v", gotPath, err)
		}
		wantEval, err := filepath.EvalSymlinks(childCfg)
		if err != nil {
			t.Fatalf("EvalSymlinks(%q): %v", childCfg, err)
		}
		if gotEval != wantEval {
			t.Errorf("normalized path = %q, want %q", gotEval, wantEval)
		}
	})

	// Filesystem-error surfacing: on Unix, a parent dir with mode 000
	// makes os.Stat on a child path fail with EACCES — distinct from
	// ErrNotExist — so resolveMCPConfig must wrap and surface it. We skip
	// on Windows (no POSIX 000 semantics) and when running as root (root
	// bypasses dir permission bits).
	t.Run("unexpected stat error is wrapped", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("permission-denied stat semantics differ on windows")
		}
		if os.Geteuid() == 0 {
			t.Skip("running as root: permission bits are not enforced for stat")
		}

		parent := t.TempDir()
		blocked := filepath.Join(parent, "blocked")
		if err := os.Mkdir(blocked, 0o000); err != nil {
			t.Fatalf("setup: mkdir blocked: %v", err)
		}
		// Restore perms so t.TempDir cleanup can recurse.
		t.Cleanup(func() {
			_ = os.Chmod(blocked, 0o755)
		})

		_, _, err := resolveMCPConfig(blocked)
		if err == nil {
			t.Fatalf("resolveMCPConfig(%q) err = nil, want wrapped permission error", blocked)
		}
		if errors.Is(err, fs.ErrNotExist) {
			t.Errorf("err = %v, must NOT classify as ErrNotExist (missing must surface as exists=false, nil err)", err)
		}
		if !strings.Contains(err.Error(), "dispatch:") {
			t.Errorf("err = %q, want message prefixed with %q for grep-ability", err.Error(), "dispatch:")
		}
	})
}
