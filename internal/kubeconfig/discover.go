package kubeconfig

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DefaultPath returns standard kubeconfig path.
func DefaultPath() string {
	if p := os.Getenv("KUBECONFIG"); p != "" {
		// First path if multiple (colon on Unix, ; on Windows — use filepath.SplitList)
		parts := filepath.SplitList(p)
		if len(parts) > 0 {
			return parts[0]
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".kube", "config")
}

// Discover returns readable kubeconfig file paths: env-based entries plus ~/.kube files.
func Discover() []string {
	seen := make(map[string]struct{})
	var out []string

	add := func(p string) {
		p = filepath.Clean(p)
		if p == "." {
			return
		}
		st, err := os.Stat(p)
		if err != nil || st.IsDir() {
			return
		}
		if _, ok := seen[p]; ok {
			return
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}

	if kc := os.Getenv("KUBECONFIG"); kc != "" {
		for _, p := range filepath.SplitList(kc) {
			add(p)
		}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		sort.Strings(out)
		return out
	}
	kdir := filepath.Join(home, ".kube")
	entries, err := os.ReadDir(kdir)
	if err != nil {
		sort.Strings(out)
		return out
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		// Skip dotfiles in quick list (still reachable via browse).
		if strings.HasPrefix(name, ".") {
			continue
		}
		add(filepath.Join(kdir, name))
	}

	sort.Strings(out)
	return out
}
