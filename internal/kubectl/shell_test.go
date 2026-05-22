package kubectl

import "testing"

func TestNoJobControlPrefix(t *testing.T) {
	tests := []struct {
		shell string
		want  string
	}{
		{"zsh", "unsetopt MONITOR 2>/dev/null; "},
		{"bash", "set +m 2>/dev/null; "},
		{"fish", ""},
		{"sh", ""},
		{"", ""},
	}
	for _, tc := range tests {
		got := noJobControlPrefix(tc.shell)
		if got != tc.want {
			t.Errorf("noJobControlPrefix(%q) = %q, want %q", tc.shell, got, tc.want)
		}
	}
}

func TestShellArgv_EmptyCommand(t *testing.T) {
	sh, args := ShellArgv("")
	if sh != "sh" || len(args) != 2 || args[0] != "-c" || args[1] != "" {
		t.Errorf("ShellArgv empty: got %q %v", sh, args)
	}
}

func TestShellArgv_KompadreShellOverride(t *testing.T) {
	t.Setenv("KOMPADRE_SHELL", "/usr/local/bin/zsh -o extendedglob -ic")
	t.Setenv("SHELL", "/bin/bash") // should be ignored when KOMPADRE_SHELL is set

	sh, args := ShellArgv("kubectl get pods")
	if sh != "/usr/local/bin/zsh" {
		t.Errorf("KOMPADRE_SHELL shell: got %q", sh)
	}
	if len(args) < 1 {
		t.Fatalf("KOMPADRE_SHELL args empty")
	}
	last := args[len(args)-1]
	if last == "kubectl get pods" {
		t.Error("expected job-control prefix in command arg")
	}
}

func TestShellArgv_ZshInteractive(t *testing.T) {
	t.Setenv("KOMPADRE_SHELL", "")
	t.Setenv("SHELL", "/bin/zsh")
	t.Setenv("KOMPADRE_LOGIN_SHELL", "")

	sh, args := ShellArgv("echo hi")
	if sh != "/bin/zsh" {
		t.Errorf("zsh shell: got %q", sh)
	}
	if len(args) != 2 {
		t.Fatalf("zsh args len: got %d", len(args))
	}
	if args[0] != "-ic" {
		t.Errorf("zsh flag: got %q", args[0])
	}
}

func TestShellArgv_LoginShell(t *testing.T) {
	t.Setenv("KOMPADRE_SHELL", "")
	t.Setenv("SHELL", "/bin/bash")
	t.Setenv("KOMPADRE_LOGIN_SHELL", "1")

	_, args := ShellArgv("echo hi")
	if len(args) < 1 {
		t.Fatal("no args")
	}
	if args[0] != "-ilc" {
		t.Errorf("login shell flag: got %q, want -ilc", args[0])
	}
}

func TestShellArgv_FallbackSh(t *testing.T) {
	t.Setenv("KOMPADRE_SHELL", "")
	t.Setenv("SHELL", "")

	sh, args := ShellArgv("echo hi")
	if sh != "sh" {
		t.Errorf("fallback shell: got %q", sh)
	}
	if len(args) != 2 || args[0] != "-c" {
		t.Errorf("fallback args: got %v", args)
	}
}
