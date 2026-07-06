package tui

// The tui package is the terminal rendering layer, so its i18n UI strings
// (i18n.T / M / TC / TN / Errorf) belong to the `tui` translation catalog
// (locales/tui/), not core. The terva-i18n-lint extractor reads the directive
// below and routes every UI string in this directory to the tui catalog. See
// packages/agent/modes/i18n_catalog.go for the companion directive on the
// interactive-mode package.
//
//i18n:catalog tui
