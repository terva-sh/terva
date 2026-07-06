// Package i18n is terva's translation layer. English source strings are
// the message keys: T("start a fresh session") looks that sentence up in
// the active operator language and returns the translation, falling back
// to the English source when there is none.
//
// Design invariant: when the active language is English (or unset), every
// T/TC/TN call returns its English source byte-for-byte. The default build
// is therefore indistinguishable from the pre-i18n code, so the existing
// golden and strings.Contains tests do not churn as strings are wrapped.
//
// This is a leaf package — it imports only golang.org/x/text. tui, agent,
// core, provider, and tools all call T, so it must import none of theirs.
// The active language is injected once at startup via Configure (never
// pulled from config), which keeps the import graph acyclic.
//
// Keying is English-as-key with a gettext-style context escape hatch (TC)
// for the rare strings whose English collides but whose translation must
// differ. Pluralization is delegated to the CLDR rules in
// golang.org/x/text/feature/plural, so languages with more than the
// English one/other split (Polish, Russian, Arabic, …) select the right
// form without terva hand-rolling the linguistics.
package i18n

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"golang.org/x/text/feature/plural"
	"golang.org/x/text/language"
)

// ctxSep joins a disambiguation context to its source string to form a
// message key, matching gettext's msgctxt separator (U+0004). pluralSep
// joins the English one/other forms into a plural entry's key.
const (
	ctxSep    = "\x04"
	pluralSep = "|"
)

// catalog is an immutable snapshot of the active language and its messages.
// It is swapped atomically by Configure so T stays lock-free on the hot
// path (the TUI renders from many goroutines).
type catalog struct {
	lang     language.Tag
	langName string
	isEN     bool
	singular map[string]string            // key -> translation
	plural   map[string]map[string]string // "one|other" -> {category -> form}
	keyed    map[string]map[string]string // catalog -> dotted key -> template (see keyed.go)
}

var active atomic.Pointer[catalog]

func init() { active.Store(&catalog{langName: "en", isEN: true}) }

func current() *catalog { return active.Load() }

// T returns the active-language translation of the English source string,
// falling back to source when untranslated. When args are supplied the
// result is rendered through fmt.Sprintf, so pass args only for strings
// that carry format verbs (matching how call sites already choose between
// a literal and fmt.Sprintf).
func T(source string, args ...any) string {
	return format(lookup(current(), source, source), args)
}

// Errorf returns an error whose message is the translated, formatted source.
// Use it for user-facing errors instead of fmt.Errorf: a translated (hence
// non-constant) format string trips go vet's printf check, but errors.New
// does not. It does NOT support %w — to wrap an error, keep a constant
// format and translate only the prefix:
//
//	return fmt.Errorf("%s: %w", i18n.T("--cwd %q", path), err)
func Errorf(source string, args ...any) error {
	return errors.New(T(source, args...))
}

// M ("mark") returns source unchanged. Use it to mark a translatable
// string that is DECLARED far from where it is displayed — a literal in a
// data table built at init (slash-command descriptions, menu labels, spinner
// lines) whose language isn't known until Configure runs later. The
// extractor records M's argument in the reference, and the display site
// translates the stored English with i18n.T at render time. This is the
// declare-vs-render split (gettext's N_/gettext_noop): never call T at
// package-init or in a var initializer — it would freeze the English active
// before Configure.
func M(source string) string { return source }

// TC is T with a disambiguation context: two identical English strings
// ("Save" the button vs. "Save" the menu) can carry different
// translations keyed by ctx. English rendering ignores ctx entirely and
// returns source, preserving the byte-identity invariant.
func TC(ctx, source string, args ...any) string {
	c := current()
	if c.isEN {
		return format(source, args)
	}
	return format(lookup(c, ctx+ctxSep+source, source), args)
}

// lookup resolves key in c, returning fallback (the English source) when c
// is English or the key is missing/empty. A miss is recorded for capture.
func lookup(c *catalog, key, fallback string) string {
	if c.isEN {
		return fallback
	}
	if tr, ok := c.singular[key]; ok && tr != "" {
		return tr
	}
	noteMiss(key)
	return fallback
}

// TN returns the plural form of a count-bearing string. one and other are
// the English singular/plural templates and double as the entry's key
// ("%d agent|%d agents"); the active language's CLDR category for n selects
// among the translated forms. When no args are given, n is used as the sole
// format argument, so TN(k, "%d agent", "%d agents") renders directly.
func TN(n int, one, other string, args ...any) string {
	if len(args) == 0 {
		args = []any{n}
	}
	c := current()
	if c.isEN {
		return fmt.Sprintf(englishPlural(n, one, other), args...)
	}
	forms, ok := c.plural[one+pluralSep+other]
	if !ok {
		noteMissPlural(one, other)
		return fmt.Sprintf(englishPlural(n, one, other), args...)
	}
	form := forms[category(c.lang, n)]
	if form == "" {
		form = forms["other"]
	}
	if form == "" {
		form = englishPlural(n, one, other)
	}
	return fmt.Sprintf(form, args...)
}

func englishPlural(n int, one, other string) string {
	if n == 1 {
		return one
	}
	return other
}

// format applies fmt.Sprintf only when args are present, so a translated
// string containing a bare '%' (e.g. "100%") is returned untouched.
func format(s string, args []any) string {
	if len(args) > 0 {
		return fmt.Sprintf(s, args...)
	}
	return s
}

// category maps n to its CLDR cardinal plural category name for lang.
func category(lang language.Tag, n int) string {
	switch plural.Cardinal.MatchPlural(lang, n, 0, 0, 0, 0) {
	case plural.Zero:
		return "zero"
	case plural.One:
		return "one"
	case plural.Two:
		return "two"
	case plural.Few:
		return "few"
	case plural.Many:
		return "many"
	default:
		return "other"
	}
}

// Configure sets the active language and loads its messages: the embedded
// locales/<lang>.json default, then $TERVA_HOME/locales/<lang>.json layered
// on top per-key (an operator overrides individual strings without copying
// the whole file). An empty tag, "en", or any "en-*" tag selects English —
// no files are read and T/TC/TN return their source verbatim. An unknown or
// malformed tag falls back to English and returns an error the caller may
// surface as a warning without aborting startup.
func Configure(lang, home string) error {
	lang = strings.TrimSpace(lang)
	low := strings.ToLower(lang)
	if lang == "" || low == "en" || strings.HasPrefix(low, "en-") {
		// English needs no embedded translation, so the common case is
		// the byte-identical fast path: T(x)==x, no files read. But
		// honor an optional operator overlay ($TERVA_HOME/locales/en.json
		// and/or prompts/en.json) so anyone — English speakers included —
		// can reword the UI or override terva's canned prompts without
		// switching language. Unoverridden keys still return their English
		// source, so opting in changes only what you actually override.
		if home == "" || !englishOverlayPresent(home) {
			active.Store(&catalog{langName: "en", isEN: true})
			return nil
		}
		c := &catalog{
			lang:     language.English,
			langName: "en",
			singular: map[string]string{},
			plural:   map[string]map[string]string{},
		}
		p := filepath.Join(home, "locales", "en.json")
		if data, err := os.ReadFile(p); err == nil {
			if err := c.merge(data); err != nil {
				return fmt.Errorf("i18n: parse %s: %w", p, err)
			}
		}
		if err := c.mergeUISubdirs(home); err != nil {
			return err
		}
		if err := c.loadKeyed(home); err != nil {
			return err
		}
		active.Store(c)
		return nil
	}
	tag, err := language.Parse(lang)
	if err != nil {
		active.Store(&catalog{langName: "en", isEN: true})
		return fmt.Errorf("i18n: unknown language %q; staying in english: %w", lang, err)
	}
	c := &catalog{
		lang:     tag,
		langName: lang,
		singular: map[string]string{},
		plural:   map[string]map[string]string{},
	}
	if data, err := localesFS.ReadFile("locales/" + lang + ".json"); err == nil {
		if err := c.merge(data); err != nil {
			return fmt.Errorf("i18n: parse embedded locale %s: %w", lang, err)
		}
	}
	if home != "" {
		p := filepath.Join(home, "locales", lang+".json")
		if data, err := os.ReadFile(p); err == nil {
			if err := c.merge(data); err != nil {
				return fmt.Errorf("i18n: parse %s: %w", p, err)
			}
		}
	}
	// The Go-merged UI subcatalogs (tui) layer into the same UI lookup.
	if err := c.mergeUISubdirs(home); err != nil {
		return err
	}
	// The dotted-key catalogs (keyed.go: prompts, help) are separate file
	// sets layered the same way (embedded < overlay).
	if err := c.loadKeyed(home); err != nil {
		return err
	}
	active.Store(c)
	return nil
}

// englishOverlayPresent reports whether the operator has opted into an
// English overlay — a $TERVA_HOME/locales/en.json (UI) or any keyed
// catalog's locales/<catalog>/en.json (prompts, help) — so Configure only
// leaves the isEN fast path for English when there is actually something
// to override.
func englishOverlayPresent(home string) bool {
	if _, err := os.Stat(filepath.Join(home, "locales", "en.json")); err == nil {
		return true
	}
	for _, cat := range mergedUICatalogs {
		if _, err := os.Stat(filepath.Join(home, "locales", cat, "en.json")); err == nil {
			return true
		}
	}
	for _, cat := range keyedCatalogs {
		if _, err := os.Stat(filepath.Join(home, "locales", cat, "en.json")); err == nil {
			return true
		}
	}
	return false
}

// ActiveLang returns the active language tag string ("en" when unset).
func ActiveLang() string { return current().langName }

// merge folds one locale JSON document into the catalog, later merges
// overriding earlier ones per-key (embedded default first, $TERVA_HOME
// overlay second).
func (c *catalog) merge(data []byte) error {
	doc, err := decodeLocale(data)
	if err != nil {
		return err
	}
	for k, v := range doc.Singular {
		if v != "" {
			c.singular[k] = v
		}
	}
	maps.Copy(c.plural, doc.Plural)
	return nil
}

// TUICatalogName is the interactive terminal-UI catalog. Like the root UI
// catalog it is English-as-key, but it lives in its own file set
// (locales/tui/<lang>.json) so the terminal surface can be translated
// independently of core. Unlike the web UI catalog (served to the browser) it
// is MERGED into the Go T lookup at Configure, because the TUI is Go code
// calling i18n.T. The terva-i18n-lint extractor routes a directory's UI strings
// here via a `//i18n:catalog tui` directive.
const TUICatalogName = "tui"

// mergedUICatalogs are the English-as-key UI file sets Configure merges into the
// active Go catalog alongside the root (locales/). The split is authoring-only:
// at runtime every UI string resolves against one merged lookup, so a
// translator can finish the surface they use (e.g. the TUI) without touching
// core. Adding one here + a `//i18n:catalog <name>` directive on its source
// directory carves out a new Go UI surface.
var mergedUICatalogs = []string{TUICatalogName}

// MergedUICatalogs returns the Go-runtime-merged UI subcatalogs, for the
// extractor (which writes their references and validates directives against it).
func MergedUICatalogs() []string { return append([]string(nil), mergedUICatalogs...) }

// mergeUISubdirs folds each Go-merged UI subcatalog (embedded default, then the
// operator overlay) into c, alongside the root UI catalog. English-as-key, so
// the lookup is one flat map; a string shared by two surfaces is duplicated in
// their reference files with the same translation and merges idempotently.
func (c *catalog) mergeUISubdirs(home string) error {
	for _, sub := range mergedUICatalogs {
		if data, err := localesFS.ReadFile("locales/" + sub + "/" + c.langName + ".json"); err == nil {
			if err := c.merge(data); err != nil {
				return fmt.Errorf("i18n: parse embedded %s %s: %w", sub, c.langName, err)
			}
		}
		if home != "" {
			p := filepath.Join(home, "locales", sub, c.langName+".json")
			if data, err := os.ReadFile(p); err == nil {
				if err := c.merge(data); err != nil {
					return fmt.Errorf("i18n: parse %s: %w", p, err)
				}
			}
		}
	}
	return nil
}
