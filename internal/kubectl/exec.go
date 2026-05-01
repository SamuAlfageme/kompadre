package kubectl

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
)

// filterZshRCNoise drops lines that zsh prints when ~/.zshrc runs in a no-TTY eval from
// `zsh -ic` (e.g. "can't change option: zle"). This is not from kubectl.
func filterZshRCNoise(s string) string {
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	out := lines[:0]
	for _, ln := range lines {
		if strings.Contains(ln, "can't change option: zle") {
			continue
		}
		out = append(out, ln)
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n")
}

// RunShell runs a command in an interactive user shell (zsh/bash -ic by default) with KUBECONFIG set
// so ~/.zshrc aliases (e.g. k=kubectl) apply. Override with KOMPADRE_SHELL or KOMPADRE_LOGIN_SHELL; see shell.go.
func RunShell(ctx context.Context, kubeconfigPath, command string) (stdout, stderr string, err error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return "", "", nil
	}

	shell, shellArgs := ShellArgv(command)
	cmd := exec.CommandContext(ctx, shell, shellArgs...)
	cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfigPath)
	detachChildProcGroup(cmd)

	devNull, derr := os.Open(os.DevNull)
	if derr != nil {
		cmd.Stdin = bytes.NewReader(nil)
	} else {
		defer devNull.Close()
		cmd.Stdin = devNull
	}
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err = cmd.Run()
	// zsh may print startup noise to stdout or stderr.
	return filterZshRCNoise(outBuf.String()), filterZshRCNoise(errBuf.String()), err
}
