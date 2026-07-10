package agent

import (
	"fmt"
	"os"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/i18n"
)

// runTrustCommand dispatches `terva trust ...` and `terva untrust ...`.
// Returns (handled=true, err) when rawArgs starts with "trust" or
// "untrust"; otherwise (false, nil) so the main router falls through to
// the regular flag parser. Mirrors runMigrateCommand's dispatch shape
// (migratecmd.go) so the router in cli.go stays uniform.
//
// These are the scriptable / headless-prep surface: trust a directory
// BEFORE a headless run so it isn't restricted (the persisted analog of
// the one-shot --trust flag). See docs/plans/workspace-trust.md.
func runTrustCommand(rawArgs []string) (handled bool, err error) {
	if len(rawArgs) == 0 {
		return false, nil
	}
	switch rawArgs[0] {
	case "trust":
		return true, runTrust(rawArgs[1:])
	case "untrust":
		return true, runUntrust(rawArgs[1:])
	default:
		return false, nil
	}
}

func runTrust(args []string) error {
	var (
		parent bool
		list   bool
		path   string
	)
	for _, a := range args {
		switch a {
		case "-h", "--help", "help":
			printTrustHelp()
			return nil
		case "--parent":
			parent = true
		case "--list", "--ls":
			list = true
		default:
			if len(a) > 0 && a[0] == '-' {
				printTrustHelp()
				return i18n.Errorf("unknown flag for `trust`: %s", a)
			}
			if path != "" {
				return fmt.Errorf("trust takes at most one path (got %q and %q)", path, a)
			}
			path = a
		}
	}

	if list {
		return printTrustList()
	}

	if path == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("resolve current directory: %w", err)
		}
		path = cwd
	}
	if err := config.TrustPath(path, parent); err != nil {
		return err
	}
	real := config.CanonicalTrustPath(path)
	if parent {
		fmt.Printf("trusted %s and everything under it (project extensions/skills/context will load there)\n", real)
	} else {
		fmt.Printf("trusted %s (its project extensions/skills/context will load)\n", real)
	}
	return nil
}

func runUntrust(args []string) error {
	var path string
	for _, a := range args {
		switch a {
		case "-h", "--help", "help":
			printTrustHelp()
			return nil
		default:
			if len(a) > 0 && a[0] == '-' {
				printTrustHelp()
				return i18n.Errorf("unknown flag for `untrust`: %s", a)
			}
			if path != "" {
				return fmt.Errorf("untrust takes at most one path (got %q and %q)", path, a)
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
	if err := config.UntrustPath(path); err != nil {
		return err
	}
	fmt.Printf("untrusted %s (its project content will no longer load)\n", config.CanonicalTrustPath(path))
	return nil
}

func printTrustList() error {
	s, err := config.LoadTrustStore()
	if err != nil {
		return err
	}
	if len(s.Trusted) == 0 {
		fmt.Println("no trusted directories (project extensions/skills/context are restricted everywhere)")
		return nil
	}
	fmt.Printf("trusted directories (%s):\n", config.TrustStorePath())
	for _, e := range s.Trusted {
		scope := ""
		if e.Parent {
			scope = " (and descendants)"
		}
		fmt.Printf("  %s%s\n", e.Real, scope)
	}
	return nil
}

func printTrustHelp() {
	fmt.Fprintln(os.Stderr, i18n.H("help.trust", `terva trust — control which directories may load project-supplied code/instructions

By default terva treats project folders as UNTRUSTED: a cloned repo's
.terva/extensions/* are not spawned and its .terva/skills/* and
context_files are not injected, so opening a repo can't auto-run its
code or steer the agent. Trust a directory to opt into loading that
project content.

usage:
  terva trust [path]            trust a directory (default: current dir)
  terva trust --parent [path]   trust the directory and everything under it
  terva trust --list            show the trusted directories
  terva untrust [path]          remove a directory from the trust list

notes:
  * The trust list lives in %s (mode 0600), outside
    any project — a repo can never trust itself.
  * For a single run without persisting, pass --trust to terva instead.
  * Trust loads project code/skills; it does NOT let a project grant
    itself tool permissions (the self-approval ban stays).`, config.TrustStorePath()))
}
