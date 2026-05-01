package kubectl

import (
	"os"
	"path/filepath"
	"strings"
)

// noJobControlPrefix turns off job control in an interactive zsh/bash so a subshell run
// from a full-screen TUI does not stop the parent app as "suspended (tty input)".
func noJobControlPrefix(shellBase string) string {
	switch shellBase {
	case "zsh":
		// MONITOR enables job control; turn it off for subshells run from the TUI.
		// Do not unset ZLE here — it can error with "can't change option: zle" and pollute stderr.
		return "unsetopt MONITOR 2>/dev/null; "
	case "bash":
		return "set +m 2>/dev/null; "
	default:
		return ""
	}
}

// ShellArgv returns the shell executable and flags plus one argument: the command string to run.
// Uses an interactive shell when possible so ~/.zshrc aliases (e.g. k→kubectl) apply.
func ShellArgv(command string) (shell string, args []string) {
	if command == "" {
		return "sh", []string{"-c", ""}
	}
	if p := os.Getenv("KOMPADRE_SHELL"); p != "" {
		// Full shell invocation, e.g. "/bin/zsh -o extendedglob -ic"
		fields := strings.Fields(p)
		if len(fields) > 0 {
			sh := fields[0]
			prefix := noJobControlPrefix(filepath.Base(sh))
			rest := append(fields[1:], prefix+command)
			return sh, rest
		}
	}

	sh := os.Getenv("SHELL")
	if sh == "" {
		return "sh", []string{"-c", command}
	}

	base := filepath.Base(sh)
	prefix := noJobControlPrefix(base)
	switch base {
	case "zsh":
		flag := "-ic"
		if os.Getenv("KOMPADRE_LOGIN_SHELL") == "1" {
			flag = "-ilc"
		}
		return sh, []string{flag, prefix + command}
	case "bash":
		flag := "-ic"
		if os.Getenv("KOMPADRE_LOGIN_SHELL") == "1" {
			flag = "-ilc"
		}
		return sh, []string{flag, prefix + command}
	default:
		return "sh", []string{"-c", command}
	}
}
