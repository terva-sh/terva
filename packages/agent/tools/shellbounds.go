package tools

// MaxShellLine and maxShellMarks bound a command line that terva gives to a
// shell parser.
//
// 🚨 The parser calls itself for each group inside a group, and a walk of its
// tree does for each stage of a pipe and each command of a chain. A stack
// past Go's limit of 1 GiB ends the process, and no recover stops that.
//
// A measurement of 37 shapes of a line on 2026-11-19 (go 1.27, amd64) gave
// the cost of one level: about 4 KiB for a subshell, 7 KiB for an index
// inside an index, and under 2 KiB for a sign in a sum. A line of 1 MiB
// ended the process in ten of those shapes. No shape did at 256 KiB.
//
// The count of the marks also bounds the time. The parser took 9.4 seconds
// for an index inside an index 65,000 deep, and that time grows with the
// square of the depth. At 16,384 it is near half a second.
//
// 🔑 Two bounds, because one mark is one level at most. The length bounds
// the levels that need no mark, such as a=a=a=1 in a sum, and those are the
// cheap ones. The count of the marks bounds the expensive ones. The count is
// one sum of every kind, because a line can put each kind inside the next,
// and then the levels of each kind add up.
//
// 128 KiB is also the longest single argument that Linux gives to a program,
// and the shell gets a command as one argument. So no longer command ran
// there before this bound.
const (
	MaxShellLine  = 128 << 10
	maxShellMarks = 16384
)

// ShellTooLong is what terva says of a command line past those bounds.
const ShellTooLong = "terva cannot read a command this long or this deep. Write a file with the write tool, and run a short command"

// ShellPastBounds reports whether a command line is past the bounds. A caller
// asks before it gives the line to a parser.
//
// It counts each mark that can nest, in quotes and out of them. So it also
// stops some lines that a parser can read, and no line that ends the process
// gets past it.
func ShellPastBounds(text string) bool {
	if len(text) > MaxShellLine {
		return true
	}
	marks, signs := 0, 0
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case '(', '{', '[', '|', '&', '!', '~':
			marks++
		case '+', '-':
			// A run of signs in a sum is as deep as it is long:
			// $((-+-+-+1)).
			signs++
		case ' ', '\t', '\n', '\r', '\\':
			// 🚨 Space of each kind stands between two signs of one run,
			// and so does the end of a line behind a backslash.
		default:
			signs = 0
		}
		if marks > maxShellMarks || signs > maxShellMarks {
			return true
		}
	}
	return false
}
