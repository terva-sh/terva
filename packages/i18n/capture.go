package i18n

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"

	"golang.org/x/text/language"

	"terva.sh/terva/packages/privfs"
)

// Gap-capture records every source string shown to the operator that has
// no translation in the active language, so a translator can fill the gaps
// as they use the app. It is opt-in (off by default) and the hot path is a
// single atomic bool load, so an untranslated string costs nothing when
// capture is off.
var (
	captureOn    atomic.Bool
	missMu       sync.Mutex
	missSingular = map[string]bool{}
	missPlural   = map[[2]string]bool{}
	missKeyed    = map[string]map[string]string{} // catalog -> dotted key -> english default
)

// Capture toggles gap-capture. Enable it (via TERVA_CAPTURE_LOCALE or a
// /locale capture toggle) while using terva in a partly-translated
// language; Flush then writes what was missing to a todo file.
func Capture(on bool) { captureOn.Store(on) }

// Capturing reports whether gap-capture is on.
func Capturing() bool { return captureOn.Load() }

func noteMiss(key string) {
	if !captureOn.Load() {
		return
	}
	missMu.Lock()
	missSingular[key] = true
	missMu.Unlock()
}

func noteMissPlural(one, other string) {
	if !captureOn.Load() {
		return
	}
	missMu.Lock()
	missPlural[[2]string{one, other}] = true
	missMu.Unlock()
}

// noteKeyedMiss records an untranslated keyed-catalog entry with its
// English default so Flush can scaffold it into that catalog's todo file —
// pre-filled with the English (not blank), since a prompt/help author edits
// from the source template rather than translating a bare key.
func noteKeyedMiss(catalog, key, english string) {
	if !captureOn.Load() {
		return
	}
	missMu.Lock()
	if missKeyed[catalog] == nil {
		missKeyed[catalog] = map[string]string{}
	}
	missKeyed[catalog][key] = english
	missMu.Unlock()
}

// Flush merges the captured misses into $TERVA_HOME/locales/<lang>.todo.json
// and returns how many new keys were added. Existing entries (including any
// the operator has already started filling in) are preserved. It is a no-op
// in English mode or when nothing was captured. The todo file mirrors the
// locale format so a filled-in entry can be moved straight into
// <lang>.json (or promoted with `terva locale merge`).
func Flush(home string) (int, error) {
	c := current()
	if c.isEN || home == "" {
		return 0, nil
	}

	missMu.Lock()
	singles := make([]string, 0, len(missSingular))
	for k := range missSingular {
		singles = append(singles, k)
	}
	plurals := make([][2]string, 0, len(missPlural))
	for k := range missPlural {
		plurals = append(plurals, k)
	}
	keyed := make(map[string]map[string]string, len(missKeyed))
	for cat, m := range missKeyed {
		keyed[cat] = maps.Clone(m)
	}
	missMu.Unlock()

	keyedAdded := 0
	for cat, misses := range keyed {
		n, err := flushKeyed(home, c.langName, cat, misses)
		if err != nil {
			return 0, err
		}
		keyedAdded += n
	}
	if len(singles) == 0 && len(plurals) == 0 {
		return keyedAdded, nil
	}

	path := filepath.Join(home, "locales", c.langName+".todo.json")
	existing := map[string]json.RawMessage{}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &existing) // a corrupt todo is overwritten, not fatal
	}

	added := 0
	for _, k := range singles {
		if _, ok := existing[k]; ok {
			continue
		}
		existing[k] = json.RawMessage(`""`)
		added++
	}
	forms := FormsFor(c.lang) // scaffold the plural categories this language uses
	for _, p := range plurals {
		key := p[0] + pluralSep + p[1]
		if _, ok := existing[key]; ok {
			continue
		}
		scaffold := map[string]string{}
		for _, f := range forms {
			scaffold[f] = ""
		}
		existing[key] = MarshalValue(scaffold)
		added++
	}
	if added == 0 {
		return keyedAdded, nil
	}

	if err := privfs.MkdirAll(filepath.Dir(path)); err != nil {
		return 0, err
	}
	out, err := MarshalLocale(existing)
	if err != nil {
		return 0, err
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return 0, err
	}
	return added + keyedAdded, nil
}

// flushKeyed scaffolds captured misses for one keyed catalog into
// $TERVA_HOME/locales/<catalog>/<lang>.todo.json, pre-filled with each
// entry's English default (a prompt/help author edits the source template,
// not a blank), and returns how many new keys were added. Existing entries
// — including any the author has begun rewriting — are kept.
func flushKeyed(home, lang, catalog string, misses map[string]string) (int, error) {
	if len(misses) == 0 {
		return 0, nil
	}
	path := filepath.Join(home, "locales", catalog, lang+".todo.json")
	existing := map[string]json.RawMessage{}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &existing)
	}
	added := 0
	for k, english := range misses {
		if _, ok := existing[k]; ok {
			continue
		}
		existing[k] = MarshalValue(english)
		added++
	}
	if added == 0 {
		return 0, nil
	}
	if err := privfs.MkdirAll(filepath.Dir(path)); err != nil {
		return 0, err
	}
	out, err := MarshalLocale(existing)
	if err != nil {
		return 0, err
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return 0, err
	}
	return added, nil
}

// FormsFor returns the CLDR cardinal plural category names a language
// actually uses ("one","other" for English/Finnish; "one","few","many",
// "other" for Polish), discovered by probing representative counts. "other"
// is always present and always last.
func FormsFor(lang language.Tag) []string {
	seen := map[string]bool{}
	// A spread of counts that triggers every category across CLDR languages.
	for _, n := range []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 11, 21, 22, 100, 101, 111, 1000} {
		seen[category(lang, n)] = true
	}
	order := []string{"zero", "one", "two", "few", "many"}
	out := make([]string, 0, len(seen))
	for _, f := range order {
		if seen[f] {
			out = append(out, f)
		}
	}
	return append(out, "other")
}

// MarshalLocale encodes a locale/todo map with sorted keys and 2-space
// indent, so files diff cleanly and stay merge-friendly for PRs. It is the
// one canonical writer shared by gap-capture and the `terva locale` command.
func MarshalLocale(m map[string]json.RawMessage) ([]byte, error) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b []byte
	b = append(b, "{\n"...)
	for i, k := range keys {
		b = append(b, "  "...)
		b = append(b, MarshalValue(k)...)
		b = append(b, ": "...)
		b = append(b, m[k]...)
		if i < len(keys)-1 {
			b = append(b, ',')
		}
		b = append(b, '\n')
	}
	b = append(b, "}\n"...)
	return b, nil
}
