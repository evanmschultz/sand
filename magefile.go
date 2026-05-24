//go:build mage

// Magefile for the sand module. Defines exported targets discoverable by mage.
//
// Top-level gate is `mage check` which runs FmtCheck (gofumpt -l), Vet, Test,
// and Tidy. NEVER invoke raw `gofmt`, `gofumpt`, `go test`, `go vet`, or `go
// mod tidy` from dispatched roles — always route through these mage targets.
// Orchestrators are the only callers permitted to bypass this rule.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/magefile/mage/sh"

	"github.com/evanmschultz/sand/internal/installseed"
)

// Install builds the sand binary from ./cmd/sand and places it at
// ~/.local/bin/sand. The destination directory is created if missing.
//
// Install ALSO seeds ~/.config/sand/backends.toml from the packaged baseline
// on first install. Existing user files are NEVER overwritten — see
// internal/installseed for the seed contents and non-overwrite invariant.
// The seed step runs alongside the binary build, not in place of it; a seed
// failure aborts Install with a wrapped error.
func Install() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("install: resolve user home dir: %w", err)
	}

	outDir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("install: ensure %s exists: %w", outDir, err)
	}

	outPath := filepath.Join(outDir, "sand")
	if err := sh.Run("go", "build", "-o", outPath, "./cmd/sand"); err != nil {
		return fmt.Errorf("install: go build ./cmd/sand -> %s: %w", outPath, err)
	}

	if err := installseed.Seed(home); err != nil {
		return fmt.Errorf("install: seed backends.toml: %w", err)
	}

	if err := installseed.SeedChains(home); err != nil {
		return fmt.Errorf("install: seed chains.toml: %w", err)
	}

	return nil
}

// Fmt formats sources in place via gofumpt (latest). Auto-installs gofumpt to
// GOBIN if missing so contributors don't have to manage it out of band.
// gofumpt is a strict superset of gofmt -s: every gofmt -s fix plus tighter
// standards. NEVER invoke gofmt or raw gofumpt directly; always route through
// `mage fmt` / `mage fmtCheck`.
func Fmt() error {
	if err := ensureGofumpt(); err != nil {
		return err
	}
	return sh.RunV("gofumpt", "-w", ".")
}

// FmtCheck fails if any file is not gofumpt-clean. Listing produced by
// `gofumpt -l`; non-empty output = drift; emits the offending paths to stderr.
func FmtCheck() error {
	if err := ensureGofumpt(); err != nil {
		return err
	}
	out, err := sh.Output("gofumpt", "-l", ".")
	if err != nil {
		return fmt.Errorf("fmtCheck: gofumpt -l .: %w", err)
	}
	if strings.TrimSpace(out) != "" {
		fmt.Fprintln(os.Stderr, out)
		return fmt.Errorf("fmtCheck: files are not gofumpt-clean (run `mage fmt`)")
	}
	return nil
}

// Vet runs `go vet ./...`.
func Vet() error {
	if err := sh.RunV("go", "vet", "./..."); err != nil {
		return fmt.Errorf("vet: go vet ./...: %w", err)
	}
	return nil
}

// Tidy runs `go mod tidy` and fails if go.mod or go.sum changed. Ensures the
// committed module manifest stays in sync with the import graph.
func Tidy() error {
	before, err := snapshot("go.mod", "go.sum")
	if err != nil {
		return fmt.Errorf("tidy: snapshot before: %w", err)
	}
	if err := sh.RunV("go", "mod", "tidy"); err != nil {
		return fmt.Errorf("tidy: go mod tidy: %w", err)
	}
	after, err := snapshot("go.mod", "go.sum")
	if err != nil {
		return fmt.Errorf("tidy: snapshot after: %w", err)
	}
	if before != after {
		return fmt.Errorf("tidy: go.mod or go.sum changed; commit the tidy result")
	}
	return nil
}

// Check is the composite commit gate: FmtCheck, Vet, Test, Tidy. Any failing
// step aborts and surfaces the underlying error.
func Check() error {
	for _, step := range []struct {
		name string
		run  func() error
	}{
		{"fmtCheck", FmtCheck},
		{"vet", Vet},
		{"test", Test},
		{"tidy", Tidy},
	} {
		if err := step.run(); err != nil {
			return fmt.Errorf("check: %s: %w", step.name, err)
		}
	}
	return nil
}

// ensureGofumpt makes `gofumpt` resolvable on PATH by installing the latest
// from upstream when missing. Idempotent — `go install` against an
// already-current binary is a no-op.
func ensureGofumpt() error {
	if _, err := exec.LookPath("gofumpt"); err == nil {
		return nil
	}
	return sh.RunV("go", "install", "mvdan.cc/gofumpt@latest")
}

// snapshot reads the given paths and concatenates their contents with path
// separators between, returning a single string suitable for equality
// comparison. Used by Tidy to detect go.mod / go.sum drift.
func snapshot(paths ...string) (string, error) {
	var b strings.Builder
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return "", fmt.Errorf("snapshot %s: %w", p, err)
		}
		b.WriteString(p)
		b.WriteByte('\n')
		b.Write(data)
		b.WriteByte('\n')
	}
	return b.String(), nil
}
