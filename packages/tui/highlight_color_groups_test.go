package tui

import "testing"

func TestStripANSIExtendedColorGroups(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"\x1b[31mred\x1b[0m", "\x1b[31mred\x1b[0m"},
		{"\x1b[38;5;45mindexed", "\x1b[38;5;45mindexed"},
		{"\x1b[38;2;40;101;107mrgb", "\x1b[38;2;40;101;107mrgb"},
		{"\x1b[1;48;5;45;38;5;101mtext", "\x1b[1;38;5;101mtext"},
		{"\x1b[48;2;40;101;107;32mtext", "\x1b[32mtext"},
		{"\x1b[40;107;49mtext", "text"},
		{"\x1b[38;5mtext", "text"},
		{"\x1b[38;2;40;101mtext", "text"},
		{"\x1b[48;2;40mtext", "text"},
		{"\x1b[1;38;5mtext", "\x1b[1mtext"},
		{"\x1b[38;9;5mtext", "text"},
		{"\x1b[38;5;999mtext", "text"},
	} {
		if got := stripANSIBackgrounds(tc.in); got != tc.want {
			t.Errorf("strip(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
