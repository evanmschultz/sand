// Package backends — hermetic CODEX_HOME helper.
//
// This file provides newHermeticCodexHome, a package-private helper
// that mirrors the bash oracle (bin/agent-dispatch.sh:521-547).
//
// The hermetic CODEX_HOME is a throwaway temp directory that contains
// ONLY the auth + identity symlinks codex needs to authenticate, plus
// an execpolicy rules file that forbids all git-mutation verbs and any
// caller-supplied bash_deny patterns. This keeps codex's global
// skills/, hooks, memories/, ambient-suggestions/, AGENTS.md etc.
// absent from every dispatch — the persona body + injected MCP
// servers are the agent's entire world.
//
// This is a HELPER ONLY. Wiring it into codexExecBackend.Spawn lives
// in the sibling droplet a3_spawn_wiring — do not call it from here.
package backends

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// gitMutationVerbs is the canonical list of git sub-commands that the
// orchestrator forbids dispatched agents from running. The list mirrors
// bin/agent-dispatch.sh:533 verbatim — any divergence from the oracle
// is a security regression.
var gitMutationVerbs = []string{
	"commit", "push", "add", "reset", "rebase", "merge",
	"checkout", "branch", "tag", "stash", "restore",
	"cherry-pick", "am", "clean", "switch", "rm", "mv",
	"update-ref", "gc", "prune", "worktree", "submodule",
	"init", "clone", "fetch", "pull", "remote", "apply",
}

// codexIdentityFiles is the set of ~/.codex/* files that codex needs
// for auth / identity. We symlink them — not copy — so that key
// rotation by the user propagates without requiring a new hermetic dir.
var codexIdentityFiles = []string{
	"auth.json",
	"version.json",
	"installation_id",
	"models_cache.json",
}

// newHermeticCodexHome creates a throwaway CODEX_HOME directory that
// contains only the auth/identity symlinks codex needs plus a rules
// file that forbids git-mutation verbs and any extra bash-deny
// patterns supplied by the caller.
//
// Behavior (mirrors bin/agent-dispatch.sh:521-547):
//
//  1. os.MkdirTemp creates the directory.
//  2. For each file in codexIdentityFiles: if ~/.codex/<file> exists,
//     os.Symlink it into <dir>/<file>. Missing source files are skipped
//     silently (matches oracle "[[ -e ... ]] && ln -s ...").
//  3. os.MkdirAll creates <dir>/rules/.
//  4. <dir>/rules/default.rules is written with:
//     - One prefix_rule line per git-mutation verb (gitMutationVerbs).
//     - One prefix_rule line per entry in bashDenyPatterns that does
//     NOT start with "git " and is not equal to "git" (those are
//     already covered by the verb block above).
//  5. Returns (dir, cleanup, nil). cleanup calls os.RemoveAll(dir) and
//     ignores the error — callers MUST defer it.
//
// Returns a non-nil error only when the temp dir or rules dir cannot be
// created, or when the rules file cannot be written. On error, any
// partially-created dir is cleaned up before returning.
func newHermeticCodexHome(bashDenyPatterns []string) (dir string, cleanup func(), err error) {
	dir, err = os.MkdirTemp("", "codex-hermetic.*")
	if err != nil {
		return "", func() {}, fmt.Errorf("backends: create hermetic codex home: %w", err)
	}

	// cleanup is always valid after MkdirTemp succeeds.
	cleanup = func() { _ = os.RemoveAll(dir) }

	// Symlink auth/identity files from ~/.codex/ into the hermetic dir.
	// We use os.UserHomeDir so the function works correctly even when
	// HOME is overridden in tests via t.Setenv.
	home, homeErr := os.UserHomeDir()
	if homeErr != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("backends: resolve HOME for codex identity files: %w", homeErr)
	}

	for _, name := range codexIdentityFiles {
		src := filepath.Join(home, ".codex", name)
		// Skip files that don't exist in the user's ~/.codex — oracle
		// behaviour: [[ -e "${HOME}/.codex/${f}" ]] && ln -s ...
		if _, statErr := os.Stat(src); os.IsNotExist(statErr) {
			continue
		}
		dst := filepath.Join(dir, name)
		if symlinkErr := os.Symlink(src, dst); symlinkErr != nil {
			cleanup()
			return "", func() {}, fmt.Errorf("backends: symlink %s into hermetic dir: %w", name, symlinkErr)
		}
	}

	// Create the rules sub-directory.
	rulesDir := filepath.Join(dir, "rules")
	if mkdirErr := os.MkdirAll(rulesDir, 0o700); mkdirErr != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("backends: create hermetic rules dir: %w", mkdirErr)
	}

	// Build rules/default.rules content.
	var sb strings.Builder

	// One line per git-mutation verb.
	for _, verb := range gitMutationVerbs {
		fmt.Fprintf(&sb, "prefix_rule(pattern=[\"git\", \"%s\"], decision=\"forbidden\")\n", verb)
	}

	// One line per non-git bashDenyPatterns entry. The oracle mirrors:
	//   case "${pat}" in git\ *|git) continue ;; esac
	//   tokenize → emit prefix_rule(pattern=[<tokens>], decision="forbidden")
	for _, pat := range bashDenyPatterns {
		if pat == "" {
			continue
		}
		// Skip patterns that are bare "git" or start with "git ".
		if pat == "git" || strings.HasPrefix(pat, "git ") {
			continue
		}
		// Tokenize on whitespace — oracle splits via python3 stdin.read().split().
		tokens := strings.Fields(pat)
		if len(tokens) == 0 {
			continue
		}
		// Build the quoted token list: "tok1", "tok2", ...
		quotedTokens := make([]string, len(tokens))
		for i, tok := range tokens {
			quotedTokens[i] = `"` + tok + `"`
		}
		fmt.Fprintf(&sb, "prefix_rule(pattern=[%s], decision=\"forbidden\")\n",
			strings.Join(quotedTokens, ", "))
	}

	rulesPath := filepath.Join(rulesDir, "default.rules")
	if writeErr := os.WriteFile(rulesPath, []byte(sb.String()), 0o600); writeErr != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("backends: write hermetic rules file: %w", writeErr)
	}

	return dir, cleanup, nil
}
