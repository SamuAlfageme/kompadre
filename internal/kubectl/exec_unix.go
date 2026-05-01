//go:build !windows

package kubectl

import (
	"os/exec"
	"syscall"
)

func detachChildProcGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// New session so the shell does not share the Bubble Tea / terminal foreground
	// process group (avoids zsh "suspended (tty input)" stopping kompadre).
	cmd.SysProcAttr.Setsid = true
}
