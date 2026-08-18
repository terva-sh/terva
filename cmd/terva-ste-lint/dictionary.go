package main

import (
	"bufio"
	"os"
	"regexp"
	"strings"
)

// The approved-word check is the one part of Simplified Technical English we
// cannot ship.
//
// ASD-STE100 is free to download from asd-ste100.org, but the copyright is
// held in full by ASD and no redistribution grant is published. The ~900-word
// dictionary is the part of the specification with real authorial selection in
// it, so vendoring it into a repository that push-mirrors to a public remote is
// not a call this lint gets to make on its own.
//
// So the word list is an EXTERNAL artifact, brought in by hand:
//
//	.ste/approved-words.txt     one lower-case word per line, # comments
//
// That path is in .gitignore and in the release cut's EXCLUDES, belt and
// braces. When the file is absent — which is the default, and the state every
// fresh clone and every CI runner is in — the vocabulary rule is skipped and
// every other rule still runs. A missing dictionary must never be an error:
// the rules carry the weight, and a lint that fails on a clean checkout is a
// lint someone deletes.
const dictionaryPath = ".ste/approved-words.txt"

// vocabularyRule names the one rule whose findings depend on an artifact the
// repository does not carry. judgedRules keys on it, so the name lives here
// beside the condition rather than as a literal in two files that can drift.
const vocabularyRule = "vocabulary"

// technicalWords are always in vocabulary regardless of the dictionary: our own
// nouns and the names of things the model operates. STE calls these Technical
// Names and Technical Verbs and expects each project to keep its own list —
// this is ours, and it ships, because we wrote it.
var technicalWords = map[string]bool{
	"terva": true, "raati": true, "swarm": true, "persona": true, "panelist": true,
	"transcript": true, "session": true, "sessions": true, "provider": true,
	"worktree": true, "worktrees": true, "workspace": true, "sandbox": true,
	"tool": true, "tools": true, "schema": true, "json": true, "glob": true,
	"regex": true, "grep": true, "bash": true, "git": true, "commit": true,
	"branch": true, "repository": true, "cache": true, "token": true, "tokens": true,
	"prompt": true, "model": true, "extension": true, "extensions": true,
	"connector": true, "skill": true, "skills": true, "lore": true, "audience": true,
	"ballot": true, "ballots": true, "verdict": true, "deliberation": true,
	"compaction": true, "umask": true, "stdout": true, "stderr": true,
	"timeout": true, "offset": true, "cursor": true, "archived": true,
	"backend": true, "credential": true, "credentials": true, "binary": true,
	"daemon": true, "supervisor": true, "operator": true, "slug": true,
}

var dictWordRe = regexp.MustCompile(`[a-zA-Z][a-zA-Z-]*`)

// loadDictionary reads the external word list. The bool reports whether a
// dictionary was found at all; callers skip the vocabulary rule when it is
// false rather than treating the absence as a failure.
func loadDictionary(root string) (map[string]bool, bool) {
	f, err := os.Open(root + "/" + dictionaryPath)
	if err != nil {
		return nil, false
	}
	defer f.Close()

	words := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Tolerate "word (part of speech)" and "word: gloss" shapes, so a list
		// transcribed by hand from the specification does not have to be
		// stripped down first.
		if i := strings.IndexAny(line, " \t:("); i > 0 {
			line = line[:i]
		}
		words[strings.ToLower(line)] = true
	}
	return words, len(words) > 0
}

// checkVocabulary flags words outside the approved list. It runs only when a
// dictionary is present.
func checkVocabulary(t Text, dict map[string]bool) []Finding {
	var fs []Finding
	seen := map[string]bool{}
	for _, para := range strings.Split(t.Body, "\n\n") {
		prose := stripCode(para)
		for _, w := range dictWordRe.FindAllString(prose, -1) {
			lw := strings.ToLower(w)
			if len(lw) < 3 || seen[lw] || dict[lw] || technicalWords[lw] || allowedINGForms[lw] {
				continue
			}
			seen[lw] = true
			fs = append(fs, Finding{
				Text: t, Rule: vocabularyRule,
				Msg:   "not in the approved word list, and not a technical name we declare",
				Quote: w,
			})
		}
	}
	return fs
}
