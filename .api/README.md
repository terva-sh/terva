# The engine's API record

This directory records the exported API of terva's engine (`packages/core`),
its wire (`packages/provider`) and the SDKs hosts build on (`packages/agent/sdk`,
`ext` and `connsdk`), one file per package. It measures the API, and the two
classes below say what each part promises. Nothing here blocks a break before
1.0. The diff under `.api/` shows it at review.

- `packages.txt` lists each package and marks it `stable` or `unstable`.
- `packages/…/<name>.txt` is a package's snapshot: every exported symbol, its
  kind, and its signature, one per line.

## The two classes

- **stable**: we intend a host to build on it. Before 1.0 it can still
  break, but only in a minor release, and `docs/migrating.md` carries a
  migration note for each break. The break is counted, and the snapshot diff
  shows it in review. Raising the `go` line of `go.mod` counts as a stable
  break.
- **unstable**: a host can import it, and it may change in any release
  without a note.

`packages/core/exp` is not listed. An experiment promises nothing.

A listed package claims the tree below it. `just api-check` fails when a
package under a listed one is missing from `packages.txt`, so a new package
cannot join the API unmeasured. `exp`, `internal` and `testdata` directories
are exempt.

## The Unstable: marker

A stable package can hold a symbol that carries no promise. Its doc comment
says so in a paragraph of its own that opens with `Unstable:`, the way Go's
`Deprecated:` marks a symbol for removal:

```go
// NewDeepSeek returns a client for DeepSeek's API.
//
// Unstable: a per-vendor constructor carries no promise before 1.0.
func NewDeepSeek(...)
```

The marker is the one place the exception is written, and a host reads it
where it reads the rest of the doc, in godoc and in the editor. A marker on a
type covers its fields and its methods. A marker on a `const (...)` or
`var (...)` group covers every name in the group. A `type (...)` group's doc
covers a type only when the group holds that one type, because godoc shows
each type of a group with its own doc.

The snapshot writes `unstable` after the kind (`func unstable`), so a new
marker shows in the diff under `.api/` at review.

A stable symbol promises every type it names. `just api-check` therefore
refuses a stable symbol whose signature names an unstable one: a symbol
marked `Unstable:`, or any symbol of a package listed `unstable` or not
listed at all. It follows names inside this module only. A host cannot
name an unexported type, but it can use the exported fields and methods of
one that a stable symbol returns, holds or embeds, so the rule follows
those members too. Mark the stable symbol too, or make what it names
stable.

## The commands

- `just api-snapshot` rewrites every snapshot from the code. Run it when you
  change an exported API on purpose, and commit the result with the change.
- `just api-check` fails when a snapshot differs from the code. It runs in
  `just lint` and in CI. It does not judge compatibility. It makes sure that
  every API change arrives as a diff here.
- `just api-since [REF]` counts removals, changes and additions per class
  since a git ref, the last release by default. It only reports. Each symbol
  counts in its own class: a symbol marked `Unstable:` counts as unstable,
  even in a stable package. It also reports how many symbols the stable
  packages mark now and at the ref.
- `just migration-notes [REF]` compares the same breaks with the notes under
  "Unreleased" in `docs/migrating.md`, and in any section sealed for a release
  that has not published. It lists each stable break no note
  covers, and each name in a note that covers no break of its class. That
  page's "Writing a note" section gives the format. It fails on either
  finding. Both CI lanes run it on every pull request, and so do `just ci`
  and `just ci-docs`.
- `just migration-notes-seal VERSION` moves the Unreleased notes under a
  `## VERSION` heading before that release is cut, with the per-class count on
  top. It refuses while a stable break has no note, and the cut refuses notes
  left unsealed. `docs/plans/release-process.md` gives the whole flow.

The census reads one package directory at a time, so a subpackage never shares
a namespace with its parent.

A var or const records its declared type, never its value. The census reads
syntax and does not type-check, so it sees a type only where the source spells
one out: a declared type, a composite literal, a func literal, or a sentinel
from `errors.New` or `fmt.Errorf`. `just api-check` refuses an exported var
whose type it cannot see, such as `var X = f()`. Write `var X T = f()`
instead. An untyped const records its default kind where the syntax shows it
(`untyped int` for `3`, `iota` or `1 << iota`, `untyped string` for a string
literal), because a change of kind breaks a caller. A const whose value names
another constant, or multiplies by one such as `time.Second`, records presence
only: the check does not ask for a type there, because a declared type changes
what a caller can do with the constant.

A generic type records its type parameters and their constraints, so a
tighter constraint shows as a change.

`just api-since` reads the manifest at the ref as well, when the ref has one.
A package deleted since then counts as removed, in the class it had there. A
symbol's break counts in the class it had at the ref, too, so a marker added
since then does not hide a stable break.

`just release-api-diff` is the older release census. It reads no manifest
and no marker, and it counts every change alike.
