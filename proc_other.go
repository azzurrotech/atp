//go:build !unix

package atp

import (
	"os"
	"os/exec"
)

// configureProcess is a no-op on platforms without process groups.
func configureProcess(cmd *exec.Cmd) {}

// killProcess asks the child to stop, or kills it when force is set.
func killProcess(p *os.Process, force bool) error {
	if p == nil {
		return nil
	}
	if force {
		return p.Kill()
	}
	return p.Signal(os.Interrupt)
}
