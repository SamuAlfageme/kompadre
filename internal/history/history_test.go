package history

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func withTempHistory(t *testing.T) {
	t.Helper()
	tmp := t.TempDir()
	// On macOS, UserConfigDir uses $HOME/Library/Application Support.
	// On Linux it uses $XDG_CONFIG_HOME or $HOME/.config.
	// Override both to redirect history to temp.
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, ".config"))
}

func TestAppendAndLoad(t *testing.T) {
	withTempHistory(t)

	Append("kubectl get pods")
	Append("kubectl get svc")
	Append("kubectl get nodes")

	entries := Load()
	if len(entries) != 3 {
		t.Fatalf("Load count: got %d, want 3", len(entries))
	}
	if entries[0] != "kubectl get nodes" {
		t.Errorf("newest entry: got %q", entries[0])
	}
	if entries[2] != "kubectl get pods" {
		t.Errorf("oldest entry: got %q", entries[2])
	}
}

func TestAppend_DedupeConsecutive(t *testing.T) {
	withTempHistory(t)

	Append("cmd1")
	Append("cmd1")
	Append("cmd1")

	entries := Load()
	if len(entries) != 1 {
		t.Errorf("dedupe: got %d entries, want 1", len(entries))
	}
}

func TestAppend_NonConsecutiveDuplicatesKept(t *testing.T) {
	withTempHistory(t)

	Append("cmd1")
	Append("cmd2")
	Append("cmd1")

	entries := Load()
	if len(entries) != 3 {
		t.Errorf("non-consecutive: got %d entries, want 3", len(entries))
	}
}

func TestAppend_EmptyIgnored(t *testing.T) {
	withTempHistory(t)

	Append("")
	Append("   ")

	entries := Load()
	if len(entries) != 0 {
		t.Errorf("empty commands should be ignored: got %d entries", len(entries))
	}
}

func TestAppend_TrimWhitespace(t *testing.T) {
	withTempHistory(t)

	Append("  kubectl get pods  ")

	entries := Load()
	if len(entries) != 1 {
		t.Fatal("expected 1 entry")
	}
	if entries[0] != "kubectl get pods" {
		t.Errorf("not trimmed: got %q", entries[0])
	}
}

func TestLoad_EmptyFile(t *testing.T) {
	withTempHistory(t)
	entries := Load()
	if entries != nil {
		t.Errorf("Load with no file: got %v", entries)
	}
}

func TestFuzzyMatch_AllTokens(t *testing.T) {
	entries := []string{
		"kubectl get pods -A",
		"kubectl get svc",
		"kubectl describe pod nginx",
		"helm list",
	}
	got := FuzzyMatch(entries, "kubectl pods")
	if len(got) != 1 || got[0] != "kubectl get pods -A" {
		t.Errorf("FuzzyMatch 'kubectl pods': got %v", got)
	}
}

func TestFuzzyMatch_CaseInsensitive(t *testing.T) {
	entries := []string{"Kubectl Get Pods"}
	got := FuzzyMatch(entries, "kubectl get")
	if len(got) != 1 {
		t.Errorf("FuzzyMatch case insensitive: got %v", got)
	}
}

func TestFuzzyMatch_EmptyQuery(t *testing.T) {
	entries := []string{"a", "b"}
	got := FuzzyMatch(entries, "")
	if len(got) != 2 {
		t.Errorf("FuzzyMatch empty query: got %v", got)
	}
}

func TestFuzzyMatch_NoMatch(t *testing.T) {
	entries := []string{"helm list", "docker ps"}
	got := FuzzyMatch(entries, "kubectl")
	if len(got) != 0 {
		t.Errorf("FuzzyMatch no match: got %v", got)
	}
}

func TestTruncation(t *testing.T) {
	withTempHistory(t)

	for i := 0; i < maxEntries+100; i++ {
		Append("cmd" + strings.Repeat("x", 3) + string(rune('A'+(i%26))))
	}

	entries := Load()
	if len(entries) > maxEntries {
		t.Errorf("truncation: got %d entries, max is %d", len(entries), maxEntries)
	}
	if len(entries) == 0 {
		t.Error("truncation: expected some entries")
	}
}

func TestFilePath_ReturnsNonEmpty(t *testing.T) {
	p := filePath()
	if p == "" {
		t.Error("filePath returned empty")
	}
	if !strings.Contains(p, "kompadre") {
		t.Errorf("filePath should contain 'kompadre': got %q", p)
	}
}

func TestLastLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test-history")
	os.WriteFile(path, []byte("first\nsecond\nthird\n"), 0o644)

	if got := lastLine(path); got != "third" {
		t.Errorf("lastLine: got %q, want 'third'", got)
	}
}

func TestLastLine_Missing(t *testing.T) {
	if got := lastLine("/no/such/file"); got != "" {
		t.Errorf("lastLine missing: got %q", got)
	}
}
