package skills

import (
	"fmt"
	"strings"
)

// Always-on skills: the operator names a skill, and terva pins its BODY into
// the system prompt at session build instead of only its description joining
// the manifest.
//
// The on-demand model assumes a skill is a procedure the model reaches for.
// That holds for handoff and every write-terva-* skill. It breaks for a skill
// that is a constraint on output, because such a skill has to be in context
// before the first token exists, and a measured 2-of-5 invocation rate is not
// a delivery mechanism for one.
//
// The choice belongs to the operator rather than the skill author, because the
// operator pays the tokens. See docs/proposals/always-on-skills.md.

// AlwaysOnNames returns the skill names to pin for this run, in order and
// without duplicates.
//
// configured is the operator's `always_on_skills`, straight from the user
// config. The pointer carries the meaning. A nil pointer means the operator has
// no opinion, and the answer is DefaultAlwaysOn. A non-nil pointer to an empty
// slice means pin nothing, which is how an operator turns the shipped default
// off. Do not collapse those two into a length test.
//
// extra is --pin-skill, which adds to the list for one run rather than
// replacing it. off is --no-always-on-skills, which wins over everything.
func AlwaysOnNames(configured *[]string, extra []string, off bool) []string {
	if off {
		return nil
	}
	base := DefaultAlwaysOn
	if configured != nil {
		base = *configured
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(base)+len(extra))
	for _, n := range append(append([]string{}, base...), extra...) {
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}

// PinSet is the outcome of resolving a name list against the discovered skills.
//
// Refused and Missing are kept apart because they need different words to the
// operator. A missing name is usually a typo. A refused name is a name that
// resolved only to a project skill, which is a trust decision terva made for
// them.
type PinSet struct {
	// Skills are the bodies to pin, in the order the operator named them.
	Skills []*Skill
	// Refused names resolved only at the project tier.
	Refused []string
	// Missing names resolved to no skill at all.
	Missing []string
}

// Names returns the pinned skill names, for a surface that lists them.
func (p PinSet) Names() []string {
	out := make([]string, 0, len(p.Skills))
	for _, s := range p.Skills {
		out = append(out, s.Name)
	}
	return out
}

// ResolveAlwaysOn turns a name list into the bodies to pin, applying the tier
// gate.
//
// The gate: a pinned body enters the prompt with no model decision in front of
// it, and a cloned repository controls the project tier. So a name that
// resolves only to a project skill is refused. A project skill that SHADOWS a
// built-in or user skill of the same name is pinned, because the operator named
// a name that exists at an allowed tier and the shadowing ladder is doing what
// it is for.
//
// Workspace trust already drops project skills from discovery in an untrusted
// workspace, so this gate is the second of two, not the only one.
//
// A name with no body is skipped rather than pinned empty: an empty block in
// the prefix teaches the model nothing and costs a cache miss to change.
//
// It sets Pinned on each skill it returns. That is a mutation in an otherwise
// pure query, and it is deliberate: the /skills picker and `terva doctor` hold
// the same pointers, so this keeps one answer rather than asking every surface
// to carry the PinSet alongside the list.
func ResolveAlwaysOn(active []*Skill, names []string) PinSet {
	var out PinSet
	for _, name := range names {
		s := Resolve(active, name)
		if s == nil {
			out.Missing = append(out.Missing, name)
			continue
		}
		if s.Project && !shadowsAllowedTier(s) {
			out.Refused = append(out.Refused, name)
			continue
		}
		if s.Body == "" {
			out.Missing = append(out.Missing, name)
			continue
		}
		s.Pinned = true
		out.Skills = append(out.Skills, s)
	}
	return out
}

// AlwaysOnAddendum renders the pinned bodies as one system-prompt block.
//
// The lead sentence tells the model these instructions are already here, so it
// does not spend a turn loading through the `skill` tool what it can already
// read. build.go also drops a pinned skill from the manifest for the same
// reason: a description line advertising text that is already present buys
// nothing and invites the wasted call.
func AlwaysOnAddendum(pinned []*Skill) string {
	if len(pinned) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("Always-on skill instructions. " +
		"The operator pinned these skills, so their full instructions are below. " +
		"Follow them. You do not need to load them with the `skill` tool.\n")
	for _, s := range pinned {
		body := strings.TrimSpace(s.Body)
		if body == "" {
			continue
		}
		fmt.Fprintf(&sb, "\n## Skill: %s\n\n%s\n", s.Name, body)
	}
	return sb.String()
}

// Excluding returns the skills in `in` that are not in `drop`, keeping order.
// Identity is by pointer, because two skills can share a bare name across
// tiers and only the resolved winner is the one that pinned.
func Excluding(in, drop []*Skill) []*Skill {
	if len(drop) == 0 {
		return in
	}
	skip := make(map[*Skill]bool, len(drop))
	for _, s := range drop {
		skip[s] = true
	}
	out := make([]*Skill, 0, len(in))
	for _, s := range in {
		if !skip[s] {
			out = append(out, s)
		}
	}
	return out
}

// shadowsAllowedTier reports whether a project skill took its name from a
// built-in or user skill. That is the one case where a project body may pin:
// the operator listed a name that exists outside the project.
func shadowsAllowedTier(s *Skill) bool {
	for _, loser := range s.Shadowed {
		if !loser.Project {
			return true
		}
	}
	return false
}
