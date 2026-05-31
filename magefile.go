//go:build mage

// Magefile for the sand module. Defines exported targets discoverable by mage.
//
// Top-level gate is `mage ci` which runs FormatCheck (gofumpt -l), Vet, Cover
// (race+cover combined), and Tidy. NEVER invoke raw `gofmt`, `gofumpt`, `go
// test`, `go vet`, or `go mod tidy` from dispatched roles — always route
// through these mage targets. Orchestrators are the only callers permitted to
// bypass this rule.
//
// Canonical 12-target shape (per tillsyn P6 — naming MUST stay identical across
// all sibling projects so dispatched agents always know the gate name):
//
//	TestFunc(pkg, fn)  builder + build-QA       go test -run "^<Func>$" -count=1 -race <pkg>
//	TestPkg(pkg)       plan-QA read-only        go test -count=1 <pkg>
//	Test               closeout/orch            go test ./...
//	RacePkg(pkg)       build-QA                 go test -race -count=1 <pkg>
//	Race               closeout/orch            go test -race ./...
//	FormatFile(file)   builder + build-QA       gofumpt -w <file>
//	Format             closeout/orch            gofumpt -w .
//	FormatCheck        ci                       gofumpt -l . && fail if non-empty
//	VetPkg(pkg)        builder + build-QA       go vet <pkg>
//	Vet                closeout/orch            go vet ./...
//	Tidy               orch-only                go mod tidy + diff-exit-code
//	CI                 closeout/orch            FormatCheck + Vet + Cover + Tidy
//
// sand-specific extras preserved: Install (binary + backends/chains seed).
// Test* + Race* live in magefile_test_targets.go.
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

// Aliases preserves the familiar hyphenated task names while keeping the visible target list small.
var Aliases = map[string]interface{}{
	"check":        CI,
	"fmt":          Format,
	"fmt-check":    FormatCheck,
	"format-check": FormatCheck,
	"format-file":  FormatFile,
	"test-func":    TestFunc,
	"test-pkg":     TestPkg,
	"race-pkg":     RacePkg,
	"vet-pkg":      VetPkg,
}

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

// Format formats sources in place via gofumpt (latest). Auto-installs gofumpt to
// GOBIN if missing so contributors don't have to manage it out of band.
// gofumpt is a strict superset of gofmt -s: every gofmt -s fix plus tighter
// standards. NEVER invoke gofmt or raw gofumpt directly; always route through
// `mage format` / `mage formatCheck`.
//
// Renamed from `Fmt` to `Format` per the canonical 12-target shape (2026-05-30).
// Hyphenated alias `fmt` preserved.
func Format() error {
	if err := ensureGofumpt(); err != nil {
		return err
	}
	return sh.RunV("gofumpt", "-w", ".")
}

// FormatFile rewrites ONE file (or directory) with gofumpt. Builder + build-QA
// surface — formats only the file(s) just edited.
func FormatFile(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("formatFile: path required (e.g. mage formatFile internal/dispatch/foo.go)")
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("formatFile: %w", err)
	}
	if err := ensureGofumpt(); err != nil {
		return err
	}
	return sh.RunV("gofumpt", "-w", path)
}

// FormatCheck fails if any file is not gofumpt-clean. Listing produced by
// `gofumpt -l`; non-empty output = drift; emits the offending paths to stderr.
//
// Renamed from `FmtCheck` per the canonical 12-target shape (2026-05-30).
// Hyphenated alias `fmt-check` / `format-check` preserved.
func FormatCheck() error {
	if err := ensureGofumpt(); err != nil {
		return err
	}
	out, err := sh.Output("gofumpt", "-l", ".")
	if err != nil {
		return fmt.Errorf("formatCheck: gofumpt -l .: %w", err)
	}
	if strings.TrimSpace(out) != "" {
		fmt.Fprintln(os.Stderr, out)
		return fmt.Errorf("formatCheck: files are not gofumpt-clean (run `mage format`)")
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

// VetPkg runs `go vet <pkg>` over ONE package path. Builder + build-QA surface.
func VetPkg(pkg string) error {
	if pkg == "" {
		return fmt.Errorf("vetPkg: package path required (e.g. mage vetPkg ./internal/dispatch)")
	}
	if err := sh.RunV("go", "vet", pkg); err != nil {
		return fmt.Errorf("vetPkg: go vet %s: %w", pkg, err)
	}
	return nil
}

// Cover runs the full suite with the race detector + coverage and prints a
// function-level coverage report. This is the CI gate's test stage — it
// exercises race-cleanliness and coverage in a single pass.
func Cover() error {
	if err := sh.RunV("go", "test", "-race", "-coverprofile=coverage.out", "./..."); err != nil {
		return fmt.Errorf("cover: go test -race -coverprofile: %w", err)
	}
	if err := sh.RunV("go", "tool", "cover", "-func=coverage.out"); err != nil {
		return fmt.Errorf("cover: go tool cover -func: %w", err)
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

// CI is the composite commit gate: FormatCheck, Vet, Cover (race+cover combined),
// Tidy. Any failing step aborts and surfaces the underlying error.
//
// Renamed from `Check` per the canonical 12-target shape (2026-05-30).
// Hyphenated alias `check` preserved.
func CI() error {
	for _, step := range []struct {
		name string
		run  func() error
	}{
		{"formatCheck", FormatCheck},
		{"vet", Vet},
		{"cover", Cover},
		{"tidy", Tidy},
	} {
		if err := step.run(); err != nil {
			return fmt.Errorf("ci: %s: %w", step.name, err)
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
