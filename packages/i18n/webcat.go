package i18n

// The web control panel's UI strings are their own English-as-key catalog,
// distinct from the root UI catalog and from the dotted-key prompts/help
// catalogs. It is authored through the same `terva locale` flow (init / fill /
// merge / export) but is never resolved in-process: the daemon serves the
// effective (embedded ⊕ $TERVA_HOME overlay) catalog to the browser, which
// resolves it client-side. So there is no T-style wrapper here and no entry in
// the runtime `catalog` struct — only on-demand loading (LoadMergedIn) and the
// authoring/lint surface (ReferenceDocIn / EmbeddedLocaleNamesIn on the "web"
// subdir). See docs/proposals/web-i18n-authoring.md.

// WebCatalogName is the subdir/name of the web control-panel string catalog.
const WebCatalogName = "web"

// uiCatalogs are English-as-key singular+plural catalogs that live in their own
// subdirectory (like the keyed catalogs' file layout, but Doc-shaped contents),
// beyond the root UI catalog. `terva locale` iterates these for init/coverage/
// merge/export.
var uiCatalogs = []string{WebCatalogName}

// UICatalogs returns the named English-as-key catalogs (currently just "web"),
// for the command/lint layers that iterate them.
func UICatalogs() []string { return append([]string(nil), uiCatalogs...) }

// WebCatalog returns the effective web catalog for lang — the embedded default
// overlaid by $TERVA_HOME/locales/web/<lang>.json — read fresh, for serving to
// the browser. English (or an unknown lang) yields whatever is embedded (empty
// unless an English overlay ships), and the client falls back to its keys.
func WebCatalog(lang, home string) (Doc, error) {
	return LoadMergedIn(WebCatalogName, lang, home)
}
