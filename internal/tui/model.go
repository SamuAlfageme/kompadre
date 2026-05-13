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
	"kompadre/internal/history"
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

// Model is the root Bubble Tea model.
type Model struct {
	phase phase
	w, h  int

	kubeList list.Model

	leftKube  string
	rightKube string
	leftCtx   string
	leftNS    string
	rightCtx  string
	rightNS   string

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
	busyDot  int

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

	// context picker (ctrl+k)
	ctxMenu    bool
	ctxSide    string // "left" | "right"
	ctxChoices []kubeconfig.ContextEntry
	ctxIndex   int
	ctxScroll  int

	// namespace picker (ctrl+n)
	nsMenu     bool
	nsSide     string // "left" | "right"
	nsChoices  []string
	nsIndex    int
	nsScroll   int
	nsFetching bool
	nsFetchDot int // animation frame (0-3)

	// autoPrompt is consumed once on Init: it pre-fills the unified prompt and immediately
	// kicks off the comparison so the TUI launches with results already rendered.
	autoPrompt string
	// autoDelta jumps from the first comparison result straight into the delta view.
	autoDelta bool

	// saveDir is the directory for saving left/right outputs (empty disables save on 's' key).
	saveDir string

	// confirmQuit gates quitting behind a y/n prompt.
	confirmQuit bool

	// history picker (ctrl+r)
	histMenu    bool
	histAll     []string // full history (newest-first), loaded on open
	histMatches []string // filtered subset
	histQuery   string
	histIndex   int
	histScroll  int

	// in-input history navigation (up/down arrows in the prompt).
	// inputHistIdx == -1 means "not browsing"; otherwise it's an index into inputHistList.
	inputHistList  []string
	inputHistIdx   int
	inputHistDraft string // user's pending text saved when entering history nav
	inputHistField string // "unified" | "left" | "right" — which field owns the draft
}

// New creates the initial model. Pass empty strings for both kubeconfigs to start the picker.
// When both leftKube and rightKube are non-empty, paths are validated and the UI opens
// directly on the compare screen. When prompt is non-empty (only valid alongside both
// kubeconfigs), the unified prompt is pre-filled and run on launch; if autoDelta is also set,
// the TUI jumps straight to the delta view as soon as results are ready.
// saveDir enables the "s" key in the delta view to persist left/right outputs to disk.
func New(leftKube, rightKube, prompt string, autoDelta bool, saveDir string) (*Model, error) {
	m := newModel()
	m.saveDir = saveDir
	leftKube = strings.TrimSpace(leftKube)
	rightKube = strings.TrimSpace(rightKube)
	prompt = strings.TrimSpace(prompt)
	if leftKube == "" && rightKube == "" {
		if prompt != "" || autoDelta {
			return nil, fmt.Errorf("a prompt or --delta requires both kubeconfig paths as positional arguments")
		}
		return m, nil
	}
	// Left kubeconfig provided, right still needs picking — skip to phasePickRight.
	if leftKube != "" && rightKube == "" {
		lk, err := validateKubeconfigFile(leftKube)
		if err != nil {
			return nil, fmt.Errorf("left kubeconfig: %w", err)
		}
		m.leftKube = lk
		m.leftCtx, m.leftNS = kubeconfig.ActiveContext(lk)
		m.phase = phasePickRight
		return m, nil
	}
	if leftKube == "" {
		return nil, fmt.Errorf("provide both kubeconfig paths as positional arguments, or omit them to use the picker")
	}
	if autoDelta && prompt == "" {
		return nil, fmt.Errorf("--delta requires a prompt as the third positional argument")
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
	m.leftCtx, m.leftNS = kubeconfig.ActiveContext(lk)
	m.rightCtx, m.rightNS = kubeconfig.ActiveContext(rk)
	m.phase = phaseCompare
	m.unifiedInput.Focus()
	if prompt != "" {
		m.unifiedInput.SetValue(prompt)
		m.unifiedInput.SetCursor(len(prompt))
		m.autoPrompt = prompt
		m.autoDelta = autoDelta
	}
	m.layoutViewports()
	return m, nil
}

func newModel() *Model {
	paths := kubeconfig.Discover()
	defaultPath := kubeconfig.DefaultPath()
	items := buildDiscoverTree(paths, defaultPath)
	l := newTreePickList(items)

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
		inputHistIdx:  -1,
		unifiedInput:  ui,
		leftInput:     li,
		rightInput:    ri,
	}
	m.browseList = newTreePickList(nil)

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
	saveDoneMsg struct {
		leftPath, rightPath string
		err                 string
	}
	nsFetchDoneMsg struct {
		side       string
		namespaces []string
	}
	nsFetchTickMsg struct{}
	busyTickMsg    struct{}
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

func (m *Model) sizePickLists() {
	if m.w <= 0 || m.h <= 0 {
		return
	}
	innerW := m.comparePaneInnerW()
	borderV := paneStyle.GetVerticalFrameSize()
	// bottom area: status/error line (1) + help bar (1)
	paneContentH := max(3, m.h-2-borderV)
	titleLine := 1
	listH := max(3, paneContentH-titleLine)

	m.kubeList.SetWidth(innerW)
	m.kubeList.SetHeight(listH)
	// Browse list has a directory path line above it
	m.browseList.SetWidth(innerW)
	m.browseList.SetHeight(max(3, listH-1))
	m.pickPathInput.Width = max(10, innerW-4)
}

func (m *Model) refreshBrowse() error {
	items, resolved, err := buildBrowseTree(m.browseDir)
	if err != nil {
		return err
	}
	m.browseDir = resolved
	m.browseList = newTreePickList(items)
	m.sizePickLists()
	return nil
}

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd {
	if m.autoPrompt != "" {
		cmd := m.autoPrompt
		m.autoPrompt = ""
		tick := m.startBusy("Running…")
		return tea.Batch(textinput.Blink, m.runBoth(cmd), tick)
	}
	return textinput.Blink
}

func (m *Model) askQuit() (tea.Model, tea.Cmd) {
	m.confirmQuit = true
	m.status = "Quit? y/n - ctrl+C"
	return m, nil
}

func (m *Model) startBusy(status string) tea.Cmd {
	m.busy = true
	m.busyDot = 0
	m.status = status
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return busyTickMsg{} })
}

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.confirmQuit {
		if km, ok := msg.(tea.KeyMsg); ok {
			m.confirmQuit = false
			switch km.String() {
			case "y", "Y", "ctrl+c":
				return m, tea.Quit
			default:
				m.status = ""
				return m, nil
			}
		}
	}

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
		if m.autoDelta && (m.leftOut != "" || m.rightOut != "") {
			m.autoDelta = false
			return m.openDelta()
		}
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

	case busyTickMsg:
		if !m.busy {
			return m, nil
		}
		m.busyDot = (m.busyDot + 1) % 10
		return m, tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return busyTickMsg{} })

	case nsFetchTickMsg:
		if !m.nsFetching {
			return m, nil
		}
		m.nsFetchDot = (m.nsFetchDot + 1) % 4
		return m, tea.Tick(250*time.Millisecond, func(time.Time) tea.Msg { return nsFetchTickMsg{} })

	case nsFetchDoneMsg:
		wasFetching := m.nsFetching
		m.nsFetching = false
		if !wasFetching {
			return m, nil
		}
		m.status = ""
		if len(msg.namespaces) == 0 {
			m.status = "no namespaces found"
			return m, nil
		}
		m.nsMenu = true
		m.nsSide = msg.side
		m.nsChoices = msg.namespaces
		m.nsIndex = 0
		m.nsScroll = 0
		activeNS := m.leftNS
		if msg.side == "right" {
			activeNS = m.rightNS
		}
		for i, ns := range msg.namespaces {
			if ns == activeNS {
				m.nsIndex = i
				break
			}
		}
		m.nsEnsureScroll()
		m.status = m.nsStatusLine()
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

	case saveDoneMsg:
		if msg.err != "" {
			m.status = "save error: " + msg.err
		} else {
			m.status = "saved → delta " + msg.leftPath + " " + msg.rightPath
		}
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
	// While typing the filter (/…), the list owns the keyboard (incl. enter to apply).
	// When a filter is already applied, we still handle enter / navigation here.
	if m.kubeList.SettingFilter() {
		switch msg.String() {
		case "ctrl+c":
			return m.askQuit()
		}
		var cmd tea.Cmd
		m.kubeList, cmd = m.kubeList.Update(msg)
		return m, cmd
	}

	switch msg.String() {
	case "ctrl+c":
		return m.askQuit()
	case "q":
		return m.askQuit()
	case "esc":
		if m.kubeList.IsFiltered() {
			var cmd tea.Cmd
			m.kubeList, cmd = m.kubeList.Update(msg)
			return m, cmd
		}
		return m.askQuit()
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
		m.pickPathInput.Width = max(20, m.comparePaneInnerW()-4)
		return m, textinput.Blink
	case "b":
		if m.phase == phasePickRight {
			m.phase = phasePickLeft
			return m, nil
		}
	}
	if isPickActivate(msg) {
		it, ok := m.kubeList.SelectedItem().(pickItem)
		if !ok {
			return m, nil
		}
		if it.isGroup {
			m.pickErr = ""
			m.pickView = pickBrowse
			m.browseDir = it.full
			if err := m.refreshBrowse(); err != nil {
				m.pickErr = err.Error()
			}
			return m, nil
		}
		return m.confirmKubePath(it.full)
	}
	var cmd tea.Cmd
	m.kubeList, cmd = m.kubeList.Update(msg)
	return m, cmd
}

func (m *Model) updatePickBrowse(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// While typing the filter (/…), the list owns the keyboard (incl. enter to apply).
	// When a filter is already applied, enter / - / u must still run our browse actions.
	if m.browseList.SettingFilter() {
		switch msg.String() {
		case "ctrl+c":
			return m.askQuit()
		}
		var cmd tea.Cmd
		m.browseList, cmd = m.browseList.Update(msg)
		return m, cmd
	}

	switch msg.String() {
	case "ctrl+c":
		return m.askQuit()
	case "q":
		return m.askQuit()
	case "esc":
		if m.browseList.IsFiltered() {
			var cmd tea.Cmd
			m.browseList, cmd = m.browseList.Update(msg)
			return m, cmd
		}
		m.pickErr = ""
		m.pickView = pickQuickList
		return m, nil
	case "-", "u":
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
	if isPickActivate(msg) {
		it, ok := m.browseList.SelectedItem().(pickItem)
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
	}
	var cmd tea.Cmd
	m.browseList, cmd = m.browseList.Update(msg)
	return m, cmd
}

func (m *Model) updatePickPath(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m.askQuit()
	case "esc":
		m.pickErr = ""
		m.pickView = pickQuickList
		m.pickPathInput.Blur()
		return m, nil
	}
	if isPickActivate(msg) {
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
		m.leftCtx, m.leftNS = kubeconfig.ActiveContext(clean)
		m.phase = phasePickRight
		return m, nil
	case phasePickRight:
		m.rightKube = clean
		m.rightCtx, m.rightNS = kubeconfig.ActiveContext(clean)
		m.phase = phaseCompare
		m.unifiedInput.Focus()
		m.layoutViewports()
		return m, textinput.Blink
	}
	return m, nil
}

func (m *Model) updateDiff(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m.askQuit()
	case "esc":
		m.phase = phaseCompare
		m.diffContent = ""
		m.diffErr = ""
		m.status = ""
		return m, textinput.Blink
	case "s":
		return m.saveDiffOutputs()
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
		return m.askQuit()
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

// embedStatusInRow places `stat` right-aligned inside a row if there's room.
// `show` gates whether this row should display the status (e.g. only the focused input in split mode).
// If `stat` doesn't fit, it's truncated rather than dropped entirely so the user
// still sees a hint of what's happening.
func (m *Model) embedStatusInRow(row, stat string, innerW int, show bool) string {
	if stat == "" || !show {
		return row
	}
	rowW := lipgloss.Width(row)
	statW := lipgloss.Width(stat)
	gap := innerW - rowW - statW
	if gap >= 1 {
		return row + strings.Repeat(" ", gap) + stat
	}
	// Not enough room — try to truncate the status to whatever space remains.
	avail := innerW - rowW - 1 // -1 for the separating space
	if avail >= 4 {
		return row + " " + truncate(stat, avail)
	}
	return row
}

// ── Context picker ──────────────────────────────────────────────────────────

const ctxMaxVisible = 10

func (m *Model) ctxMenuReservedLines() int {
	if !m.ctxMenu || len(m.ctxChoices) == 0 {
		return 0
	}
	shown := min(len(m.ctxChoices), ctxMaxVisible)
	lines := 2 + 1 + shown // border + header + rows
	if len(m.ctxChoices) > ctxMaxVisible {
		lines++
	}
	return lines + 1
}

func (m *Model) viewCtxMenu() string {
	if !m.ctxMenu || len(m.ctxChoices) == 0 {
		return ""
	}
	n := len(m.ctxChoices)
	end := min(m.ctxScroll+ctxMaxVisible, n)
	boxW := max(20, m.w-4)
	selStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("229")).Background(lipgloss.Color("236")).Bold(true)
	normalStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	curMark := lipgloss.NewStyle().Foreground(lipgloss.Color("114")).Render("✱ ")
	nsStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))

	sideLabel := "A (left)"
	if m.ctxSide == "right" {
		sideLabel = "B (right)"
	}
	title := lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render("context for " + sideLabel + "  (tab switch pane)")

	var lines []string
	for i := m.ctxScroll; i < end; i++ {
		e := m.ctxChoices[i]
		cur := "  "
		if e.IsCurrent {
			cur = curMark
		}
		nsInfo := nsStyle.Render("  ns:" + e.Namespace)
		label := truncate(e.Name, boxW/2) + nsInfo
		if i == m.ctxIndex {
			lines = append(lines, selStyle.Width(boxW).Render(" ▸ "+cur+label))
		} else {
			lines = append(lines, normalStyle.Width(boxW).Render("   "+cur+label))
		}
	}
	if n > ctxMaxVisible {
		foot := lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render(
			fmt.Sprintf("  showing %d–%d of %d · pgup/pgdn scroll", m.ctxScroll+1, end, n),
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

func (m *Model) openCtxMenu(side string) {
	kubePath := m.leftKube
	if side == "right" {
		kubePath = m.rightKube
	}
	choices := kubeconfig.ListContexts(kubePath)
	if len(choices) == 0 {
		m.status = "no contexts found"
		return
	}
	m.ctxMenu = true
	m.ctxSide = side
	m.ctxChoices = choices
	m.ctxIndex = 0
	m.ctxScroll = 0
	// Pre-select the current context.
	activeCtx := m.leftCtx
	if side == "right" {
		activeCtx = m.rightCtx
	}
	for i, c := range choices {
		if c.Name == activeCtx {
			m.ctxIndex = i
			break
		}
	}
	m.ctxEnsureScroll()
	m.status = m.ctxStatusLine()
	m.layoutViewports()
}

func (m *Model) closeCtxMenu() {
	m.ctxMenu = false
	m.ctxChoices = nil
	m.ctxIndex = 0
	m.ctxScroll = 0
	m.status = ""
	m.layoutViewports()
}

func (m *Model) ctxStatusLine() string {
	n := len(m.ctxChoices)
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("ctx %d/%d (↑↓ • tab pane • enter • esc)", m.ctxIndex+1, n)
}

func (m *Model) ctxEnsureScroll() {
	n := len(m.ctxChoices)
	if n == 0 {
		return
	}
	if m.ctxIndex < m.ctxScroll {
		m.ctxScroll = m.ctxIndex
	}
	if m.ctxIndex >= m.ctxScroll+ctxMaxVisible {
		m.ctxScroll = m.ctxIndex - ctxMaxVisible + 1
	}
	maxScr := max(0, n-ctxMaxVisible)
	if m.ctxScroll > maxScr {
		m.ctxScroll = maxScr
	}
}

func (m *Model) handleCtxMenu(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	n := len(m.ctxChoices)
	switch {
	case msg.String() == "up" || msg.String() == "k":
		if m.ctxIndex > 0 {
			m.ctxIndex--
		} else {
			m.ctxIndex = n - 1
		}
		m.ctxEnsureScroll()
		m.status = m.ctxStatusLine()
		return m, nil

	case msg.String() == "down" || msg.String() == "j":
		if m.ctxIndex < n-1 {
			m.ctxIndex++
		} else {
			m.ctxIndex = 0
		}
		m.ctxEnsureScroll()
		m.status = m.ctxStatusLine()
		return m, nil

	case msg.String() == "pgup":
		m.ctxScroll = max(0, m.ctxScroll-ctxMaxVisible)
		m.ctxEnsureScroll()
		m.status = m.ctxStatusLine()
		return m, nil

	case msg.String() == "pgdown":
		maxScr := max(0, n-ctxMaxVisible)
		m.ctxScroll = min(maxScr, m.ctxScroll+ctxMaxVisible)
		m.ctxEnsureScroll()
		m.status = m.ctxStatusLine()
		return m, nil

	case msg.String() == "tab":
		newSide := "right"
		if m.ctxSide == "right" {
			newSide = "left"
		}
		m.closeCtxMenu()
		m.openCtxMenu(newSide)
		return m, tea.ClearScreen

	case msg.String() == "enter":
		if m.ctxIndex < n {
			chosen := m.ctxChoices[m.ctxIndex]
			if m.ctxSide == "left" {
				m.leftCtx = chosen.Name
				m.leftNS = chosen.Namespace
			} else {
				m.rightCtx = chosen.Name
				m.rightNS = chosen.Namespace
			}
		}
		m.closeCtxMenu()
		m.leftOut = ""
		m.rightOut = ""
		m.layoutViewports()
		mdl, cmd := m.rerunLastCommand()
		return mdl, tea.Batch(cmd, tea.ClearScreen)

	case msg.String() == "esc" || msg.String() == "ctrl+k":
		m.closeCtxMenu()
		return m, tea.ClearScreen

	case msg.String() == "ctrl+c":
		return m.askQuit()

	default:
		return m, nil
	}
}

// ── namespace picker ──────────────────────────────────────────────────

const nsMaxVisible = 12

func (m *Model) nsMenuReservedLines() int {
	if m.nsFetching {
		return 4 // border(2) + text(1) + gap(1)
	}
	if !m.nsMenu || len(m.nsChoices) == 0 {
		return 0
	}
	shown := min(len(m.nsChoices), nsMaxVisible)
	lines := 2 + 1 + shown // border + header + rows
	if len(m.nsChoices) > nsMaxVisible {
		lines++
	}
	return lines + 1
}

func (m *Model) viewNsMenu() string {
	if m.nsFetching {
		dots := strings.Repeat(".", m.nsFetchDot)
		pad := strings.Repeat(" ", 3-m.nsFetchDot)
		sideLabel := "A (left)"
		if m.nsSide == "right" {
			sideLabel = "B (right)"
		}
		spin := spinFrames[(m.nsFetchDot)%len(spinFrames)]
		msg := spin + "  Fetching namespaces for " + sideLabel + " from cluster" + dots + pad
		boxW := max(20, m.w-4)
		return lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("62")).
			Foreground(lipgloss.Color("245")).
			Width(boxW).
			Render(msg)
	}
	if !m.nsMenu || len(m.nsChoices) == 0 {
		return ""
	}
	n := len(m.nsChoices)
	end := min(m.nsScroll+nsMaxVisible, n)
	boxW := max(20, m.w-4)
	selStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("229")).Background(lipgloss.Color("236")).Bold(true)
	normalStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))

	activeNS := m.leftNS
	if m.nsSide == "right" {
		activeNS = m.rightNS
	}
	sideLabel := "A (left)"
	if m.nsSide == "right" {
		sideLabel = "B (right)"
	}
	title := lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render("namespace for " + sideLabel + "  (tab switch pane)")

	curMark := lipgloss.NewStyle().Foreground(lipgloss.Color("114")).Render("✱ ")

	var lines []string
	for i := m.nsScroll; i < end; i++ {
		ns := m.nsChoices[i]
		cur := "  "
		if ns == activeNS {
			cur = curMark
		}
		label := truncate(ns, boxW-8)
		if i == m.nsIndex {
			lines = append(lines, selStyle.Width(boxW).Render(" ▸ "+cur+label))
		} else {
			lines = append(lines, normalStyle.Width(boxW).Render("   "+cur+label))
		}
	}
	if n > nsMaxVisible {
		foot := lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render(
			fmt.Sprintf("  showing %d–%d of %d · pgup/pgdn scroll", m.nsScroll+1, end, n),
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

func (m *Model) fetchNamespaces(side string) tea.Cmd {
	kubePath := m.leftKube
	kctx := m.leftCtx
	if side == "right" {
		kubePath = m.rightKube
		kctx = m.rightCtx
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), kubectlTimeout)
		defer cancel()
		cmd := "kubectl get namespaces -o jsonpath='{.items[*].metadata.name}'"
		if kctx != "" {
			cmd = "kubectl --context=" + kctx + " get namespaces -o jsonpath='{.items[*].metadata.name}'"
		}
		stdout, _, _ := kubectl.RunShell(ctx, kubePath, cmd)
		stdout = strings.Trim(stdout, "' \n")
		var nsList []string
		for _, ns := range strings.Fields(stdout) {
			if ns != "" {
				nsList = append(nsList, ns)
			}
		}
		return nsFetchDoneMsg{side: side, namespaces: nsList}
	}
}

func (m *Model) closeNsMenu() {
	m.nsMenu = false
	m.nsFetching = false
	m.nsChoices = nil
	m.nsIndex = 0
	m.nsScroll = 0
	m.status = ""
	m.layoutViewports()
}

func (m *Model) nsStatusLine() string {
	n := len(m.nsChoices)
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("ns %d/%d (↑↓ • tab pane • enter • esc)", m.nsIndex+1, n)
}

func (m *Model) nsEnsureScroll() {
	n := len(m.nsChoices)
	if n == 0 {
		return
	}
	if m.nsIndex < m.nsScroll {
		m.nsScroll = m.nsIndex
	}
	if m.nsIndex >= m.nsScroll+nsMaxVisible {
		m.nsScroll = m.nsIndex - nsMaxVisible + 1
	}
}

func (m *Model) handleNsMenu(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	n := len(m.nsChoices)
	switch {
	case msg.String() == "up" || msg.String() == "k":
		if m.nsIndex > 0 {
			m.nsIndex--
		} else {
			m.nsIndex = n - 1
		}
		m.nsEnsureScroll()
		m.status = m.nsStatusLine()
		return m, nil

	case msg.String() == "down" || msg.String() == "j":
		if m.nsIndex < n-1 {
			m.nsIndex++
		} else {
			m.nsIndex = 0
		}
		m.nsEnsureScroll()
		m.status = m.nsStatusLine()
		return m, nil

	case msg.String() == "pgup":
		m.nsScroll = max(0, m.nsScroll-nsMaxVisible)
		m.nsEnsureScroll()
		m.status = m.nsStatusLine()
		return m, nil

	case msg.String() == "pgdown":
		maxScr := max(0, n-nsMaxVisible)
		m.nsScroll = min(maxScr, m.nsScroll+nsMaxVisible)
		m.nsEnsureScroll()
		m.status = m.nsStatusLine()
		return m, nil

	case msg.String() == "tab":
		newSide := "right"
		if m.nsSide == "right" {
			newSide = "left"
		}
		m.closeNsMenu()
		m.nsFetching = true
		m.nsFetchDot = 0
		m.nsSide = newSide
		m.layoutViewports()
		tick := tea.Tick(250*time.Millisecond, func(time.Time) tea.Msg { return nsFetchTickMsg{} })
		return m, tea.Batch(m.fetchNamespaces(newSide), tick, tea.ClearScreen)

	case msg.String() == "enter":
		if m.nsIndex < n {
			chosen := m.nsChoices[m.nsIndex]
			if m.nsSide == "left" {
				m.leftNS = chosen
			} else {
				m.rightNS = chosen
			}
		}
		m.closeNsMenu()
		m.leftOut = ""
		m.rightOut = ""
		m.layoutViewports()
		mdl, cmd := m.rerunLastCommand()
		return mdl, tea.Batch(cmd, tea.ClearScreen)

	case msg.String() == "esc" || msg.String() == "ctrl+n":
		m.closeNsMenu()
		return m, tea.ClearScreen

	case msg.String() == "ctrl+c":
		return m.askQuit()

	default:
		return m, nil
	}
}

// ── History picker (ctrl+r) ──────────────────────────────────────────

const histMaxVisible = 12

func (m *Model) histMenuReservedLines() int {
	if !m.histMenu {
		return 0
	}
	shown := min(len(m.histMatches), histMaxVisible)
	if shown == 0 {
		shown = 1 // "no matches" line
	}
	return 2 + 1 + 1 + shown + 1 // border(2) + title(1) + filter(1) + rows + slack
}

func (m *Model) openHistMenu() {
	all := history.Load()
	m.histAll = all
	m.histQuery = ""
	m.histMatches = all
	m.histIndex = 0
	m.histScroll = 0
	m.histMenu = true
	m.status = m.histStatusLine()
	m.layoutViewports()
}

func (m *Model) closeHistMenu() {
	m.histMenu = false
	m.histAll = nil
	m.histMatches = nil
	m.histQuery = ""
	m.histIndex = 0
	m.histScroll = 0
	m.status = ""
	m.layoutViewports()
}

// activeInputField returns which prompt field is currently focused.
func (m *Model) activeInputField() string {
	if !m.splitMode {
		return "unified"
	}
	if m.leftInput.Focused() {
		return "left"
	}
	return "right"
}

func (m *Model) getInputValue(field string) string {
	switch field {
	case "left":
		return m.leftInput.Value()
	case "right":
		return m.rightInput.Value()
	default:
		return m.unifiedInput.Value()
	}
}

func (m *Model) setInputValue(field, v string) {
	switch field {
	case "left":
		m.leftInput.SetValue(v)
		m.leftInput.SetCursor(len(v))
	case "right":
		m.rightInput.SetValue(v)
		m.rightInput.SetCursor(len(v))
	default:
		m.unifiedInput.SetValue(v)
		m.unifiedInput.SetCursor(len(v))
	}
}

// resetInputHistNav clears any in-progress in-input history navigation
// (called when the user submits, switches fields, or types a normal key).
func (m *Model) resetInputHistNav() {
	m.inputHistIdx = -1
	m.inputHistList = nil
	m.inputHistDraft = ""
	m.inputHistField = ""
}

// stepInputHistory cycles the focused prompt through command history.
// dir == -1 → older (up), dir == +1 → newer (down).
func (m *Model) stepInputHistory(dir int) {
	field := m.activeInputField()
	// If we were browsing on a different field, reset state.
	if m.inputHistIdx >= 0 && m.inputHistField != field {
		m.resetInputHistNav()
	}
	if m.inputHistIdx < 0 {
		// Fresh entry into history nav: snapshot the draft and load the list.
		m.inputHistList = history.Load()
		if len(m.inputHistList) == 0 {
			return
		}
		m.inputHistDraft = m.getInputValue(field)
		m.inputHistField = field
		if dir < 0 {
			m.inputHistIdx = 0
			m.setInputValue(field, m.inputHistList[0])
		}
		// dir == +1 from idle does nothing (already at "new line").
		return
	}
	// Already browsing.
	if dir < 0 { // older
		if m.inputHistIdx+1 < len(m.inputHistList) {
			m.inputHistIdx++
			m.setInputValue(field, m.inputHistList[m.inputHistIdx])
		}
		return
	}
	// newer
	if m.inputHistIdx > 0 {
		m.inputHistIdx--
		m.setInputValue(field, m.inputHistList[m.inputHistIdx])
		return
	}
	// Stepping past the newest entry returns to the user's pending draft.
	draft := m.inputHistDraft
	m.resetInputHistNav()
	m.setInputValue(field, draft)
}

func (m *Model) histRefilter() {
	m.histMatches = history.FuzzyMatch(m.histAll, m.histQuery)
	m.histIndex = 0
	m.histScroll = 0
}

func (m *Model) histStatusLine() string {
	n := len(m.histMatches)
	if n == 0 {
		return "history: no matches"
	}
	return fmt.Sprintf("history %d/%d (↑↓ • enter select • esc cancel)", m.histIndex+1, n)
}

func (m *Model) histEnsureScroll() {
	n := len(m.histMatches)
	if n == 0 {
		return
	}
	if m.histIndex < m.histScroll {
		m.histScroll = m.histIndex
	}
	if m.histIndex >= m.histScroll+histMaxVisible {
		m.histScroll = m.histIndex - histMaxVisible + 1
	}
	maxScr := max(0, n-histMaxVisible)
	if m.histScroll > maxScr {
		m.histScroll = maxScr
	}
}

func (m *Model) handleHistMenu(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m.askQuit()
	case "esc", "ctrl+r":
		m.closeHistMenu()
		return m, tea.ClearScreen
	case "enter":
		if len(m.histMatches) > 0 && m.histIndex < len(m.histMatches) {
			chosen := m.histMatches[m.histIndex]
			m.closeHistMenu()
			if m.splitMode {
				if m.leftInput.Focused() {
					m.leftInput.SetValue(chosen)
					m.leftInput.SetCursor(len(chosen))
				} else {
					m.rightInput.SetValue(chosen)
					m.rightInput.SetCursor(len(chosen))
				}
			} else {
				m.unifiedInput.SetValue(chosen)
				m.unifiedInput.SetCursor(len(chosen))
			}
			return m, tea.Batch(textinput.Blink, tea.ClearScreen)
		}
		return m, nil
	case "up", "ctrl+p":
		n := len(m.histMatches)
		if n > 0 {
			m.histIndex = (m.histIndex - 1 + n) % n
			m.histEnsureScroll()
			m.status = m.histStatusLine()
		}
		return m, nil
	case "down", "ctrl+n":
		n := len(m.histMatches)
		if n > 0 {
			m.histIndex = (m.histIndex + 1) % n
			m.histEnsureScroll()
			m.status = m.histStatusLine()
		}
		return m, nil
	case "pgup":
		m.histScroll = max(0, m.histScroll-histMaxVisible)
		m.histEnsureScroll()
		m.status = m.histStatusLine()
		return m, nil
	case "pgdown":
		maxScr := max(0, len(m.histMatches)-histMaxVisible)
		m.histScroll = min(maxScr, m.histScroll+histMaxVisible)
		m.histEnsureScroll()
		m.status = m.histStatusLine()
		return m, nil
	case "backspace":
		if len(m.histQuery) > 0 {
			m.histQuery = m.histQuery[:len(m.histQuery)-1]
			m.histRefilter()
			m.histEnsureScroll()
			m.status = m.histStatusLine()
		}
		return m, nil
	default:
		if msg.Type == tea.KeyRunes && len(msg.Runes) > 0 {
			r := msg.Runes[0]
			if unicode.IsPrint(r) {
				m.histQuery += string(r)
				m.histRefilter()
				m.histEnsureScroll()
				m.status = m.histStatusLine()
				return m, nil
			}
		}
		return m, nil
	}
}

func (m *Model) viewHistMenu() string {
	if !m.histMenu {
		return ""
	}
	boxW := max(20, m.w-4)
	selStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("229")).Background(lipgloss.Color("236")).Bold(true)
	normalStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	title := lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render("command history (ctrl+r)")

	filterLine := lipgloss.NewStyle().Foreground(lipgloss.Color("213")).Render("/ ") +
		lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Render(m.histQuery) +
		lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render("▏")

	n := len(m.histMatches)
	var rows []string
	if n == 0 {
		rows = append(rows, dimStyle.Width(boxW).Render("   (no matches)"))
	} else {
		end := min(m.histScroll+histMaxVisible, n)
		for i := m.histScroll; i < end; i++ {
			label := truncate(m.histMatches[i], boxW-6)
			if i == m.histIndex {
				rows = append(rows, selStyle.Width(boxW).Render(" ▸ "+label))
			} else {
				rows = append(rows, normalStyle.Width(boxW).Render("   "+label))
			}
		}
		if n > histMaxVisible {
			foot := dimStyle.Render(
				fmt.Sprintf("  showing %d–%d of %d · pgup/pgdn scroll", m.histScroll+1, end, n),
			)
			rows = append(rows, foot)
		}
	}
	body := strings.Join(rows, "\n")
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("62")).
		Width(boxW).
		Render(lipgloss.JoinVertical(lipgloss.Left, title, filterLine, body))
}

func (m *Model) updateCompare(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.histMenu {
		return m.handleHistMenu(msg)
	}
	if m.ctxMenu {
		return m.handleCtxMenu(msg)
	}
	if m.nsMenu {
		return m.handleNsMenu(msg)
	}
	if m.nsFetching {
		if msg.String() == "esc" || msg.String() == "ctrl+n" || msg.String() == "ctrl+c" {
			m.nsFetching = false
			m.status = ""
			m.layoutViewports()
			if msg.String() == "ctrl+c" {
				return m.askQuit()
			}
			return m, tea.ClearScreen
		}
		return m, nil
	}
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
	case "up":
		m.stepInputHistory(-1)
		return m, nil
	case "down":
		m.stepInputHistory(+1)
		return m, nil
	}

	// While kubectl/delta is running, keep the prompt live so you can type the next
	// command and use Tab completion; only block actions that conflict with the in-flight run.
	if m.busy {
		switch msg.String() {
		case "ctrl+c":
			return m.askQuit()
		case "enter":
			m.status = "waiting for current run…"
			return m, nil
		case "esc":
			return m, nil
		case "ctrl+d":
			return m, nil
		}
	}

	ks := msg.String()
	if m.splitMode && (ks == "left" || ks == "right") {
		if ks == "left" && m.rightInput.Focused() {
			m.resetInputHistNav()
			m.rightInput.Blur()
			m.leftInput.Focus()
			return m, textinput.Blink
		}
		if ks == "right" && m.leftInput.Focused() {
			m.resetInputHistNav()
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

	if ks == "ctrl+k" {
		side := "left"
		if m.splitMode && m.rightInput.Focused() {
			side = "right"
		}
		m.openCtxMenu(side)
		return m, nil
	}

	if ks == "ctrl+n" {
		side := "left"
		if m.splitMode && m.rightInput.Focused() {
			side = "right"
		}
		m.nsFetching = true
		m.nsFetchDot = 0
		m.nsSide = side
		m.status = ""
		m.layoutViewports()
		tick := tea.Tick(250*time.Millisecond, func(time.Time) tea.Msg { return nsFetchTickMsg{} })
		return m, tea.Batch(m.fetchNamespaces(side), tick)
	}

	if ks == "ctrl+r" {
		m.openHistMenu()
		return m, nil
	}

	if key.Matches(msg, toggleSplitKeys) {
		m.clearCompMenu()
		m.completionEpoch++
		m.resetInputHistNav()
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
		return m.askQuit()
	case "esc":
		m.clearCompMenu()
		m.completionEpoch++
		m.phase = phasePickRight
		m.pickView = pickQuickList
		m.kubeList.Title = "Select kubeconfig — RIGHT cluster"
		return m, nil

	case "enter":
		return m.submitCompare()

	case "ctrl+d":
		if m.leftOut != "" || m.rightOut != "" {
			return m.openDelta()
		}
	}

	// If the user types or edits while browsing history, lock in the current
	// text and exit history-nav mode (the saved draft is discarded).
	if m.inputHistIdx >= 0 && isContentEditingKey(msg) {
		m.inputHistIdx = -1
		m.inputHistList = nil
		m.inputHistDraft = ""
		m.inputHistField = ""
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

// isContentEditingKey reports whether a key event modifies buffer content
// (so we can lock in any in-progress history navigation).
func isContentEditingKey(msg tea.KeyMsg) bool {
	switch msg.String() {
	case "backspace", "delete", "ctrl+w", "ctrl+u":
		return true
	}
	if msg.Type == tea.KeyRunes && len(msg.Runes) > 0 {
		return unicode.IsPrint(msg.Runes[0])
	}
	return false
}

func (m *Model) rerunLastCommand() (tea.Model, tea.Cmd) {
	if m.splitMode {
		lc := strings.TrimSpace(m.leftInput.Value())
		rc := strings.TrimSpace(m.rightInput.Value())
		if lc == "" && rc == "" {
			return m, nil
		}
		tick := m.startBusy("Running…")
		m.layoutViewports()
		return m, tea.Batch(m.runSplit(lc, rc), tick)
	}
	cmd := strings.TrimSpace(m.unifiedInput.Value())
	if cmd == "" {
		return m, nil
	}
	tick := m.startBusy("Running…")
	m.layoutViewports()
	return m, tea.Batch(m.runBoth(cmd), tick)
}

func (m *Model) submitCompare() (tea.Model, tea.Cmd) {
	if m.busy {
		m.status = "waiting for current run…"
		return m, nil
	}
	m.resetInputHistNav()
	if m.splitMode {
		lc := strings.TrimSpace(m.leftInput.Value())
		rc := strings.TrimSpace(m.rightInput.Value())
		if lc == "" && rc == "" {
			m.status = "enter at least one command"
			return m, nil
		}
		m.discardCompletionUI()
		history.Append(lc)
		if rc != lc {
			history.Append(rc)
		}
		tick := m.startBusy("Running…")
		m.layoutViewports()
		return m, tea.Batch(m.runSplit(lc, rc), tick)
	}

	cmd := strings.TrimSpace(m.unifiedInput.Value())
	if cmd == "" {
		m.status = "empty command"
		return m, nil
	}
	m.discardCompletionUI()
	history.Append(cmd)
	tick := m.startBusy("Running…")
	m.layoutViewports()
	return m, tea.Batch(m.runBoth(cmd), tick)
}

// prependKubeFlags injects --context and --namespace flags into a kubectl/k command.
// Non-kubectl commands are returned unchanged.
func prependKubeFlags(command, kctx, ns string) string {
	if kctx == "" {
		return command
	}
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return command
	}
	fields := strings.Fields(trimmed)
	bin := fields[0]
	if bin != "kubectl" && bin != "k" {
		return command
	}
	rest := strings.TrimSpace(trimmed[len(bin):])
	flags := "--context=" + kctx
	if ns != "" {
		flags += " --namespace=" + ns
	}
	if rest == "" {
		return bin + " " + flags
	}
	return bin + " " + flags + " " + rest
}

func (m *Model) runBoth(command string) tea.Cmd {
	leftKube := m.leftKube
	rightKube := m.rightKube
	leftCmd := prependKubeFlags(command, m.leftCtx, m.leftNS)
	rightCmd := prependKubeFlags(command, m.rightCtx, m.rightNS)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), kubectlTimeout)
		defer cancel()

		var lo, le, ro, re string
		var errL, errR error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			lo, le, errL = kubectl.RunShell(ctx, leftKube, leftCmd)
		}()
		go func() {
			defer wg.Done()
			ro, re, errR = kubectl.RunShell(ctx, rightKube, rightCmd)
		}()
		wg.Wait()

		var err error
		if errL != nil {
			err = errL
		} else if errR != nil {
			err = errR
		}
		return runBothDoneMsg{
			leftOut:  kubectl.FormatOutput(lo, le, errL),
			rightOut: kubectl.FormatOutput(ro, re, errR),
			err:      err,
		}
	}
}

func (m *Model) runSplit(leftCmd, rightCmd string) tea.Cmd {
	lk, rk := m.leftKube, m.rightKube
	lc := prependKubeFlags(leftCmd, m.leftCtx, m.leftNS)
	rc := prependKubeFlags(rightCmd, m.rightCtx, m.rightNS)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), kubectlTimeout)
		defer cancel()

		var lo, le string
		var errL error
		if lc != "" {
			lo, le, errL = kubectl.RunShell(ctx, lk, lc)
		}

		var ro, re string
		var errR error
		if rc != "" {
			ro, re, errR = kubectl.RunShell(ctx, rk, rc)
		}

		return runSplitDoneMsg{
			leftOut:  kubectl.FormatOutput(lo, le, errL),
			rightOut: kubectl.FormatOutput(ro, re, errR),
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

func (m *Model) saveDiffOutputs() (tea.Model, tea.Cmd) {
	dir := m.saveDir
	if dir == "" {
		dir = "."
	}
	left := m.leftOut
	right := m.rightOut
	return m, func() tea.Msg {
		lp, rp, err := delta.SavePair(dir, left, right)
		if err != nil {
			return saveDoneMsg{err: err.Error()}
		}
		return saveDoneMsg{leftPath: lp, rightPath: rp}
	}
}

func (m *Model) openDelta() (tea.Model, tea.Cmd) {
	if m.busy {
		return m, nil
	}
	m.discardCompletionUI()
	tick := m.startBusy("Running delta…")
	m.layoutViewports()
	left := m.leftOut
	right := m.rightOut
	w := m.diffVP.Width
	return m, tea.Batch(func() tea.Msg {
		text, err := delta.Diff(left, right, w)
		if err != nil {
			return diffDoneMsg{err: err.Error()}
		}
		return diffDoneMsg{text: text}
	}, tick)
}

func (m *Model) viewPick() string {
	paneArg := m.comparePaneLipglossWidthArg()
	innerW := m.comparePaneInnerW()
	titleBudget := max(4, innerW-6)
	paneTitleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("62"))

	leftLabel := "LEFT cluster"
	rightLabel := "RIGHT cluster"
	if m.leftKube != "" {
		leftLabel = truncate(shortenHome(m.leftKube), titleBudget)
	}
	if m.rightKube != "" {
		rightLabel = truncate(shortenHome(m.rightKube), titleBudget)
	}
	leftTitle := paneTitleStyle.Render("A  " + leftLabel)
	rightTitle := paneTitleStyle.Render("B  " + rightLabel)

	var leftBody, rightBody string
	if m.phase == phasePickLeft {
		leftBody = m.viewPickActivePane()
		rightBody = m.viewPickWaiting()
	} else {
		leftBody = m.viewPickConfirmed(m.leftKube)
		rightBody = m.viewPickActivePane()
	}

	borderV := paneStyle.GetVerticalFrameSize()
	paneContentH := max(3, m.h-2-borderV) // -statusLine(1) -helpBar(1)

	leftContent := lipgloss.NewStyle().Height(paneContentH).Render(
		lipgloss.JoinVertical(lipgloss.Left, leftTitle, leftBody))
	rightContent := lipgloss.NewStyle().Height(paneContentH).Render(
		lipgloss.JoinVertical(lipgloss.Left, rightTitle, rightBody))

	leftStyled := paneStyle.Width(paneArg).Render(leftContent)
	rightStyled := paneStyle.Width(paneArg).Render(rightContent)
	panes := lipgloss.JoinHorizontal(lipgloss.Top, leftStyled, divider, rightStyled)

	var statusLine string
	if m.pickErr != "" {
		statusLine = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Render(m.pickErr)
	}

	helpStyle := lipgloss.NewStyle().
		Background(lipgloss.Color("235")).
		Foreground(lipgloss.Color("252")).
		Padding(0, 1).
		Align(lipgloss.Right)
	if m.w > 0 {
		helpStyle = helpStyle.Width(m.w)
	}
	help := helpStyle.Render(m.pickHelpLine())

	block := []string{panes}
	if statusLine != "" {
		block = append(block, statusLine)
	}
	block = append(block, help)

	joined := lipgloss.JoinVertical(lipgloss.Left, block...)
	if m.w > 0 && m.h > 0 {
		return lipgloss.Place(m.w, m.h, lipgloss.Left, lipgloss.Top, joined)
	}
	return joined
}

func (m *Model) viewPickActivePane() string {
	switch m.pickView {
	case pickBrowse:
		dirLine := lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render(
			shortenHome(m.browseDir) + "/")
		return lipgloss.JoinVertical(lipgloss.Left, dirLine, m.browseList.View())
	case pickPathEntry:
		lbl := lipgloss.NewStyle().Bold(true).Render("Path to kubeconfig:")
		return lipgloss.JoinVertical(lipgloss.Left, lbl, "", m.pickPathInput.View())
	default:
		return m.kubeList.View()
	}
}

func (m *Model) viewPickWaiting() string {
	style := lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Italic(true)
	return "\n" + style.Render("  Waiting for selection…")
}

func (m *Model) viewPickConfirmed(path string) string {
	check := lipgloss.NewStyle().Foreground(lipgloss.Color("78")).Bold(true).Render("✓")
	pathStr := lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Render(shortenHome(path))
	lines := "\n" + check + " " + pathStr

	ctx, ns := m.leftCtx, m.leftNS
	if path == m.rightKube {
		ctx, ns = m.rightCtx, m.rightNS
	}
	info := m.kubeInfoLine(ctx, ns, 40)
	if info != "" {
		lines += "\n" + info
	}
	return lines
}

func (m *Model) pickHelpLine() string {
	switch m.pickView {
	case pickBrowse:
		return "↑/↓ navigate • / filter • enter open/select • esc quick list • - parent • ctrl+c quit"
	case pickPathEntry:
		return "enter confirm • esc cancel • ctrl+c quit"
	default:
		base := "↑/↓ navigate • / filter • enter select • o browse • p path"
		if m.phase == phasePickRight {
			base += " • b back"
		}
		return base + " • ctrl+c quit"
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

// buildPaneLines assembles exactly `targetH` lines for a compare-view pane:
// title, optional info, then viewport lines — padded or truncated to targetH.
func (m *Model) buildPaneLines(title, info, vpView string, targetH int) []string {
	lines := make([]string, 0, targetH)
	lines = append(lines, title)
	if info != "" {
		lines = append(lines, info)
	}
	for _, l := range strings.Split(vpView, "\n") {
		lines = append(lines, l)
	}
	// Pad short content.
	for len(lines) < targetH {
		lines = append(lines, "")
	}
	// Truncate long content (e.g. viewport wider than pane caused wrapping).
	if len(lines) > targetH {
		lines = lines[:targetH]
	}
	return lines
}

func (m *Model) kubeInfoLine(ctx, ns string, budget int) string {
	if ctx == "" {
		return ""
	}
	ctxLabel := lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render("ctx:")
	ctxVal := lipgloss.NewStyle().Foreground(lipgloss.Color("222")).Render(truncate(ctx, budget/2))
	nsLabel := lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render("ns:")
	nsVal := lipgloss.NewStyle().Foreground(lipgloss.Color("114")).Render(truncate(ns, budget/2))
	return ctxLabel + " " + ctxVal + "  " + nsLabel + " " + nsVal
}

func (m *Model) viewCompare() string {
	paneArg := m.comparePaneLipglossWidthArg()
	titleBudget := max(4, m.comparePaneInnerW()-6)
	paneTitleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("62"))
	leftTitle := paneTitleStyle.Render("A  " + truncate(m.leftKube, titleBudget))
	rightTitle := paneTitleStyle.Render("B  " + truncate(m.rightKube, titleBudget))
	leftInfo := m.kubeInfoLine(m.leftCtx, m.leftNS, titleBudget)
	rightInfo := m.kubeInfoLine(m.rightCtx, m.rightNS, titleBudget)

	// Build pane content as explicit line slices to guarantee both panes
	// have identical line counts. No lipgloss style nesting — paneStyle alone
	// handles width/borders, avoiding any padding/wrapping conflicts.
	headerRows := 1
	if leftInfo != "" || rightInfo != "" {
		headerRows = 2
	}
	contentH := max(3, m.leftVP.Height+headerRows)

	leftLines := m.buildPaneLines(leftTitle, leftInfo, m.leftVP.View(), contentH)
	rightLines := m.buildPaneLines(rightTitle, rightInfo, m.rightVP.View(), contentH)
	leftStyled := paneStyle.Width(paneArg).Render(strings.Join(leftLines, "\n"))
	rightStyled := paneStyle.Width(paneArg).Render(strings.Join(rightLines, "\n"))
	panes := lipgloss.JoinHorizontal(lipgloss.Top, leftStyled, divider, rightStyled)

	var stat string
	if m.busy {
		spin := spinFrames[m.busyDot%len(spinFrames)]
		stat = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Render(spin + " " + m.status)
	} else if m.status != "" {
		stat = lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Render(m.status)
	}

	menuBlock := m.viewCompMenu()
	ctxMenuBlock := m.viewCtxMenu()
	nsMenuBlock := m.viewNsMenu()
	histMenuBlock := m.viewHistMenu()

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
		leftRow := m.embedStatusInRow(
			lipgloss.JoinHorizontal(lipgloss.Left, leftMarker, m.leftInput.View()),
			stat, paneArg-2, m.leftInput.Focused(),
		)
		rightRow := m.embedStatusInRow(
			lipgloss.JoinHorizontal(lipgloss.Left, rightMarker, m.rightInput.View()),
			stat, paneArg-2, m.rightInput.Focused(),
		)
		leftBox := inputFrameStyle.Width(paneArg).Render(leftRow)
		rightBox := inputFrameStyle.Width(paneArg).Render(rightRow)
		inputBlock = lipgloss.JoinHorizontal(lipgloss.Top, leftBox, divider, rightBox)
	} else {
		prompt := lipgloss.NewStyle().Foreground(lipgloss.Color("213")).Bold(true).Render("› ")
		row := lipgloss.JoinHorizontal(lipgloss.Left, prompt, m.unifiedInput.View())
		frameW := m.w - 2
		innerW := frameW - 2 // minus Padding(0,1)
		row = m.embedStatusInRow(row, stat, innerW, true)
		if m.w > 2 {
			inputBlock = inputFrameStyle.Width(frameW).Render(row)
		} else {
			inputBlock = inputFrameStyle.Render(row)
		}
	}

	// Bottom command strip.
	helpPalette := "ctrl+s split • ←/→ focus A|B • ↑/↓ history • ctrl+o complete (split) • ctrl+r history search • ctrl+k ctx • ctrl+n ns • pgup/dn panes • ctrl+d delta • ctrl+c quit"
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
	if menuBlock != "" {
		block = append(block, menuBlock)
	}
	if ctxMenuBlock != "" {
		block = append(block, ctxMenuBlock)
	}
	if nsMenuBlock != "" {
		block = append(block, nsMenuBlock)
	}
	if histMenuBlock != "" {
		block = append(block, histMenuBlock)
	}
	block = append(block, inputBlock, help)

	joined := lipgloss.JoinVertical(lipgloss.Left, block...)
	if m.w > 0 && m.h > 0 {
		return lipgloss.Place(m.w, m.h, lipgloss.Left, lipgloss.Top, joined)
	}
	return joined
}

func (m *Model) viewDiff() string {
	// Bordered pane for the delta output.
	paneW := m.w - 2 // lipgloss Width arg (outer = arg + 2 for border)
	if paneW < 10 {
		paneW = 10
	}
	var paneContent string
	if m.diffErr != "" {
		errBox := lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Render(m.diffErr)
		paneContent = lipgloss.JoinVertical(lipgloss.Left, errBox, m.diffVP.View())
	} else {
		paneContent = m.diffVP.View()
	}
	paneBlock := paneStyle.Width(paneW).Render(paneContent)

	// Bottom help bar — same style as compare screen.
	helpKeys := "s save • esc back • ctrl+c quit"
	helpStyle := lipgloss.NewStyle().
		Background(lipgloss.Color("235")).
		Foreground(lipgloss.Color("252")).
		Padding(0, 1)
	barW := m.w
	if barW < 10 {
		barW = 10
	}
	helpStyle = helpStyle.Width(barW)
	var help string
	if m.status != "" {
		statusRendered := lipgloss.NewStyle().
			Background(lipgloss.Color("235")).
			Foreground(lipgloss.Color("114")).
			Render(m.status)
		keysRendered := lipgloss.NewStyle().
			Background(lipgloss.Color("235")).
			Foreground(lipgloss.Color("252")).
			Render(helpKeys)
		innerW := barW - 2 // subtract Padding(0,1)
		gap := innerW - lipgloss.Width(m.status) - lipgloss.Width(helpKeys)
		if gap < 1 {
			gap = 1
		}
		help = helpStyle.Render(statusRendered + strings.Repeat(" ", gap) + keysRendered)
	} else {
		help = helpStyle.Align(lipgloss.Right).Render(helpKeys)
	}

	block := lipgloss.JoinVertical(lipgloss.Left, paneBlock, help)
	if m.w > 0 && m.h > 0 {
		return lipgloss.Place(m.w, m.h, lipgloss.Left, lipgloss.Top, block)
	}
	return block
}

var spinFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

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
	ctxR := m.ctxMenuReservedLines()
	nsR := m.nsMenuReservedLines()
	histR := m.histMenuReservedLines()
	belowPanes := menuR + ctxR + nsR + histR + inputVisualRows + 1
	paneOuterH := m.h - belowPanes
	borderV := paneStyle.GetVerticalFrameSize()
	headerRows := 1 // pane title
	if m.leftCtx != "" || m.rightCtx != "" {
		headerRows = 2 // pane title + context/namespace info
	}
	innerH := paneOuterH - borderV - headerRows
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
	const statusReserve = 20 // right-aligned status area inside the prompt frame
	unifiedWrapInner := m.w - 2 - 2 // Width(m.w-2) minus left+right padding
	m.unifiedInput.Width = max(20, unifiedWrapInner-lipgloss.Width("› ")-textInputRenderSlack-statusReserve)
	splitWrapInner := m.comparePaneLipglossWidthArg() - 2
	const splitStatusReserve = 20
	m.leftInput.Width = max(4, splitWrapInner-splitMarkerCells-textInputRenderSlack-splitStatusReserve)
	m.rightInput.Width = max(4, splitWrapInner-splitMarkerCells-textInputRenderSlack-splitStatusReserve)

	// Delta view: bordered pane (paneStyle) + 1-line help bar at the bottom.
	// paneStyle has Border (2 cols) + Padding(0,1) (2 cols). lipgloss Width(arg) includes
	// padding but excludes border, so outer = arg+2. Content area = arg - padding = arg - 2.
	diffBorderV := paneStyle.GetVerticalFrameSize()
	diffPaneW := m.w - 2 // lipgloss Width arg; outer pane = m.w
	if diffPaneW < 10 {
		diffPaneW = 10
	}
	diffContentW := diffPaneW - 2 // subtract horizontal padding (Padding 0,1 → 1 left + 1 right)
	m.diffVP.Width = max(10, diffContentW)
	m.diffVP.Height = max(6, m.h-diffBorderV-1) // -1 for help bar
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
