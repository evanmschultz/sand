//go:build mage

// Test targets for the sand module. Companion to magefile.go; mage discovers
// exported targets across every file in package main carrying the //go:build
// mage tag, so this file contributes the Test* family without disturbing
// Install or Check.
//
// This file currently provides only the module-wide Test target. Sibling
// droplets add TestPkg (package-scoped) and TestFunc (function-scoped) in
// subsequent extensions of this same file.
package main

import (
	"fmt"
	"os"

	"github.com/magefile/mage/sh"
)

// Test runs `go test ./...` against every package in the module. It is the
// module-level test gate used by orchestrator-level QA and by `mage Check`'s
// downstream consumers. Output is streamed via sh.RunV so test progress is
// visible in real time.
func Test() error {
	if err := sh.RunV("go", "test", "./..."); err != nil {
		return fmt.Errorf("test: go test ./...: %w", err)
	}
	return nil
}

// TestPkg runs `go test ./<pkg>` against a single caller-supplied package
// path. The caller passes the package path WITHOUT a `./` prefix (e.g.
// `cmd/sand` or `internal/dispatch`); TestPkg prepends `./` internally so the
// resulting target is a relative package pattern that `go test` resolves
// against the module root. This is the package-scoped gate used by builder
// and QA personas under the cascade-isolation rule (test only your slice).
// Output is streamed via sh.RunV.
func TestPkg(pkg string) error {
	target := "./" + pkg
	if err := sh.RunV("go", "test", target); err != nil {
		return fmt.Errorf("testPkg: go test %s: %w", target, err)
	}
	return nil
}

// TestFunc runs `go test -run <pattern> <pkg>` where pattern is a Go regex
// matched against test function names and pkg is taken from the TA_TEST_PKG
// environment variable (defaulting to `./...` when unset). This is the
// function-scoped gate used by builder and QA personas under the
// cascade-isolation rule — a builder operating below package level runs only
// the test(s) covering their slice. Output is streamed via sh.RunV so test
// progress is visible in real time.
func TestFunc(pattern string) error {
	pkg := os.Getenv("TA_TEST_PKG")
	if pkg == "" {
		pkg = "./..."
	}
	if err := sh.RunV("go", "test", "-run", pattern, pkg); err != nil {
		return fmt.Errorf("testFunc: go test -run %s %s: %w", pattern, pkg, err)
	}
	return nil
}
