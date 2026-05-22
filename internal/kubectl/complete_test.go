package kubectl

import "testing"

func TestParseCompleteOutput_Normal(t *testing.T) {
	out := "get\nlist\napply\n:4\n"
	got := parseCompleteOutput(out)
	if len(got) != 3 || got[0] != "get" || got[1] != "list" || got[2] != "apply" {
		t.Errorf("parseCompleteOutput normal: got %v", got)
	}
}

func TestParseCompleteOutput_WithTabs(t *testing.T) {
	out := "pods\tList pods\nservices\tList services\n:4\n"
	got := parseCompleteOutput(out)
	if len(got) != 2 || got[0] != "pods" || got[1] != "services" {
		t.Errorf("parseCompleteOutput tabs: got %v", got)
	}
}

func TestParseCompleteOutput_FilterDirectiveNoise(t *testing.T) {
	out := "pods\nCompletion ended with directive: ShellCompDirectiveNoFileComp\n:4\n"
	got := parseCompleteOutput(out)
	if len(got) != 1 || got[0] != "pods" {
		t.Errorf("parseCompleteOutput directive: got %v", got)
	}
}

func TestParseCompleteOutput_Empty(t *testing.T) {
	if got := parseCompleteOutput(""); got != nil {
		t.Errorf("parseCompleteOutput empty: got %v", got)
	}
}

func TestIsCompletionDebugLine(t *testing.T) {
	tests := []struct {
		line string
		want bool
	}{
		{"Completion ended with directive: ShellCompDirectiveNoFileComp", true},
		{"  ShellCompDirectiveNoSpace  ", true},
		{"completion ended with something", true},
		{"pods", false},
		{"get", false},
		{"", true},
	}
	for _, tc := range tests {
		if got := isCompletionDebugLine(tc.line); got != tc.want {
			t.Errorf("isCompletionDebugLine(%q) = %v, want %v", tc.line, got, tc.want)
		}
	}
}

func TestFilterCompletions(t *testing.T) {
	comps := []string{"pods", "pvc", "pv", "services", "nodes"}
	got := filterCompletions("p", comps)
	if len(got) != 3 {
		t.Errorf("filterCompletions 'p': got %v (len %d)", got, len(got))
	}
	for _, c := range got {
		if c != "pods" && c != "pvc" && c != "pv" {
			t.Errorf("unexpected completion: %q", c)
		}
	}
}

func TestFilterCompletions_CaseInsensitive(t *testing.T) {
	comps := []string{"Pods", "PVC"}
	got := filterCompletions("p", comps)
	if len(got) != 2 {
		t.Errorf("filterCompletions case: got %v", got)
	}
}

func TestFilterCompletions_EmptyPrefix(t *testing.T) {
	comps := []string{"a", "b"}
	got := filterCompletions("", comps)
	if len(got) != 2 {
		t.Errorf("filterCompletions empty prefix: got %v", got)
	}
}

func TestLongestCommonPrefix(t *testing.T) {
	tests := []struct {
		in   []string
		want string
	}{
		{nil, ""},
		{[]string{"hello"}, "hello"},
		{[]string{"abc", "abd", "abe"}, "ab"},
		{[]string{"get", "get-all"}, "get"},
		{[]string{"x", "y"}, ""},
	}
	for _, tc := range tests {
		if got := longestCommonPrefix(tc.in); got != tc.want {
			t.Errorf("longestCommonPrefix(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestApplyChoice_Basic(t *testing.T) {
	line := "kubectl get "
	nl, nc := ApplyChoice(line, "pods", 12, 12, false)
	if nl != "kubectl get pods" {
		t.Errorf("ApplyChoice basic line: got %q", nl)
	}
	if nc != 16 {
		t.Errorf("ApplyChoice basic cursor: got %d", nc)
	}
}

func TestApplyChoice_ReplacePartial(t *testing.T) {
	line := "kubectl get po"
	nl, nc := ApplyChoice(line, "pods", 12, 14, false)
	if nl != "kubectl get pods" {
		t.Errorf("ApplyChoice partial line: got %q", nl)
	}
	if nc != 16 {
		t.Errorf("ApplyChoice partial cursor: got %d", nc)
	}
}

func TestApplyChoice_NeedSep(t *testing.T) {
	line := "kubectl"
	nl, nc := ApplyChoice(line, "get", 7, 7, true)
	if nl != "kubectl get" {
		t.Errorf("ApplyChoice needSep line: got %q", nl)
	}
	if nc != 11 {
		t.Errorf("ApplyChoice needSep cursor: got %d", nc)
	}
}

func TestInvocationCompletions_KuPrefix(t *testing.T) {
	rs := []rune("ku")
	choices, start, end, ok := invocationCompletions(rs, 2)
	if !ok {
		t.Fatal("invocationCompletions('ku') not ok")
	}
	if start != 0 || end != 2 {
		t.Errorf("span: got [%d:%d]", start, end)
	}
	found := false
	for _, c := range choices {
		if c == "kubectl" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'kubectl' in choices: %v", choices)
	}
}

func TestInvocationCompletions_FullKubectl(t *testing.T) {
	rs := []rune("kubectl")
	_, _, _, ok := invocationCompletions(rs, 7)
	if ok {
		t.Error("invocationCompletions('kubectl') should not offer completions for exact match")
	}
}

func TestInvocationCompletions_AfterSpace(t *testing.T) {
	rs := []rune("kubectl ")
	_, _, _, ok := invocationCompletions(rs, 8)
	if ok {
		t.Error("invocationCompletions after space should return false")
	}
}

func TestInvocationCompletions_MultipleTokens(t *testing.T) {
	rs := []rune("kubectl get")
	_, _, _, ok := invocationCompletions(rs, 11)
	if ok {
		t.Error("invocationCompletions with multiple tokens should return false")
	}
}
