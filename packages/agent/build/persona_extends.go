package build

import (
	"fmt"
	"strings"
)

// Charter composition: `extends` in a persona's frontmatter.
//
// A charter does not stack by default and should not. A security reviewer
// dispatched for one bounded question has no use for a collaboration preamble,
// and the shipped crew is written that way on purpose — every specialist
// carries a self-contained charter. So inheritance is opt-in, explicit, and
// declared per file.
//
// What it is for is the narrower case the default hides: a long-lived,
// general-purpose agent that wants to ADD a domain to the default way of
// working. Writing one meant hand-copying the default charter, which forks
// upstream text with no link home — the copy then drifts silently every time
// the original is revised, and nothing reports the drift. `extends` is that
// copy, made into a reference.

// extendsBase is the only value `extends` accepts today: the default persona.
//
// Deliberately not a general persona-to-persona graph. Restricting v1 to the
// default gets the entire observed benefit with no resolution order, no cycle
// detection beyond the two checks below, and — the real cost avoided — no
// question about what `extends: vartija` means when a user file shadows the
// built-in of that name. The graph can come later if anyone asks for it; an
// unshipped feature is far cheaper than a shipped ambiguity.
const extendsBase = "mieli"

// ComposeCharter resolves p.Extends into an inherited base charter, returning
// the persona with Inherited/InheritedSource filled. A persona that extends
// nothing comes back untouched.
//
// This runs at SELECTION time (ResolvePersona), never at parse time.
// ParsePersona is called for every file in the library to build a roster, so a
// catalog lookup inside it would be a walk nested in a walk, and a persona that
// failed to resolve would vanish from `persona list` — silently, which is the
// exact failure this whole area is being fixed for. Parsing therefore always
// succeeds and selection is where a bad `extends` is reported, loudly.
func ComposeCharter(p Persona) (Persona, error) {
	ref := strings.TrimSpace(p.Extends)
	if ref == "" {
		return p, nil
	}
	where := p.Origin()

	// An immersive charter IS the whole prompt — terva's identity and
	// conventions are already gone. Prepending another charter to it would put
	// a coding collaborator's operating rules inside a roleplay identity, so
	// this combination has no coherent meaning and must not quietly pick one.
	if p.Immersive {
		return p, fmt.Errorf("persona %s: `extends: %s` cannot be combined with `immersive: true` — an immersive charter is the whole prompt, so there is nothing for a base charter to be additive to; drop one of the two", where, ref)
	}
	if !strings.EqualFold(ref, extendsBase) {
		return p, fmt.Errorf("persona %s: `extends: %s` is not supported — only `extends: %s` (the default persona) works today", where, ref, extendsBase)
	}

	base, ok := LookupPersona(ref)
	if !ok {
		return p, fmt.Errorf("persona %s: `extends: %s` — no persona named %q was found", where, ref, ref)
	}
	// Both guards below are reachable through one door: a user file at
	// $TERVA_HOME/personas/mieli.md shadows the built-in, so `extends: mieli`
	// can resolve to the extending file itself, or to another file that extends
	// in turn. Neither is a chain we support, and neither may loop.
	if base.Key() == p.Key() {
		return p, fmt.Errorf("persona %s: `extends: %s` resolves to this same persona — a persona cannot extend itself", where, ref)
	}
	if strings.TrimSpace(base.Extends) != "" {
		return p, fmt.Errorf("persona %s: `extends: %s` resolves to %s, which itself extends %q — chains are not supported", where, ref, base.Origin(), base.Extends)
	}
	if strings.TrimSpace(base.Charter) == "" {
		return p, fmt.Errorf("persona %s: `extends: %s` resolves to %s, which has no charter body to inherit", where, ref, base.Origin())
	}

	p.Inherited = strings.TrimSpace(base.Charter)
	p.InheritedSource = base.Source
	return p, nil
}

// ComposedCharter is the charter text as it reaches the prompt: the inherited
// base first, then this persona's own, joined exactly as SystemSegments joins
// the two segments.
//
// The order is the point. A specialization qualifies the general contract; the
// general contract must not be allowed to qualify the specialization, which is
// what the reverse order would read as.
func (p Persona) ComposedCharter() string {
	own := strings.TrimSpace(p.Charter)
	base := strings.TrimSpace(p.Inherited)
	switch {
	case base == "":
		return own
	case own == "":
		return base
	}
	return base + "\n\n" + own
}
