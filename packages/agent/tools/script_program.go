//go:build terva_scripting

package tools

// scriptProgram wraps a model-supplied script in a function body before
// anything parses it.
//
// A bare script is a Program, and `return` is illegal at the top of one. The
// model writes the natural early exit anyway:
//
//	if (!m) { print('NO_SCRIPT'); return; }
//
// and gets `SyntaxError: Illegal return statement`. In the session behind this
// change that happened twice, and both times the model recovered by wrapping
// its own program in an IIFE on the next turn. Two turns each, to rediscover a
// shape the tool could have provided. The description documented the bindings
// and the limits but never the evaluation shape, so there was nothing to read
// that would have prevented it.
//
// Inside a function body `return` is an ordinary early exit, a leading
// "use strict" becomes a proper directive prologue, and top-level declarations
// stop colliding with anything the runtime already defines.
//
// Two details in the exact string, both load-bearing:
//
//   - The prefix stays ON line 1, joined to the script's own first line rather
//     than followed by a newline. A syntax error therefore still reports the
//     line the model wrote. Prepending "\n" would shift every diagnostic by
//     one and trade a parse failure for a worse one.
//   - The suffix STARTS with a newline, so a script whose last line ends in a
//     `//` comment cannot swallow the closing `})()`.
//
// Nothing here changes what the script returns to the caller. Output is
// whatever print(...) wrote, never the completion value, so wrapping is
// invisible to the result path.
//
// Apply this at every site that parses or runs the script. The binding
// pre-check and the engine must see the SAME source, or the account shown to
// the approval prompt describes a program that is not the one that runs.
func scriptProgram(src string) string {
	return "(function(){" + src + "\n})()"
}
