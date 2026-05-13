package tui

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ErrNotDir is returned when buildBrowseTree is given a non-directory path.
var ErrNotDir = errors.New("path is not a directory")

type pickView int

const (
	pickQuickList pickView = iota
	pickBrowse
	pickPathEntry
)

// pickItem represents an entry in the tree-style kubeconfig picker.
type pickItem struct {
	label   string
	full    string
	isDir   bool
	isGroup bool // directory group header (non-selectable file, opens browse)
	isLast  bool // last child in its group (renders └── instead of ├──)
}

func (p pickItem) Title() string       { return p.label }
func (p pickItem) Description() string { return "" }
func (p pickItem) FilterValue() string {
	if p.isGroup {
		return ""
	}
	return p.full
}

// isPickActivate matches Enter/Return across terminals: most send CR (KeyEnter),
// but some send LF (KeyCtrlJ) or a lone newline/rune.
func isPickActivate(msg tea.KeyMsg) bool {
	switch msg.Type {
	case tea.KeyEnter, tea.KeyCtrlJ:
		return true
	case tea.KeyRunes:
		if msg.Paste || len(msg.Runes) != 1 {
			return false
		}
		r := msg.Runes[0]
		return r == '\r' || r == '\n'
	default:
		return false
	}
}

// treeDelegate renders pick items with tree connectors and icons.
type treeDelegate struct{}

var _ list.ItemDelegate = treeDelegate{}

func (d treeDelegate) Height() int                                   { return 1 }
func (d treeDelegate) Spacing() int                                  { return 0 }
func (d treeDelegate) Update(_ tea.Msg, _ *list.Model) tea.Cmd      { return nil }

func (d treeDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	pi, ok := item.(pickItem)
	if !ok {
		return
	}

	selected := index == m.Index()
	listW := m.Width()

	if pi.isGroup {
		s := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
		if selected {
			s = s.Bold(true).Foreground(lipgloss.Color("252"))
		}
		fmt.Fprint(w, s.Render(truncate(pi.label, listW-1)))
		return
	}

	conn := "├── "
	if pi.isLast {
		conn = "└── "
	}

	cursor := "  "
	if selected {
		cursor = "▸ "
	}

	const prefixCells = 6 // connector(4) + cursor(2)
	maxLabel := max(4, listW-prefixCells-1)

	label := pi.label
	if pi.isDir && label != ".." {
		label += "/"
	}
	label = truncate(label, maxLabel)

	connStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("238"))

	var nameStyle lipgloss.Style
	if selected {
		if pi.isDir {
			nameStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("117")).Bold(true)
		} else {
			nameStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("229")).Bold(true)
		}
	} else {
		if pi.isDir {
			nameStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("75"))
		} else {
			nameStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
		}
	}

	cursorRendered := cursor
	if selected {
		cursorRendered = lipgloss.NewStyle().Foreground(lipgloss.Color("229")).Bold(true).Render(cursor)
	}

	fmt.Fprint(w, connStyle.Render(conn)+cursorRendered+nameStyle.Render(label))
}

// newTreePickList creates a list.Model with the tree delegate and minimal chrome.
func newTreePickList(items []list.Item) list.Model {
	l := list.New(items, treeDelegate{}, 0, 0)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetFilteringEnabled(true)
	l.FilterInput.Prompt = "/ "
	l.FilterInput.PromptStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("213"))
	l.Styles.ActivePaginationDot = lipgloss.NewStyle().Foreground(lipgloss.Color("62"))
	l.Styles.InactivePaginationDot = lipgloss.NewStyle().Foreground(lipgloss.Color("238"))
	l.Styles.NoItems = lipgloss.NewStyle().Foreground(lipgloss.Color("240")).MarginLeft(2)

	// Start cursor on the first selectable (non-header) item.
	for i, item := range items {
		if pi, ok := item.(pickItem); ok && !pi.isGroup {
			l.Select(i)
			break
		}
	}

	return l
}

// buildDiscoverTree groups kubeconfig paths by parent directory into a tree.
func buildDiscoverTree(paths []string, defaultPath string) []list.Item {
	if len(paths) == 0 && defaultPath != "" {
		if st, err := os.Stat(defaultPath); err == nil && !st.IsDir() {
			paths = []string{defaultPath}
		}
	}
	if len(paths) == 0 {
		return nil
	}

	type group struct {
		dir   string
		files []string
	}
	seen := map[string]int{}
	var groups []group
	for _, p := range paths {
		dir := filepath.Dir(p)
		if idx, ok := seen[dir]; ok {
			groups[idx].files = append(groups[idx].files, p)
		} else {
			seen[dir] = len(groups)
			groups = append(groups, group{dir: dir, files: []string{p}})
		}
	}

	var items []list.Item
	for _, g := range groups {
		items = append(items, pickItem{
			label: shortenHome(g.dir) + "/", full: g.dir, isDir: true, isGroup: true,
		})
		for i, f := range g.files {
			items = append(items, pickItem{
				label:  filepath.Base(f),
				full:   f,
				isLast: i == len(g.files)-1,
			})
		}
	}
	return items
}

// buildBrowseTree reads a directory and returns tree items.
func buildBrowseTree(dir string) ([]list.Item, string, error) {
	dir = filepath.Clean(dir)
	info, err := os.Stat(dir)
	if err != nil {
		return nil, dir, err
	}
	if !info.IsDir() {
		return nil, dir, ErrNotDir
	}

	type entry struct {
		name  string
		full  string
		isDir bool
	}
	var dirs, files []entry
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, dir, err
	}

	for _, e := range entries {
		name := e.Name()
		full := filepath.Join(dir, name)
		if e.IsDir() {
			dirs = append(dirs, entry{name, full, true})
			continue
		}
		fi, err := os.Stat(full)
		if err != nil || fi.IsDir() {
			continue
		}
		files = append(files, entry{name, full, false})
	}

	sort.Slice(dirs, func(i, j int) bool {
		return strings.ToLower(dirs[i].name) < strings.ToLower(dirs[j].name)
	})
	sort.Slice(files, func(i, j int) bool {
		return strings.ToLower(files[i].name) < strings.ToLower(files[j].name)
	})

	var all []entry
	parent := filepath.Dir(dir)
	if parent != dir {
		all = append(all, entry{"..", parent, true})
	}
	all = append(all, dirs...)
	all = append(all, files...)

	items := make([]list.Item, len(all))
	for i, e := range all {
		items[i] = pickItem{
			label:  e.name,
			full:   e.full,
			isDir:  e.isDir,
			isLast: i == len(all)-1,
		}
	}
	return items, dir, nil
}

func shortenHome(path string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if path == home {
		return "~"
	}
	if strings.HasPrefix(path, home+string(filepath.Separator)) {
		return "~" + path[len(home):]
	}
	return path
}
