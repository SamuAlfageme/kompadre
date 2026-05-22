package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func tmpKubeconfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "kubeconfig")
	content := `apiVersion: v1
kind: Config
current-context: test-ctx
contexts:
  - name: test-ctx
    context:
      cluster: test-cluster
      namespace: test-ns
`
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// ── New() validation ──────────────────────────────────────────────────

func TestNew_NoArgs(t *testing.T) {
	m, err := New("", "", "", false, "")
	if err != nil {
		t.Fatalf("New no args: %v", err)
	}
	if m.phase != phasePickLeft {
		t.Errorf("phase: got %d, want phasePickLeft", m.phase)
	}
}

func TestNew_PromptWithoutKubeconfigs(t *testing.T) {
	_, err := New("", "", "get pods", false, "")
	if err == nil {
		t.Error("New with prompt but no kubeconfigs should error")
	}
}

func TestNew_DeltaWithoutKubeconfigs(t *testing.T) {
	_, err := New("", "", "", true, "")
	if err == nil {
		t.Error("New with autoDelta but no kubeconfigs should error")
	}
}

func TestNew_LeftOnly(t *testing.T) {
	kc := tmpKubeconfig(t)
	m, err := New(kc, "", "", false, "")
	if err != nil {
		t.Fatalf("New left only: %v", err)
	}
	if m.phase != phasePickRight {
		t.Errorf("phase: got %d, want phasePickRight", m.phase)
	}
	if m.leftKube != kc {
		t.Errorf("leftKube: got %q", m.leftKube)
	}
}

func TestNew_BothKubeconfigs(t *testing.T) {
	kc := tmpKubeconfig(t)
	m, err := New(kc, kc, "", false, "")
	if err != nil {
		t.Fatalf("New both: %v", err)
	}
	if m.phase != phaseCompare {
		t.Errorf("phase: got %d, want phaseCompare", m.phase)
	}
}

func TestNew_InvalidLeftPath(t *testing.T) {
	_, err := New("/no/such/file", "", "", false, "")
	if err == nil {
		t.Error("New with invalid left path should error")
	}
}

func TestNew_LeftIsDirectory(t *testing.T) {
	dir := t.TempDir()
	_, err := New(dir, "", "", false, "")
	if err == nil {
		t.Error("New with directory as kubeconfig should error")
	}
}

func TestNew_DeltaWithoutPrompt(t *testing.T) {
	kc := tmpKubeconfig(t)
	_, err := New(kc, kc, "", true, "")
	if err == nil {
		t.Error("New with autoDelta but no prompt should error")
	}
}

func TestNew_WithPrompt(t *testing.T) {
	kc := tmpKubeconfig(t)
	m, err := New(kc, kc, "kubectl get pods", false, "")
	if err != nil {
		t.Fatal(err)
	}
	if m.autoPrompt != "kubectl get pods" {
		t.Errorf("autoPrompt: got %q", m.autoPrompt)
	}
}

func TestNew_SaveDir(t *testing.T) {
	kc := tmpKubeconfig(t)
	m, err := New(kc, kc, "", false, "/tmp/save")
	if err != nil {
		t.Fatal(err)
	}
	if m.saveDir != "/tmp/save" {
		t.Errorf("saveDir: got %q", m.saveDir)
	}
}

// ── prependKubeFlags ──────────────────────────────────────────────────

func TestPrependKubeFlags_Kubectl(t *testing.T) {
	got := prependKubeFlags("kubectl get pods", "staging", "kube-system")
	if !strings.Contains(got, "--context=staging") {
		t.Errorf("missing --context: got %q", got)
	}
	if !strings.Contains(got, "--namespace=kube-system") {
		t.Errorf("missing --namespace: got %q", got)
	}
	if !strings.HasPrefix(got, "kubectl ") {
		t.Errorf("should start with kubectl: got %q", got)
	}
	if !strings.HasSuffix(got, " get pods") {
		t.Errorf("should end with rest of command: got %q", got)
	}
}

func TestPrependKubeFlags_KAlias(t *testing.T) {
	got := prependKubeFlags("k get pods", "prod", "default")
	if !strings.HasPrefix(got, "k ") {
		t.Errorf("should preserve 'k' alias: got %q", got)
	}
	if !strings.Contains(got, "--context=prod") {
		t.Errorf("missing --context: got %q", got)
	}
}

func TestPrependKubeFlags_NonKubectl(t *testing.T) {
	cmd := "helm list --all"
	got := prependKubeFlags(cmd, "staging", "ns")
	if got != cmd {
		t.Errorf("non-kubectl should be unchanged: got %q", got)
	}
}

func TestPrependKubeFlags_EmptyContext(t *testing.T) {
	cmd := "kubectl get pods"
	got := prependKubeFlags(cmd, "", "ns")
	if got != cmd {
		t.Errorf("empty context should be unchanged: got %q", got)
	}
}

func TestPrependKubeFlags_EmptyCommand(t *testing.T) {
	got := prependKubeFlags("", "ctx", "ns")
	if got != "" {
		t.Errorf("empty command should stay empty: got %q", got)
	}
}

func TestPrependKubeFlags_KubectlOnly(t *testing.T) {
	got := prependKubeFlags("kubectl", "ctx", "ns")
	if !strings.Contains(got, "--context=ctx") {
		t.Errorf("kubectl alone should get flags: got %q", got)
	}
}

// ── validateKubeconfigFile ────────────────────────────────────────────

func TestValidateKubeconfigFile_Valid(t *testing.T) {
	kc := tmpKubeconfig(t)
	got, err := validateKubeconfigFile(kc)
	if err != nil {
		t.Fatalf("validateKubeconfigFile valid: %v", err)
	}
	if got != kc {
		t.Errorf("validateKubeconfigFile path: got %q", got)
	}
}

func TestValidateKubeconfigFile_Empty(t *testing.T) {
	_, err := validateKubeconfigFile("")
	if err == nil {
		t.Error("empty path should error")
	}
}

func TestValidateKubeconfigFile_Directory(t *testing.T) {
	_, err := validateKubeconfigFile(t.TempDir())
	if err == nil {
		t.Error("directory should error")
	}
}

func TestValidateKubeconfigFile_Missing(t *testing.T) {
	_, err := validateKubeconfigFile("/no/such/file")
	if err == nil {
		t.Error("missing file should error")
	}
}

func TestValidateKubeconfigFile_TildeExpansion(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no HOME")
	}
	// Create a temp file in home to test tilde expansion.
	tmp := filepath.Join(home, ".kompadre-test-validate-"+t.Name())
	if err := os.WriteFile(tmp, []byte("test"), 0o644); err != nil {
		t.Skip("cannot write to home")
	}
	defer os.Remove(tmp)

	got, err := validateKubeconfigFile("~/.kompadre-test-validate-" + t.Name())
	if err != nil {
		t.Fatalf("tilde expansion: %v", err)
	}
	if got != tmp {
		t.Errorf("tilde expansion path: got %q, want %q", got, tmp)
	}
}

// ── Watch mode (toggle + tick message routing) ────────────────────────

func compareModel(t *testing.T) *Model {
	t.Helper()
	kc := tmpKubeconfig(t)
	m, err := New(kc, kc, "", false, "")
	if err != nil {
		t.Fatal(err)
	}
	m.w, m.h = 120, 40
	m.layoutViewports()
	return m
}

func sendKey(m *Model, keyStr string) (tea.Model, tea.Cmd) {
	return m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(keyStr)})
}

func sendCtrl(m *Model, keyStr string) (tea.Model, tea.Cmd) {
	var kt tea.KeyType
	switch keyStr {
	case "ctrl+w":
		kt = tea.KeyCtrlW
	case "ctrl+c":
		kt = tea.KeyCtrlC
	case "ctrl+d":
		kt = tea.KeyCtrlD
	case "ctrl+s":
		kt = tea.KeyCtrlS
	default:
		kt = tea.KeyRunes
	}
	return m.Update(tea.KeyMsg{Type: kt})
}

func TestWatch_ToggleOnWithCommand(t *testing.T) {
	m := compareModel(t)
	m.unifiedInput.SetValue("kubectl get pods")

	result, cmd := sendCtrl(m, "ctrl+w")
	mdl := result.(*Model)
	if !mdl.watching {
		t.Error("watch should be true after ctrl+w with command")
	}
	if cmd == nil {
		t.Error("ctrl+w should return a command (batch of run + tick)")
	}
}

func TestWatch_ToggleOff(t *testing.T) {
	m := compareModel(t)
	m.unifiedInput.SetValue("kubectl get pods")
	m.watching = true

	result, _ := sendCtrl(m, "ctrl+w")
	mdl := result.(*Model)
	if mdl.watching {
		t.Error("watch should be false after second ctrl+w")
	}
	if mdl.status != "watch stopped" {
		t.Errorf("status: got %q, want 'watch stopped'", mdl.status)
	}
}

func TestWatch_NoCommandGuard(t *testing.T) {
	m := compareModel(t)
	m.unifiedInput.SetValue("")

	result, _ := sendCtrl(m, "ctrl+w")
	mdl := result.(*Model)
	if mdl.watching {
		t.Error("watch should not start without a command")
	}
	if mdl.status != "enter a command first" {
		t.Errorf("status: got %q", mdl.status)
	}
}

func TestWatch_NoCommandGuard_SplitMode(t *testing.T) {
	m := compareModel(t)
	m.splitMode = true
	m.leftInput.SetValue("")
	m.rightInput.SetValue("")

	result, _ := sendCtrl(m, "ctrl+w")
	mdl := result.(*Model)
	if mdl.watching {
		t.Error("watch should not start with empty split commands")
	}
}

func TestWatch_SplitModeWithCommand(t *testing.T) {
	m := compareModel(t)
	m.splitMode = true
	m.leftInput.SetValue("kubectl get pods")
	m.rightInput.SetValue("")

	result, cmd := sendCtrl(m, "ctrl+w")
	mdl := result.(*Model)
	if !mdl.watching {
		t.Error("watch should start with at least one split command")
	}
	if cmd == nil {
		t.Error("should return run command")
	}
}

func TestWatch_TickWhenWatching(t *testing.T) {
	m := compareModel(t)
	m.watching = true
	m.unifiedInput.SetValue("kubectl get pods")

	result, cmd := m.Update(watchTickMsg{})
	mdl := result.(*Model)
	if !mdl.busy {
		t.Error("tick should trigger rerun (busy)")
	}
	if cmd == nil {
		t.Error("tick should return a command")
	}
}

func TestWatch_TickIgnoredWhenNotWatching(t *testing.T) {
	m := compareModel(t)
	m.watching = false

	_, cmd := m.Update(watchTickMsg{})
	if cmd != nil {
		t.Error("tick should be no-op when not watching")
	}
}

func TestWatch_TickIgnoredWhenBusy(t *testing.T) {
	m := compareModel(t)
	m.watching = true
	m.busy = true

	_, cmd := m.Update(watchTickMsg{})
	if cmd != nil {
		t.Error("tick should be no-op when busy")
	}
}

func TestWatch_TickIgnoredWrongPhase(t *testing.T) {
	m := compareModel(t)
	m.watching = true
	m.phase = phaseDiff

	_, cmd := m.Update(watchTickMsg{})
	if cmd != nil {
		t.Error("tick should be no-op in diff phase")
	}
}

func TestWatch_EscStopsWatch(t *testing.T) {
	m := compareModel(t)
	m.watching = true

	result, _ := m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	mdl := result.(*Model)
	if mdl.watching {
		t.Error("esc should stop watching")
	}
}

func TestWatch_DeltaStopsWatch(t *testing.T) {
	m := compareModel(t)
	m.watching = true
	m.leftOut = "output"

	result, _ := sendCtrl(m, "ctrl+d")
	mdl := result.(*Model)
	if mdl.watching {
		t.Error("ctrl+d should stop watching")
	}
}

func TestWatch_RunBothDoneReschedules(t *testing.T) {
	m := compareModel(t)
	m.watching = true

	_, cmd := m.Update(runBothDoneMsg{leftOut: "l", rightOut: "r"})
	if cmd == nil {
		t.Error("runBothDoneMsg while watching should schedule next tick")
	}
}

func TestWatch_RunBothDoneNoRescheduleWhenNotWatching(t *testing.T) {
	m := compareModel(t)
	m.watching = false

	_, cmd := m.Update(runBothDoneMsg{leftOut: "l", rightOut: "r"})
	if cmd != nil {
		t.Error("runBothDoneMsg without watching should not schedule tick")
	}
}

func TestWatch_RunSplitDoneReschedules(t *testing.T) {
	m := compareModel(t)
	m.watching = true

	_, cmd := m.Update(runSplitDoneMsg{leftOut: "l", rightOut: "r"})
	if cmd == nil {
		t.Error("runSplitDoneMsg while watching should schedule next tick")
	}
}

// ── Quit confirmation gate ────────────────────────────────────────────

func TestQuit_CtrlCEntersConfirm(t *testing.T) {
	m := compareModel(t)

	result, _ := sendCtrl(m, "ctrl+c")
	mdl := result.(*Model)
	if !mdl.confirmQuit {
		t.Error("ctrl+c should enter confirm state")
	}
	if mdl.status != "Quit? y/n - ctrl+C" {
		t.Errorf("status: got %q", mdl.status)
	}
}

func TestQuit_YConfirmsQuit(t *testing.T) {
	m := compareModel(t)
	m.confirmQuit = true

	_, cmd := sendKey(m, "y")
	if cmd == nil {
		t.Fatal("y during confirm should return quit command")
	}
}

func TestQuit_UpperYConfirmsQuit(t *testing.T) {
	m := compareModel(t)
	m.confirmQuit = true

	_, cmd := sendKey(m, "Y")
	if cmd == nil {
		t.Fatal("Y during confirm should return quit command")
	}
}

func TestQuit_CtrlCConfirmsQuit(t *testing.T) {
	m := compareModel(t)
	m.confirmQuit = true

	_, cmd := sendCtrl(m, "ctrl+c")
	if cmd == nil {
		t.Fatal("ctrl+c during confirm should return quit command")
	}
}

func TestQuit_OtherKeyCancelsConfirm(t *testing.T) {
	m := compareModel(t)
	m.confirmQuit = true

	result, _ := sendKey(m, "n")
	mdl := result.(*Model)
	if mdl.confirmQuit {
		t.Error("'n' should cancel confirm")
	}
	if mdl.status != "" {
		t.Errorf("status should be cleared: got %q", mdl.status)
	}
}

// ── Phase transitions ─────────────────────────────────────────────────

func TestPhase_CompareToPickOnEsc(t *testing.T) {
	m := compareModel(t)

	result, _ := m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	mdl := result.(*Model)
	if mdl.phase != phasePickRight {
		t.Errorf("esc from compare: got phase %d, want phasePickRight", mdl.phase)
	}
}

func TestPhase_RunBothDoneUpdatesOutput(t *testing.T) {
	m := compareModel(t)

	result, _ := m.Update(runBothDoneMsg{leftOut: "left data", rightOut: "right data"})
	mdl := result.(*Model)
	if mdl.leftOut != "left data" || mdl.rightOut != "right data" {
		t.Errorf("outputs: left=%q right=%q", mdl.leftOut, mdl.rightOut)
	}
	if mdl.busy {
		t.Error("should not be busy after done message")
	}
}

func TestPhase_DiffDoneTransitions(t *testing.T) {
	m := compareModel(t)
	m.busy = true

	result, _ := m.Update(diffDoneMsg{text: "diff output"})
	mdl := result.(*Model)
	if mdl.phase != phaseDiff {
		t.Errorf("diffDoneMsg: got phase %d, want phaseDiff", mdl.phase)
	}
	if mdl.diffContent != "diff output" {
		t.Errorf("diffContent: got %q", mdl.diffContent)
	}
}

// ── Submit behavior ───────────────────────────────────────────────────

func TestSubmit_EmptyCommandShowsError(t *testing.T) {
	m := compareModel(t)
	m.unifiedInput.SetValue("")

	result, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mdl := result.(*Model)
	if mdl.status != "empty command" {
		t.Errorf("status: got %q, want 'empty command'", mdl.status)
	}
}

func TestSubmit_BusyShowsWaiting(t *testing.T) {
	m := compareModel(t)
	m.busy = true
	m.unifiedInput.SetValue("kubectl get pods")

	result, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mdl := result.(*Model)
	if mdl.status != "waiting for current run…" {
		t.Errorf("status: got %q", mdl.status)
	}
}

func TestSubmit_SplitBothEmpty(t *testing.T) {
	m := compareModel(t)
	m.splitMode = true
	m.leftInput.SetValue("")
	m.rightInput.SetValue("")

	result, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mdl := result.(*Model)
	if mdl.status != "enter at least one command" {
		t.Errorf("status: got %q", mdl.status)
	}
}

// ── Window resize ─────────────────────────────────────────────────────

func TestWindowResize(t *testing.T) {
	m := compareModel(t)

	result, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 60})
	mdl := result.(*Model)
	if mdl.w != 200 || mdl.h != 60 {
		t.Errorf("size: got %dx%d", mdl.w, mdl.h)
	}
}

// ── Busy tick ─────────────────────────────────────────────────────────

func TestBusyTick_WhenBusy(t *testing.T) {
	m := compareModel(t)
	m.busy = true
	m.busyDot = 0

	result, cmd := m.Update(busyTickMsg{})
	mdl := result.(*Model)
	if mdl.busyDot != 1 {
		t.Errorf("busyDot: got %d", mdl.busyDot)
	}
	if cmd == nil {
		t.Error("busy tick should schedule next tick")
	}
}

func TestBusyTick_WhenNotBusy(t *testing.T) {
	m := compareModel(t)
	m.busy = false

	_, cmd := m.Update(busyTickMsg{})
	if cmd != nil {
		t.Error("busy tick when not busy should be no-op")
	}
}

// ── View smoke test ───────────────────────────────────────────────────

func TestView_DoesNotPanic(t *testing.T) {
	m := compareModel(t)
	out := m.View()
	if out == "" {
		t.Error("View should produce output")
	}
}

func TestView_WatchIndicator(t *testing.T) {
	m := compareModel(t)
	m.watching = true
	out := m.View()
	if !strings.Contains(out, "watching") {
		t.Error("View should show watch indicator when watching")
	}
}

func TestView_HelpBarContainsWatchHint(t *testing.T) {
	m := compareModel(t)
	out := m.View()
	if !strings.Contains(out, "ctrl+w") {
		t.Error("help bar should mention ctrl+w")
	}
}

// ── Helper functions ──────────────────────────────────────────────────

func TestTruncate(t *testing.T) {
	tests := []struct {
		s    string
		max  int
		want string
	}{
		{"hello world", 11, "hello world"},
		{"hello world", 5, "hell…"},
		{"abc", 3, "…"},
		{"abc", 2, "…"},
		{"abcde", 4, "abc…"},
		{"", 5, ""},
	}
	for _, tc := range tests {
		got := truncate(tc.s, tc.max)
		if got != tc.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tc.s, tc.max, got, tc.want)
		}
	}
}

func TestMax(t *testing.T) {
	if max(3, 5) != 5 {
		t.Error("max(3,5)")
	}
	if max(10, 2) != 10 {
		t.Error("max(10,2)")
	}
}

func TestMin(t *testing.T) {
	if min(3, 5) != 3 {
		t.Error("min(3,5)")
	}
	if min(10, 2) != 2 {
		t.Error("min(10,2)")
	}
}
