package i18n

// Keyed catalogs are the OTHER translatable surface: large, stable text
// keyed by a stable dotted id rather than English-as-key. There are three,
// split by audience into separate file sets under locales/<catalog>/ so
// different hands never share a file:
//
//   prompts — model-facing (P): canned English terva sends INTO a model
//             request (the /study task, the auto-swarm summary, the base
//             system-prompt segments, compaction instructions).
//   help    — operator-facing (H): the large `terva <cmd> --help` screens.
//   tools   — model-facing (D): each tool's description, which is the text
//             the model reads to decide whether to call it.
//
// tools is kept apart from prompts, though both are model-facing, because
// they are edited for different reasons. A prompt changes what terva SAYS;
// a tool description changes what the model DOES, and the effect shows up
// in tool-call behaviour. One file would mean an operator tuning a
// description reads past the compaction instructions to find it, and a
// diff to either would look like a diff to the other.
//
// The overlay is the point of this one. A tool description is the highest-
// leverage model-facing text in the system — a recorded session had the
// model pin raati to level 1 for its whole run because the text never
// offered the alternative — and until now it could only be changed by
// rebuilding the binary. $TERVA_HOME/locales/tools/en.json now overrides
// any description in place, which also makes an A/B of two wordings a file
// swap instead of two builds.
//
// Both differ from UI strings (T) the same way: UI text uses English-as-
// key — low-churn, self-describing for translators — but these are large
// (whole paragraphs make terrible catalog keys), few, and stable, so a
// dotted id (P("study.file", …), H("help.bot", …)) reads better and lets
// the English be reworded without silently orphaning a translation. Each
// entry is one parameterised template, never English glue around
// fragments, so a translation reads as coherent <lang>.

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
)

// keyedCatalogs are the named dotted-key catalogs, each its own file set
// (embedded locales/<name>/<lang>.json + $TERVA_HOME overlay). Adding a
// catalog here + a wrapper like P/H is all it takes to introduce a new
// keyed surface.
var keyedCatalogs = []string{"prompts", "help", "tools"}

// KeyedCatalogs returns the named keyed catalogs, for the command/lint
// layers that iterate them (init, coverage, -check).
func KeyedCatalogs() []string { return append([]string(nil), keyedCatalogs...) }

// P returns the active-language rendering of a model-facing prompt keyed
// by a stable dotted id. See keyedText.
func P(key, english string, args ...any) string {
	return keyedText("prompts", key, english, args...)
}

// H returns the active-language rendering of an operator-facing help
// block keyed by a stable dotted id. See keyedText.
func H(key, english string, args ...any) string {
	return keyedText("help", key, english, args...)
}

// D returns the active-language rendering of a tool description keyed by a
// stable dotted id. See keyedText.
//
// Call it from the tool's Description() method, never from a package-level
// var: Description() is invoked per request by Registry.SpecsVisible, which
// is after Configure has chosen a language, whereas a var initialiser runs
// before it and would freeze the English. A large description held as a
// documented const is fine — pass the const as the english default, which
// is what the extractor records.
func D(key, english string, args ...any) string {
	return keyedText("tools", key, english, args...)
}

// keyedText resolves key in the named catalog, falling back to the built-in
// english default when the language is English or has no override. args are
// applied via fmt.Sprintf only when present (matching T), so text carrying
// no verbs is returned untouched even if it contains a bare '%'. The english
// default keeps the source at the call site and is what the extractor
// records for the catalog's reference.
func keyedText(catalog, key, english string, args ...any) string {
	c := current()
	if c.isEN {
		return format(english, args)
	}
	if m := c.keyed[catalog]; m != nil {
		if tr, ok := m[key]; ok && tr != "" {
			return format(tr, args)
		}
	}
	noteKeyedMiss(catalog, key, english)
	return format(english, args)
}

// loadKeyed layers every keyed catalog for c.langName (embedded default,
// then the operator overlay) into c.keyed. Configure calls it after the UI
// catalog is loaded.
func (c *catalog) loadKeyed(home string) error {
	c.keyed = make(map[string]map[string]string, len(keyedCatalogs))
	for _, cat := range keyedCatalogs {
		m := map[string]string{}
		if data, err := localesFS.ReadFile("locales/" + cat + "/" + c.langName + ".json"); err == nil {
			if err := mergeKeyed(m, data); err != nil {
				return fmt.Errorf("i18n: parse embedded %s %s: %w", cat, c.langName, err)
			}
		}
		if home != "" {
			p := filepath.Join(home, "locales", cat, c.langName+".json")
			if data, err := os.ReadFile(p); err == nil {
				if err := mergeKeyed(m, data); err != nil {
					return fmt.Errorf("i18n: parse %s: %w", p, err)
				}
			}
		}
		c.keyed[cat] = m
	}
	return nil
}

// mergeKeyed folds one flat keyed JSON document ({key: template}) into m,
// later merges overriding earlier per-key; empty values fall through to the
// English default at the call site.
func mergeKeyed(m map[string]string, data []byte) error {
	var raw map[string]string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for k, v := range raw {
		if v != "" {
			m[k] = v
		}
	}
	return nil
}

// KeyedReferenceDoc returns the embedded English reference for a catalog as
// a flat key->english map (the extractor writes it; empty when none is
// embedded yet). It is the coverage denominator and init template for that
// catalog, mirroring ReferenceDoc for UI strings.
func KeyedReferenceDoc(catalog string) (map[string]string, error) {
	out := map[string]string{}
	data, err := localesFS.ReadFile("locales/" + catalog + "/en.json")
	if err != nil {
		return out, nil
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return out, err
	}
	return out, nil
}

// LoadMergedKeyed returns a catalog's translations for lang as they resolve
// at runtime — the embedded locale overlaid by $TERVA_HOME/locales/
// <catalog>/<lang>.json — WITHOUT changing the active language. Empty values
// are preserved so the command layer can tell "translated" from "present but
// blank". Mirrors LoadMerged for UI strings.
func LoadMergedKeyed(catalog, lang, home string) (map[string]string, error) {
	merged := map[string]string{}
	if data, err := localesFS.ReadFile("locales/" + catalog + "/" + lang + ".json"); err == nil {
		var m map[string]string
		if err := json.Unmarshal(data, &m); err != nil {
			return merged, err
		}
		maps.Copy(merged, m)
	}
	if home != "" {
		if data, err := os.ReadFile(filepath.Join(home, "locales", catalog, lang+".json")); err == nil {
			var m map[string]string
			if err := json.Unmarshal(data, &m); err != nil {
				return merged, err
			}
			maps.Copy(merged, m)
		}
	}
	return merged, nil
}
