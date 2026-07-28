// Package slug turns display names into filesystem stems, and validates the
// ids that arrive off the wire.
//
// Every on-disk library in terva files its entries by a stem derived from the
// entry's name — cards, card history, card models, groups, personas, user
// personas, worlds. They do not merely happen to agree on how: a persona file
// and a card directory MUST resolve to the same stem, or a user copy stops
// shadowing the built-in it is meant to replace (the Seppä case, in Of's own
// note). That shared definition is what this package is.
//
// # On being exported
//
// These functions were unexported helpers in package build, and the file they
// lived in carried a note saying they should stay that way if they were ever
// moved out: "a stem is an internal filing decision, and an exported slugger
// invites callers outside build to mint paths."
//
// The intent survives; the mechanism could not. A package cannot be shared
// without exporting, and the stores that need it now live in two packages
// (build and persona) with a third plausible. So the fence moved rather than
// came down: importers_test.go names every package allowed to import this one,
// with the store it owns, and an unlisted importer fails the build's own gate.
// An allow-list a reviewer reads beats a scope rule that only held while every
// caller happened to be a neighbour.
//
// The `card` prefixes are gone with the move for the opposite reason to why
// they were kept. Inside build, cardSlug had to say "card" so its shared-ness
// was visible among fifty unrelated helpers; here the package name says it, and
// slug.Card would only stutter.
package slug

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// ID is a stable, human-legible library id: a stem of the name plus a short
// content hash. The hash makes an import idempotent and disambiguates two
// entries that share a name.
func ID(name string, normalized []byte) string {
	return IDFrom(Of(name), normalized)
}

// IDFrom is ID with the stem already chosen — card import uses it to probe the
// id an existing entry was filed under before diacritic folding.
func IDFrom(stem string, normalized []byte) string {
	sum := sha256.Sum256(normalized)
	h := hex.EncodeToString(sum[:])[:12]
	if stem == "" {
		return h
	}
	return stem + "-" + h
}

// Of lowercases a name and keeps [a-z0-9], collapsing every other run to a
// single dash, capped so an absurd name can't make an absurd directory. A letter
// carrying diacritics folds to its base letter first (NFD splits off the
// combining marks, which are then skipped), so "Seppä" slugs to "seppa" — the
// stem the built-in crew file uses, which is what lets a user copy shadow it by
// Key — rather than losing the letter to a dash.
func Of(name string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range norm.NFD.String(strings.ToLower(strings.TrimSpace(name))) {
		if unicode.Is(unicode.Mn, r) {
			continue // a stripped diacritic is not a word boundary
		}
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevDash = false
		} else if b.Len() > 0 && !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
		if b.Len() >= 32 {
			break
		}
	}
	return strings.Trim(b.String(), "-")
}

// Legacy is Of as it behaved before diacritic folding: a non-ASCII letter
// collapsed to a dash instead of its base letter, so "Seppä" minted "sepp" and
// "Zoë" minted "zo". It exists only so the stores can find files written under
// the old spelling; never mint a new name with it. Identical to Of for
// pure-ASCII names — callers use "Legacy != Of" as the cheap "could a pre-fold
// file even exist" test.
func Legacy(name string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevDash = false
		} else if b.Len() > 0 && !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
		if b.Len() >= 32 {
			break
		}
	}
	return strings.Trim(b.String(), "-")
}

// ValidID rejects ids that could escape the library directory. Ids come off the
// wire (cards.get / cards.edit / cards.delete), so a "../../etc" id must not
// resolve to a real path.
func ValidID(id string) error {
	if id == "" || strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return fmt.Errorf("invalid card id %q", id)
	}
	return nil
}
