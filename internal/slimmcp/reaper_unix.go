//go:build unix

package slimmcp

import (
	"os/exec"
	"syscall"
)

// configureProcAttr makes the child its own process-group leader so the whole
// tree (the upstream and anything it spawns) can be killed at once.
func configureProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup force-kills the process group led by pid (best-effort).
func killProcessGroup(pid int) {
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}
