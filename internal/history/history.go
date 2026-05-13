package history

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

const maxEntries = 500

func filePath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		if home, e := os.UserHomeDir(); e == nil {
			dir = home
		} else {
			dir = "."
		}
	}
	return filepath.Join(dir, "kompadre", "history")
}

// Load reads the history file and returns entries newest-first.
func Load() []string {
	data, err := os.ReadFile(filePath())
	if err != nil {
		return nil
	}
	var lines []string
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if l != "" {
			lines = append(lines, l)
		}
	}
	// Reverse so newest is first.
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	return lines
}

// Append adds a command to the history file (deduplicating against the last entry).
func Append(cmd string) {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return
	}
	p := filePath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	// Avoid consecutive duplicates.
	if last := lastLine(p); last == cmd {
		return
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString(cmd + "\n")

	truncateIfNeeded(p)
}

// FuzzyMatch returns entries matching all space-separated tokens (case-insensitive),
// preserving the input order (newest-first).
func FuzzyMatch(entries []string, query string) []string {
	query = strings.TrimSpace(query)
	if query == "" {
		return entries
	}
	tokens := strings.Fields(strings.ToLower(query))
	var out []string
	for _, e := range entries {
		lower := strings.ToLower(e)
		match := true
		for _, t := range tokens {
			if !strings.Contains(lower, t) {
				match = false
				break
			}
		}
		if match {
			out = append(out, e)
		}
	}
	return out
}

func lastLine(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	var last string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" {
			last = l
		}
	}
	return last
}

func truncateIfNeeded(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var lines []string
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) <= maxEntries {
		return
	}
	lines = lines[len(lines)-maxEntries:]
	os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}
