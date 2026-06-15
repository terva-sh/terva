package modes

import "terva.sh/terva/packages/tui"

// welcomeBanner returns the intro text shown at the top of an empty chat.
// It uses the `terva` label color (same as the assistant) for consistency.
//
// When version is non-empty AND showVersion is true, the headline
// reads "i'm terva (vX.Y.Z). ..." so users see which build they're on
// the moment terva starts. After welcomeVersionDuration the caller
// flips showVersion off and the headline reverts to plain text.
// greeting is the rotating tagline shown after "i'm terva." — chosen once
// per session by the caller (Theme.Greeting) and held stable, so it doesn't
// reshuffle when the version suffix drops off.
func welcomeBanner(th tui.Theme, version string, showVersion bool, greeting string) []string {
	text := "i'm terva. " + greeting
	if showVersion && version != "" {
		text = "i'm terva (" + version + "). " + greeting
	}
	headline := th.AccentBar(th.Assistant) + th.FG256(th.Assistant, tui.Bold(text))
	return []string{
		headline,
		th.FG256(th.Muted, "  ask anything, or type /help to see commands."),
		"",
	}
}
