package chains

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestResolve covers the 4-rung hierarchical chain-config resolution order
// per SAND-SPEC §2.1, plus the ErrChainConfigNotFound terminal case and
// the XDG-unset skip case pinned by qa_falsification_chains_resolve_l3.
//
// Each subtest isolates HOME + XDG_CONFIG_HOME via t.Setenv and seeds only
// the file(s) that should win for that rung — never assuming any other rung
// is absent on the developer machine.
func TestResolve(t *testing.T) {
	// No t.Parallel: subtests use t.Setenv / t.Chdir on process-global
	// state, which is incompatible with parallel execution.

	t.Run("project_wins", func(t *testing.T) {
		projectDir := t.TempDir()
		xdgDir := t.TempDir()
		homeDir := t.TempDir()

		// Seed every rung. Project must still win.
		projectPath := writeFile(t, filepath.Join(projectDir, ".claude", "sand-chains.toml"), "project")
		writeFile(t, filepath.Join(xdgDir, "sand", "chains.toml"), "xdg")
		writeFile(t, filepath.Join(homeDir, ".config", "sand", "chains.toml"), "home-config")
		writeFile(t, filepath.Join(homeDir, ".sand", "chains.toml"), "home-dotfile")

		t.Setenv("XDG_CONFIG_HOME", xdgDir)
		t.Setenv("HOME", homeDir)

		gotPath, gotSource, err := Resolve(projectDir)
		if err != nil {
			t.Fatalf("Resolve: unexpected error: %v", err)
		}
		if gotPath != projectPath {
			t.Errorf("path: got %q want %q", gotPath, projectPath)
		}
		if gotSource != "project" {
			t.Errorf("source: got %q want %q", gotSource, "project")
		}
	})

	t.Run("xdg_wins_when_project_absent", func(t *testing.T) {
		projectDir := t.TempDir()
		xdgDir := t.TempDir()
		homeDir := t.TempDir()

		xdgPath := writeFile(t, filepath.Join(xdgDir, "sand", "chains.toml"), "xdg")
		writeFile(t, filepath.Join(homeDir, ".config", "sand", "chains.toml"), "home-config")
		writeFile(t, filepath.Join(homeDir, ".sand", "chains.toml"), "home-dotfile")

		t.Setenv("XDG_CONFIG_HOME", xdgDir)
		t.Setenv("HOME", homeDir)

		gotPath, gotSource, err := Resolve(projectDir)
		if err != nil {
			t.Fatalf("Resolve: unexpected error: %v", err)
		}
		if gotPath != xdgPath {
			t.Errorf("path: got %q want %q", gotPath, xdgPath)
		}
		if gotSource != "xdg" {
			t.Errorf("source: got %q want %q", gotSource, "xdg")
		}
	})

	t.Run("home_config_wins_when_project_and_xdg_absent", func(t *testing.T) {
		projectDir := t.TempDir()
		homeDir := t.TempDir()

		homeConfigPath := writeFile(t, filepath.Join(homeDir, ".config", "sand", "chains.toml"), "home-config")
		writeFile(t, filepath.Join(homeDir, ".sand", "chains.toml"), "home-dotfile")

		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("HOME", homeDir)

		gotPath, gotSource, err := Resolve(projectDir)
		if err != nil {
			t.Fatalf("Resolve: unexpected error: %v", err)
		}
		if gotPath != homeConfigPath {
			t.Errorf("path: got %q want %q", gotPath, homeConfigPath)
		}
		if gotSource != "home-config" {
			t.Errorf("source: got %q want %q", gotSource, "home-config")
		}
	})

	t.Run("home_dotfile_wins_when_higher_rungs_absent", func(t *testing.T) {
		projectDir := t.TempDir()
		homeDir := t.TempDir()

		dotfilePath := writeFile(t, filepath.Join(homeDir, ".sand", "chains.toml"), "home-dotfile")

		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("HOME", homeDir)

		gotPath, gotSource, err := Resolve(projectDir)
		if err != nil {
			t.Fatalf("Resolve: unexpected error: %v", err)
		}
		if gotPath != dotfilePath {
			t.Errorf("path: got %q want %q", gotPath, dotfilePath)
		}
		if gotSource != "home-dotfile" {
			t.Errorf("source: got %q want %q", gotSource, "home-dotfile")
		}
	})

	t.Run("xdg_unset_skips_rung_entirely", func(t *testing.T) {
		// Pinned test for qa_falsification_chains_resolve_l3 attack D:
		// when XDG_CONFIG_HOME is empty, the resolver MUST NOT probe a
		// relative `sand/chains.toml` path (i.e. must not call
		// filepath.Join("", "sand", "chains.toml")). We prove this by
		// creating a deceptive `sand/chains.toml` file inside a temp
		// dir, chdir'ing into it, ensuring HOME points elsewhere with
		// no chain configs, and asserting Resolve returns NotFound —
		// never the relative-path file.
		decoyDir := t.TempDir()
		decoyRelativePath := filepath.Join(decoyDir, "sand", "chains.toml")
		writeFile(t, decoyRelativePath, "decoy-must-not-match")

		// chdir so a relative "sand/chains.toml" probe would resolve
		// to the decoy file.
		cwd, err := os.Getwd()
		if err != nil {
			t.Fatalf("getwd: %v", err)
		}
		t.Cleanup(func() {
			if err := os.Chdir(cwd); err != nil {
				t.Errorf("restore cwd: %v", err)
			}
		})
		if err := os.Chdir(decoyDir); err != nil {
			t.Fatalf("chdir decoy: %v", err)
		}

		projectDir := t.TempDir()
		homeDir := t.TempDir() // empty — no chain configs

		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("HOME", homeDir)

		gotPath, gotSource, resolveErr := Resolve(projectDir)
		if !errors.Is(resolveErr, ErrChainConfigNotFound) {
			t.Fatalf("expected ErrChainConfigNotFound, got path=%q source=%q err=%v",
				gotPath, gotSource, resolveErr)
		}
		if gotPath != "" || gotSource != "" {
			t.Errorf("expected empty path+source on miss, got path=%q source=%q",
				gotPath, gotSource)
		}
		// And confirm the error message does NOT include the bare
		// relative path that would indicate a filepath.Join("", ...)
		// regression.
		if strings.Contains(resolveErr.Error(), "sand/chains.toml") &&
			!strings.Contains(resolveErr.Error(), homeDir) &&
			!strings.Contains(resolveErr.Error(), projectDir) {
			t.Errorf("error message looks like it leaked a relative path: %v", resolveErr)
		}
	})

	t.Run("not_found_when_all_rungs_absent", func(t *testing.T) {
		projectDir := t.TempDir()
		xdgDir := t.TempDir()
		homeDir := t.TempDir()

		t.Setenv("XDG_CONFIG_HOME", xdgDir)
		t.Setenv("HOME", homeDir)

		gotPath, gotSource, err := Resolve(projectDir)
		if !errors.Is(err, ErrChainConfigNotFound) {
			t.Fatalf("expected ErrChainConfigNotFound, got %v", err)
		}
		if gotPath != "" || gotSource != "" {
			t.Errorf("expected empty path+source on miss, got path=%q source=%q",
				gotPath, gotSource)
		}

		// All 4 candidate paths must be present in the error message
		// for diagnostic context.
		want := []string{
			filepath.Join(projectDir, ".claude", "sand-chains.toml"),
			filepath.Join(xdgDir, "sand", "chains.toml"),
			filepath.Join(homeDir, ".config", "sand", "chains.toml"),
			filepath.Join(homeDir, ".sand", "chains.toml"),
		}
		msg := err.Error()
		for _, p := range want {
			if !strings.Contains(msg, p) {
				t.Errorf("error message missing candidate path %q: %v", p, err)
			}
		}
	})
}

// writeFile creates the parent directory tree if absent and writes content to
// path. Returns the absolute path it wrote, so callers can compare against
// Resolve's return value directly.
func writeFile(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %q: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
	return path
}
