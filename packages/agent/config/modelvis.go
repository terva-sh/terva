package config

import "strings"

// Model visibility: which models the pickers offer, and which they keep out of
// the way.
//
// This exists for one shape of problem — a provider that serves a catalogue far
// larger than any one person uses. OpenRouter lists hundreds of models; a
// picker that shows all of them is a picker you scroll past rather than read.
// Favourites solve the opposite half (pin the handful you love) and solve it
// badly here: pinning six models does nothing about the three hundred you still
// have to scroll through to reach anything unpinned.
//
// The rules are ORDERED and LAST MATCH WINS, which is the whole design. A flat
// blocklist cannot express "hide this whole provider except these four" without
// listing every model you did not want, and that list rots every time the
// provider adds a model. With ordering, the broad stroke comes first and the
// rescues come after:
//
//	"openrouter/*"                      hide the lot
//	"!openrouter/anthropic/claude-*"    …except Anthropic's
//	"openrouter/anthropic/claude-2*"    …but not the old ones
//
// A "!" prefix un-hides. Anything else hides. A model no rule matches is
// visible, so an empty list means "show everything" and nobody who never opens
// this feature pays for it.
//
// Hiding is about CHOOSING, never about capability. It is not a security
// control and it is not an availability control: the catalogue itself is
// untouched, so a hidden model keeps its context window, its cost data, and its
// reasoning ladder, and a session already running on one carries on. See
// IsModelHidden's callers for where the line is drawn.

// ModelKey is the "provider/id" identity that visibility rules and the
// favourites list both match against. Ids can collide across providers (the
// same model reached through a subscription and through a gateway), so neither
// half identifies a model on its own.
func ModelKey(provider, id string) string { return provider + "/" + id }

// ModelVisibility is a compiled rule list. Build it once per read of the config
// and query it per model: a picker asks this several hundred times in a row for
// a big provider, and re-parsing the rules each time is the obvious way to make
// opening a menu feel slow.
type ModelVisibility struct{ rules []visRule }

type visRule struct {
	pattern string // lowercased, "!" already stripped
	raw     string // the rule as the operator wrote it, for reporting
	hide    bool   // false for a "!" rescue
}

// NewModelVisibility compiles the ordered rule list from a config's
// HiddenModels. Blank entries are skipped rather than treated as a pattern: a
// stray empty string in hand-edited JSON would otherwise match nothing and
// confuse anyone reading it back, and a bare "!" is likewise inert.
func NewModelVisibility(rules []string) ModelVisibility {
	out := make([]visRule, 0, len(rules))
	for _, r := range rules {
		trimmed := strings.TrimSpace(r)
		if trimmed == "" || trimmed == "!" {
			continue
		}
		hide := true
		pattern := trimmed
		if strings.HasPrefix(pattern, "!") {
			hide, pattern = false, pattern[1:]
		}
		out = append(out, visRule{pattern: strings.ToLower(pattern), raw: trimmed, hide: hide})
	}
	if len(out) == 0 {
		return ModelVisibility{}
	}
	return ModelVisibility{rules: out}
}

// Empty reports whether no rule is configured, so a caller can skip the whole
// hidden-model code path (and the "show hidden" affordances that go with it)
// rather than render an empty section.
func (v ModelVisibility) Empty() bool { return len(v.rules) == 0 }

// Hidden reports whether this model is hidden from the pickers.
func (v ModelVisibility) Hidden(provider, id string) bool {
	hidden, _ := v.HiddenBy(provider, id)
	return hidden
}

// HiddenBy reports whether the model is hidden AND which rule decided it, as
// the operator wrote that rule. The second half is not decoration: once broad
// patterns are in play, "why is this model missing?" is a real question, and a
// picker that can answer "openrouter/*" turns a confusing absence into an
// obvious one. The rule is empty when nothing matched.
//
// Last match wins, so this walks backwards and stops at the first hit.
func (v ModelVisibility) HiddenBy(provider, id string) (bool, string) {
	key := strings.ToLower(ModelKey(provider, id))
	for i := len(v.rules) - 1; i >= 0; i-- {
		if globMatch(v.rules[i].pattern, key) {
			return v.rules[i].hide, v.rules[i].raw
		}
	}
	return false, ""
}

// ToggleHiddenModel returns the rule list with this ONE model hidden or shown,
// leaving every other model's verdict untouched.
//
// The subtlety is un-hiding something a broad pattern covers. Deleting a rule
// cannot do it — the model is hidden by "openrouter/*", and that pattern is
// there to hide two hundred others. So the toggle appends an explicit rule,
// which by last-match-wins beats whatever came before, and a single model can
// always be rescued from any pattern.
//
// It first drops this model's own exact-match rules, whichever polarity they
// carry. Those are the rules this toggle wrote, so re-writing them is its job;
// without that the list would grow by one entry every time somebody flipped the
// same model back and forth. Patterns are never touched: the operator wrote
// those by hand and they speak for many models.
//
// Appending is skipped when the remaining rules already give the answer asked
// for, which keeps the common case — hide a model, change your mind, show it
// again — from leaving any trace at all.
func ToggleHiddenModel(rules []string, provider, id string, hide bool) []string {
	key := ModelKey(provider, id)
	lower := strings.ToLower(key)

	out := make([]string, 0, len(rules)+1)
	for _, r := range rules {
		if exactRuleKey(r) == lower {
			continue
		}
		out = append(out, r)
	}
	if NewModelVisibility(out).Hidden(provider, id) != hide {
		if hide {
			out = append(out, key)
		} else {
			out = append(out, "!"+key)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// exactRuleKey returns the lowercased key a rule names exactly, or "" when the
// rule is a pattern (or blank). Only exact rules belong to the per-model
// toggle; a pattern is the operator's, and rewriting it would change the
// verdict for models they never asked about.
func exactRuleKey(rule string) string {
	trimmed := strings.TrimSpace(rule)
	trimmed = strings.TrimPrefix(trimmed, "!")
	if trimmed == "" || strings.Contains(trimmed, "*") {
		return ""
	}
	return strings.ToLower(trimmed)
}

// globMatch matches a pattern containing "*" wildcards against s. Both are
// expected lowercased; matching is otherwise literal.
//
// "*" spans ANY run of characters, slashes included, and that is the one thing
// this must not get wrong. path.Match refuses to cross a "/", which looks
// harmless until you meet OpenRouter: its ids are themselves "vendor/model", so
// a key is "openrouter/anthropic/claude-sonnet-4.5" with two slashes in it, and
// under path.Match the obvious rule "openrouter/*" would match none of the
// models it plainly means. Hence a wildcard matcher of our own rather than the
// standard library's path-shaped one.
//
// Linear time, no backtracking blowup: on a mismatch it returns to the last "*"
// and gives it one more character, which is the standard two-pointer wildcard
// match and cannot go quadratic on the pathological patterns ("*a*a*a*") that a
// naive recursive version dies on.
func globMatch(pattern, s string) bool {
	var (
		px, sx    int
		star      = -1
		afterStar int
	)
	for sx < len(s) {
		switch {
		case px < len(pattern) && pattern[px] == '*':
			star, afterStar = px, sx
			px++
		case px < len(pattern) && pattern[px] == s[sx]:
			px++
			sx++
		case star >= 0:
			// Backtrack: let the last "*" swallow one more character.
			px = star + 1
			afterStar++
			sx = afterStar
		default:
			return false
		}
	}
	// Trailing "*"s can match the empty remainder.
	for px < len(pattern) && pattern[px] == '*' {
		px++
	}
	return px == len(pattern)
}
