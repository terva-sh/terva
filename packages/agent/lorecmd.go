package agent

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/lore"
	"terva.sh/terva/packages/agent/permissions"
	"terva.sh/terva/packages/i18n"
)

// loreSoftBudget is the per-entry content size (bytes) above which `terva
// lore validate` warns: a large always-on entry is costly in the cached
// prefix, and a large triggered entry eats into the per-turn budget.
const loreSoftBudget = 4000

// runLoreCommand dispatches `terva lore ...` subcommands. Returns
// (handled=true, err) when rawArgs starts with "lore"; otherwise
// (handled=false, nil) so the main router falls through to the flag parser.
func runLoreCommand(rawArgs []string) (handled bool, err error) {
	if len(rawArgs) == 0 || rawArgs[0] != "lore" {
		return false, nil
	}
	if len(rawArgs) == 1 {
		printLoreHelp()
		return true, nil
	}
	switch rawArgs[1] {
	case "list", "ls":
		return true, loreList()
	case "validate", "check":
		return true, loreValidate(rawArgs[2:])
	case "help", "-h", "--help":
		printLoreHelp()
		return true, nil
	default:
		printLoreHelp()
		return true, i18n.Errorf("unknown lore subcommand: %s", rawArgs[1])
	}
}

func printLoreHelp() {
	fmt.Fprintln(os.Stderr, i18n.H("help.lore", `terva lore — inspect keyed-context (lore) entries

usage:
  terva lore list                 list active lore entries across all tiers
  terva lore validate <path>...   check lore .md files (or a directory)

Lore is authored, file-backed context injected when it is keyword-relevant:
an always-on (constant) entry, or one triggered by keys, plus a Markdown body.
Entries live in $TERVA_HOME/lore/, .terva/lore/ (trust-gated), and extension
lore/ bundles; collection settings (scan_depth, token_budget, recursive_
scanning) live in a lore.json at the directory root. Disable for a run with
--no-lore.`))
}

// loreList prints the active lore entries (what the current directory would
// load, honoring Workspace Trust for the project tier).
func loreList() error {
	cwd, _ := os.Getwd()
	trusted := permissions.ResolveTrustState(cwd, false).IsTrusted()
	entries, _, errs := lore.Discover(config.TervaHome(), cwd,
		lore.Gate{TrustProject: trusted, Disabled: config.ResolveConfig(cwd, trusted).Config.DisableExtensions})
	for _, e := range errs {
		fmt.Fprintf(os.Stderr, "warning: %v\n", e)
	}
	if len(entries) == 0 {
		fmt.Println("no lore entries found")
		return nil
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Order != entries[j].Order {
			return entries[i].Order > entries[j].Order
		}
		return loreName(entries[i]) < loreName(entries[j])
	})
	nameW := len("NAME")
	for _, e := range entries {
		if l := len([]rune(loreName(e))); l > nameW {
			nameW = l
		}
	}
	fmt.Printf("%-*s  %-6s  %-32s  %s\n", nameW, "NAME", "ORDER", "TRIGGER", "SOURCE")
	for _, e := range entries {
		trig := "always"
		if !e.Constant {
			trig = "keys: " + strings.Join(e.Keys, ", ")
		}
		fmt.Printf("%-*s  %-6d  %-32s  %s\n", nameW, loreName(e), e.Order, loreClip(trig, 32), e.Source)
	}
	return nil
}

// loreValidate checks lore .md files (or directories of them), returning an
// error if any is invalid.
func loreValidate(args []string) error {
	if len(args) == 0 {
		return i18n.Errorf("usage: terva lore validate <file.md | dir> ...")
	}
	var files []string
	for _, p := range args {
		info, err := os.Stat(p)
		if err != nil {
			fmt.Printf("✗ %s: %v\n", p, err)
			return fmt.Errorf("one or more lore paths are invalid")
		}
		if info.IsDir() {
			_ = filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return nil
				}
				if strings.HasSuffix(strings.ToLower(d.Name()), ".md") && !strings.EqualFold(d.Name(), "README.md") {
					files = append(files, path)
				}
				return nil
			})
			continue
		}
		files = append(files, p)
	}
	if len(files) == 0 {
		fmt.Println("no lore .md files found")
		return nil
	}
	failed := false
	for _, f := range files {
		if !validateOneLore(f) {
			failed = true
		}
	}
	if failed {
		return fmt.Errorf("one or more lore entries are invalid")
	}
	return nil
}

// validateOneLore prints a verdict for one file and reports whether it
// passed (warnings don't fail).
func validateOneLore(path string) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		fmt.Printf("✗ %s: %v\n", path, err)
		return false
	}
	e, ok, err := lore.ParseEntry(string(raw), filepath.Base(path))
	if err != nil {
		fmt.Printf("✗ %s: %v\n", path, err)
		return false
	}
	if !ok {
		fmt.Printf("• %s: disabled (enabled: false)\n", path)
		return true
	}
	trig := fmt.Sprintf("%d key(s)", len(e.Keys))
	if e.Constant {
		trig = "always-on"
	}
	fmt.Printf("✓ %s: ok (%s, order %d)\n", path, trig, e.Order)
	if n := len(e.Content); n > loreSoftBudget {
		where := "the per-turn budget"
		if e.Constant {
			where = "the cached prefix"
		}
		fmt.Printf("  ! large content (%d bytes, ~%d tok) — costly in %s\n", n, lore.ApproxTokens(e.Content), where)
	}
	return true
}

func loreName(e lore.Entry) string {
	if e.Name != "" {
		return e.Name
	}
	return e.Source
}

func loreClip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}
