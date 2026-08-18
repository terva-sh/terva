package persona

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
	"terva.sh/terva/packages/agent/slug"

	"terva.sh/terva/packages/privfs"
)

// The persona side of the library. Unlike cards (untrusted data), a persona
// charter shapes identity in the cached prefix — a trusted-tier write — so the
// CONTROL surface gates create/edit; this file is just the read/serialize/write
// mechanics the workspace calls after it has checked trust.
//
// Personas already had a rich read side (All / Tiers / Origin);
// what was missing was a way to WRITE one back (persona files were hand-authored
// or copied verbatim by `persona init`). Marshal + Write close
// that, so the control plane can create a new persona and copy-to-edit a
// built-in, both landing in the user library at $TERVA_HOME/personas.

// Lookup finds a persona by a bare name/stem or a "namespace:name" ref,
// across every tier (user on-disk > extension > embedded), returning the
// highest-precedence match.
func Lookup(query string) (Persona, bool) {
	if strings.TrimSpace(query) == "" {
		return Persona{}, false
	}
	for _, p := range All() {
		if p.matches(query) {
			return p, true
		}
	}
	return Persona{}, false
}

// Marshal serializes a persona to its on-disk .md form (YAML frontmatter
// + charter body) — the inverse of Parse, so a round-trip is stable.
func Marshal(p Persona) ([]byte, error) {
	if strings.TrimSpace(p.Name) == "" {
		return nil, fmt.Errorf("persona: missing required name")
	}
	front, err := yaml.Marshal(frontmatter{
		Name:              strings.TrimSpace(p.Name),
		Pronunciation:     strings.TrimSpace(p.Pronunciation),
		Specialty:         strings.TrimSpace(p.Specialty),
		Summary:           strings.TrimSpace(p.Summary),
		Emoji:             strings.TrimSpace(p.Emoji),
		AccentColor:       strings.TrimSpace(p.AccentColor),
		Group:             strings.TrimSpace(p.Group),
		RecommendedSkills: p.RecommendedSkills,
		GoodFor:           p.GoodFor,
		AvoidFor:          p.AvoidFor,
		Immersive:         p.Immersive,
		AgentIntroduction: strings.TrimSpace(p.Introduction),
		// Extends, not the RESOLVED Inherited charter. Writing the resolved text
		// back would turn a reference into the hand-copied fork that `extends`
		// exists to replace — and it would do it the first time anyone opened
		// the persona in an editor and pressed save.
		Extends: strings.TrimSpace(p.Extends),
	})
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	b.WriteString("---\n")
	b.Write(front) // yaml.Marshal already ends in a newline
	b.WriteString("---\n\n")
	if c := strings.TrimSpace(p.Charter); c != "" {
		b.WriteString(c)
		b.WriteString("\n")
	}
	return []byte(b.String()), nil
}

// Path is the file a persona occupies in the user library — the ONE place that
// decides where a persona's file goes, so Write, UserPath and Delete cannot
// disagree about it.
//
// It follows the persona's IDENTITY, which is its Key: "<namespace>:<stem>".
// Both halves matter, and the write path used to reconstruct both from the
// display name — right only when the persona is top-level AND its file was
// named after its name. Ten of the sixteen shipped personas fail the first,
// three of those fail the second as well, and for all ten the copy a user saved
// shadowed nothing: the qualified ref every roster prints, every --persona flag
// and every swarm dispatch uses still resolved to the built-in, while the
// library reported the edit as saved.
//
// The namespace is fenced because this is where a namespace stops being a label
// and becomes a directory. Nothing else builds a path from one, so nothing else
// has to care that an extension's namespace is its self-declared manifest name
// — an arbitrary string from a bundle the user downloaded. "../../.ssh" is a
// display oddity in a roster and a directory traversal here.
func Path(p Persona) (string, error) {
	stem := writeStem(p)
	if stem == "" {
		return "", fmt.Errorf("persona: name %q has no usable filename", p.Name)
	}
	ns := strings.TrimSpace(p.Namespace)
	if ns == "" {
		return filepath.Join(Dir(), stem+".md"), nil
	}
	if err := slug.ValidID(ns); err != nil {
		return "", fmt.Errorf("persona: namespace %q cannot be a directory: %w", p.Namespace, err)
	}
	return filepath.Join(Dir(), ns, stem+".md"), nil
}

// writeStem is the file stem a write takes: the stem the persona is already
// FILED under when it has one, else a fresh one slugged from its name.
//
// Filed-under wins because that stem is half the Key, and a name is not
// reliably the stem. raati-crew/yata.md carries the name YATA-1; slugging the
// name gives "yata-1", so a saved copy keyed raati-crew:yata-1 while the panel
// convenes raati-crew:yata — two personas, and the edit reaches neither seat.
// Nothing enforces that an author name a file after its frontmatter, and three
// shipped personas do not.
//
// One case still folds forward: a file under the pre-diacritic spelling of its
// OWN name ("sepp.md" for "Seppä") migrates to the folded stem, which is what
// Write's legacy sweep then clears. Distinguishable from YATA-1 because the
// filed stem is exactly slug.Legacy of the name it carries, and YATA-1's is not.
func writeStem(p Persona) string {
	filed := p.stem()
	if filed == "" || (filed == slug.Legacy(p.Name) && filed != slug.Of(p.Name)) {
		return slug.Of(p.Name)
	}
	return filed
}

// Overwrite files `form` as the same persona as `into` — same namespace, same
// file. It is what makes an edit an EDIT rather than a same-named neighbour,
// and it is the caller's half of the contract Path describes: a persona's
// identity travels on Namespace and Source, and a form arriving off the wire
// carries neither.
//
// A RENAME is not an overwrite. When form names a different persona than into,
// the file is left to be minted from the new name, exactly as before — only the
// shelf (namespace) is inherited, so a renamed crew member stays with its crew.
func Overwrite(form, into Persona) Persona {
	form.Namespace = into.Namespace
	if strings.EqualFold(strings.TrimSpace(form.Name), strings.TrimSpace(into.Name)) {
		form.Source = into.Source
	}
	return form
}

// UserPath returns the on-disk library path for a persona name or qualified
// "namespace:name" ref, and whether a file already exists there — how the
// control surface tells a create (must be new) from an edit (may overwrite /
// shadow a built-in), and which file a delete removes.
//
// Three probes, in the order a caller means them:
//
//  1. The exact file the ref names, which is the fast and unambiguous case.
//  2. Any user file the query RESOLVES to — the roster's own matching rule. A
//     bare name may sit under a team subdirectory (what `terva persona init`
//     writes), and a ref may name a persona whose file is not named after it
//     (raati-crew:YATA-1 lives in yata.md). Anything Lookup can find, delete
//     and edit have to be able to find, or the surface refuses to remove a file
//     it is simultaneously showing you.
//  3. A persona saved before slugs folded diacritics ("sepp.md" for "Seppä");
//     that file is reported instead, so an edit lands on the persona the user
//     actually has — and Write is what moves it to the folded stem.
func UserPath(name string) (string, bool) {
	ns, bare := splitRef(name)
	dest, err := Path(Persona{Name: bare, Namespace: ns})
	if err != nil {
		return "", false
	}
	if _, err := os.Stat(dest); err == nil {
		return dest, true
	}
	if resolved, ok := userFileMatching(name); ok {
		return resolved, true
	}
	if legacy, ok := legacyPersonaFile(ns, bare); ok {
		return legacy, true
	}
	return dest, false
}

// userFileMatching finds the user file a query resolves to, by the same rule
// the roster uses. Consulted only AFTER the exact-path probe: when a top-level
// file and a nested one share a stem, the roster resolves a bare name to the
// top-level one, and this must not answer differently.
func userFileMatching(query string) (string, bool) {
	for _, p := range listOnDisk() {
		if p.matches(query) {
			return p.Source, true
		}
	}
	return "", false
}

// ByFile returns the roster entry the given library file produced, and whether
// the merged roster resolves to it.
//
// "Did the library take what I just wrote" is a different question from "which
// persona does this name resolve to", and once a write can be targeted by ref
// the two have different answers: edit review-crew:vartija while a top-level
// Vartija of yours also exists, and re-reading by name describes the top-level
// one — a persona the call did not touch — as the saved result.
func ByFile(path string) (Persona, bool) {
	for _, p := range All() {
		if p.Source == path {
			return p, true
		}
	}
	return Persona{}, false
}

// legacyPersonaFile reports the pre-diacritic-fold library file for a name in
// namespace ns, if one is present AND actually names this persona — "Sepp"
// legitimately owns sepp.md, and a lookup for "Seppä" must not claim it.
func legacyPersonaFile(ns, name string) (string, bool) {
	legacy := slug.Legacy(name)
	if legacy == "" || legacy == slug.Of(name) {
		return "", false
	}
	// Through Path so a pre-fold file in a team subdirectory is found where its
	// folded successor will be written, rather than only at the top level.
	p, err := Path(Persona{Name: legacy, Namespace: ns})
	if err != nil {
		return "", false
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return "", false
	}
	old, err := Parse(string(raw), p)
	if err != nil || !strings.EqualFold(old.Name, name) {
		return "", false
	}
	return p, true
}

// Write writes a persona into the user library ($TERVA_HOME/personas),
// returning the file path. Copy-to-edit of a built-in falls out for free: the
// written user file shadows the embedded one by Key (user tier wins) — which is
// why the file must land on the FOLDED slug ("seppa.md"), the stem the built-in
// uses, and under the built-in's NAMESPACE, the other half of that Key.
//
// So p.Namespace is load-bearing input, not decoration. A caller copying a
// built-in has to carry it across or the copy shadows nothing; workspace's
// PersonasEdit takes it from the persona it resolved. Enforcing the trust gate
// is the CALLER's job — this is the mechanism, not the policy.
func Write(p Persona) (string, error) {
	raw, err := Marshal(p)
	if err != nil {
		return "", err
	}
	dest, err := Path(p)
	if err != nil {
		return "", err
	}
	if err := privfs.MkdirAll(filepath.Dir(dest)); err != nil {
		return "", err
	}
	if err := privfs.WriteFileMode(dest, raw, 0o644); err != nil {
		return "", err
	}
	// A file this persona minted before diacritic folding ("sepp.md") would
	// otherwise linger as a second roster entry that still fails to shadow its
	// built-in. legacyPersonaFile has name-checked, so this cannot remove a
	// distinct persona's file.
	if legacy, ok := legacyPersonaFile(p.Namespace, p.Name); ok && legacy != dest {
		_ = os.Remove(legacy)
	}
	return dest, nil
}

// Delete removes a persona from the user library ($TERVA_HOME/personas), by
// bare name or qualified ref — the inverse of Write, and it resolves the file
// the same way, through UserPath. It touches ONLY the user tier: the embedded
// crew and extension bundles are not on disk here and are unaffected, so
// deleting a user file that shadowed a built-in un-shadows it and the built-in
// becomes visible again — the way back from a copy-to-edit.
//
// Reports whether a file was removed, so a caller can tell "deleted" from
// "there was nothing of yours by that name" and answer accordingly. As with
// Write, the trust gate is the CALLER's job.
func Delete(name string) (bool, error) {
	dest, exists := UserPath(name)
	if dest == "" {
		return false, fmt.Errorf("persona: name %q has no usable filename", name)
	}
	if !exists {
		return false, nil
	}
	if err := os.Remove(dest); err != nil {
		return false, err
	}
	return true, nil
}
