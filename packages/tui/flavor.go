package tui

import (
	"math/rand"
	"strings"
)

// Flavor text — the rotating tagline in the startup greeting and the
// spinner's working lines — is template-driven. A template may embed
// {verb} and {noun} placeholders, each filled with an independent random
// pick from the theme's FlavorVerbs / FlavorNouns pools:
//
//	"{verb} the {noun}."  ->  "harness the wildcard."
//
// Plain strings (no placeholders) pass through unchanged, so a fixed line
// and a generated one can sit in the same list. Themes override the pools
// and the template lists via JSON (see theme_loader.go), so the whole
// voice is customizable without a rebuild.

// defaultGreetings is the tagline shown after "i'm terva." — mostly
// {verb} the {noun} templates that riff on the brand line ("harness the
// wildcard"), plus a couple of fixed lines. Voice: lowercase, cheeky, and
// deliberately not boxed into "coding" — terva operates whatever tools you
// give it.
var defaultGreetings = []string{
	"{verb} the {noun}.",
	"here to {verb} the {noun}.",
	"i {verb} the {noun} so you don't have to.",
	"an agent harness; let's {verb} the {noun}.",
	"your harness for the {noun}.",
	"ask anything; i'll {verb} it.",
	"point me at anything.",
}

// defaultFlavorVerbs / defaultFlavorNouns fill the {verb} / {noun} slots.
// Verbs are imperative ("harness", not "harnessing") so they read in the
// greeting; nouns are bare so "the {noun}" stays grammatical. "harness" +
// "wildcard" lead so the brand line is always reachable.
var defaultFlavorVerbs = []string{
	"harness", "wrangle", "tame", "herd", "corral", "marshal",
	"conjure", "summon", "coax", "steer", "rig", "tar",
}
var defaultFlavorNouns = []string{
	"wildcard", "chaos", "entropy", "tokens", "daemons", "gremlins",
	"machines", "swarm", "unknown", "wilderness", "longtail",
}

// FillFlavor replaces {verb}/{noun} tokens in s with a random word from the
// given pools, using the package RNG; each occurrence is chosen
// independently. Plain strings pass through unchanged. Exported for callers
// (the spinner) that hold the pools without a full Theme.
func FillFlavor(s string, verbs, nouns []string) string {
	return fillFlavor(s, verbs, nouns, rand.Intn)
}

// fillFlavor is FillFlavor with an injectable index picker (pick(n) returns
// an index in [0,n)) so tests are deterministic. Unknown tokens, and tokens
// whose pool is empty, are left verbatim.
func fillFlavor(s string, verbs, nouns []string, pick func(n int) int) string {
	if !strings.ContainsRune(s, '{') {
		return s
	}
	var b strings.Builder
	for {
		open := strings.IndexByte(s, '{')
		if open < 0 {
			b.WriteString(s)
			break
		}
		end := strings.IndexByte(s[open:], '}')
		if end < 0 {
			b.WriteString(s)
			break
		}
		end += open
		b.WriteString(s[:open])
		var pool []string
		switch s[open+1 : end] {
		case "verb":
			pool = verbs
		case "noun":
			pool = nouns
		}
		if len(pool) > 0 {
			b.WriteString(pool[pick(len(pool))])
		} else {
			b.WriteString(s[open : end+1]) // leave the unknown/empty {token} verbatim
		}
		s = s[end+1:]
	}
	return b.String()
}

// FillFlavor fills {verb}/{noun} placeholders in s from this theme's flavor
// pools. Plain strings pass through unchanged.
func (t Theme) FillFlavor(s string) string {
	return FillFlavor(s, t.FlavorVerbs, t.FlavorNouns)
}

// Greeting returns a random rendered tagline for the startup headline (the
// text shown after the "i'm …" prefix — "i'm terva." on the help/usage
// screen, "i'm <persona>." in the interactive welcome banner). Falls back to a
// plain line when no templates are configured.
func (t Theme) Greeting() string {
	if len(t.Greetings) == 0 {
		return "an agent harness; ask anything."
	}
	return t.FillFlavor(t.Greetings[rand.Intn(len(t.Greetings))])
}
