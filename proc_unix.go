//go:build unix

package atp

import (
	"os"
	"os/exec"
	"syscall"
)

// configureProcess puts the child in its own process group so Stop can signal
// the whole tree. Without this, "go run ./cmd/x" would leave the compiled
// grandchild running after the launcher exits.
func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcess signals the child's process group, escalating to SIGKILL when
// force is set.
func killProcess(p *os.Process, force bool) error {
	if p == nil {
		return nil
	}
	sig := syscall.SIGTERM
	if force {
		sig = syscall.SIGKILL
	}
	if err := syscall.Kill(-p.Pid, sig); err == nil {
		return nil
	}
	if force {
		return p.Kill()
	}
	return p.Signal(sig)
}
