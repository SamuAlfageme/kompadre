package kubeconfig

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type kubeConfigFile struct {
	CurrentContext string `yaml:"current-context"`
	Contexts       []struct {
		Name    string `yaml:"name"`
		Context struct {
			Cluster   string `yaml:"cluster"`
			Namespace string `yaml:"namespace"`
		} `yaml:"context"`
	} `yaml:"contexts"`
}

// ActiveContext returns the current-context name and its namespace from the
// given kubeconfig file. Empty strings are returned on any error.
func ActiveContext(path string) (ctx, ns string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", ""
	}
	var kc kubeConfigFile
	if err := yaml.Unmarshal(data, &kc); err != nil {
		return "", ""
	}
	ctx = kc.CurrentContext
	for _, c := range kc.Contexts {
		if c.Name == ctx {
			ns = c.Context.Namespace
			break
		}
	}
	if ns == "" {
		ns = "default"
	}
	return ctx, ns
}

// ContextEntry describes a single context inside a kubeconfig file.
type ContextEntry struct {
	Name      string
	Cluster   string
	Namespace string
	IsCurrent bool
}

// ListContexts returns all contexts defined in the given kubeconfig file.
func ListContexts(path string) []ContextEntry {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var kc kubeConfigFile
	if err := yaml.Unmarshal(data, &kc); err != nil {
		return nil
	}
	out := make([]ContextEntry, 0, len(kc.Contexts))
	for _, c := range kc.Contexts {
		ns := c.Context.Namespace
		if ns == "" {
			ns = "default"
		}
		out = append(out, ContextEntry{
			Name:      c.Name,
			Cluster:   c.Context.Cluster,
			Namespace: ns,
			IsCurrent: c.Name == kc.CurrentContext,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

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
