package delta

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizeDiffOutput_RemovesTempHeaders(t *testing.T) {
	input := "/tmp/kompadre-left-12345.txt    │ /tmp/kompadre-right-12345.txt\nactual diff line 1\nactual diff line 2"
	got := sanitizeDiffOutput(input)
	if strings.Contains(got, "kompadre-left-") || strings.Contains(got, "kompadre-right-") {
		t.Errorf("sanitize should remove temp paths:\ngot: %q", got)
	}
	if !strings.Contains(got, "actual diff line 1") {
		t.Error("sanitize should keep real diff content")
	}
}

func TestSanitizeDiffOutput_RemovesNoNewline(t *testing.T) {
	input := "line 1\n\\ No newline at end of file\nline 2"
	got := sanitizeDiffOutput(input)
	if strings.Contains(got, "No newline") {
		t.Errorf("sanitize should remove 'No newline' messages: got %q", got)
	}
}

func TestSanitizeDiffOutput_RemovesUnifiedDiffHeaders(t *testing.T) {
	input := "--- /tmp/kompadre-left-abc.txt\n+++ /tmp/kompadre-right-abc.txt\n@@ -1 +1 @@\n-old\n+new"
	got := sanitizeDiffOutput(input)
	if strings.Contains(got, "--- /tmp/kompadre-left") {
		t.Error("sanitize should remove unified diff left header")
	}
	if strings.Contains(got, "+++ /tmp/kompadre-right") {
		t.Error("sanitize should remove unified diff right header")
	}
	if !strings.Contains(got, "@@ -1 +1 @@") {
		t.Error("sanitize should keep hunk markers")
	}
}

func TestSanitizeDiffOutput_Empty(t *testing.T) {
	if got := sanitizeDiffOutput(""); got != "" {
		t.Errorf("sanitize empty: got %q", got)
	}
}

func TestSanitizeDiffOutput_CleanInput(t *testing.T) {
	input := "line one\nline two\nline three"
	got := sanitizeDiffOutput(input)
	if got != input {
		t.Errorf("sanitize clean input changed:\ngot:  %q\nwant: %q", got, input)
	}
}

func TestSavePair(t *testing.T) {
	dir := t.TempDir()
	left := "left output data"
	right := "right output data"

	lp, rp, err := SavePair(dir, left, right)
	if err != nil {
		t.Fatalf("SavePair error: %v", err)
	}

	if !strings.HasPrefix(filepath.Base(lp), "kompadre-left-") {
		t.Errorf("left filename: got %q", filepath.Base(lp))
	}
	if !strings.HasPrefix(filepath.Base(rp), "kompadre-right-") {
		t.Errorf("right filename: got %q", filepath.Base(rp))
	}

	lb, err := os.ReadFile(lp)
	if err != nil {
		t.Fatal(err)
	}
	if string(lb) != left {
		t.Errorf("left content: got %q", string(lb))
	}

	rb, err := os.ReadFile(rp)
	if err != nil {
		t.Fatal(err)
	}
	if string(rb) != right {
		t.Errorf("right content: got %q", string(rb))
	}
}

func TestSavePair_CreatesDir(t *testing.T) {
	base := t.TempDir()
	nested := filepath.Join(base, "sub", "dir")

	_, _, err := SavePair(nested, "l", "r")
	if err != nil {
		t.Fatalf("SavePair nested: %v", err)
	}

	st, err := os.Stat(nested)
	if err != nil || !st.IsDir() {
		t.Error("SavePair should create nested directory")
	}
}

func TestSavePair_EmptyDirDefaultsCwd(t *testing.T) {
	orig, _ := os.Getwd()
	tmp := t.TempDir()
	os.Chdir(tmp)
	defer os.Chdir(orig)

	lp, rp, err := SavePair("", "a", "b")
	if err != nil {
		t.Fatalf("SavePair empty dir: %v", err)
	}
	if filepath.Dir(lp) != "." && filepath.Dir(lp) != tmp {
		t.Errorf("left path dir: got %q", filepath.Dir(lp))
	}
	_ = rp
}

func TestWriteTemp(t *testing.T) {
	path, err := writeTemp("test-prefix-", "content here")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "content here" {
		t.Errorf("writeTemp content: got %q", string(data))
	}
	if !strings.Contains(filepath.Base(path), "test-prefix-") {
		t.Errorf("writeTemp prefix: got %q", filepath.Base(path))
	}
}

func TestDiff_FallbackWhenDeltaMissing(t *testing.T) {
	// Hide delta but keep diff accessible by prepending a dir without delta.
	emptyDir := t.TempDir()
	t.Setenv("PATH", emptyDir+":/usr/bin:/bin")

	got, err := Diff("hello\n", "world\n", 80)
	if err != nil {
		t.Fatalf("Diff fallback error: %v", err)
	}
	if !strings.Contains(got, "hello") || !strings.Contains(got, "world") {
		t.Errorf("Diff fallback should contain both inputs:\ngot: %q", got)
	}
}

func noDeltaPath(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir()+":/usr/bin:/bin")
}

func TestDiff_IdenticalInputs(t *testing.T) {
	noDeltaPath(t)
	got, err := Diff("same\n", "same\n", 80)
	if err != nil {
		t.Fatalf("Diff identical error: %v", err)
	}
	_ = got
}

func TestDiff_EmptyInputs(t *testing.T) {
	noDeltaPath(t)
	got, err := Diff("", "", 80)
	if err != nil {
		t.Fatalf("Diff empty error: %v", err)
	}
	_ = got
}

func TestDiff_NarrowWidth(t *testing.T) {
	noDeltaPath(t)
	_, err := Diff("a\n", "b\n", 10)
	if err != nil {
		t.Fatalf("Diff narrow width error: %v", err)
	}
}
