package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// A baseline is how this lint lands on a corpus that does not pass it yet.
//
// The alternative is what kills retrofitted lints: turn it on, get 36 findings
// on day one, and either nobody can commit or somebody adds a blanket ignore
// that never comes off. The baseline records exactly the findings that exist
// today, by fingerprint. A NEW finding fails the gate. An old one does not.
//
// The rule that keeps it honest: a fingerprint in the baseline that no longer
// fires is an error, not a shrug. The baseline can only shrink. Otherwise it
// rots into a permanent amnesty and the count stops meaning anything — which
// is the same failure as a silent cap on coverage.
const baselinePath = ".ste/baseline.json"

type baselineFile struct {
	Comment string   `json:"_comment"`
	Note    string   `json:"_note"`
	Count   int      `json:"count"`
	Entries []string `json:"entries"`
}

// fingerprint identifies a finding across edits that do not touch it. It is
// deliberately NOT line-based: a line number moves every time someone edits the
// file above it, and a baseline that churns on unrelated edits is noise.
func fingerprint(f Finding) string {
	norm := strings.Join(strings.Fields(f.Quote), " ")
	sum := sha256.Sum256([]byte(f.Text.File + "\x00" + f.Rule + "\x00" + norm))
	return f.Text.File + ":" + f.Rule + ":" + hex.EncodeToString(sum[:])[:12]
}

func loadBaseline(root string) (map[string]bool, bool) {
	b, err := os.ReadFile(root + "/" + baselinePath)
	if err != nil {
		return nil, false
	}
	var bf baselineFile
	if json.Unmarshal(b, &bf) != nil {
		return nil, false
	}
	set := map[string]bool{}
	for _, e := range bf.Entries {
		set[e] = true
	}
	return set, true
}

func writeBaseline(root string, findings []Finding) error {
	seen := map[string]bool{}
	var entries []string
	for _, f := range findings {
		fp := fingerprint(f)
		if seen[fp] {
			continue
		}
		seen[fp] = true
		entries = append(entries, fp)
	}
	sort.Strings(entries)
	bf := baselineFile{
		Comment: "Findings terva-ste-lint accepts today. A NEW finding fails the gate; " +
			"an entry here that no longer fires ALSO fails it, so this file can only shrink. " +
			"Regenerate with: just ste-lint-baseline",
		Note: "These are not accepted policy. They are the residue of the Phase 1 conversion " +
			"— mostly sentences that carry two clauses joined by 'and' or 'because'. Burn them down.",
		Count:   len(entries),
		Entries: entries,
	}
	out, err := json.MarshalIndent(bf, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root+"/.ste", 0o755); err != nil {
		return err
	}
	return os.WriteFile(root+"/"+baselinePath, append(out, '\n'), 0o644)
}

// applyBaseline splits findings into the ones that are new (which fail) and
// reports baseline entries that no longer fire (which also fail, so the file
// cannot rot).
func applyBaseline(findings []Finding, base map[string]bool) (fresh []Finding, stale []string) {
	fired := map[string]bool{}
	for _, f := range findings {
		fp := fingerprint(f)
		fired[fp] = true
		if !base[fp] {
			fresh = append(fresh, f)
		}
	}
	for fp := range base {
		if !fired[fp] {
			stale = append(stale, fp)
		}
	}
	sort.Strings(stale)
	return fresh, stale
}

func reportStale(stale []string) {
	if len(stale) == 0 {
		return
	}
	noun, verb := "entries", "fire"
	if len(stale) == 1 {
		noun, verb = "entry", "fires"
	}
	fmt.Fprintf(os.Stderr, "\nterva-ste-lint: %d baseline %s no longer %s — the text improved, so shrink the baseline:\n",
		len(stale), noun, verb)
	for _, s := range stale {
		fmt.Fprintf(os.Stderr, "  %s\n", s)
	}
	fmt.Fprintln(os.Stderr, "  run: just ste-lint-baseline")
}
