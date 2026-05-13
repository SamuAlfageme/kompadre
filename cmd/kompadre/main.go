package main

import (
	"context"
	"fmt"
	"os"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"kompadre/internal/delta"
	"kompadre/internal/kubectl"
	"kompadre/internal/tui"

	tea "github.com/charmbracelet/bubbletea"
	xterm "github.com/charmbracelet/x/term"
)

const headlessTimeout = 3 * time.Minute

// version may be overridden at build time via -ldflags "-X main.version=..."
// (e.g. from `git describe --tags --always --dirty`). When unset, we fall back to
// runtime build info embedded by the Go toolchain.
var version = ""

// versionString returns a human-readable version line including the git commit
// sha (and dirty marker) when available.
func versionString() string {
	tag := strings.TrimSpace(version)
	commit := ""
	commitTime := ""
	modified := false

	if bi, ok := debug.ReadBuildInfo(); ok {
		if tag == "" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			tag = bi.Main.Version
		}
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				commit = s.Value
			case "vcs.time":
				commitTime = s.Value
			case "vcs.modified":
				if s.Value == "true" {
					modified = true
				}
			}
		}
	}

	if tag == "" {
		tag = "dev"
	}
	short := commit
	if len(short) > 12 {
		short = short[:12]
	}

	out := "kompadre " + tag
	if short != "" {
		out += " (" + short
		if modified {
			out += "-dirty"
		}
		out += ")"
	}
	if commitTime != "" {
		out += " built " + commitTime
	}
	return out
}

func usage() {
	fmt.Fprintf(os.Stderr, `Usage:
  kompadre [flags] [kubeconfig ...] [prompt]

  With no arguments and no KUBECONFIG env, choose both kubeconfigs interactively.
  With no arguments but KUBECONFIG set, use it for the left pane and browse for the right.
  With one kubeconfig and KUBECONFIG set, open side-by-side (KUBECONFIG left, argument right).
  With one kubeconfig and no KUBECONFIG, use it for the left pane and browse for the right.
  With two kubeconfigs, start in compare mode.
  With three positional arguments (two kubeconfigs + prompt), pre-fill and run the prompt.

Flags:
  --self    Use a single kubeconfig for both panes. With no positional argument,
            uses $KUBECONFIG. With one positional argument, uses it for both sides.
  --delta   When a prompt is provided, open the TUI directly on the delta view.
  --print   When a prompt is provided, run it headless and print the delta to stdout
            (no TUI). Implies delta output.
  --save[=DIR]
            Save left/right outputs as files for later diffing. In headless mode
            (--print), writes to DIR (default "."). In the TUI delta view, press
            "s" to save. Files are named kompadre-{left,right}-TIMESTAMP.txt.
  -v, --version
            Print version and git commit, then exit.
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
		selfMode   bool
		saveMode   bool
		saveDir    string
	)
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-h" || a == "--help":
			usage()
			os.Exit(0)
		case a == "-v" || a == "--version":
			fmt.Println(versionString())
			os.Exit(0)
		case a == "--delta":
			deltaMode = true
		case a == "--print":
			printMode = true
		case a == "--self":
			selfMode = true
		case a == "--save":
			saveMode = true
			saveDir = "."
		case strings.HasPrefix(a, "--save="):
			saveMode = true
			saveDir = strings.TrimPrefix(a, "--save=")
		default:
			positional = append(positional, a)
		}
	}

	envKube := os.Getenv("KUBECONFIG")

	var left, right, prompt string
	switch len(positional) {
	case 0:
		if selfMode {
			if envKube == "" {
				fmt.Fprintln(os.Stderr, "error: --self requires $KUBECONFIG or a kubeconfig argument.")
				os.Exit(2)
			}
			left, right = envKube, envKube
		} else if envKube != "" {
			left = envKube // right stays empty → TUI picks it
		}
		if (deltaMode || printMode) && (left == "" || right == "") {
			usage()
			fmt.Fprintln(os.Stderr, "error: --delta and --print require two kubeconfig paths and a prompt.")
			os.Exit(2)
		}
	case 1:
		if selfMode {
			left, right = positional[0], positional[0]
		} else if envKube != "" {
			left, right = envKube, positional[0]
		} else {
			left = positional[0] // right stays empty → TUI picks it
		}
		if deltaMode || printMode {
			usage()
			fmt.Fprintln(os.Stderr, "error: --delta and --print require a prompt as the third positional argument.")
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
		fmt.Fprintln(os.Stderr, "error: too many positional arguments.")
		os.Exit(2)
	}

	if printMode {
		if err := runHeadlessDelta(left, right, prompt, saveMode, saveDir); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	tuiSaveDir := ""
	if saveMode {
		tuiSaveDir = saveDir
	}
	model, err := tui.New(left, right, prompt, deltaMode, tuiSaveDir)
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
func runHeadlessDelta(leftKube, rightKube, prompt string, save bool, saveDir string) error {
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

	if save {
		lp, rp, err := delta.SavePair(saveDir, leftCombined, rightCombined)
		if err != nil {
			return fmt.Errorf("--save: %w", err)
		}
		fmt.Fprintf(os.Stderr, "saved: %s  %s\n", lp, rp)
		fmt.Fprintf(os.Stderr, "  → delta %s %s\n", lp, rp)
	}

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
