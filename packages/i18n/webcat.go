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

// StageCatalogName is the subdir/name of the Stage app's string catalog.
// Stage is its own installable PWA and the surface most likely to be handed to
// a non-operator, so it is authored as its own catalog (translate the surface
// you use first — the same reasoning as the tui/core split). The split is
// authoring-only: WebCatalog serves web ⊕ stage as one merged document, so the
// browser keeps a single lookup and the wire shape is unchanged.
const StageCatalogName = "stage"

// uiCatalogs are English-as-key singular+plural catalogs that live in their own
// subdirectory (like the keyed catalogs' file layout, but Doc-shaped contents),
// beyond the root UI catalog. `terva locale` iterates these for init/coverage/
// merge/export. TUICatalogName is ALSO merged into the Go T lookup at runtime
// (see mergedUICatalogs); WebCatalogName and StageCatalogName are served to the
// browser instead.
var uiCatalogs = []string{TUICatalogName, WebCatalogName, StageCatalogName}

// UICatalogs returns the named English-as-key catalogs beyond core/tui, for
// the command/lint layers that iterate them.
func UICatalogs() []string { return append([]string(nil), uiCatalogs...) }

// WebCatalog returns the effective browser catalog for lang — the embedded
// defaults overlaid by $TERVA_HOME/locales/{web,stage}/<lang>.json — read
// fresh, for serving to the browser. The web and stage catalogs are merged
// into one document (stage wins a key collision — its strings are the ones on
// a Stage screen). English (or an unknown lang) yields whatever is embedded
// (empty unless an English overlay ships), and the client falls back to its
// keys.
func WebCatalog(lang, home string) (Doc, error) {
	web, err := LoadMergedIn(WebCatalogName, lang, home)
	if err != nil {
		return Doc{}, err
	}
	stage, err := LoadMergedIn(StageCatalogName, lang, home)
	if err != nil {
		return Doc{}, err
	}
	mergeInto(&web, stage)
	return web, nil
}
