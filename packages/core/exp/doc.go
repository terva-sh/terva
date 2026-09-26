// Package exp holds engine experiments: features a host may import and try,
// which promise nothing.
//
// Anything here can change shape or disappear in any release, including a
// patch. terva-apidiff does not count this package, so a change here never
// moves the version (docs/plans/release-process.md), and nothing here is part
// of the engine's compatibility promise (decision 0021, rule 7).
//
// # Graduation
//
// An experiment does not stay here. It ends one of two ways:
//
//   - It graduates. It moves into a stable package, and the release cut commit
//     that ships it says so in a line of its own, because that is the moment it
//     starts to promise something.
//   - It is deleted, with the ticket or the measurement that decided against it
//     named in the commit that removes it.
//
// Each experiment's documentation names the ticket that will decide it. An
// experiment with nothing deciding it is one that will stay by default, which
// is the outcome this rule exists to prevent.
//
// # What belongs here
//
// A feature whose value is still being measured, such as a compaction strategy
// under an A/B. A feature the engine needs for itself is not an experiment,
// and neither is one a host needs to build something basic: the generic
// harness in examples/harness must work from the stable packages alone, and if
// it needs exp, the stable API is incomplete.
//
// An experiment attaches to an Agent through the same public seams any host
// has: observers, policies, the gate, and the assembler. It gets no private
// access to the engine. If it cannot be built that way, a seam is missing, and
// the repair is the seam.
package exp
