package agent

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/i18n"
)

// personaCharterBudget is the soft size ceiling for a charter: it targets the
// 2 KiB static-context-block budget so a persona stays cheap in the cached
// prefix. Over budget is a warning, not a failure.
const personaCharterBudget = 2000

var (
	personaMacroRe  = regexp.MustCompile(`\{\{char\}\}|\{\{user\}\}|<START>`)
	personaAccentRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
)

// runPersonaCommand dispatches `terva persona ...` subcommands. Returns
// (handled=true, err) when rawArgs starts with "persona"; otherwise
// (handled=false, nil) so the main router falls through to the flag parser.
func runPersonaCommand(rawArgs []string) (handled bool, err error) {
	if len(rawArgs) == 0 || rawArgs[0] != "persona" {
		return false, nil
	}
	if len(rawArgs) == 1 {
		printPersonaHelp()
		return true, nil
	}
	switch rawArgs[1] {
	case "list", "ls":
		return true, personaList()
	case "validate", "check":
		return true, personaValidate(rawArgs[2:])
	case "init":
		return true, personaInit(rawArgs[2:])
	case "help", "-h", "--help":
		printPersonaHelp()
		return true, nil
	default:
		printPersonaHelp()
		return true, i18n.Errorf("unknown persona subcommand: %s", rawArgs[1])
	}
}

func printPersonaHelp() {
	fmt.Fprintln(os.Stderr, i18n.H("help.persona", `terva persona — inspect and manage personas

usage:
  terva persona list                list available personas (on-disk ∪ built-in)
  terva persona validate <file>...  check a persona .md against the format rules
  terva persona init [--force]      copy the built-in crew into $TERVA_HOME/personas/ to edit

A persona is a Markdown file: YAML frontmatter (name, pronunciation, specialty,
summary, emoji, accent_color, good_for/avoid_for) + a behavioral charter body.
Select one at launch with --persona <name|file>, or set a default with
default_persona in config.json or a $TERVA_HOME/persona.md file.`))
}

// personaList prints the merged roster: qualified names + visible provenance
// (where each persona is sourced from, and what it overrides).
func personaList() error {
	type row struct {
		p         build.Persona
		overrides []string // origins shadowed by this winner, lower tiers first
	}
	seen := map[string]int{}
	var rows []row
	for _, set := range build.PersonaTiers() {
		for _, p := range set {
			k := p.Key()
			if idx, ok := seen[k]; ok {
				rows[idx].overrides = append(rows[idx].overrides, p.Origin())
				continue
			}
			seen[k] = len(rows)
			rows = append(rows, row{p: p})
		}
	}
	if len(rows) == 0 {
		fmt.Println("no personas found")
		return nil
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].p.Qualified() < rows[j].p.Qualified() })

	nameW := len("NAME")
	for _, r := range rows {
		if n := len([]rune(r.p.Qualified())); n > nameW {
			nameW = n
		}
	}
	fmt.Printf("%-*s  %-30s  %s\n", nameW, "NAME", "SPECIALTY", "SOURCE")
	for _, r := range rows {
		src := r.p.Origin()
		if len(r.overrides) > 0 {
			src += " (overrides " + strings.Join(r.overrides, ", ") + ")"
		}
		em := strings.TrimSpace(r.p.Emoji)
		if em != "" {
			em = " " + em
		}
		fmt.Printf("%-*s  %-30s  %s%s\n", nameW, r.p.Qualified(), personaClip(r.p.Specialty, 30), src, em)
	}
	return nil
}

// personaValidate checks one or more persona files against the format rules,
// returning an error if any file is invalid.
func personaValidate(args []string) error {
	if len(args) == 0 {
		return i18n.Errorf("usage: terva persona validate <file.md> [more.md ...]")
	}
	failed := false
	for _, path := range args {
		if !validateOnePersona(path) {
			failed = true
		}
	}
	if failed {
		return fmt.Errorf("one or more personas are invalid")
	}
	return nil
}

// validateOnePersona prints a verdict for one file and reports whether it
// passed (warnings don't fail).
func validateOnePersona(path string) (ok bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		fmt.Printf("✗ %s: %v\n", path, err)
		return false
	}
	var problems, warns []string
	p, perr := build.ParsePersona(string(raw), path)
	if perr != nil {
		problems = append(problems, perr.Error())
	} else {
		if p.Charter == "" {
			problems = append(problems, "empty charter body")
		}
		if p.AccentColor != "" && !personaAccentRe.MatchString(p.AccentColor) {
			problems = append(problems, fmt.Sprintf("accent_color %q is not a #RRGGBB hex value", p.AccentColor))
		}
		// The static-block budget only applies to an ADDITIVE charter, which the
		// host injects as a bounded block. An immersive charter becomes the whole
		// system prompt (the --system-prompt path), so the budget does not bind.
		if n := len(p.Charter); n > personaCharterBudget && !p.Immersive {
			warns = append(warns, fmt.Sprintf("charter is %d chars (over the %d static-block budget)", n, personaCharterBudget))
		}
	}
	if personaMacroRe.Match(raw) {
		problems = append(problems, "leftover SillyTavern macro ({{char}}/{{user}}/<START>) — clean it from the charter")
	}

	if len(problems) > 0 {
		fmt.Printf("✗ %s — invalid:\n", path)
		for _, pr := range problems {
			fmt.Printf("    • %s\n", pr)
		}
		return false
	}
	fmt.Printf("✓ %s — valid persona (%s)\n", path, p.Name)
	for _, w := range warns {
		fmt.Printf("    ⚠ %s\n", w)
	}
	return true
}

// personaInit copies the embedded crew into $TERVA_HOME/personas/ for editing.
// Existing files are skipped unless --force is given.
func personaInit(args []string) error {
	force := false
	for _, a := range args {
		if a == "--force" || a == "-f" {
			force = true
		}
	}
	dir := build.PersonasDir()
	written, skipped := 0, 0
	err := fs.WalkDir(build.BuiltinPersonasFS, build.BuiltinPersonasRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel := strings.TrimPrefix(p, build.BuiltinPersonasRoot+"/")
		dest := filepath.Join(dir, filepath.FromSlash(rel))
		if !force {
			if _, statErr := os.Stat(dest); statErr == nil {
				skipped++
				return nil
			}
		}
		raw, readErr := fs.ReadFile(build.BuiltinPersonasFS, p)
		if readErr != nil {
			return readErr
		}
		if mkErr := os.MkdirAll(filepath.Dir(dest), 0o755); mkErr != nil {
			return mkErr
		}
		if wErr := os.WriteFile(dest, raw, 0o644); wErr != nil {
			return wErr
		}
		fmt.Printf("wrote %s\n", dest)
		written++
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Printf("materialized %d file(s) into %s", written, dir)
	if skipped > 0 {
		fmt.Printf(" (%d already present; pass --force to overwrite)", skipped)
	}
	fmt.Println()
	return nil
}

// personaClip truncates s to n runes with an ellipsis when it overflows.
func personaClip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}
