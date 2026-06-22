package modes

import "terva.sh/terva/packages/tui"

// welcomeBanner returns the intro text shown at the top of an empty chat.
// It uses the `terva` label color (same as the assistant) for consistency.
//
// When showVersion is true and version is non-empty, the headline reads
// "i'm <persona> (vX.Y.Z). ..." so users see which build they're on the
// moment terva starts. After welcomeVersionDuration the caller flips
// showVersion off; the version suffix is then replaced by the persona's
// pronunciation hint when one is known ("i'm Mieli (MYEH-lee). ...").
// greeting is the rotating tagline shown after the name — chosen once per
// session by the caller (Theme.Greeting) and held stable, so it doesn't
// reshuffle when the trailing suffix changes.
func welcomeBanner(th tui.Theme, persona, phonetic, version string, showVersion bool, greeting string) []string {
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
	headline := th.AccentBar(th.Assistant) + th.FG256(th.Assistant, tui.Bold(text))
	return []string{
		headline,
		th.FG256(th.Muted, "  ask anything, or type /help to see commands."),
		"",
	}
}
