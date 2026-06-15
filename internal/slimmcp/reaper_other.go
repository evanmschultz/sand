//go:build !unix

package slimmcp

import "os/exec"

// configureProcAttr is a no-op on non-unix platforms.
func configureProcAttr(cmd *exec.Cmd) {}

// killProcessGroup is a no-op on non-unix platforms.
func killProcessGroup(pid int) {}
