package agent

import (
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/permissions"
	"terva.sh/terva/packages/agent/skills"
)

// runSkillsCommand dispatches `terva skills [list]`. Returns (handled=true,
// err) when rawArgs starts with "skills"; otherwise (false, nil) so the main
// router falls through to the regular flag parser. Mirrors runTrustCommand's
// dispatch shape so the router in cli.go stays uniform.
//
// This exists because "skills" was previously not a subcommand at all: the word
// fell through to the bare-prompt path and launched an interactive TUI with
// "skills" pre-filled as the prompt. In a non-TTY shell that is a hang, not an
// error — a script asking a reasonable question got a terminal UI instead of an
// answer. It is also the name a person types first for this feature, which
// makes it the worst possible word to leave falling through.
func runSkillsCommand(rawArgs []string) (handled bool, err error) {
	if len(rawArgs) == 0 || rawArgs[0] != "skills" {
		return false, nil
	}
	rest := rawArgs[1:]
	if len(rest) > 0 {
		switch rest[0] {
		case "list":
			rest = rest[1:]
		case "help", "-h", "--help":
			skillsUsage(os.Stdout)
			return true, nil
		default:
			return true, fmt.Errorf("unknown argument %q — usage: terva skills [list]", rest[0])
		}
	}
	if len(rest) > 0 {
		return true, fmt.Errorf("unexpected argument %q — usage: terva skills [list]", rest[0])
	}
	cwd, err := os.Getwd()
	if err != nil {
		return true, err
	}
	return true, listSkills(os.Stdout, cwd)
}

func skillsUsage(w io.Writer) {
	fmt.Fprint(w, `usage: terva skills [list]

List every SKILL.md this workspace resolves, with the tier it came from, the
file behind it, and any name it shadows. Reads only; prints to stdout.

Project skills load only in a trusted workspace. Run "terva trust" first, or
the listing says so and leaves them out.
`)
}

// listSkills prints the resolved skill set for the current directory.
//
// It resolves against the SAME trust verdict a real session here would, and
// says so when the workspace is untrusted: reporting skills that a session
// would never load would be a lie in the reassuring direction — the reader
// would go on believing a skill is available that never loads.
func listSkills(w io.Writer, cwd string) error {
	userHome, _ := os.UserHomeDir()
	trusted := permissions.ResolveTrustState(cwd, false).IsTrusted()
	found, errs := skills.Discover(config.TervaHome(), cwd, userHome, true, true,
		skills.Gate{TrustProject: trusted, Disabled: config.ResolveConfig(cwd, trusted).Config.DisableExtensions})

	if len(found) == 0 {
		fmt.Fprintln(w, "no skills discovered")
		printSkillCaveats(w, trusted, errs)
		return nil
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tTIER\tPATH")
	for _, s := range found {
		if s == nil {
			continue
		}
		// Ref, not Name: a skill that lost its bare name to a higher tier is
		// listed under the qualified one, because printing the bare name would
		// hand back a string that loads a DIFFERENT skill.
		// A built-in carries a "builtin:<name>" pseudo-path rather than a file,
		// which is already the honest answer to "where does this live".
		fmt.Fprintf(tw, "%s\t%s\t%s\n", s.Ref(), s.Source, s.Path)
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	fmt.Fprintf(w, "\n%d skill(s)\n", len(found))
	printSkillCollisions(w, found)
	printSkillCaveats(w, trusted, errs)
	return nil
}

// printSkillCollisions names every shadowed skill and the spelling that still
// reaches it. A collision is exactly the case where the obvious name does not
// do the obvious thing, so a listing that stayed silent about it would mislead
// precisely where the reader most needs help.
func printSkillCollisions(w io.Writer, found []*skills.Skill) {
	collisions := skills.Collisions(found)
	if len(collisions) == 0 {
		return
	}
	fmt.Fprintf(w, "\n%d name collision(s):\n", len(collisions))
	for _, winner := range collisions {
		for _, loser := range winner.Shadowed {
			if loser == nil {
				continue
			}
			fmt.Fprintf(w, "  %q resolves to %s; %s is shadowed (load it as %s)\n",
				winner.Name, winner.Qualified(), loser.Source, loser.Qualified())
		}
	}
}

func printSkillCaveats(w io.Writer, trusted bool, errs []error) {
	if !trusted {
		fmt.Fprintln(w, "\nthis workspace is untrusted, so its project skills are not listed — \"terva trust\" loads them")
	}
	if len(errs) > 0 {
		fmt.Fprintf(w, "\n%d unreadable skill file(s):\n", len(errs))
		for _, e := range errs {
			fmt.Fprintf(w, "  %v\n", e)
		}
	}
}
