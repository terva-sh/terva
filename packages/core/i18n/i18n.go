// Package i18n is how the engine gets translated text. It holds no catalog
// and reads nothing. A host installs a Translator with Use, and until one
// does, every call returns its English source.
//
// terva's own catalogs live in terva.sh/terva/packages/i18n, which loads
// them from the embedded locales and from $TERVA_HOME, and which installs
// itself here from its init. A program that links that package therefore
// translates the engine's text exactly as before, and a host that never
// links it gets English.
//
// An engine can also carry a translator of its own (core.WithTranslator), so
// two engines in one process can speak different languages. Its text goes
// through In: i18n.In(tr).T(source) renders with tr, and with the process-wide
// translator when tr is nil.
//
// 🔑 The package is named i18n, and the engine calls i18n.T, i18n.P and
// i18n.In(tr).T, on purpose. cmd/terva-i18n-lint finds translatable strings by
// those selector calls, so a translator reached any other way (a field on
// Agent called directly, a function value, an In result kept in a variable)
// would drop the engine's strings from the reference catalogs. The lint
// refuses an In call it cannot read through. The decision and the options
// that lost are in docs/plans/engine-extraction.md, under phase 1.
package i18n

import (
	"errors"
	"fmt"
	"sync/atomic"
)

// Translator renders the engine's user-facing and model-facing text.
//
// T translates an English source string, which is also its key. P renders a
// model-facing prompt keyed by a stable dotted id, falling back to english.
// Both apply fmt.Sprintf with args only when args are present, so text with
// a bare '%' and no args comes back untouched.
type Translator interface {
	T(source string, args ...any) string
	P(key, english string, args ...any) string
}

// box lets an atomic.Pointer carry an interface value.
type box struct{ tr Translator }

var active atomic.Pointer[box]

// Use installs tr as the engine's translator. Use(nil) restores English.
// It is safe to call at any time, and every later call to T, P, or Errorf
// sees tr.
func Use(tr Translator) {
	if tr == nil {
		active.Store(nil)
		return
	}
	active.Store(&box{tr: tr})
}

// T returns the translation of source, or source itself (formatted with
// args when there are any) when no translator is installed.
func T(source string, args ...any) string {
	if b := active.Load(); b != nil {
		return b.tr.T(source, args...)
	}
	return format(source, args)
}

// P returns the rendering of the model-facing prompt key, or english
// (formatted with args when there are any) when no translator is installed.
func P(key, english string, args ...any) string {
	if b := active.Load(); b != nil {
		return b.tr.P(key, english, args...)
	}
	return format(english, args)
}

// Errorf returns an error whose message is T(source, args...). Like
// packages/i18n's Errorf, it does not support %w.
func Errorf(source string, args ...any) error {
	return errors.New(T(source, args...))
}

func format(s string, args []any) string {
	if len(args) > 0 {
		return fmt.Sprintf(s, args...)
	}
	return s
}

// Text renders through one translator. In builds it; the zero Text renders
// through the process-wide translator, as T and P do.
type Text struct{ tr Translator }

// In returns the text functions of tr, or of the process-wide translator when
// tr is nil. Call a method on the result directly, i18n.In(tr).T("..."):
// cmd/terva-i18n-lint reads only that shape, and refuses an In result kept
// for later.
func In(tr Translator) Text { return Text{tr: tr} }

// T is the package T, rendered through x's translator.
func (x Text) T(source string, args ...any) string {
	if x.tr == nil {
		return T(source, args...)
	}
	return x.tr.T(source, args...)
}

// P is the package P, rendered through x's translator.
func (x Text) P(key, english string, args ...any) string {
	if x.tr == nil {
		return P(key, english, args...)
	}
	return x.tr.P(key, english, args...)
}

// Errorf is the package Errorf, rendered through x's translator. Like it, it
// does not support %w.
func (x Text) Errorf(source string, args ...any) error {
	return errors.New(x.T(source, args...))
}
