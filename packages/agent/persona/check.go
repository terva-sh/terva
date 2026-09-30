package persona

import (
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"terva.sh/terva/packages/agent/look"
)

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

// writtenMark returns the mark the frontmatter writes, and nil when it writes
// none. Parse cannot tell `mark: {}` from no mark, and a roster refuses the
// first, so Check reads the key itself to refuse it too.
func writtenMark(raw []byte) *look.Mark {
	front, _ := splitFrontmatter(string(raw))
	var fm struct {
		Mark *look.Mark `yaml:"mark"`
	}
	if yaml.Unmarshal([]byte(front), &fm) != nil {
		return nil
	}
	return fm.Mark
}

// Check parses a persona file and reports every problem that would stop it
// from starting: a parse failure, an empty charter, a bad accent colour, an
// extends that does not resolve, and a leftover character-card macro. The
// persona it returns has its charter composed, so a caller measures what the
// model reads. `terva persona validate` and the Talkoot recruiter's approval
// share it, so a persona the recruiter writes passes the same bar a person's
// file does.
func Check(raw []byte, source string) (Persona, []string) {
	var problems []string
	p, err := Parse(string(raw), source)
	if err != nil {
		problems = append(problems, err.Error())
	} else {
		if p.Charter == "" {
			problems = append(problems, "empty charter body")
		}
		if p.AccentColor != "" && !personaAccentRe.MatchString(p.AccentColor) {
			problems = append(problems, fmt.Sprintf("accent_color %q is not a #RRGGBB hex value", p.AccentColor))
		}
		if m := writtenMark(raw); m != nil {
			problems = append(problems, look.Check(markOf(m))...)
		}
		// Resolve `extends` here so the verdict is about the charter that will
		// actually be assembled, not the half of it in this file. A bad extends
		// is a hard error at run time (ResolvePersona composes on every path),
		// so it must be a problem here too.
		composed, cerr := ComposeCharter(p)
		if cerr != nil {
			problems = append(problems, cerr.Error())
		} else {
			p = composed
		}
	}
	if personaMacroRe.MatchString(stripCodeRegions(string(raw))) {
		problems = append(problems, "leftover SillyTavern macro ({{char}}/{{user}}/<START>) — personas get no macro substitution, so it reaches the model literally; write it as `code` if you meant to discuss it")
	}
	return p, problems
}
