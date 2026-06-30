package modes

import "terva.sh/terva/packages/tui"

// welcomeBanner returns the intro text shown at the top of an empty chat.
//
// The headline is tinted with the persona's accent_color when it supplies a
// valid #RRGGBB, falling back to the `terva` label color (same as the
// assistant) otherwise; a persona emoji, when set, leads the line. So a
// persona's identity surfaces visually, not just by name.
//
// When showVersion is true and version is non-empty, the headline reads
// "i'm <persona> (vX.Y.Z). ..." so users see which build they're on the
// moment terva starts. After welcomeVersionDuration the caller flips
// showVersion off; the version suffix is then replaced by the persona's
// pronunciation hint when one is known ("i'm Mieli (MYEH-lee). ...").
// greeting is the rotating tagline shown after the name — chosen once per
// session by the caller (Theme.Greeting) and held stable, so it doesn't
// reshuffle when the trailing suffix changes.
func welcomeBanner(th tui.Theme, persona, phonetic, emoji, accentHex, version string, showVersion bool, greeting string) []string {
	// The transient version suffix and the pronunciation hint both ride in
	// parentheses after the name, so they take turns: version while it's
	// shown at startup, then the phonetic once it drops.
	label := persona
	switch {
	case showVersion && version != "":
		label = persona + " (" + version + ")"
	case phonetic != "":
		label = persona + " (" + phonetic + ")"
	}
	text := "i'm " + label + ". " + greeting
	if emoji != "" {
		text = emoji + "  " + text
	}
	// A persona accent_color tints the bar + headline; otherwise the theme's
	// assistant colour is used, so non-persona sessions look exactly as before.
	accent := tui.Color256(th.Assistant)
	if c, ok := tui.ParseHexColor(accentHex); ok {
		accent = c
	}
	headline := th.FGColor(accent, "▌ ") + th.FGColor(accent, tui.Bold(text))
	return []string{
		headline,
		th.FG256(th.Muted, "  ask anything, or type /help to see commands."),
		"",
	}
}
