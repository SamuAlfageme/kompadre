package kubectl

import (
	"errors"
	"testing"
)

func TestFilterShellNoise_ZleWarning(t *testing.T) {
	input := "line one\ncan't change option: zle\nline two"
	got := filterShellNoise(input)
	want := "line one\nline two"
	if got != want {
		t.Errorf("filterShellNoise zle:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestFilterShellNoise_KubeConfigWarning(t *testing.T) {
	input := "pods listed\nWARNING: Kubernetes configuration file is group-readable\ninsecure. Please fix\nLocation: /home/me/.kube/config\n\nmore output"
	got := filterShellNoise(input)
	want := "pods listed\nmore output"
	if got != want {
		t.Errorf("filterShellNoise kubeconfig warning:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestFilterShellNoise_Empty(t *testing.T) {
	if got := filterShellNoise(""); got != "" {
		t.Errorf("filterShellNoise empty: got %q", got)
	}
}

func TestFilterShellNoise_NoNoise(t *testing.T) {
	input := "clean\noutput"
	if got := filterShellNoise(input); got != input {
		t.Errorf("filterShellNoise no noise: got %q want %q", got, input)
	}
}

func TestFormatOutput_StdoutOnly(t *testing.T) {
	got := FormatOutput("hello", "", nil)
	if got != "hello" {
		t.Errorf("FormatOutput stdout only: got %q", got)
	}
}

func TestFormatOutput_StderrOnly(t *testing.T) {
	got := FormatOutput("", "warn", nil)
	if got != "warn" {
		t.Errorf("FormatOutput stderr only: got %q", got)
	}
}

func TestFormatOutput_Both(t *testing.T) {
	got := FormatOutput("out", "err", nil)
	if got != "out\nerr" {
		t.Errorf("FormatOutput both: got %q", got)
	}
}

func TestFormatOutput_ErrorFallback(t *testing.T) {
	got := FormatOutput("", "", errors.New("timeout"))
	if got != "timeout" {
		t.Errorf("FormatOutput error fallback: got %q", got)
	}
}

func TestFormatOutput_ErrorIgnoredWhenOutput(t *testing.T) {
	got := FormatOutput("data", "", errors.New("exit 1"))
	if got != "data" {
		t.Errorf("FormatOutput error with stdout: got %q", got)
	}
}
