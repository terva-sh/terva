// Command terva-boundary-guard fails when a pull request grows an engine
// boundary baseline.
//
// packages/core keeps two baselines that are meant to only shrink: the import
// baseline and the I/O baseline. TestImportBoundary and TestIOBoundary hold the
// tree to them, but a change can add an import or an I/O call together with a
// matching baseline line, and both tests pass. A test cannot close that from
// inside go test: any limit kept in the tree can be edited in the same change,
// and the public release tree has no history to compare with. This command
// compares each baseline with its own version at the merge base, and fails on
// growth (TKT-01M368AWKN).
//
// What counts as growth is in guard.go. In short: an io import that is new or
// became io, or an I/O reference count that rose. A removal passes, and so does
// an edited reason and a pure or wire import. A pure package that moves is a
// removal and a pure import, so it passes. An io package or an I/O call that
// moves reads as growth and needs the trailer, and so does a baseline file that
// is deleted or moved.
//
// One trailer allows all the growth the branch carries. The guard prints every
// growth line beside the reasons, so a reviewer reads both together. Naming
// each entry in its trailer would copy the baseline line into the commit
// message.
//
// To grow a baseline on purpose, add a trailer to a commit on the branch:
//
//	Boundary-Grows: <why this I/O belongs in the engine>
//
// The guard then prints the growth and the reason, and passes. A trailer is
// in the history beside the change, so a reviewer reads the reason in the same
// place as the diff, and it outlives the pull request. A forge label was the
// other choice. It leaves no trace in git, and CI would have to call the
// forge's API to read it.
//
// Usage:
//
//	terva-boundary-guard -base <ref>
//
// The ref is the branch the change will merge into. The guard compares the
// working tree with the merge base of that ref and HEAD.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const trailerKey = "Boundary-Grows"

func main() {
	base := flag.String("base", "", "the ref the change merges into (required)")
	flag.Parse()
	if *base == "" {
		fmt.Fprintln(os.Stderr, "terva-boundary-guard: -base is required")
		os.Exit(2)
	}
	ok, err := run(".", *base, os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "terva-boundary-guard:", err)
		os.Exit(2)
	}
	if !ok {
		os.Exit(1)
	}
}

// run reports whether the baselines in the repository at dir may stand. An
// error means the guard could not compare, which is not the same as a clean
// result.
func run(dir, base string, out io.Writer) (bool, error) {
	git := func(args ...string) (string, error) { return gitIn(dir, args...) }
	mb, err := git("merge-base", base, "HEAD")
	if err != nil {
		return false, fmt.Errorf("no merge base between %s and HEAD: %w", base, err)
	}
	mb = strings.TrimSpace(mb)
	var grew []string
	for _, b := range baselines {
		// A baseline that did not exist at the base is new, and every line of
		// it is growth. Any other failure to read it is an error.
		old, existed := "", false
		if _, err := git("cat-file", "-e", mb+":"+b.path); err == nil {
			existed = true
			if old, err = git("show", mb+":"+b.path); err != nil {
				return false, err
			}
		}
		// A baseline deleted on the branch counts as growth. It may have
		// moved, with its test reading the new path, and the guard would not
		// see the lines it gained there. Retiring one on purpose carries the
		// trailer like any other growth.
		cur, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(b.path)))
		if errors.Is(err, fs.ErrNotExist) {
			if existed {
				grew = append(grew, b.path+": the file was deleted or moved")
			}
			continue
		} else if err != nil {
			return false, err
		}
		g, err := b.growth(old, string(cur))
		if err != nil {
			return false, fmt.Errorf("%s: %w", b.path, err)
		}
		for _, line := range g {
			grew = append(grew, b.path+": "+line)
		}
	}
	short := mb
	if len(short) > 12 {
		short = short[:12]
	}
	if len(grew) == 0 {
		fmt.Fprintf(out, "no engine boundary baseline grew since the merge base %s\n", short)
		return true, nil
	}
	log, err := git("log", "--format=%(trailers:key="+trailerKey+",valueonly)", mb+"..HEAD")
	if err != nil {
		return false, err
	}
	var reasons []string
	for _, line := range strings.Split(log, "\n") {
		if r := strings.TrimSpace(line); r != "" {
			reasons = append(reasons, r)
		}
	}
	fmt.Fprintf(out, "an engine boundary baseline grew since the merge base %s:\n", short)
	for _, g := range grew {
		fmt.Fprintln(out, "  "+g)
	}
	if len(reasons) > 0 {
		fmt.Fprintf(out, "allowed by a %s trailer on the branch:\n", trailerKey)
		for _, r := range reasons {
			fmt.Fprintln(out, "  "+r)
		}
		return true, nil
	}
	fmt.Fprintf(out, `The baselines only shrink (decision 0021, rule 1). Take the new I/O through
an interface the host implements. If it belongs in the engine anyway, add a
trailer to a commit on this branch that says why:

  %s: <reason>
`, trailerKey)
	return false, nil
}

func gitIn(dir string, args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return "", err
		}
		return "", errors.New(msg)
	}
	return stdout.String(), nil
}
