package delta

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Diff runs delta on two text blobs. If the delta binary is missing, returns a simple unified diff.
func Diff(left, right string, termWidth int) (string, error) {
	leftPath, err := writeTemp("kompadre-left-", left)
	if err != nil {
		return "", err
	}
	defer os.Remove(leftPath)

	rightPath, err := writeTemp("kompadre-right-", right)
	if err != nil {
		return "", err
	}
	defer os.Remove(rightPath)

	deltaPath, lookErr := exec.LookPath("delta")
	if lookErr != nil {
		return fallbackDiff(leftPath, rightPath)
	}

	// Side-by-side needs reasonable width; delta splits columns.
	w := termWidth
	if w < 40 {
		w = 80
	}
	args := []string{
		"--side-by-side",
		fmt.Sprintf("--width=%d", w),
		leftPath,
		rightPath,
	}

	cmd := exec.Command(deltaPath, args...)
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	cmd.Stdin = bytes.NewReader(nil)
	err = cmd.Run()
	outStr := out.String()
	errStr := stderr.String()
	if len(errStr) > 2000 {
		errStr = errStr[:2000] + "…"
	}

	if err == nil {
		return sanitizeDiffOutput(outStr), nil
	}

	// delta follows diff(1)-style exit codes: 1 means the inputs differ (success with output).
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		if strings.TrimSpace(outStr) != "" {
			return sanitizeDiffOutput(outStr), nil
		}
		if strings.TrimSpace(errStr) != "" {
			return sanitizeDiffOutput(errStr), nil
		}
		// No output despite exit 1 — fall back to unified diff.
		return fallbackDiff(leftPath, rightPath)
	}

	if errStr == "" {
		return "", fmt.Errorf("delta: %w", err)
	}
	return "", fmt.Errorf("delta: %w: %s", err, strings.TrimSpace(errStr))
}

func writeTemp(prefix, content string) (string, error) {
	f, err := os.CreateTemp("", prefix+"*.txt")
	if err != nil {
		return "", err
	}
	path := f.Name()
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		os.Remove(path)
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}

func fallbackDiff(leftPath, rightPath string) (string, error) {
	cmd := exec.Command("diff", "-u", leftPath, rightPath)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	cmd.Stdin = bytes.NewReader(nil)
	_ = cmd.Run() // diff exits 1 when files differ
	return sanitizeDiffOutput(out.String()), nil
}

func sanitizeDiffOutput(s string) string {
	if s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	i := 0

	for ; i < len(lines); i++ {
		ln := lines[i]
		// Hide any wrapped temp-file header fragments from delta.
		if strings.Contains(ln, "kompadre-left-") || strings.Contains(ln, "kompadre-right-") {
			continue
		}
		if strings.HasPrefix(ln, `\ No newline at end of file`) {
			continue
		}
		// Hide fallback unified-diff temp-file headers too.
		if strings.HasPrefix(ln, "--- ") && strings.Contains(ln, "kompadre-left-") {
			continue
		}
		if strings.HasPrefix(ln, "+++ ") && strings.Contains(ln, "kompadre-right-") {
			continue
		}
		out = append(out, ln)
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n")
}
