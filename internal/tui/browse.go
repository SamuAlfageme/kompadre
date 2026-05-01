package tui

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/list"
)

// ErrNotDir is returned when browseItems is given a non-directory path.
var ErrNotDir = errors.New("path is not a directory")

type pickView int

const (
	pickQuickList pickView = iota
	pickBrowse
	pickPathEntry
)

type browseItem struct {
	label string
	full  string
	isDir bool
}

func (b browseItem) Title() string {
	if b.isDir && b.label != ".." {
		return b.label + "/"
	}
	return b.label
}

func (b browseItem) Description() string {
	if b.isDir {
		return "directory"
	}
	return "file"
}

func (b browseItem) FilterValue() string {
	return b.full + " " + b.label
}

// browseItems returns list items for a directory: parent .., subdirs, files.
func browseItems(dir string) ([]list.Item, string, error) {
	dir = filepath.Clean(dir)
	info, err := os.Stat(dir)
	if err != nil {
		return nil, dir, err
	}
	if !info.IsDir() {
		return nil, dir, ErrNotDir
	}

	var dirs, files []browseItem
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, dir, err
	}

	for _, e := range entries {
		name := e.Name()
		full := filepath.Join(dir, name)
		if e.IsDir() {
			dirs = append(dirs, browseItem{label: name, full: full, isDir: true})
			continue
		}
		// Include regular files and readable symlinks to files.
		fi, err := os.Stat(full)
		if err != nil || fi.IsDir() {
			continue
		}
		files = append(files, browseItem{label: name, full: full, isDir: false})
	}

	sort.Slice(dirs, func(i, j int) bool {
		return strings.ToLower(dirs[i].label) < strings.ToLower(dirs[j].label)
	})
	sort.Slice(files, func(i, j int) bool {
		return strings.ToLower(files[i].label) < strings.ToLower(files[j].label)
	})

	var items []list.Item
	parent := filepath.Dir(dir)
	if parent != dir {
		items = append(items, browseItem{label: "..", full: parent, isDir: true})
	}
	for _, d := range dirs {
		d := d
		items = append(items, d)
	}
	for _, f := range files {
		f := f
		items = append(items, f)
	}

	out := make([]list.Item, len(items))
	for i := range items {
		out[i] = items[i]
	}
	return out, dir, nil
}
