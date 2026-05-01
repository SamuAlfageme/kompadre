package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"kompadre/internal/delta"
	"kompadre/internal/kubeconfig"
	"kompadre/internal/kubectl"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const kubectlTimeout = 3 * time.Minute

// Tab is often KeyRunes {'\t'} (String is "\t"), not KeyTab ("tab"). Match both.
var completeTab = key.NewBinding(key.WithKeys("tab", "\t"))
var shiftTab = key.NewBinding(key.WithKeys("shift+tab"))

// Toggle unified vs split prompts.
var toggleSplitKeys = key.NewBinding(key.WithKeys("ctrl+s"))

const compMaxVisible = 10

type phase int

const (
	phasePickLeft phase = iota
	phasePickRight
	phaseCompare
	phaseDiff
)

type kubeItem struct{ path string }

func (k kubeItem) Title() string       { return k.path }
func (k kubeItem) Description() string { return "" }
func (k kubeItem) FilterValue() string { return k.path }

// Model is the root Bubble Tea model.
type Model struct {
	phase phase
	w, h  int

	kubeList list.Model

	leftKube  string
	rightKube string

	pickView      pickView
	browseDir     string
	browseList    list.Model
	pickPathInput textinput.Model
	pickErr       string

	splitMode    bool
	unifiedInput textinput.Model
	leftInput    textinput.Model
	rightInput   textinput.Model

	leftVP  viewport.Model
	rightVP viewport.Model
	diffVP  viewport.Model

	leftOut  string
	rightOut string
	status   string
	busy     bool

	diffContent string
	diffErr     string

	// kubectl completion picker (fzf-style)
	compMenu     bool
	compChoices  []string
	compIndex    int
	compScroll   int
	compField    string // "unified" | "left" | "right"
	compRepStart int
	compRepEnd   int
	compNeedSep  bool

	// completionEpoch bumps on each new Tab-fetch and when a picker result is dismissed/applied,
	// so late async kubectl __complete responses cannot reopen the UI after you've moved on.
	completionEpoch uint64
}

// New creates the initial model. Pass empty strings to start the kubeconfig picker.
// When both leftKube and rightKube are non-empty, paths are validated and the UI opens
// directly on the compare screen.
func New(leftKube, rightKube string) (*Model, error) {
	m := newModel()
	leftKube = strings.TrimSpace(leftKube)
	rightKube = strings.TrimSpace(rightKube)
	if leftKube == "" && rightKube == "" {
		return m, nil
	}
	if leftKube == "" || rightKube == "" {
		return nil, fmt.Errorf("provide both kubeconfig paths as positional arguments, or omit them to use the picker")
	}
	lk, err := validateKubeconfigFile(leftKube)
	if err != nil {
		return nil, fmt.Errorf("left kubeconfig: %w", err)
	}
	rk, err := validateKubeconfigFile(rightKube)
	if err != nil {
		return nil, fmt.Errorf("right kubeconfig: %w", err)
	}
	m.leftKube = lk
	m.rightKube = rk
	m.phase = phaseCompare
	m.unifiedInput.Focus()
	m.layoutViewports()
	return m, nil
}

func newModel() *Model {
	paths := kubeconfig.Discover()
	items := make([]list.Item, 0, len(paths))
	for _, p := range paths {
		items = append(items, kubeItem{path: p})
	}
	defaultPath := kubeconfig.DefaultPath()
	if len(items) == 0 && defaultPath != "" {
		items = append(items, kubeItem{path: defaultPath})
	}

	l := newStyledPickList(items, "Select kubeconfig — LEFT cluster")

	pi := textinput.New()
	pi.Placeholder = "/path/to/kubeconfig"
	pi.CharLimit = 4096
	pi.Width = 72

	ui := textinput.New()
	ui.Placeholder = "kubectl get pods -A"
	ui.Prompt = ""
	ui.Focus()
	ui.CharLimit = 8000
	ui.Width = 72
	ui.KeyMap.AcceptSuggestion = key.NewBinding(key.WithDisabled())

	li := textinput.New()
	li.Placeholder = "command for LEFT cluster"
	li.Prompt = ""
	li.CharLimit = 8000
	li.KeyMap.AcceptSuggestion = key.NewBinding(key.WithDisabled())

	ri := textinput.New()
	ri.Placeholder = "command for RIGHT cluster"
	ri.Prompt = ""
	ri.CharLimit = 8000
	ri.KeyMap.AcceptSuggestion = key.NewBinding(key.WithDisabled())

	m := &Model{
		phase:         phasePickLeft,
		pickView:      pickQuickList,
		kubeList:      l,
		pickPathInput: pi,
		unifiedInput:  ui,
		leftInput:     li,
		rightInput:    ri,
	}
	m.browseList = newStyledPickList([]list.Item{}, "Browse disk — LEFT kubeconfig")

	m.leftVP = viewport.New(0, 0)
	m.rightVP = viewport.New(0, 0)
	m.diffVP = viewport.New(0, 0)
	return m
}

func validateKubeconfigFile(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", fmt.Errorf("path is empty")
	}
	p = filepath.Clean(expandHomeInPath(p))
	st, err := os.Stat(p)
	if err != nil {
		return "", err
	}
	if st.IsDir() {
		return "", fmt.Errorf("expected a file, got a directory")
	}
	return p, nil
}

type (
	runBothDoneMsg struct {
		leftOut, rightOut string
		err               error
	}
	runSplitDoneMsg struct {
		leftOut, rightOut string
		err               error
	}
	diffDoneMsg struct {
		text string
		err  string
	}
	completeDoneMsg struct {
		epoch    uint64
		field    string
		menuOpen bool
		choices  []string
		repStart int
		repEnd   int
		needSep  bool
		line     string
		cursor   int
		hint     string
	}
)

func newStyledPickList(items []list.Item, title string) list.Model {
	delegate := list.NewDefaultDelegate()
	l := list.New(items, delegate, 0, 0)
	l.Title = title
	l.SetShowStatusBar(true)
	l.SetFilteringEnabled(true)
	l.Styles.Title = lipgloss.NewStyle().Foreground(lipgloss.Color("62")).Bold(true)
	return l
}

func (m *Model) browseListTitle() string {
	if m.phase == phasePickLeft {
		return "Browse disk — LEFT kubeconfig"
	}
	return "Browse disk — RIGHT kubeconfig"
}

func (m *Model) pickListHeight() int {
	if m.h <= 0 {
		return 8
	}
	return max(5, m.h-8)
}

func (m *Model) sizePickLists() {
	if m.w <= 0 {
		return
	}
	lw := m.w - 4
	lh := m.pickListHeight()
	m.kubeList.SetWidth(lw)
	m.kubeList.SetHeight(lh)
	m.browseList.SetWidth(lw)
	m.browseList.SetHeight(lh)
}

func (m *Model) refreshBrowse() error {
	items, resolved, err := browseItems(m.browseDir)
	if err != nil {
		return err
	}
	m.browseDir = resolved
	m.browseList = newStyledPickList(items, m.browseListTitle())
	m.sizePickLists()
	return nil
}

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd {
	return textinput.Blink
}

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w = msg.Width
		m.h = msg.Height
		m.sizePickLists()
		m.pickPathInput.Width = max(20, msg.Width-6)
		m.layoutViewports()
		return m, nil

	case runBothDoneMsg:
		m.busy = false
		m.leftOut = msg.leftOut
		m.rightOut = msg.rightOut
		m.status = ""
		if msg.err != nil {
			m.status = msg.err.Error()
		}
		m.layoutViewports()
		return m, nil

	case runSplitDoneMsg:
		m.busy = false
		m.leftOut = msg.leftOut
		m.rightOut = msg.rightOut
		m.status = ""
		if msg.err != nil {
			m.status = msg.err.Error()
		}
		m.layoutViewports()
		return m, nil

	case diffDoneMsg:
		m.busy = false
		m.status = ""
		m.diffErr = msg.err
		m.diffContent = msg.text
		m.phase = phaseDiff
		m.layoutViewports()
		return m, nil

	case completeDoneMsg:
		if msg.epoch != m.completionEpoch {
			return m, nil
		}
		if msg.menuOpen {
			m.compMenu = true
			m.compChoices = msg.choices
			m.compIndex = 0
			m.compScroll = 0
			m.compField = msg.field
			m.compRepStart = msg.repStart
			m.compRepEnd = msg.repEnd
			m.compNeedSep = msg.needSep
			m.compEnsureScroll()
			m.status = m.compStatusLine()
			m.layoutViewports()
			return m, nil
		}
		switch msg.field {
		case "unified":
			m.unifiedInput.SetValue(msg.line)
			m.unifiedInput.SetCursor(msg.cursor)
		case "left":
			m.leftInput.SetValue(msg.line)
			m.leftInput.SetCursor(msg.cursor)
		case "right":
			m.rightInput.SetValue(msg.line)
			m.rightInput.SetCursor(msg.cursor)
		}
		if msg.hint != "" {
			m.status = msg.hint
		} else {
			m.status = ""
		}
		m.completionEpoch++
		m.layoutViewports()
		return m, textinput.Blink

	case tea.KeyMsg:
		switch m.phase {
		case phasePickLeft, phasePickRight:
			return m.updatePick(msg)
		case phaseCompare:
			return m.updateCompare(msg)
		case phaseDiff:
			return m.updateDiff(msg)
		}

	default:
		return m.passThrough(msg)
	}

	return m, nil
}

func (m *Model) passThrough(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch m.phase {
	case phaseCompare:
		if m.splitMode {
			if m.leftInput.Focused() {
				m.leftInput, cmd = m.leftInput.Update(msg)
			} else {
				m.rightInput, cmd = m.rightInput.Update(msg)
			}
		} else {
			m.unifiedInput, cmd = m.unifiedInput.Update(msg)
		}
	case phaseDiff:
		m.diffVP, cmd = m.diffVP.Update(msg)
	case phasePickLeft, phasePickRight:
		switch m.pickView {
		case pickPathEntry:
			m.pickPathInput, cmd = m.pickPathInput.Update(msg)
		case pickBrowse:
			m.browseList, cmd = m.browseList.Update(msg)
		default:
			m.kubeList, cmd = m.kubeList.Update(msg)
		}
	}
	return m, cmd
}

func (m *Model) updatePick(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.pickView {
	case pickBrowse:
		return m.updatePickBrowse(msg)
	case pickPathEntry:
		return m.updatePickPath(msg)
	default:
		return m.updatePickQuick(msg)
	}
}

func (m *Model) updatePickQuick(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "q":
		if m.kubeList.SettingFilter() {
			var cmd tea.Cmd
			m.kubeList, cmd = m.kubeList.Update(msg)
			return m, cmd
		}
		return m, tea.Quit
	case "esc":
		return m, tea.Quit
	case "o":
		m.pickErr = ""
		m.pickView = pickBrowse
		if m.browseDir == "" {
			if home, err := os.UserHomeDir(); err == nil {
				m.browseDir = home
			} else {
				m.browseDir = "/"
			}
		}
		if err := m.refreshBrowse(); err != nil {
			m.pickErr = err.Error()
		}
		return m, nil
	case "p":
		m.pickErr = ""
		m.pickView = pickPathEntry
		m.pickPathInput.SetValue("")
		m.pickPathInput.Focus()
		m.pickPathInput.Width = max(20, m.w-6)
		return m, textinput.Blink
	case "enter":
		it, ok := m.kubeList.SelectedItem().(kubeItem)
		if !ok {
			return m, nil
		}
		return m.confirmKubePath(it.path)
	case "b":
		if m.phase == phasePickRight {
			m.phase = phasePickLeft
			m.kubeList.Title = "Select kubeconfig — LEFT cluster"
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.kubeList, cmd = m.kubeList.Update(msg)
	return m, cmd
}

func (m *Model) updatePickBrowse(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "q":
		if m.browseList.SettingFilter() {
			var cmd tea.Cmd
			m.browseList, cmd = m.browseList.Update(msg)
			return m, cmd
		}
		return m, tea.Quit
	case "esc":
		// Let the list handle esc while editing or clearing an applied filter;
		// otherwise esc leaves browse for the quick list.
		if m.browseList.SettingFilter() || m.browseList.IsFiltered() {
			var cmd tea.Cmd
			m.browseList, cmd = m.browseList.Update(msg)
			return m, cmd
		}
		m.pickErr = ""
		m.pickView = pickQuickList
		return m, nil
	case "enter":
		// Enter applies the filter while typing; otherwise opens the selection.
		if m.browseList.SettingFilter() {
			var cmd tea.Cmd
			m.browseList, cmd = m.browseList.Update(msg)
			return m, cmd
		}
		it, ok := m.browseList.SelectedItem().(browseItem)
		if !ok {
			return m, nil
		}
		if it.isDir {
			m.browseDir = it.full
			m.pickErr = ""
			if err := m.refreshBrowse(); err != nil {
				m.pickErr = err.Error()
			}
			return m, nil
		}
		return m.confirmKubePath(it.full)
	case "-", "u":
		// Parent navigation must not steal "-" / "u" while typing a filter (e.g. "kube").
		if m.browseList.SettingFilter() {
			var cmd tea.Cmd
			m.browseList, cmd = m.browseList.Update(msg)
			return m, cmd
		}
		parent := filepath.Dir(m.browseDir)
		if parent != m.browseDir {
			m.browseDir = parent
			m.pickErr = ""
			if err := m.refreshBrowse(); err != nil {
				m.pickErr = err.Error()
			}
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.browseList, cmd = m.browseList.Update(msg)
	return m, cmd
}

func (m *Model) updatePickPath(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.pickErr = ""
		m.pickView = pickQuickList
		m.pickPathInput.Blur()
		return m, nil
	case "enter":
		return m.confirmKubePath(m.pickPathInput.Value())
	}
	var cmd tea.Cmd
	m.pickPathInput, cmd = m.pickPathInput.Update(msg)
	return m, cmd
}

func expandHomeInPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return p
		}
		if p == "~" {
			return home
		}
		return filepath.Join(home, p[2:])
	}
	return p
}

func (m *Model) confirmKubePath(path string) (tea.Model, tea.Cmd) {
	clean, err := validateKubeconfigFile(path)
	if err != nil {
		m.pickErr = err.Error()
		return m, nil
	}

	m.pickErr = ""
	m.pickView = pickQuickList
	m.pickPathInput.Blur()

	switch m.phase {
	case phasePickLeft:
		m.leftKube = clean
		m.phase = phasePickRight
		m.kubeList.Title = "Select kubeconfig — RIGHT cluster"
		return m, nil
	case phasePickRight:
		m.rightKube = clean
		m.phase = phaseCompare
		m.unifiedInput.Focus()
		m.layoutViewports()
		return m, textinput.Blink
	}
	return m, nil
}

func (m *Model) updateDiff(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q":
		return m, tea.Quit
	case "esc":
		m.phase = phaseCompare
		m.diffContent = ""
		m.diffErr = ""
		m.status = ""
		return m, textinput.Blink
	case "down", "j":
		m.diffVP.LineDown(1)
		return m, nil
	case "up", "k":
		m.diffVP.LineUp(1)
		return m, nil
	case "pgdown", "f", " ":
		m.diffVP.ViewDown()
		return m, nil
	case "pgup", "b":
		m.diffVP.ViewUp()
		return m, nil
	case "home", "g":
		m.diffVP.GotoTop()
		return m, nil
	case "end", "G":
		m.diffVP.GotoBottom()
		return m, nil
	}
	var cmd tea.Cmd
	m.diffVP, cmd = m.diffVP.Update(msg)
	return m, cmd
}

func (m *Model) completeDeferred(field string, kube, line string, cursor int) tea.Cmd {
	m.completionEpoch++
	epoch := m.completionEpoch
	return func() tea.Msg {
		ctx := context.Background()
		choices, repS, repE, needSep, hint := kubectl.ListCompletions(ctx, kube, line, cursor)
		if hint != "" {
			return completeDoneMsg{epoch: epoch, field: field, hint: hint}
		}
		if len(choices) == 0 {
			return completeDoneMsg{epoch: epoch, field: field, hint: "no completions"}
		}
		if len(choices) == 1 {
			nl, nc := kubectl.ApplyChoice(line, choices[0], repS, repE, needSep)
			return completeDoneMsg{epoch: epoch, field: field, line: nl, cursor: nc}
		}
		return completeDoneMsg{
			epoch: epoch, field: field, menuOpen: true,
			choices: choices, repStart: repS, repEnd: repE, needSep: needSep,
		}
	}
}

func (m *Model) clearCompMenu() {
	m.compMenu = false
	m.compChoices = nil
	m.compIndex = 0
	m.compScroll = 0
	m.compRepStart = 0
	m.compRepEnd = 0
	m.compNeedSep = false
	m.compField = ""
	m.layoutViewports()
}

// discardCompletionUI closes the kubectl picker and invalidates in-flight completion responses.
func (m *Model) discardCompletionUI() {
	m.clearCompMenu()
	m.completionEpoch++
	m.status = ""
}

// compareMenuReservedLines estimates vertical lines taken by the completion box (must fit in layout).
func (m *Model) compareMenuReservedLines() int {
	if m.busy || !m.compMenu || len(m.compChoices) == 0 {
		return 0
	}
	n := len(m.compChoices)
	shown := min(n, compMaxVisible)
	// Bordered box: frame + title + option rows (+ footer when scrolling long lists)
	lines := 2 + 1 + shown
	if n > compMaxVisible {
		lines++
	}
	return lines + 1 // slack so total view height stays within the terminal (lipgloss borders/padding)
}

func (m *Model) compStatusLine() string {
	n := len(m.compChoices)
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("complete %d/%d (↑↓ • tab/shift+tab • enter • esc)", m.compIndex+1, n)
}

func (m *Model) compClampScroll() {
	n := len(m.compChoices)
	maxScr := max(0, n-compMaxVisible)
	if m.compScroll > maxScr {
		m.compScroll = maxScr
	}
	if m.compScroll < 0 {
		m.compScroll = 0
	}
}

func (m *Model) compEnsureScroll() {
	n := len(m.compChoices)
	if n == 0 {
		return
	}
	if m.compIndex < m.compScroll {
		m.compScroll = m.compIndex
	}
	if m.compIndex >= m.compScroll+compMaxVisible {
		m.compScroll = m.compIndex - compMaxVisible + 1
	}
	m.compClampScroll()
}

func (m *Model) applyCompChoice() (tea.Model, tea.Cmd) {
	if len(m.compChoices) == 0 {
		m.clearCompMenu()
		return m, nil
	}
	choice := m.compChoices[m.compIndex]
	fld := m.compField
	rs, re, sep := m.compRepStart, m.compRepEnd, m.compNeedSep
	m.clearCompMenu()

	var line string
	switch fld {
	case "unified":
		line = m.unifiedInput.Value()
	case "left":
		line = m.leftInput.Value()
	case "right":
		line = m.rightInput.Value()
	default:
		return m, nil
	}
	nl, nc := kubectl.ApplyChoice(line, choice, rs, re, sep)
	switch fld {
	case "unified":
		m.unifiedInput.SetValue(nl)
		m.unifiedInput.SetCursor(nc)
	case "left":
		m.leftInput.SetValue(nl)
		m.leftInput.SetCursor(nc)
	case "right":
		m.rightInput.SetValue(nl)
		m.rightInput.SetCursor(nc)
	}
	m.status = ""
	m.completionEpoch++
	return m, textinput.Blink
}

func (m *Model) compCloseAndForward(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	fld := m.compField
	m.clearCompMenu()
	m.status = ""
	m.completionEpoch++
	var cmd tea.Cmd
	switch fld {
	case "unified":
		m.unifiedInput, cmd = m.unifiedInput.Update(msg)
	case "left":
		m.leftInput, cmd = m.leftInput.Update(msg)
	case "right":
		m.rightInput, cmd = m.rightInput.Update(msg)
	}
	return m, cmd
}

func (m *Model) handleCompMenu(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	}

	n := len(m.compChoices)
	if n == 0 {
		m.clearCompMenu()
		return m, nil
	}

	switch {
	case key.Matches(msg, completeTab):
		m.compIndex = (m.compIndex + 1) % n
		m.compEnsureScroll()
		m.status = m.compStatusLine()
		return m, nil

	case key.Matches(msg, shiftTab):
		m.compIndex = (m.compIndex - 1 + n) % n
		m.compEnsureScroll()
		m.status = m.compStatusLine()
		return m, nil

	case msg.String() == "up" || msg.String() == "ctrl+p":
		m.compIndex = (m.compIndex - 1 + n) % n
		m.compEnsureScroll()
		m.status = m.compStatusLine()
		return m, nil

	case msg.String() == "down" || msg.String() == "ctrl+n":
		m.compIndex = (m.compIndex + 1) % n
		m.compEnsureScroll()
		m.status = m.compStatusLine()
		return m, nil

	case msg.String() == "pgup":
		m.compScroll = max(0, m.compScroll-compMaxVisible)
		m.compClampScroll()
		m.status = m.compStatusLine()
		return m, nil

	case msg.String() == "pgdown":
		maxScr := max(0, n-compMaxVisible)
		m.compScroll = min(maxScr, m.compScroll+compMaxVisible)
		m.compClampScroll()
		m.status = m.compStatusLine()
		return m, nil

	case msg.String() == "enter":
		return m.applyCompChoice()

	case msg.String() == "esc":
		m.clearCompMenu()
		m.status = ""
		m.completionEpoch++
		return m, nil

	case msg.String() == "ctrl+s":
		m.clearCompMenu()
		m.status = ""
		m.completionEpoch++
		return m.updateCompare(msg)

	default:
		if msg.Type == tea.KeyRunes && len(msg.Runes) > 0 {
			r := msg.Runes[0]
			if unicode.IsPrint(r) {
				return m.compCloseAndForward(msg)
			}
		}
		return m, nil
	}
}

func (m *Model) viewCompMenu() string {
	if m.busy || !m.compMenu || len(m.compChoices) == 0 {
		return ""
	}
	n := len(m.compChoices)
	end := min(m.compScroll+compMaxVisible, n)
	boxW := max(20, m.w-4)
	selStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("229")).Background(lipgloss.Color("236")).Bold(true)
	normalStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	title := lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render("kubectl completions")
	var lines []string
	for i := m.compScroll; i < end; i++ {
		label := truncate(m.compChoices[i], boxW-6)
		var row string
		if i == m.compIndex {
			row = selStyle.Width(boxW).Render(" ▸ " + label)
		} else {
			row = normalStyle.Width(boxW).Render("   " + label)
		}
		lines = append(lines, row)
	}
	if n > compMaxVisible {
		foot := lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render(
			fmt.Sprintf("  showing %d–%d of %d · pgup/pgdn scroll", m.compScroll+1, end, n),
		)
		lines = append(lines, foot)
	}
	body := strings.Join(lines, "\n")
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("62")).
		Width(boxW).
		Render(lipgloss.JoinVertical(lipgloss.Left, title, body))
}

func (m *Model) updateCompare(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.compMenu {
		return m.handleCompMenu(msg)
	}

	// Scroll panes (arrows stay with the prompt for cursor movement).
	switch msg.String() {
	case "pgdown", "pgup", "home", "end":
		var cmd tea.Cmd
		m.leftVP, cmd = m.leftVP.Update(msg)
		m.rightVP, _ = m.rightVP.Update(msg)
		return m, cmd
	}

	// While kubectl/delta is running, keep the prompt live so you can type the next
	// command and use Tab completion; only block actions that conflict with the in-flight run.
	if m.busy {
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "enter":
			m.status = "waiting for current run…"
			return m, nil
		case "esc":
			return m, nil
		case "d":
			return m, nil
		}
	}

	ks := msg.String()
	if m.splitMode && (ks == "left" || ks == "right") {
		if ks == "left" && m.rightInput.Focused() {
			m.rightInput.Blur()
			m.leftInput.Focus()
			return m, textinput.Blink
		}
		if ks == "right" && m.leftInput.Focused() {
			m.leftInput.Blur()
			m.rightInput.Focus()
			return m, textinput.Blink
		}
	}
	// Tab: see completeTab — must not compare only to "tab" (often "\t" as KeyRunes).
	if key.Matches(msg, completeTab) {
		if !m.splitMode {
			return m, m.completeDeferred("unified", m.leftKube, m.unifiedInput.Value(), m.unifiedInput.Position())
		}
		if m.leftInput.Focused() {
			m.leftInput.Blur()
			m.rightInput.Focus()
		} else {
			m.rightInput.Blur()
			m.leftInput.Focus()
		}
		return m, textinput.Blink
	}
	// In split mode, Tab switches A/B focus; use ctrl+o for kubectl completions on the focused field.
	if ks == "ctrl+o" && m.splitMode {
		if m.leftInput.Focused() {
			return m, m.completeDeferred("left", m.leftKube, m.leftInput.Value(), m.leftInput.Position())
		}
		return m, m.completeDeferred("right", m.rightKube, m.rightInput.Value(), m.rightInput.Position())
	}

	if key.Matches(msg, toggleSplitKeys) {
		m.clearCompMenu()
		m.completionEpoch++
		m.splitMode = !m.splitMode
		if m.splitMode {
			m.unifiedInput.Blur()
			m.leftInput.Focus()
			m.rightInput.Blur()
		} else {
			m.leftInput.Blur()
			m.rightInput.Blur()
			m.unifiedInput.Focus()
		}
		m.layoutViewports()
		return m, textinput.Blink
	}

	switch ks {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.clearCompMenu()
		m.completionEpoch++
		m.phase = phasePickRight
		m.pickView = pickQuickList
		m.kubeList.Title = "Select kubeconfig — RIGHT cluster"
		return m, nil

	case "enter":
		return m.submitCompare()

	case "d":
		if m.leftOut != "" || m.rightOut != "" {
			return m.openDelta()
		}
	}

	var cmd tea.Cmd
	if m.splitMode {
		if m.leftInput.Focused() {
			m.leftInput, cmd = m.leftInput.Update(msg)
		} else {
			m.rightInput, cmd = m.rightInput.Update(msg)
		}
	} else {
		m.unifiedInput, cmd = m.unifiedInput.Update(msg)
	}
	return m, cmd
}

func (m *Model) submitCompare() (tea.Model, tea.Cmd) {
	if m.busy {
		m.status = "waiting for current run…"
		return m, nil
	}
	if m.splitMode {
		lc := strings.TrimSpace(m.leftInput.Value())
		rc := strings.TrimSpace(m.rightInput.Value())
		if lc == "" && rc == "" {
			m.status = "enter at least one command"
			return m, nil
		}
		m.discardCompletionUI()
		m.busy = true
		m.status = "Running…"
		m.layoutViewports()
		return m, m.runSplit(lc, rc)
	}

	cmd := strings.TrimSpace(m.unifiedInput.Value())
	if cmd == "" {
		m.status = "empty command"
		return m, nil
	}
	m.discardCompletionUI()
	m.busy = true
	m.status = "Running…"
	m.layoutViewports()
	return m, m.runBoth(cmd)
}

func (m *Model) runBoth(command string) tea.Cmd {
	leftKube := m.leftKube
	rightKube := m.rightKube
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), kubectlTimeout)
		defer cancel()

		var lo, le, ro, re string
		var errL, errR error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			lo, le, errL = kubectl.RunShell(ctx, leftKube, command)
		}()
		go func() {
			defer wg.Done()
			ro, re, errR = kubectl.RunShell(ctx, rightKube, command)
		}()
		wg.Wait()

		var err error
		if errL != nil {
			err = errL
		} else if errR != nil {
			err = errR
		}
		return runBothDoneMsg{
			leftOut:  formatRunOutput(lo, le, errL),
			rightOut: formatRunOutput(ro, re, errR),
			err:      err,
		}
	}
}

func (m *Model) runSplit(leftCmd, rightCmd string) tea.Cmd {
	lk, rk := m.leftKube, m.rightKube
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), kubectlTimeout)
		defer cancel()

		var lo, le string
		var errL error
		if leftCmd != "" {
			lo, le, errL = kubectl.RunShell(ctx, lk, leftCmd)
		}

		var ro, re string
		var errR error
		if rightCmd != "" {
			ro, re, errR = kubectl.RunShell(ctx, rk, rightCmd)
		}

		return runSplitDoneMsg{
			leftOut:  formatRunOutput(lo, le, errL),
			rightOut: formatRunOutput(ro, re, errR),
			err:      firstErr(errL, errR),
		}
	}
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

func formatRunOutput(stdout, stderr string, err error) string {
	var b strings.Builder
	if stdout != "" {
		b.WriteString(stdout)
	}
	if stderr != "" {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(stderr)
	}
	if err != nil && b.Len() == 0 {
		b.WriteString(err.Error())
	}
	return b.String()
}

func (m *Model) openDelta() (tea.Model, tea.Cmd) {
	if m.busy {
		return m, nil
	}
	m.discardCompletionUI()
	m.busy = true
	m.status = "Running delta…"
	m.layoutViewports()
	left := m.leftOut
	right := m.rightOut
	w := m.w
	return m, func() tea.Msg {
		text, err := delta.Diff(left, right, w)
		if err != nil {
			return diffDoneMsg{err: err.Error()}
		}
		return diffDoneMsg{text: text}
	}
}

func (m *Model) viewPick() string {
	var body string
	switch m.pickView {
	case pickBrowse:
		pathLine := lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render(m.browseDir)
		body = lipgloss.JoinVertical(lipgloss.Left, pathLine, m.browseList.View())
	case pickPathEntry:
		lbl := lipgloss.NewStyle().Bold(true).Render("Path to kubeconfig file:")
		body = lipgloss.JoinVertical(lipgloss.Left, lbl, m.pickPathInput.View())
	default:
		body = m.kubeList.View()
	}
	var errLine string
	if m.pickErr != "" {
		errLine = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Render(m.pickErr) + "\n"
	}
	var help string
	if m.w > 0 {
		help = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241")).
			Width(m.w).
			Align(lipgloss.Right).
			Render(m.pickHelpLine())
	} else {
		help = lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render(m.pickHelpLine())
	}
	return lipgloss.JoinVertical(lipgloss.Left, body, errLine, help)
}

func (m *Model) pickHelpLine() string {
	switch m.pickView {
	case pickBrowse:
		return "↑/↓ navigate • / filter • enter open dir or choose file • esc quick list • u / - parent • ctrl+c quit • q quit when not filtering"
	case pickPathEntry:
		return "enter confirm • esc cancel • ctrl+c quit"
	default:
		return "↑/↓ navigate • / filter • enter choose • o browse disk • p type path • b back (step 2) • esc • ctrl+c quit • q quit when not filtering"
	}
}

// View implements tea.Model.
func (m *Model) View() string {
	if m.w == 0 {
		return "loading…"
	}
	switch m.phase {
	case phasePickLeft, phasePickRight:
		return m.viewPick()
	case phaseCompare:
		return m.viewCompare()
	case phaseDiff:
		return m.viewDiff()
	default:
		return ""
	}
}

func (m *Model) viewCompare() string {
	paneArg := m.comparePaneLipglossWidthArg()
	titleBudget := max(4, m.comparePaneInnerW()-6)
	paneTitleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("62"))
	leftTitle := paneTitleStyle.Render("A  " + truncate(m.leftKube, titleBudget))
	rightTitle := paneTitleStyle.Render("B  " + truncate(m.rightKube, titleBudget))

	leftPaneContent := lipgloss.JoinVertical(lipgloss.Left, leftTitle, m.leftVP.View())
	rightPaneContent := lipgloss.JoinVertical(lipgloss.Left, rightTitle, m.rightVP.View())
	leftStyled := paneStyle.Width(paneArg).Render(leftPaneContent)
	rightStyled := paneStyle.Width(paneArg).Render(rightPaneContent)
	panes := lipgloss.JoinHorizontal(lipgloss.Top, leftStyled, divider, rightStyled)

	stat := m.status
	if m.busy {
		stat = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Render(m.status)
	} else if stat != "" {
		stat = lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Render(stat)
	}

	menuBlock := m.viewCompMenu()

	var inputBlock string
	if m.splitMode {
		leftMarker := lipgloss.NewStyle().Foreground(lipgloss.Color("71")).Bold(true).Render("A")
		rightMarker := lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true).Render("B")
		if m.leftInput.Focused() {
			leftMarker = lipgloss.NewStyle().Foreground(lipgloss.Color("229")).Background(lipgloss.Color("57")).Bold(true).Render(" A ")
			rightMarker = lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true).Render(" B ")
		} else {
			leftMarker = lipgloss.NewStyle().Foreground(lipgloss.Color("71")).Bold(true).Render(" A ")
			rightMarker = lipgloss.NewStyle().Foreground(lipgloss.Color("229")).Background(lipgloss.Color("57")).Bold(true).Render(" B ")
		}
		leftRow := lipgloss.JoinHorizontal(lipgloss.Left, leftMarker, m.leftInput.View())
		rightRow := lipgloss.JoinHorizontal(lipgloss.Left, rightMarker, m.rightInput.View())
		leftBox := inputFrameStyle.Width(paneArg).Render(leftRow)
		rightBox := inputFrameStyle.Width(paneArg).Render(rightRow)
		inputBlock = lipgloss.JoinHorizontal(lipgloss.Top, leftBox, divider, rightBox)
	} else {
		prompt := lipgloss.NewStyle().Foreground(lipgloss.Color("213")).Bold(true).Render("› ")
		row := lipgloss.JoinHorizontal(lipgloss.Left, prompt, m.unifiedInput.View())
		if m.w > 2 {
			inputBlock = inputFrameStyle.Width(m.w - 2).Render(row)
		} else {
			inputBlock = inputFrameStyle.Render(row)
		}
	}

	// Bottom command strip (no enter/tab/esc — those stay implicit for the prompt / menu).
	helpPalette := "ctrl+s split • ←/→ focus A|B • ctrl+o complete (split) • pgup/dn panes • d delta • ctrl+c quit"
	helpStyle := lipgloss.NewStyle().
		Background(lipgloss.Color("235")).
		Foreground(lipgloss.Color("252")).
		Padding(0, 1).
		Align(lipgloss.Right)
	if m.w > 0 {
		helpStyle = helpStyle.Width(m.w)
	}
	help := helpStyle.Render(helpPalette)

	block := []string{panes}
	if stat != "" {
		block = append(block, stat)
	}
	if menuBlock != "" {
		block = append(block, menuBlock)
	}
	block = append(block, inputBlock, help)

	joined := lipgloss.JoinVertical(lipgloss.Left, block...)
	if m.w > 0 && m.h > 0 {
		return lipgloss.Place(m.w, m.h, lipgloss.Left, lipgloss.Top, joined)
	}
	return joined
}

func (m *Model) viewDiff() string {
	footerText := lipgloss.NewStyle().Bold(true).Render("delta — esc back • q quit")
	footer := footerText
	if m.w > 0 {
		footer = lipgloss.Place(m.w, 1, lipgloss.Right, lipgloss.Center, footerText)
	}

	var block string
	if m.diffErr != "" {
		errBox := lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Render(m.diffErr)
		block = lipgloss.JoinVertical(lipgloss.Left, errBox, m.diffVP.View(), footer)
	} else {
		block = lipgloss.JoinVertical(lipgloss.Left, m.diffVP.View(), footer)
	}
	if m.w > 0 && m.h > 0 {
		return lipgloss.Place(m.w, m.h, lipgloss.Left, lipgloss.Top, block)
	}
	return block
}

var (
	paneStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	divider   = lipgloss.NewStyle().Foreground(lipgloss.Color("238")).Render("│")
	// Square frame around command line(s) (distinct from rounded output panes).
	inputFrameStyle = lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color("238")).
		Padding(0, 1)
)

// lipgloss measures outer width of bordered pane/input blocks as Width(arg)+2 (borders outside the width).
// comparePaneLipglossWidthArg returns the Width(...) value so physical outer width equals comparePanePhysicalOuterW.
func (m *Model) comparePanePhysicalOuterW() int {
	g := lipgloss.Width(divider)
	if m.w <= g {
		return 10
	}
	return max(10, (m.w-g)/2)
}

func (m *Model) comparePaneLipglossWidthArg() int {
	return max(4, m.comparePanePhysicalOuterW()-2)
}

func (m *Model) comparePaneInnerW() int {
	return max(4, m.comparePanePhysicalOuterW()-paneStyle.GetHorizontalFrameSize())
}

func (m *Model) layoutViewports() {
	if m.w == 0 || m.h == 0 {
		return
	}
	// Prompt row is one text line inside inputFrameStyle; count border/padding height too.
	inputVisualRows := 1 + inputFrameStyle.GetVerticalFrameSize()
	menuR := m.compareMenuReservedLines()
	// Terminal rows: bordered pane row + stat(1) + menu(menuR) + framed prompt + help(1)
	belowPanes := 1 + menuR + inputVisualRows + 1
	paneOuterH := m.h - belowPanes
	borderV := paneStyle.GetVerticalFrameSize()
	innerH := paneOuterH - borderV - 1 // one content row reserved for in-frame pane title
	if innerH < 3 {
		innerH = 3
	}

	innerW := m.comparePaneInnerW()

	m.leftVP.Width = innerW
	m.leftVP.Height = innerH
	m.rightVP.Width = innerW
	m.rightVP.Height = innerH

	m.leftVP.SetContent(m.leftOut)
	m.rightVP.SetContent(m.rightOut)

	const splitMarkerCells = 3 // " A " / " B " at most
	// inputFrameStyle uses Width(...); lipgloss word-wraps at width minus horizontal padding
	// (Padding(0,1) → −2). bubbles textinput can render one cell wider than Model.Width, so
	// leave slack or the row exceeds the wrap width and a second line appears inside the box.
	const textInputRenderSlack = 1
	unifiedWrapInner := m.w - 2 - 2 // Width(m.w-2) minus left+right padding
	m.unifiedInput.Width = max(20, unifiedWrapInner-lipgloss.Width("› ")-textInputRenderSlack)
	splitWrapInner := m.comparePaneLipglossWidthArg() - 2
	m.leftInput.Width = max(4, splitWrapInner-splitMarkerCells-textInputRenderSlack)
	m.rightInput.Width = max(4, splitWrapInner-splitMarkerCells-textInputRenderSlack)

	m.diffVP.Width = m.w
	m.diffVP.Height = max(6, m.h-2)
	m.diffVP.SetContent(m.diffContent)
}

func truncate(s string, max int) string {
	if max <= 3 {
		return "…"
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
