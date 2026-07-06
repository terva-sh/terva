package modes

// This directory renders the interactive terminal UI, so its i18n UI strings
// (i18n.T / M / TC / TN / Errorf) belong to the `tui` translation catalog
// (locales/tui/), not core — letting the terminal surface be translated
// independently of the CLI/engine. The terva-i18n-lint extractor reads the
// directive below and routes every UI string in THIS directory to the tui
// catalog. It is non-recursive: the connector sub-packages (modes/discord,
// modes/telegram) are separate directories and stay in core.
//
//i18n:catalog tui
