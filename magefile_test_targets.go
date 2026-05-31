//go:build mage

// Test targets for the sand module. Companion to magefile.go; mage discovers
// exported targets across every file in package main carrying the //go:build
// mage tag, so this file contributes the Test*/Race* family without disturbing
// Install or CI.
//
// Canonical 12-target shape (per tillsyn P6) — the Test/TestPkg/TestFunc/Race/
// RacePkg targets here keep their names identical to every sibling project so
// dispatched agents always know the gate name.
package main

import (
	"fmt"
	"strings"

	"github.com/magefile/mage/sh"
)

// Test runs `go test ./...` against every package in the module (no race, no
// coverage). Closeout/orchestrator surface — fastest all-package gate. Output
// is streamed via sh.RunV so test progress is visible in real time.
func Test() error {
	if err := sh.RunV("go", "test", "./..."); err != nil {
		return fmt.Errorf("test: go test ./...: %w", err)
	}
	return nil
}

// TestPkg runs `go test -count=1 <pkg>` against ONE caller-supplied package
// path (no race). Plan-QA read-only surface — verifies a code claim against a
// single package without race overhead. The caller passes the package path
// as-given (e.g. `./internal/dispatch` or a full import path). Output is
// streamed via sh.RunV.
func TestPkg(pkg string) error {
	pkg = strings.TrimSpace(pkg)
	if pkg == "" {
		return fmt.Errorf("testPkg: package path required (e.g. mage testPkg ./internal/dispatch)")
	}
	if err := sh.RunV("go", "test", "-count=1", pkg); err != nil {
		return fmt.Errorf("testPkg: go test -count=1 %s: %w", pkg, err)
	}
	return nil
}

// TestFunc runs `go test -run "^<testName>$" -race -count=1 <pkg>` — ONE named
// test function in ONE package path with race detection and no result caching.
// Builder + build-QA surface — verifies a builder's just-edited test func
// without sibling pollution.
//
// Signature is (pkg, testName) per the canonical 12-target shape (2026-05-30);
// the prior 1-arg (pattern) + TA_TEST_PKG env form is retired so the contract
// matches every sibling project. Output is streamed via sh.RunV.
func TestFunc(pkg, testName string) error {
	pkg = strings.TrimSpace(pkg)
	testName = strings.TrimSpace(testName)
	if pkg == "" {
		return fmt.Errorf("testFunc: package path required (e.g. mage testFunc ./internal/dispatch TestMyThing)")
	}
	if testName == "" {
		return fmt.Errorf("testFunc: test function name required (e.g. mage testFunc ./internal/dispatch TestMyThing)")
	}
	runPattern := "^" + testName + "$"
	if err := sh.RunV("go", "test", "-run", runPattern, "-race", "-count=1", pkg); err != nil {
		return fmt.Errorf("testFunc: go test -run %s -race -count=1 %s: %w", runPattern, pkg, err)
	}
	return nil
}

// Race runs `go test -race ./...` against every package. Closeout/orchestrator
// surface. Use RacePkg for one package.
func Race() error {
	if err := sh.RunV("go", "test", "-race", "./..."); err != nil {
		return fmt.Errorf("race: go test -race ./...: %w", err)
	}
	return nil
}

// RacePkg runs `go test -race -count=1 <pkg>` against ONE package path.
// Build-QA surface — verifies a builder's just-shipped package is race-clean.
func RacePkg(pkg string) error {
	pkg = strings.TrimSpace(pkg)
	if pkg == "" {
		return fmt.Errorf("racePkg: package path required (e.g. mage racePkg ./internal/dispatch)")
	}
	if err := sh.RunV("go", "test", "-race", "-count=1", pkg); err != nil {
		return fmt.Errorf("racePkg: go test -race -count=1 %s: %w", pkg, err)
	}
	return nil
}
