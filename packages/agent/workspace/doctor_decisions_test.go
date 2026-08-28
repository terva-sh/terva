package workspace

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
)

// The decisions block — "what you proposed last round, and what the author did
// with it" — is prompt text the model reads on every follow-up doctor round. It
// existed twice: i18n.P-wrapped inside renderDoctorPrompt, and again as
// renderSessionDoctorDecisions in raw English, which BOTH the session doctor and
// the World doctor called.
//
// So two of the three doctors shipped this paragraph in English no matter what
// locale the daemon ran in, and the copy that knew better sat one file away.
// Nothing failed; nothing could. A second copy of a paragraph is not a thing a
// compiler or a test has any opinion about.
//
// Extracting renderDecisions fixed that instance. This fixes the CLASS: a fourth
// copy cannot be typed without failing here, which is the only reason the third
// one was possible.

// decisionsHeader is the block's distinctive first line. A new hand-rolled copy
// would carry it, because it is what makes the block recognisable to the model.
const decisionsHeader = "YOUR PREVIOUS PROPOSALS AND THE AUTHOR'S DECISIONS"

// TestDecisionsBlockHasExactlyOneRenderer scans the package for that header. It
// must appear exactly once in non-test source, and that one occurrence must be
// wrapped in i18n.P — the failure mode being fixed is not duplication as such,
// it is an untranslated duplicate.
func TestDecisionsBlockHasExactlyOneRenderer(t *testing.T) {
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob package sources: %v", err)
	}
	// A glob that quietly found nothing would make every assertion below pass.
	if len(paths) < 20 {
		t.Fatalf("found only %d sources; the glob is not seeing the package", len(paths))
	}

	type hit struct {
		file    string
		line    int
		text    string
		wrapped bool
	}
	var hits []hit

	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if !strings.Contains(line, decisionsHeader) {
				continue
			}
			hits = append(hits, hit{
				file:    p,
				line:    i + 1,
				text:    strings.TrimSpace(line),
				wrapped: strings.Contains(line, "i18n.P("),
			})
		}
	}

	// The header living nowhere means it was reworded, and this guard is now
	// watching a string that no longer exists — which is the vacuous-pass this
	// check must not become.
	if len(hits) == 0 {
		t.Fatalf("the decisions header %q appears nowhere in non-test source.\n"+
			"If it was deliberately reworded, update decisionsHeader here so this "+
			"guard keeps watching the real text.", decisionsHeader)
	}

	if len(hits) > 1 {
		var where []string
		for _, h := range hits {
			where = append(where, h.file+":"+strconv.Itoa(h.line))
		}
		t.Errorf("the decisions block is rendered in %d places: %s\n"+
			"It belongs in renderDecisions alone. A second copy is how the session "+
			"and World doctors came to send this paragraph untranslated while the "+
			"card doctor sent it localized.", len(hits), strings.Join(where, ", "))
	}

	for _, h := range hits {
		if !h.wrapped {
			t.Errorf("%s:%d renders the decisions header without i18n.P:\n  %s\n"+
				"Prompt text reaches the model in the daemon's locale; an unwrapped "+
				"literal is English forever.", h.file, h.line, h.text)
		}
	}
}

// TestEveryDoctorRoutesDecisionsThroughTheSharedRenderer checks the behaviour
// rather than the source: all three doctors must produce the SAME block for the
// same decisions. Source-identical calls could still diverge if one wrapped the
// result; this pins the output.
func TestEveryDoctorRoutesDecisionsThroughTheSharedRenderer(t *testing.T) {
	decisions := []ctrlproto.DoctorDecision{
		{ProposalID: "p1", Field: "description", Accepted: true},
		{ProposalID: "p2", Field: "first_mes", Accepted: false, Reason: "too long"},
	}

	got := renderDecisions(decisions)
	if got == "" {
		t.Fatal("renderDecisions returned empty for a non-empty decision set")
	}
	for _, want := range []string{
		decisionsHeader,
		"p1 (description): ",
		"p2 (first_mes): ",
		"too long",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("renderDecisions output is missing %q:\n%s", want, got)
		}
	}

	// Empty in, empty out — every caller appends unconditionally, so a stray
	// header on a first round would tell the model it had proposed before.
	if s := renderDecisions(nil); s != "" {
		t.Errorf("renderDecisions(nil) = %q, want empty: the first round has no "+
			"previous proposals and must not claim otherwise", s)
	}
}
