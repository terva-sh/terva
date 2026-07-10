package extensions

// Info is the installed-extension view: config-derived enablement plus
// ground truth from the live driver. It lives here, not in the TUI, because the
// ctrlproto server and the build layer both need it and neither may import modes.

// Info is one installed extension's display + state for the
// /extensions dialog. The host (cli) scans the extension dirs and
// resolves the state; this package only renders it and emits toggle
// actions. Two independent on/off controls:
//
//   - GlobalEnabled mirrors the manifest `enabled` field (what `terva
//     ext enable/disable` writes) — the 'g' toggle.
//   - ProjectDisabled mirrors this project's .terva/config.json
//     disable_extensions list — the 'p' toggle. Restrict-only: it can
//     disable a globally-enabled extension here, but cannot force-enable
//     a globally-disabled one.
//
// Effective is whether the extension actually loads (global-enabled AND
// not disabled by any config AND not gated by an untrusted workspace).
type Info struct {
	Name        string
	Version     string
	Language    string
	Description string
	Scope       string // "global" | "project"

	GlobalEnabled      bool
	ProjectDisabled    bool
	UserConfigDisabled bool
	ProjectGated       bool // project extension in an untrusted workspace
	Effective          bool // config says it should load

	// Running and the provide-counts are ground truth from the live
	// manager (the embedded extdriver.Driver). Running can be false while
	// Effective is true — an enabled extension that crashed on spawn.
	Running  bool
	Commands int
	Tools    int

	HasLog bool

	// LastLog is a one-line "why it's off" reason pulled from the tail of
	// the extension's log (its own stderr — usually the crash), shown in the
	// detail when the extension is off. Empty when running or no log.
	LastLog string

	// HasConfig is true when the extension declares a `config` schema in
	// its manifest — the 'c' key then opens the per-extension config form.
	HasConfig bool
}
