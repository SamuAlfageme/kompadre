package kubectl

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

const completeTimeout = 45 * time.Second

// ListCompletions fetches kubectl __complete candidates for the token at cursor.
// If the typed prefix filters out everything, the full list is returned so you can still pick (fzf-style).
func ListCompletions(ctx context.Context, kubeconfigPath, line string, cursor int) (choices []string, repStart, repEnd int, needSep bool, hint string) {
	line = strings.ReplaceAll(line, "\t", " ")
	rs := []rune(line)
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(rs) {
		cursor = len(rs)
	}

	if invChoices, s, e, ok := invocationCompletions(rs, cursor); ok {
		return invChoices, s, e, false, ""
	}

	kubectlBin, invokeArgs, repS, repE, ok := parseForComplete(rs, cursor)
	if !ok {
		return nil, 0, 0, false, ""
	}

	ctx, cancel := context.WithTimeout(ctx, completeTimeout)
	defer cancel()

	out, err := runComplete(ctx, kubeconfigPath, kubectlBin, invokeArgs)
	if err != nil {
		return nil, 0, 0, false, err.Error()
	}
	if strings.TrimSpace(out) == "" {
		return nil, 0, 0, false, ""
	}

	comps := parseCompleteOutput(out)
	if len(comps) == 0 {
		return nil, 0, 0, false, ""
	}

	prefix := string(rs[repS:repE])
	match := filterCompletions(prefix, comps)
	if len(match) == 0 {
		match = comps
	}
	sort.Strings(match)

	needSep = repS == repE && repS > 0 && rs[repS-1] != ' ' && rs[repS-1] != '\t'
	return match, repS, repE, needSep, ""
}

// invocationCompletions offers command-name completion before kubectl __complete.
// Example: "ku" + Tab => "kubectl".
func invocationCompletions(rs []rune, cursor int) (choices []string, repStart, repEnd int, ok bool) {
	if cursor < 0 || cursor > len(rs) {
		return nil, 0, 0, false
	}
	before := string(rs[:cursor])
	trimmedRight := strings.TrimRight(before, " \t")
	trailingSpace := len(trimmedRight) < len(before)
	if trailingSpace {
		// After a space we should complete args, not the executable token.
		return nil, 0, 0, false
	}

	fields := strings.Fields(trimmedRight)
	if len(fields) != 1 {
		return nil, 0, 0, false
	}
	token := fields[0]
	if token == "" {
		return nil, 0, 0, false
	}

	// Only support completion when cursor is at the end of the first token.
	if cursor != len([]rune(token)) {
		return nil, 0, 0, false
	}

	candidates := []string{"kubectl", "k"}
	var out []string
	tl := strings.ToLower(token)
	for _, c := range candidates {
		if strings.HasPrefix(strings.ToLower(c), tl) && c != token {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return nil, 0, 0, false
	}
	sort.Strings(out)
	return out, 0, cursor, true
}

// ApplyChoice inserts choice at [repStart:repEnd] (rune indices).
func ApplyChoice(line, choice string, repStart, repEnd int, needSep bool) (newLine string, newCursor int) {
	line = strings.ReplaceAll(line, "\t", " ")
	rs := []rune(line)
	if repStart < 0 {
		repStart = 0
	}
	if repEnd > len(rs) {
		repEnd = len(rs)
	}
	if repStart > repEnd {
		repStart, repEnd = repEnd, repStart
	}
	sep := ""
	if needSep {
		sep = " "
	}
	before := string(rs[:repStart])
	after := string(rs[repEnd:])
	newLine = before + sep + choice + after
	newCursor = repStart + len([]rune(sep+choice))
	return newLine, newCursor
}

// CompletePrompt runs kubectl __complete and returns an updated line/cursor (single-shot LCP behavior).
func CompletePrompt(ctx context.Context, kubeconfigPath, line string, cursor int) (newLine string, newCursor int, hint string) {
	choices, repStart, repEnd, needSep, errHint := ListCompletions(ctx, kubeconfigPath, line, cursor)
	if errHint != "" {
		return line, cursor, errHint
	}
	if len(choices) == 0 {
		return line, cursor, ""
	}

	rs := []rune(strings.ReplaceAll(line, "\t", " "))
	prefix := string(rs[repStart:repEnd])

	if len(choices) == 1 {
		nl, nc := ApplyChoice(line, choices[0], repStart, repEnd, needSep)
		return nl, nc, ""
	}

	lcp := longestCommonPrefix(choices)
	if lcp == "" {
		return line, cursor, fmt.Sprintf("%d completions — tab opens picker", len(choices))
	}

	newTok := lcp
	before := string(rs[:repStart])
	after := string(rs[repEnd:])
	sep := ""
	if needSep {
		sep = " "
	}
	newLine = before + sep + newTok + after
	newCursor = repStart + len([]rune(sep+newTok))

	hint = ""
	if len(choices) > 1 && newTok == prefix {
		hint = fmt.Sprintf("%d matches — tab opens picker", len(choices))
	}
	return newLine, newCursor, hint
}

func runComplete(ctx context.Context, kubeconfigPath, kubectlBin string, invokeArgs []string) (string, error) {
	args := append([]string{"__complete"}, invokeArgs...)
	cmd := exec.CommandContext(ctx, kubectlBin, args...)
	cmd.Env = append(execEnvNoCompletionDebug(), "KUBECONFIG="+kubeconfigPath)
	cmd.Stdin = bytes.NewReader(nil)
	var outBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &outBuf
	err := cmd.Run()
	if err != nil {
		return "", err
	}
	return outBuf.String(), nil
}

func execEnvNoCompletionDebug() []string {
	base := os.Environ()
	out := make([]string, 0, len(base)+1)
	for _, e := range base {
		if strings.HasPrefix(e, "KUBECTL_COMPLETION_DEBUG=") {
			continue
		}
		out = append(out, e)
	}
	out = append(out, "KUBECTL_COMPLETION_DEBUG=")
	return out
}

func parseCompleteOutput(out string) []string {
	out = strings.TrimRight(out, "\n")
	if out == "" {
		return nil
	}
	lines := strings.Split(out, "\n")
	// Last line is :<directive>
	if len(lines) > 0 {
		last := lines[len(lines)-1]
		if strings.HasPrefix(last, ":") {
			if _, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(last, ":"))); err == nil {
				lines = lines[:len(lines)-1]
			}
		}
	}
	var completions []string
	for _, ln := range lines {
		if ln == "" {
			continue
		}
		if isCompletionDebugLine(ln) {
			continue
		}
		if strings.HasPrefix(ln, ":") && !strings.HasPrefix(ln, "::") {
			continue
		}
		word := strings.TrimSpace(strings.SplitN(ln, "\t", 2)[0])
		if word != "" {
			completions = append(completions, word)
		}
	}
	return completions
}

// isCompletionDebugLine filters Cobra / kubectl __complete noise (e.g. ShellCompDirective lines).
func isCompletionDebugLine(ln string) bool {
	s := strings.TrimSpace(ln)
	if s == "" {
		return true
	}
	ls := strings.ToLower(s)
	if strings.Contains(ls, "shellcompdirective") {
		return true
	}
	if strings.Contains(ls, "completion ended with directive") {
		return true
	}
	if strings.HasPrefix(ls, "completion ended") {
		return true
	}
	return false
}

func filterCompletions(prefix string, comps []string) []string {
	if prefix == "" {
		return comps
	}
	var out []string
	pl := strings.ToLower(prefix)
	for _, c := range comps {
		if strings.HasPrefix(strings.ToLower(c), pl) {
			out = append(out, c)
		}
	}
	return out
}

func longestCommonPrefix(strs []string) string {
	if len(strs) == 0 {
		return ""
	}
	if len(strs) == 1 {
		return strs[0]
	}
	ref := strs[0]
	n := len(ref)
	for i := range ref {
		ch := ref[i]
		for _, s := range strs[1:] {
			if i >= len(s) || s[i] != ch {
				return ref[:i]
			}
		}
	}
	return ref[:n]
}

// parseForComplete returns argv for kubectl __complete (subcommands and flags), and rune span of token being completed.
func parseForComplete(rs []rune, cursor int) (kubectlBin string, invokeArgs []string, repStart, repEnd int, ok bool) {
	kubectlBin, err := exec.LookPath("kubectl")
	if err != nil {
		return "", nil, 0, 0, false
	}

	before := string(rs[:cursor])
	trimmedRight := strings.TrimRight(before, " \t")
	trailingSpace := len(trimmedRight) < len(before)

	var ws []string
	if trailingSpace {
		if trimmedRight == "" {
			ws = []string{""}
		} else {
			ws = strings.Fields(trimmedRight)
			ws = append(ws, "")
		}
	} else {
		ws = strings.Fields(trimmedRight)
	}

	if len(ws) == 0 {
		return kubectlBin, nil, 0, 0, false
	}

	inv := ws[0]
	if inv != "kubectl" && inv != "k" {
		return kubectlBin, nil, 0, 0, false
	}

	binRunes := []rune(inv)
	// Only complete when cursor is at end of the binary name (not mid-word).
	if len(ws) == 1 && !trailingSpace {
		if cursor != len(binRunes) {
			return kubectlBin, nil, 0, 0, false
		}
		invokeArgs = []string{""}
		repStart = cursor
		repEnd = cursor
		return kubectlBin, invokeArgs, repStart, repEnd, true
	}

	invokeArgs = ws[1:]
	if len(invokeArgs) == 0 {
		invokeArgs = []string{""}
	}

	lastSep := -1
	for i := 0; i < cursor; i++ {
		if rs[i] == ' ' || rs[i] == '\t' {
			lastSep = i
		}
	}
	repStart = lastSep + 1
	repEnd = cursor

	return kubectlBin, invokeArgs, repStart, repEnd, true
}
