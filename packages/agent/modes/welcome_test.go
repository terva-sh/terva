package modes

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/tui"
)

func TestWelcomeBannerPersonaTheming(t *testing.T) {
	th := tui.Theme{Assistant: 117, Muted: 244}

	// With a persona emoji + accent_color, the headline leads with the emoji
	// and is tinted with a truecolor (38;2) escape from the hex.
	lines := welcomeBanner(th, "Data", "DAY-tuh", "🖖", "#c8a25a", "v1.0.0", false, "let's go")
	head := lines[0]
	if !strings.Contains(head, "🖖") {
		t.Errorf("headline should lead with the persona emoji: %q", head)
	}
	if !strings.Contains(head, "Data") {
		t.Errorf("headline should name the persona: %q", head)
	}
	if !strings.Contains(head, "\x1b[38;2;200;162;90m") {
		t.Errorf("headline should be tinted with the accent truecolor escape: %q", head)
	}

	// Without persona display metadata, the banner is unchanged from before:
	// no emoji, no truecolor — the theme's 256-color assistant slot is used.
	plain := welcomeBanner(th, "Mieli", "MYEH-lee", "", "", "", false, "let's go")[0]
	if strings.Contains(plain, "\x1b[38;2;") {
		t.Errorf("no accent ⇒ no truecolor escape: %q", plain)
	}
	if !strings.Contains(plain, "\x1b[38;5;117m") {
		t.Errorf("no accent ⇒ falls back to the assistant 256-color slot: %q", plain)
	}
	if !strings.Contains(plain, "Mieli (MYEH-lee)") {
		t.Errorf("phonetic shown when version suffix is off: %q", plain)
	}

	// A malformed accent falls back gracefully (no truecolor, no crash).
	bad := welcomeBanner(th, "X", "", "🌒", "not-a-hex", "", false, "hi")[0]
	if strings.Contains(bad, "\x1b[38;2;") {
		t.Errorf("invalid accent should fall back, not emit truecolor: %q", bad)
	}
	if !strings.Contains(bad, "🌒") {
		t.Errorf("emoji still shown with a bad accent: %q", bad)
	}
}
