//go:build mage

// Magefile for the sand module. Defines exported targets discoverable by mage.
//
// This file currently provides the Install target; sibling droplets add Check
// and the Test* family in companion files within the same mage package.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/magefile/mage/sh"
)

// Install builds the sand binary from ./cmd/sand and places it at
// ~/.local/bin/sand. The destination directory is created if missing.
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

	return nil
}

// Check is the commit gate. It runs gofmt -l (formatting), go vet (static
// analysis), and go test against every package in the module. Any
// unformatted file, vet diagnostic, or test failure causes Check to fail.
func Check() error {
	// gofmt -l prints the names of files whose formatting differs from
	// gofmt's; it exits 0 even when offenders exist, so capture stdout and
	// fail if any file is listed.
	offenders, err := sh.Output("gofmt", "-l", ".")
	if err != nil {
		return fmt.Errorf("check: gofmt -l .: %w", err)
	}
	if strings.TrimSpace(offenders) != "" {
		return fmt.Errorf("check: gofmt found unformatted files:\n%s", offenders)
	}

	if err := sh.RunV("go", "vet", "./..."); err != nil {
		return fmt.Errorf("check: go vet ./...: %w", err)
	}

	if err := sh.RunV("go", "test", "./..."); err != nil {
		return fmt.Errorf("check: go test ./...: %w", err)
	}

	return nil
}
