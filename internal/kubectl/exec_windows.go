//go:build windows

package kubectl

import "os/exec"

func detachChildProcGroup(cmd *exec.Cmd) {}
