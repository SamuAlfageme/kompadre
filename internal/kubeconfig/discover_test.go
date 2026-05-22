package kubeconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func writeKubeconfig(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const sampleKubeconfig = `apiVersion: v1
kind: Config
current-context: staging
contexts:
  - name: production
    context:
      cluster: prod-cluster
      namespace: prod-ns
  - name: staging
    context:
      cluster: staging-cluster
  - name: dev
    context:
      cluster: dev-cluster
      namespace: dev-ns
`

func TestActiveContext(t *testing.T) {
	dir := t.TempDir()
	path := writeKubeconfig(t, dir, "config", sampleKubeconfig)

	ctx, ns := ActiveContext(path)
	if ctx != "staging" {
		t.Errorf("ActiveContext ctx: got %q, want 'staging'", ctx)
	}
	if ns != "default" {
		t.Errorf("ActiveContext ns: got %q, want 'default' (staging has no namespace)", ns)
	}
}

func TestActiveContext_WithNamespace(t *testing.T) {
	kc := `apiVersion: v1
kind: Config
current-context: dev
contexts:
  - name: dev
    context:
      cluster: dev-cluster
      namespace: my-ns
`
	dir := t.TempDir()
	path := writeKubeconfig(t, dir, "config", kc)

	ctx, ns := ActiveContext(path)
	if ctx != "dev" || ns != "my-ns" {
		t.Errorf("ActiveContext with ns: got ctx=%q ns=%q", ctx, ns)
	}
}

func TestActiveContext_MissingFile(t *testing.T) {
	ctx, ns := ActiveContext("/nonexistent/path")
	if ctx != "" || ns != "" {
		t.Errorf("ActiveContext missing file: got ctx=%q ns=%q", ctx, ns)
	}
}

func TestActiveContext_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := writeKubeconfig(t, dir, "bad", "not: [yaml: {broken")

	ctx, ns := ActiveContext(path)
	if ctx != "" || ns != "" {
		t.Errorf("ActiveContext invalid yaml: got ctx=%q ns=%q", ctx, ns)
	}
}

func TestListContexts(t *testing.T) {
	dir := t.TempDir()
	path := writeKubeconfig(t, dir, "config", sampleKubeconfig)

	entries := ListContexts(path)
	if len(entries) != 3 {
		t.Fatalf("ListContexts count: got %d, want 3", len(entries))
	}

	byName := map[string]ContextEntry{}
	for _, e := range entries {
		byName[e.Name] = e
	}

	if e := byName["staging"]; !e.IsCurrent {
		t.Error("staging should be current context")
	}
	if e := byName["production"]; e.Namespace != "prod-ns" {
		t.Errorf("production ns: got %q", e.Namespace)
	}
	if e := byName["dev"]; e.Namespace != "dev-ns" {
		t.Errorf("dev ns: got %q", e.Namespace)
	}
	if e := byName["staging"]; e.Namespace != "default" {
		t.Errorf("staging ns: got %q, want 'default'", e.Namespace)
	}
}

func TestListContexts_Sorted(t *testing.T) {
	dir := t.TempDir()
	path := writeKubeconfig(t, dir, "config", sampleKubeconfig)

	entries := ListContexts(path)
	for i := 1; i < len(entries); i++ {
		if entries[i].Name < entries[i-1].Name {
			t.Errorf("ListContexts not sorted: %q after %q", entries[i].Name, entries[i-1].Name)
		}
	}
}

func TestListContexts_MissingFile(t *testing.T) {
	if entries := ListContexts("/no/such/file"); entries != nil {
		t.Errorf("ListContexts missing: got %v", entries)
	}
}

func TestDiscover_IncludesKubeDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KUBECONFIG", "")

	kubeDir := filepath.Join(home, ".kube")
	if err := os.MkdirAll(kubeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeKubeconfig(t, kubeDir, "config", sampleKubeconfig)
	writeKubeconfig(t, kubeDir, "staging", "apiVersion: v1\nkind: Config\n")

	paths := Discover()
	if len(paths) < 2 {
		t.Fatalf("Discover: expected >= 2 paths, got %d: %v", len(paths), paths)
	}
}

func TestDiscover_SkipsDotfiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KUBECONFIG", "")

	kubeDir := filepath.Join(home, ".kube")
	if err := os.MkdirAll(kubeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeKubeconfig(t, kubeDir, ".hidden", "data")
	writeKubeconfig(t, kubeDir, "config", sampleKubeconfig)

	paths := Discover()
	for _, p := range paths {
		if filepath.Base(p) == ".hidden" {
			t.Error("Discover should skip dotfiles in quick list")
		}
	}
}

func TestDiscover_IncludesEnvEntries(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	kubeDir := filepath.Join(home, ".kube")
	if err := os.MkdirAll(kubeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	extra := writeKubeconfig(t, home, "extra-kube", "data")
	t.Setenv("KUBECONFIG", extra)

	paths := Discover()
	found := false
	for _, p := range paths {
		if p == extra {
			found = true
		}
	}
	if !found {
		t.Errorf("Discover should include KUBECONFIG env entries: got %v", paths)
	}
}

func TestDiscover_Sorted(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KUBECONFIG", "")

	kubeDir := filepath.Join(home, ".kube")
	if err := os.MkdirAll(kubeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeKubeconfig(t, kubeDir, "z-config", "data")
	writeKubeconfig(t, kubeDir, "a-config", "data")

	paths := Discover()
	for i := 1; i < len(paths); i++ {
		if paths[i] < paths[i-1] {
			t.Errorf("Discover not sorted: %q after %q", paths[i], paths[i-1])
		}
	}
}

func TestDefaultPath_FromEnv(t *testing.T) {
	t.Setenv("KUBECONFIG", "/custom/path/config")
	if got := DefaultPath(); got != "/custom/path/config" {
		t.Errorf("DefaultPath from env: got %q", got)
	}
}

func TestDefaultPath_FallbackHome(t *testing.T) {
	t.Setenv("KUBECONFIG", "")
	got := DefaultPath()
	if got == "" {
		t.Skip("no HOME available")
	}
	if !filepath.IsAbs(got) {
		t.Errorf("DefaultPath should be absolute: got %q", got)
	}
}
