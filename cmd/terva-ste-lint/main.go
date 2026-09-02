// Command terva-ste-lint holds terva's model-visible tool text to Simplified
// Technical English.
//
// It reads the text the model actually receives — every tool's Description()
// return and every "description" field in every tool schema — and reports the
// writing that a literal reader has to work to parse: sentences over the length
// cap, passive voice, participles, contractions, typographic emphasis, asides
// smuggled in behind an em-dash or a semicolon, and terms we have spelled more
// than one way.
//
// Usage:
//
//	terva-ste-lint                # report findings, exit 0
//	terva-ste-lint -check         # report findings, exit 1 if any (the CI gate)
//	terva-ste-lint -rule term     # one rule only
//	terva-ste-lint -list          # show the enrolled text, check nothing
//
// Scope is per directory and lives in scope.go, so a tool added to an enrolled
// package is covered without anyone remembering to enrol it.
//
// The approved-word check needs an external dictionary that we cannot ship;
// see dictionary.go. Its absence is normal and is never an error.
//
// It is a lint, not part of the shipped binary.
package main

import (
	"flag"
	"fmt"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	var (
		root      = flag.String("root", ".", "repository root")
		checkErr  = flag.Bool("check", false, "exit 1 on a finding the baseline does not already hold")
		only      = flag.String("rule", "", "report one rule only")
		list      = flag.Bool("list", false, "print the enrolled tool text and exit")
		quiet     = flag.Bool("q", false, "summary only")
		noBase    = flag.Bool("no-baseline", false, "ignore the baseline: report every finding")
		writeBase = flag.Bool("write-baseline", false, "record today's findings as the accepted baseline")
	)
	flag.Parse()

	texts, err := collect(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "terva-ste-lint:", err)
		os.Exit(2)
	}
	if *list {
		for _, t := range texts {
			fmt.Printf("%s:%d %s\n%s\n\n", t.File, t.Line, t.What, t.Body)
		}
		fmt.Fprintf(os.Stderr, "%d pieces of tool text in %d enrolled directories\n", len(texts), len(enrolledDirs))
		return
	}

	dict, haveDict := loadDictionary(*root)

	var all []Finding
	for _, t := range texts {
		all = append(all, check(t)...)
		if haveDict {
			all = append(all, checkVocabulary(t, dict)...)
		}
	}
	if *only != "" {
		var kept []Finding
		for _, f := range all {
			if f.Rule == *only {
				kept = append(kept, f)
			}
		}
		all = kept
	}

	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Text.File != all[j].Text.File {
			return all[i].Text.File < all[j].Text.File
		}
		return all[i].Text.Line < all[j].Text.Line
	})

	if *writeBase {
		// A narrowed run sees one rule's findings, and writeBaseline records
		// exactly what it is given: -write-baseline -rule X would drop every
		// other rule's accepted entry on the floor. That satisfies "the file
		// can only shrink" in the one way the invariant did not mean.
		if *only != "" {
			fmt.Fprintf(os.Stderr, "terva-ste-lint: -write-baseline cannot be narrowed with -rule %s — "+
				"it would drop every other rule's entries from %s\n", *only, baselinePath)
			os.Exit(2)
		}
		if err := writeBaseline(*root, all); err != nil {
			fmt.Fprintln(os.Stderr, "terva-ste-lint:", err)
			os.Exit(2)
		}
		fmt.Fprintf(os.Stderr, "terva-ste-lint: wrote %s with %d finding(s)\n", baselinePath, len(all))
		return
	}

	total := len(all)
	var stale, unjudged []string
	if !*noBase {
		if base, ok := loadBaseline(*root); ok {
			all, stale, unjudged = applyBaseline(all, base, judgedRules(*only, haveDict))
		}
	}

	byRule := map[string]int{}
	for _, f := range all {
		byRule[f.Rule]++
		if !*quiet {
			fmt.Println(f)
		}
	}

	if !*quiet && len(all) > 0 {
		fmt.Println()
	}
	if total != len(all) {
		fmt.Fprintf(os.Stderr, "terva-ste-lint: %d new finding(s) over %d pieces of tool text (%d held by the baseline)\n",
			len(all), len(texts), total-len(all))
	} else {
		fmt.Fprintf(os.Stderr, "terva-ste-lint: %d finding(s) over %d pieces of tool text\n", len(all), len(texts))
	}
	if len(byRule) > 0 {
		rules := make([]string, 0, len(byRule))
		for r := range byRule {
			rules = append(rules, r)
		}
		sort.Slice(rules, func(i, j int) bool { return byRule[rules[i]] > byRule[rules[j]] })
		for _, r := range rules {
			fmt.Fprintf(os.Stderr, "  %-18s %d\n", r, byRule[r])
		}
	}
	if !haveDict {
		fmt.Fprintf(os.Stderr, "  (vocabulary rule skipped: no %s — see cmd/terva-ste-lint/dictionary.go)\n", dictionaryPath)
	}
	reportUnjudged(unjudged)
	reportStale(stale)
	if *checkErr && (len(all) > 0 || len(stale) > 0) {
		os.Exit(1)
	}
}

// judgedRules reports, for this invocation, whether a baseline entry for a
// given rule can be judged at all.
//
// A rule that did not run produces no findings. An entry for it therefore does
// not fire — but that is not evidence the text improved, because nothing looked
// at the text. Calling it stale is a guess, and a guess that fails the gate.
//
// Two things take a rule out of the run, and both have already been paid for:
//
//   - vocabulary needs .ste/approved-words.txt, which dictionary.go keeps
//     OUT of the repository on purpose, so every CI runner and every fresh
//     clone is without it. A baseline regenerated on the one machine that has
//     the dictionary carries vocabulary entries that can never fire anywhere
//     else, and the gate reds permanently — telling the reader to "shrink the
//     baseline" for text that is fine. That was live from the moment the gate
//     started running in CI.
//   - -rule X narrows the run to one rule, so every OTHER rule's entries went
//     stale at once. `terva-ste-lint -rule term -check` could not pass.
//
// This is a predicate, not a list of the rules that are active. A new
// unconditional rule needs no change here; a new CONDITIONAL one has to state
// its condition in the single place that decides whether it can be judged,
// which is the decision worth making by hand.
func judgedRules(only string, haveDict bool) func(rule string) bool {
	return func(rule string) bool {
		if only != "" && rule != only {
			return false
		}
		if rule == vocabularyRule && !haveDict {
			return false
		}
		return true
	}
}

// collect walks the enrolled directories and returns every piece of tool text,
// in a stable order.
func collect(root string) ([]Text, error) {
	fset := token.NewFileSet()
	var out []Text
	for _, dir := range enrolledDirs {
		abs := filepath.Join(root, filepath.FromSlash(dir))
		entries, err := os.ReadDir(abs)
		if err != nil {
			return nil, fmt.Errorf("enrolled directory %s: %w", dir, err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
				continue
			}
			rel := dir + "/" + e.Name()
			if !enrolled(rel) {
				continue
			}
			src, err := os.ReadFile(filepath.Join(abs, e.Name()))
			if err != nil {
				return nil, err
			}
			ts, err := extractFile(fset, rel, string(src))
			if err != nil {
				// A file that does not parse is the compiler's problem, not
				// ours; skipping keeps the lint usable mid-edit.
				continue
			}
			out = append(out, ts...)
		}
	}
	// Not named "skills": that identifier is the imported package this file's
	// sibling reads DefaultAlwaysOn from, and shadowing it here reads as a bug.
	descs, err := collectSkillDescriptions(root)
	if err != nil {
		return nil, err
	}
	out = append(out, descs...)
	pinned, err := collectPinnedSkillBodies(root)
	if err != nil {
		return nil, err
	}
	out = append(out, pinned...)
	agents, err := collectAgentsMD(root)
	if err != nil {
		return nil, err
	}
	out = append(out, agents...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out, nil
}
