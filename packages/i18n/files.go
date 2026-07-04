package i18n

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/text/language"
)

// localesFS holds the embedded locale files. locales/en.json is the
// reference template — the identity "translation" listing every
// translatable source string (English-as-key), used as the coverage
// denominator and by `terva locale init`. Configure("en") short-circuits
// before reading it, so it is a tooling artifact, never a runtime lookup.
//
//go:embed locales
var localesFS embed.FS

// Doc is a decoded locale file: singular translations plus plural entries
// (keyed by the "one|other" English composite, each mapping CLDR category
// names to forms). ParseLocaleFile and the runtime share this shape.
type Doc struct {
	Singular map[string]string
	Plural   map[string]map[string]string
}

// decodeLocale parses one locale JSON document. Each value is either a
// string (singular) or an object of plural forms; anything else is an error
// naming the offending key.
func decodeLocale(data []byte) (Doc, error) {
	doc := Doc{Singular: map[string]string{}, Plural: map[string]map[string]string{}}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return doc, err
	}
	for k, v := range raw {
		v = bytes.TrimSpace(v)
		if len(v) == 0 {
			continue
		}
		switch v[0] {
		case '"':
			var s string
			if err := json.Unmarshal(v, &s); err != nil {
				return doc, fmt.Errorf("key %q: %w", k, err)
			}
			doc.Singular[k] = s
		case '{':
			var m map[string]string
			if err := json.Unmarshal(v, &m); err != nil {
				return doc, fmt.Errorf("key %q: %w", k, err)
			}
			doc.Plural[k] = m
		default:
			return doc, fmt.Errorf("key %q: value must be a string or an object of plural forms", k)
		}
	}
	return doc, nil
}

// ParseLocaleFile decodes a locale/todo JSON document for the command layer
// (validate, diff, merge). It shares the runtime's parser so the on-disk
// format has exactly one definition.
func ParseLocaleFile(data []byte) (Doc, error) { return decodeLocale(data) }

// embedLocalePath / overlayLocalePath map a (subdir, lang) to the embedded FS
// path and the $TERVA_HOME overlay path. subdir "" is the root UI catalog;
// a non-empty subdir (e.g. "web") is a sibling English-as-key singular+plural
// catalog in its own directory, laid out like the keyed catalogs but decoded
// as a Doc.
func embedLocalePath(subdir, lang string) string {
	if subdir == "" {
		return "locales/" + lang + ".json"
	}
	return "locales/" + subdir + "/" + lang + ".json"
}

func overlayLocalePath(home, subdir, lang string) string {
	if subdir == "" {
		return filepath.Join(home, "locales", lang+".json")
	}
	return filepath.Join(home, "locales", subdir, lang+".json")
}

// EmbeddedLocaleNames lists the target languages shipped in the binary
// (every embedded locale except the English reference), sorted.
func EmbeddedLocaleNames() []string { return EmbeddedLocaleNamesIn("") }

// EmbeddedLocaleNamesIn is EmbeddedLocaleNames for a named UI catalog subdir
// ("" = the root UI catalog, "web" = the control-panel catalog).
func EmbeddedLocaleNamesIn(subdir string) []string {
	dir := "locales"
	if subdir != "" {
		dir = "locales/" + subdir
	}
	entries, err := fs.ReadDir(localesFS, dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".json")
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || name == "en" {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ReferenceKeys returns every translatable source key (singular sources and
// "one|other" plural composites) from the embedded English reference,
// sorted. It is the denominator for translation coverage and the seed set
// for `terva locale init`. Returns nil when no reference is embedded yet.
func ReferenceKeys() []string { return ReferenceKeysIn("") }

// ReferenceKeysIn is ReferenceKeys for a named UI catalog subdir.
func ReferenceKeysIn(subdir string) []string {
	data, err := localesFS.ReadFile(embedLocalePath(subdir, "en"))
	if err != nil {
		return nil
	}
	doc, err := decodeLocale(data)
	if err != nil {
		return nil
	}
	keys := make([]string, 0, len(doc.Singular)+len(doc.Plural))
	for k := range doc.Singular {
		keys = append(keys, k)
	}
	for k := range doc.Plural {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ReferenceDoc returns the parsed English reference (its plural entries give
// the English forms used to scaffold a new locale's plural categories).
func ReferenceDoc() (Doc, error) { return ReferenceDocIn("") }

// ReferenceDocIn is ReferenceDoc for a named UI catalog subdir.
func ReferenceDocIn(subdir string) (Doc, error) {
	data, err := localesFS.ReadFile(embedLocalePath(subdir, "en"))
	if err != nil {
		return Doc{Singular: map[string]string{}, Plural: map[string]map[string]string{}}, nil
	}
	return decodeLocale(data)
}

// verbRE matches a printf verb, ignoring escaped %%. It is deliberately
// permissive (flags, width, precision, then a verb letter) — enough to
// catch a translation that drops or reorders a %s/%d, the common bug.
var verbRE = regexp.MustCompile(`%[-+ #0]*[0-9*]*(?:\.[0-9*]+)?[a-zA-Z%]`)

// Verbs returns the printf verbs in s (e.g. ["%s","%d"]), excluding the
// literal "%%". Used to check that a translation preserves its source's
// format arguments.
func Verbs(s string) []string {
	var out []string
	for _, m := range verbRE.FindAllString(s, -1) {
		if m == "%%" {
			continue
		}
		out = append(out, m)
	}
	return out
}

// LoadMerged returns the translations for lang as they resolve at runtime —
// the embedded locale overlaid by $TERVA_HOME/locales/<lang>.json — WITHOUT
// changing the active language. Empty values are preserved (so the command
// layer can distinguish "translated" from "present but blank"). Used by
// `terva locale` for coverage, diff, and export.
func LoadMerged(lang, home string) (Doc, error) { return LoadMergedIn("", lang, home) }

// LoadMergedIn is LoadMerged for a named UI catalog subdir ("" = root UI
// catalog, "web" = the control-panel catalog served to the browser). It reads
// the files fresh on every call, so an operator's overlay edit is picked up
// without restarting the daemon — the basis for the web panel's reload loop.
func LoadMergedIn(subdir, lang, home string) (Doc, error) {
	merged := Doc{Singular: map[string]string{}, Plural: map[string]map[string]string{}}
	if data, err := localesFS.ReadFile(embedLocalePath(subdir, lang)); err == nil {
		d, err := decodeLocale(data)
		if err != nil {
			return merged, err
		}
		mergeInto(&merged, d)
	}
	if home != "" {
		if data, err := os.ReadFile(overlayLocalePath(home, subdir, lang)); err == nil {
			d, err := decodeLocale(data)
			if err != nil {
				return merged, err
			}
			mergeInto(&merged, d)
		}
	}
	return merged, nil
}

func mergeInto(dst *Doc, src Doc) {
	maps.Copy(dst.Singular, src.Singular)
	maps.Copy(dst.Plural, src.Plural)
}

// FormsForLang returns the CLDR plural category names a language uses, given
// a BCP-47 tag string. A malformed tag degrades to English's ["one","other"]
// so callers (locale scaffolding) never fail on a typo.
func FormsForLang(lang string) []string {
	tag, err := language.Parse(lang)
	if err != nil {
		return []string{"one", "other"}
	}
	return FormsFor(tag)
}

// PluralKey composes the entry key for a plural pair, matching TN.
func PluralKey(one, other string) string { return one + pluralSep + other }

// ContextKey composes the message key TC uses for a context+source pair, so
// the extractor emits reference keys that match runtime lookups.
func ContextKey(ctx, source string) string { return ctx + ctxSep + source }

// MarshalValue encodes v as a JSON value WITHOUT HTML-escaping <, >, and & —
// so locale files stay readable (e.g. "<lang>" not "<lang>") for
// the operators who hand-edit them. It is the encoder every locale-file
// writer should use for both keys and values.
func MarshalValue(v any) json.RawMessage {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return json.RawMessage(bytes.TrimRight(buf.Bytes(), "\n"))
}

// SplitPluralKey reverses PluralKey; ok is false when key is not a composite.
func SplitPluralKey(key string) (one, other string, ok bool) {
	return strings.Cut(key, pluralSep)
}
