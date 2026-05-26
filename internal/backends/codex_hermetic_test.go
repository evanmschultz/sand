// Tests for the newHermeticCodexHome helper (codex_hermetic.go).
//
// These tests mirror the oracle (bin/agent-dispatch.sh:521-547) and
// the QA-derived F9 amendment: the symlink sub-test seeds a temp HOME
// with the four ~/.codex/* identity files and asserts each corresponding
// path inside the returned hermetic dir is a SYMLINK before cleanup.
package backends

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNewHermeticCodexHome_DirCreated verifies that the returned dir
// path exists as an actual directory on the filesystem immediately
// after the call.
func TestNewHermeticCodexHome_DirCreated(t *testing.T) {
	t.Parallel()

	dir, cleanup, err := newHermeticCodexHome(nil)
	if err != nil {
		t.Fatalf("newHermeticCodexHome: unexpected error: %v", err)
	}
	t.Cleanup(cleanup)

	fi, statErr := os.Stat(dir)
	if statErr != nil {
		t.Fatalf("returned dir %q does not exist: %v", dir, statErr)
	}
	if !fi.IsDir() {
		t.Fatalf("returned path %q is not a directory", dir)
	}
}

// TestNewHermeticCodexHome_RulesFile verifies that rules/default.rules
// exists inside the hermetic dir and contains the mandatory git-verb
// prefix_rule lines. We assert at minimum commit and push (load-bearing
// orchestrator-is-sole-committer constraint) plus a sample from the
// middle of the list (rebase) to confirm the full verb list was written.
func TestNewHermeticCodexHome_RulesFile(t *testing.T) {
	t.Parallel()

	dir, cleanup, err := newHermeticCodexHome(nil)
	if err != nil {
		t.Fatalf("newHermeticCodexHome: unexpected error: %v", err)
	}
	t.Cleanup(cleanup)

	rulesPath := filepath.Join(dir, "rules", "default.rules")
	raw, readErr := os.ReadFile(rulesPath)
	if readErr != nil {
		t.Fatalf("rules/default.rules not found at %q: %v", rulesPath, readErr)
	}
	content := string(raw)

	// Full oracle verb list — every verb must produce a line.
	wantVerbs := []string{
		"commit", "push", "add", "reset", "rebase", "merge",
		"checkout", "branch", "tag", "stash", "restore",
		"cherry-pick", "am", "clean", "switch", "rm", "mv",
		"update-ref", "gc", "prune", "worktree", "submodule",
		"init", "clone", "fetch", "pull", "remote", "apply",
	}
	for _, verb := range wantVerbs {
		want := `prefix_rule(pattern=["git", "` + verb + `"], decision="forbidden")`
		if !strings.Contains(content, want) {
			t.Errorf("rules/default.rules missing line for verb %q\nwant substring: %s\ngot:\n%s", verb, want, content)
		}
	}
}

// TestNewHermeticCodexHome_ExtraDenyPatterns verifies that non-git
// entries in bashDenyPatterns are tokenized and emitted as additional
// prefix_rule lines, and that entries starting with "git " or equal to
// "git" are skipped (already covered by the git-verb block).
func TestNewHermeticCodexHome_ExtraDenyPatterns(t *testing.T) {
	t.Parallel()

	patterns := []string{
		"mage install", // two-token non-git → should appear
		"go get",       // two-token non-git → should appear
		"git commit",   // starts with "git " → must NOT produce a duplicate
		"git",          // bare "git" → must NOT produce a line
		"go mod tidy",  // three-token non-git → should appear
	}

	dir, cleanup, err := newHermeticCodexHome(patterns)
	if err != nil {
		t.Fatalf("newHermeticCodexHome: unexpected error: %v", err)
	}
	t.Cleanup(cleanup)

	rulesPath := filepath.Join(dir, "rules", "default.rules")
	raw, readErr := os.ReadFile(rulesPath)
	if readErr != nil {
		t.Fatalf("rules/default.rules not found: %v", readErr)
	}
	content := string(raw)

	// "mage install" → prefix_rule(pattern=["mage", "install"], ...)
	if !strings.Contains(content, `prefix_rule(pattern=["mage", "install"], decision="forbidden")`) {
		t.Errorf(`rules file missing line for "mage install":\n%s`, content)
	}

	// "go get" → prefix_rule(pattern=["go", "get"], ...)
	if !strings.Contains(content, `prefix_rule(pattern=["go", "get"], decision="forbidden")`) {
		t.Errorf(`rules file missing line for "go get":\n%s`, content)
	}

	// "go mod tidy" → three tokens
	if !strings.Contains(content, `prefix_rule(pattern=["go", "mod", "tidy"], decision="forbidden")`) {
		t.Errorf(`rules file missing line for "go mod tidy":\n%s`, content)
	}

	// "git commit" must NOT produce a SECOND prefix_rule beyond the canonical one.
	// We count occurrences of the commit line — must be exactly 1.
	commitLine := `prefix_rule(pattern=["git", "commit"], decision="forbidden")`
	count := strings.Count(content, commitLine)
	if count != 1 {
		t.Errorf("expected exactly 1 occurrence of git-commit rule, got %d", count)
	}

	// "git" bare must not introduce a bare-git line with no verb.
	bareLine := `prefix_rule(pattern=["git"], decision="forbidden")`
	if strings.Contains(content, bareLine) {
		t.Errorf("unexpected bare-git rule found in rules file:\n%s", content)
	}
}

// TestNewHermeticCodexHome_Cleanup verifies that the cleanup function
// removes the hermetic dir from the filesystem.
func TestNewHermeticCodexHome_Cleanup(t *testing.T) {
	t.Parallel()

	dir, cleanup, err := newHermeticCodexHome(nil)
	if err != nil {
		t.Fatalf("newHermeticCodexHome: unexpected error: %v", err)
	}

	// Confirm dir exists before cleanup.
	if _, statErr := os.Stat(dir); statErr != nil {
		t.Fatalf("dir %q should exist before cleanup: %v", dir, statErr)
	}

	cleanup()

	// After cleanup the dir must be gone.
	if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
		t.Errorf("dir %q still exists after cleanup (statErr=%v)", dir, statErr)
	}
}

// TestNewHermeticCodexHome_Symlinks is the F9-mandated focused test.
// It seeds a synthetic HOME directory containing all four ~/.codex/*
// identity files, sets HOME via t.Setenv (auto-restored after test),
// calls newHermeticCodexHome, and asserts each corresponding path
// inside the returned dir is a SYMLINK (os.Lstat + ModeSymlink) —
// BEFORE cleanup runs. All four file names are covered explicitly.
//
// NOTE: t.Setenv is incompatible with t.Parallel() — this test is
// intentionally sequential (Go testing package panics otherwise).
func TestNewHermeticCodexHome_Symlinks(t *testing.T) {
	// Build a synthetic HOME with all four identity files.
	fakeHome := t.TempDir()
	codexDir := filepath.Join(fakeHome, ".codex")
	if err := os.MkdirAll(codexDir, 0o700); err != nil {
		t.Fatalf("create fake .codex dir: %v", err)
	}

	identityFiles := []string{
		"auth.json",
		"version.json",
		"installation_id",
		"models_cache.json",
	}
	for _, name := range identityFiles {
		p := filepath.Join(codexDir, name)
		if err := os.WriteFile(p, []byte(`{"test":true}`), 0o600); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}

	// Override HOME so newHermeticCodexHome resolves ~/.codex/* against
	// our synthetic tree. t.Setenv restores the original value on cleanup.
	t.Setenv("HOME", fakeHome)

	dir, cleanup, err := newHermeticCodexHome(nil)
	if err != nil {
		t.Fatalf("newHermeticCodexHome: unexpected error: %v", err)
	}
	// Defer cleanup AFTER all assertions — cleanup removes dir.
	defer cleanup()

	for _, name := range identityFiles {
		target := filepath.Join(dir, name)
		fi, lstatErr := os.Lstat(target)
		if lstatErr != nil {
			t.Errorf("identity file %q not found in hermetic dir: %v", name, lstatErr)
			continue
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("identity file %q is not a symlink (mode=%v)", name, fi.Mode())
		}
	}
}
