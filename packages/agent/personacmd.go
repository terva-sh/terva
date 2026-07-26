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

// personaCharterBudget is the soft size ceiling for a charter, guarding the
// cached prefix. Over budget is a warning, not a failure.
//
// Where the number comes from, since the first one was inherited rather than
// measured: it was 2000, targeting a 2 KiB cap on an extension's static context
// block. That cap is now 8192 (extdriver's maxStaticContextBytes) and this never
// followed — and it was the wrong thing to borrow from anyway, because an
// extension's block is one of N while a charter is the single standing
// instruction for the whole agent.
//
// 3500 is the largest charter terva itself ships (seppa, 2535) composed with the
// default charter `extends` inherits (mieli, 666) — 3201 — plus headroom. Both
// halves matter. Under 2000 the project warned on two of its own six top-level
// personas, and a warning that fires on shipped content is one authors learn to
// scroll past, taking the ✓/ℹ lines beside it. And `extends` shipped after 2000
// was set, spending 666 before its author writes a word, so the persona `extends`
// exists to serve — a long-lived agent adding a domain to the default — had a
// third less room than a self-contained one.
//
// Deliberately close to that 3201 rather than up at the 8192 anchor: a soft warn
// nothing reaches is not a budget. What it costs when reached is ~875 tokens
// instead of ~500, against a cold prefix measured at ~10.7k (tool schemas
// dominate it; the default system prompt is ~240), paid once per cache lifetime
// rather than per turn.
const personaCharterBudget = 3500

var (
	personaMacroRe  = regexp.MustCompile(`\{\{char\}\}|\{\{user\}\}|<START>`)
	personaAccentRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

	// Code regions, blanked before the macro scan. Fences first: a fenced block
	// may contain backticks, and stripping spans first would leave its fence
	// markers behind to pair up with the wrong thing.
	personaFenceRe    = regexp.MustCompile("(?s)```.*?```")
	personaCodeSpanRe = regexp.MustCompile("`[^`\n]*`")
)

// stripCodeRegions blanks fenced blocks and inline code spans.
//
// The macro check is about a macro that would be EMITTED: personas get no
// substitution, so a `{{char}}` left over from a converted character card
// reaches the model as four literal braces where a name should be. A macro
// written as code is being SHOWN, not used — and terva ships two personas whose
// whole job is editing character cards, so charters that discuss macros are a
// normal thing to write, not a mistake.
//
// Blanking rather than deleting: a scan only asks whether a match EXISTS, and
// splicing the text around removals could join two halves into a macro that was
// never written.
func stripCodeRegions(s string) string {
	blank := func(m string) string { return strings.Repeat(" ", len(m)) }
	return personaCodeSpanRe.ReplaceAllStringFunc(personaFenceRe.ReplaceAllStringFunc(s, blank), blank)
}

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

// charterScopeNote states what a valid charter will actually do to the prompt.
//
// It exists because the one thing a persona author cannot see is the thing that
// is NOT there. A charter replaces the default persona's outright — Mieli is a
// default, not a base layer — so any operating guidance that ships in mieli.md
// is absent from every run that selects something else. Nothing said so, and a
// fleet ran for months on a persona missing the orientation contract before a
// long session made the gap visible. One line here, at the moment someone is
// authoring the file, is where that is cheapest to learn.
// The composing case is the one where a bare char count would mislead: what
// occupies the cached prefix is the ASSEMBLY, so that is the number reported,
// broken down so the author can see which half they control.
func charterScopeNote(p build.Persona) string {
	if p.Immersive {
		return fmt.Sprintf("%d-char charter, and it is the WHOLE prompt: immersive replaces terva's identity and harness conventions too, so include any operating guidance you want", len(p.Charter))
	}
	if p.Inherited != "" {
		return fmt.Sprintf("%d-char charter assembled: %d inherited from %s, then your %d — the inherited half comes first, so yours qualifies it",
			len(p.ComposedCharter()), len(p.Inherited), p.Extends, len(p.Charter))
	}
	return fmt.Sprintf("%d-char charter, and it is the ONLY charter in the prompt: a persona is chosen INSTEAD of the default (Mieli), so nothing of Mieli's charter comes with it", len(p.Charter))
}

// validateOnePersona prints a verdict for one file and reports whether it
// passed (warnings don't fail).
func validateOnePersona(path string) (ok bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		fmt.Printf("✗ %s: %v\n", path, err)
		return false
	}
	var problems, warns, notes []string
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
		// Resolve `extends` here so the verdict is about the charter that will
		// actually be assembled, not the half of it in this file. A bad extends
		// is a hard error at run time (ResolvePersona composes on every path),
		// so it must be a problem here too — reporting it as a warning would
		// print a ✓ for a persona that cannot start.
		composed, cerr := build.ComposeCharter(p)
		if cerr != nil {
			problems = append(problems, cerr.Error())
		} else {
			p = composed
		}
		// The static-block budget only applies to an ADDITIVE charter, which the
		// host injects as a bounded block. An immersive charter becomes the whole
		// system prompt (the --system-prompt path), so the budget does not bind.
		//
		// It measures the ASSEMBLED charter, inherited half included. That is
		// what lands in the cached prefix, and a budget that stops measuring the
		// thing it protects is worse than one that occasionally warns.
		if n := len(p.ComposedCharter()); n > personaCharterBudget && !p.Immersive {
			warns = append(warns, fmt.Sprintf("charter is %d chars (over the %d static-block budget)", n, personaCharterBudget))
		}
		if p.Charter != "" {
			notes = append(notes, charterScopeNote(p))
		}
	}
	if personaMacroRe.MatchString(stripCodeRegions(string(raw))) {
		problems = append(problems, "leftover SillyTavern macro ({{char}}/{{user}}/<START>) — personas get no macro substitution, so it reaches the model literally; write it as `code` if you meant to discuss it")
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
	for _, n := range notes {
		fmt.Printf("    ℹ %s\n", n)
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
