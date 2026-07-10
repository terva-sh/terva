package dialogs

// The modal overlays are part of the interactive terminal UI, so their
// i18n UI strings (i18n.T / M / TC / TN / Errorf) belong to the `tui`
// translation catalog (locales/tui/), exactly as the loop's do.
//
// The directive is non-recursive, so extracting this package out of
// packages/agent/modes needed a copy of it here. Without one, every
// string in these 26 dialogs silently reroutes to the core catalog:
// `just ci` catches that as a stale-catalog failure, which is how this
// file came to exist.
//
//i18n:catalog tui
