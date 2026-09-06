package dialogs

// Rune filters for the numeric form rows. A dialog hands one of these to a
// tui.LineBuf as its Accept, so the row rejects what it cannot hold at the
// keystroke rather than at the validator. Both are also the reason a rejected
// rune has to be swallowed: 'r' typed into a context-window row must not reach
// the model editor's reset shortcut.
//
// Neither filter is validation. A row can still hold "1.2.3" or a number the
// registry rejects, and the registry is what decides on commit.

func acceptDigits(r rune) bool { return r >= '0' && r <= '9' }

func acceptDecimal(r rune) bool { return acceptDigits(r) || r == '.' }
