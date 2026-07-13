package agent

import (
	"fmt"
	"os"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/i18n"
)

// runUnjailCommand dispatches `terva unjail ...` and `terva jail ...`.
// Returns (handled=true, err) when rawArgs starts with one of those; otherwise
// (false, nil) so the router falls through. Mirrors runTrustCommand's shape.
//
// This is the persisted analog of the one-shot --no-jail flag and the
// session-only /unjail: a directory recorded here runs unjailed every time,
// without having to remember a flag.
func runUnjailCommand(rawArgs []string) (handled bool, err error) {
	if len(rawArgs) == 0 {
		return false, nil
	}
	switch rawArgs[0] {
	case "unjail":
		return true, runUnjail(rawArgs[1:])
	case "jail":
		return true, runRejail(rawArgs[1:])
	default:
		return false, nil
	}
}

func runUnjail(args []string) error {
	var (
		parent bool
		list   bool
		path   string
	)
	for _, a := range args {
		switch a {
		case "-h", "--help", "help":
			printUnjailHelp()
			return nil
		case "--parent":
			parent = true
		case "--list", "--ls":
			list = true
		default:
			if len(a) > 0 && a[0] == '-' {
				printUnjailHelp()
				return i18n.Errorf("unknown flag for `unjail`: %s", a)
			}
			if path != "" {
				return fmt.Errorf("unjail takes at most one path (got %q and %q)", path, a)
			}
			path = a
		}
	}

	if list {
		return printUnjailList()
	}

	if path == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("resolve current directory: %w", err)
		}
		path = cwd
	}
	if err := config.UnjailPath(path, parent); err != nil {
		return err
	}
	real := config.CanonicalTrustPath(path)
	if parent {
		fmt.Printf("unjailed %s and everything under it\n", real)
	} else {
		fmt.Printf("unjailed %s\n", real)
	}
	// Say what was actually given away. The sandbox is the boundary that keeps
	// a mistake inside the working directory; without it a wrong path is a
	// wrong path anywhere the process can reach.
	fmt.Println("tools running there may now read and write outside that directory.")
	fmt.Printf("undo with `terva jail %s`.\n", real)
	return nil
}

func runRejail(args []string) error {
	var path string
	for _, a := range args {
		switch a {
		case "-h", "--help", "help":
			printUnjailHelp()
			return nil
		default:
			if len(a) > 0 && a[0] == '-' {
				printUnjailHelp()
				return i18n.Errorf("unknown flag for `jail`: %s", a)
			}
			if path != "" {
				return fmt.Errorf("jail takes at most one path (got %q and %q)", path, a)
			}
			path = a
		}
	}
	if path == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("resolve current directory: %w", err)
		}
		path = cwd
	}
	if err := config.RejailPath(path); err != nil {
		return err
	}
	fmt.Printf("jailed %s (back to the default: tools are confined to it)\n", config.CanonicalTrustPath(path))
	return nil
}

func printUnjailList() error {
	s, err := config.LoadUnjailStore()
	if err != nil {
		return err
	}
	if len(s.Unjailed) == 0 {
		fmt.Println("no unjailed directories (tools are confined to the working directory everywhere)")
		return nil
	}
	fmt.Printf("unjailed directories (%s):\n", config.UnjailStorePath())
	for _, e := range s.Unjailed {
		scope := ""
		if e.Parent {
			scope = " (and descendants)"
		}
		fmt.Printf("  %s%s\n", e.Real, scope)
	}
	return nil
}

func printUnjailHelp() {
	fmt.Fprintln(os.Stderr, i18n.H("help.unjail", `terva unjail — let a directory run without the filesystem sandbox

By default an interactive terva is JAILED: its built-in tools may only
read and write inside the working directory. That confinement is what
makes the default approval mode safe to leave alone — built-in tools
are auto-approved precisely BECAUSE they cannot reach outside.

Unjail a directory when the work genuinely lives elsewhere too — a
dotfiles repo that writes into your home, a build that emits outside the
tree. The decision is recorded per directory, so it survives a restart
and you don't have to remember --no-jail every time.

usage:
  terva unjail [path]            unjail a directory (default: current dir)
  terva unjail --parent [path]   unjail the directory and everything under it
  terva unjail --list            show the unjailed directories
  terva jail [path]              remove a directory (back to confined)

notes:
  * The list lives in %s (mode 0600), outside
    any project — a repo can never unjail itself.
  * --jail and --no-jail still win for a single run.
  * /unjail in the TUI is session-only; /unjail --always records it here.
  * Unjail is NOT trust, and neither implies the other. Trust decides
    whether a project's own code and instructions load; unjail decides
    where tools may write. A repo you trust is not thereby a repo you
    want writing to your home directory.
  * With an auto-approving approval mode (workspace, yolo), an unjailed
    directory means built-in tools may write anywhere without asking.
    terva says so at startup rather than leaving you to infer it.`, config.UnjailStorePath()))
}
