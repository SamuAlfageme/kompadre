package main

import (
	"fmt"
	"os"

	"kompadre/internal/tui"

	tea "github.com/charmbracelet/bubbletea"
)

func usage() {
	fmt.Fprintf(os.Stderr, `Usage:
  kompadre [left-kubeconfig right-kubeconfig]

  With two positional arguments, start in compare mode using those kubeconfig files.
  With no arguments, choose kubeconfigs interactively.

`)
}

func main() {
	args := os.Args[1:]
	for _, a := range args {
		if a == "-h" || a == "--help" {
			usage()
			os.Exit(0)
		}
	}

	var left, right string
	switch len(args) {
	case 0:
	case 2:
		left, right = args[0], args[1]
	default:
		usage()
		fmt.Fprintln(os.Stderr, "error: provide zero arguments (interactive) or exactly two kubeconfig paths.")
		os.Exit(2)
	}

	model, err := tui.New(left, right)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	p := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
