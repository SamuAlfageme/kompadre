package main

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"kompadre/internal/delta"
	"kompadre/internal/kubectl"
	"kompadre/internal/tui"

	tea "github.com/charmbracelet/bubbletea"
	xterm "github.com/charmbracelet/x/term"
)

const headlessTimeout = 3 * time.Minute

func usage() {
	fmt.Fprintf(os.Stderr, `Usage:
  kompadre [--delta|--print] [left-kubeconfig right-kubeconfig [prompt]]

  With no arguments, choose kubeconfigs interactively.
  With two positional arguments, start in compare mode using those kubeconfig files.
  With three positional arguments, also pre-fill the unified prompt and run it on launch.

Flags:
  --delta   When a prompt is provided, open the TUI directly on the delta view.
  --print   When a prompt is provided, run it headless and print the delta to stdout
            (no TUI). Implies delta output.
  -h, --help
            Show this help.

`)
}

func main() {
	args := os.Args[1:]

	var (
		positional []string
		deltaMode  bool
		printMode  bool
	)
	for _, a := range args {
		switch {
		case a == "-h" || a == "--help":
			usage()
			os.Exit(0)
		case a == "--delta":
			deltaMode = true
		case a == "--print":
			printMode = true
		default:
			positional = append(positional, a)
		}
	}

	var left, right, prompt string
	switch len(positional) {
	case 0:
		if deltaMode || printMode {
			usage()
			fmt.Fprintln(os.Stderr, "error: --delta and --print require two kubeconfig paths and a prompt.")
			os.Exit(2)
		}
	case 2:
		left, right = positional[0], positional[1]
		if deltaMode || printMode {
			usage()
			fmt.Fprintln(os.Stderr, "error: --delta and --print require a prompt as the third positional argument.")
			os.Exit(2)
		}
	case 3:
		left, right, prompt = positional[0], positional[1], positional[2]
	default:
		usage()
		fmt.Fprintln(os.Stderr, "error: provide zero arguments (interactive), two kubeconfig paths, or two paths plus a prompt.")
		os.Exit(2)
	}

	if printMode {
		if err := runHeadlessDelta(left, right, prompt); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	model, err := tui.New(left, right, prompt, deltaMode)
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

// runHeadlessDelta executes the prompt against both kubeconfigs in parallel and prints the
// delta diff to stdout without launching the TUI. Output width follows the stdout terminal
// when attached to a tty; otherwise delta picks its own default.
func runHeadlessDelta(leftKube, rightKube, prompt string) error {
	ctx, cancel := context.WithTimeout(context.Background(), headlessTimeout)
	defer cancel()

	var (
		leftStdout, leftStderr, rightStdout, rightStderr string
		errL, errR                                       error
		wg                                               sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		leftStdout, leftStderr, errL = kubectl.RunShell(ctx, leftKube, prompt)
	}()
	go func() {
		defer wg.Done()
		rightStdout, rightStderr, errR = kubectl.RunShell(ctx, rightKube, prompt)
	}()
	wg.Wait()

	leftCombined := kubectl.FormatOutput(leftStdout, leftStderr, errL)
	rightCombined := kubectl.FormatOutput(rightStdout, rightStderr, errR)

	width := 0
	fd := os.Stdout.Fd()
	if xterm.IsTerminal(fd) {
		if w, _, err := xterm.GetSize(fd); err == nil {
			width = w
		}
	}

	text, err := delta.Diff(leftCombined, rightCombined, width)
	if err != nil {
		return err
	}
	if text != "" {
		fmt.Fprintln(os.Stdout, text)
	}
	if errL != nil {
		return fmt.Errorf("left kubectl: %w", errL)
	}
	if errR != nil {
		return fmt.Errorf("right kubectl: %w", errR)
	}
	return nil
}
