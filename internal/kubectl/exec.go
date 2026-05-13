package kubectl

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
)

// filterShellNoise drops lines that are noise from zsh startup or kubectl config warnings.
func filterShellNoise(s string) string {
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	out := lines[:0]
	skip := false
	for _, ln := range lines {
		if strings.Contains(ln, "can't change option: zle") {
			continue
		}
		// kubectl prints multi-line config permission warnings; drop them.
		if strings.Contains(ln, "WARNING: Kubernetes configuration file is") {
			skip = true
			continue
		}
		if skip {
			// The warning continuation lines ("insecure. Location: ..." or blank).
			trimmed := strings.TrimSpace(ln)
			if trimmed == "" || strings.HasPrefix(trimmed, "insecure") || strings.HasPrefix(trimmed, "Location:") {
				continue
			}
			skip = false
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
	return filterShellNoise(outBuf.String()), filterShellNoise(errBuf.String()), err
}

// FormatOutput merges stdout/stderr/err from a single RunShell call into one string,
// matching the layout the TUI shows in its panes (stdout first, then stderr, with the
// error message used only when both buffers are empty).
func FormatOutput(stdout, stderr string, err error) string {
	var b strings.Builder
	if stdout != "" {
		b.WriteString(stdout)
	}
	if stderr != "" {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(stderr)
	}
	if err != nil && b.Len() == 0 {
		b.WriteString(err.Error())
	}
	return b.String()
}
