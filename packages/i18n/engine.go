package i18n

import enginei18n "terva.sh/terva/packages/core/i18n"

// 🔑 The engine (packages/core) takes its translated text through
// packages/core/i18n, which does no I/O, rather than importing this package,
// which reads catalogs from disk. Installing this package there from init
// means every program that links it, which is every terva binary, renders the
// engine's text exactly as it did when the engine imported this package
// directly. Configure swaps the catalog that T and P read, so the order of
// init and Configure does not matter.
func init() { enginei18n.Use(engineTranslator{}) }

// engineTranslator adapts this package's T and P to the engine's Translator.
type engineTranslator struct{}

func (engineTranslator) T(source string, args ...any) string { return T(source, args...) }

func (engineTranslator) P(key, english string, args ...any) string {
	return P(key, english, args...)
}
