package agent

import (
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/raati"
)

// TestRunRaatiCommandRouting: the router claims only `terva raati ...`
// argv shapes, leaving everything else to the rest of the chain.
func TestRunRaatiCommandRouting(t *testing.T) {
	for _, argv := range [][]string{{}, {"bot", "run"}, {"--print", "hi"}, {"raatii"}} {
		if handled, _ := runRaatiCommand(argv, "test"); handled {
			t.Errorf("runRaatiCommand(%v) claimed argv it does not own", argv)
		}
	}
	// `terva raati --help` is handled without resolving providers.
	handled, err := runRaatiCommand([]string{"raati", "--help"}, "test")
	if !handled || err != nil {
		t.Errorf("raati --help: handled=%v err=%v, want handled cleanly", handled, err)
	}
}

func TestExtractRaatiFlags(t *testing.T) {
	rest, o, err := extractRaatiFlags([]string{
		"--class", "gate",
		"--evidence", "a.md",
		"--evidence=b.md",
		"--round-timeout", "90s",
		"--single-round",
		"--veto-holder=KUSANAGI-2",
		"should", "we?",
		"--provider", "ollama",
	})
	if err != nil {
		t.Fatalf("extractRaatiFlags: %v", err)
	}
	if o.class != raati.ClassGate || !o.singleRound || o.vetoHolder != "KUSANAGI-2" {
		t.Errorf("opts = %+v", o)
	}
	if o.timeout != 90*time.Second {
		t.Errorf("timeout = %v, want 90s", o.timeout)
	}
	if len(o.evidence) != 2 || o.evidence[0] != "a.md" || o.evidence[1] != "b.md" {
		t.Errorf("evidence = %v", o.evidence)
	}
	// Shared flags and the positional question flow through untouched
	// for ParseArgs.
	if got := strings.Join(rest, " "); got != "should we? --provider ollama" {
		t.Errorf("rest = %q", got)
	}
}

func TestExtractRaatiFlagsRejectsBadValues(t *testing.T) {
	cases := [][]string{
		{"--class", "tribunal"},
		{"--level", "one"},
		{"--level", "3"}, // the ladder is 0–2
		{"--level", "-1"},
		{"--round-timeout", "soon"},
		{"--round-timeout", "-3s"},
		{"--evidence", ""},
	}
	for _, argv := range cases {
		if _, _, err := extractRaatiFlags(argv); err == nil {
			t.Errorf("extractRaatiFlags(%v) accepted a malformed value", argv)
		}
	}
}

// TestExtractRaatiFlagsProfile: --profile parses in both forms, and the
// explicit-flag markers distinguish "the invocation said class/level"
// from the defaults — the profile only fills what was left unsaid.
func TestExtractRaatiFlagsProfile(t *testing.T) {
	_, o, err := extractRaatiFlags([]string{"--profile", "code-review", "q?"})
	if err != nil {
		t.Fatalf("extractRaatiFlags: %v", err)
	}
	if o.profile != "code-review" || o.classSet || o.levelSet {
		t.Errorf("opts = %+v", o)
	}
	_, o, err = extractRaatiFlags([]string{"--profile=ethics", "--class", "gate", "--level=2", "q?"})
	if err != nil {
		t.Fatalf("extractRaatiFlags: %v", err)
	}
	if o.profile != "ethics" || !o.classSet || !o.levelSet {
		t.Errorf("opts = %+v", o)
	}
	if _, _, err := extractRaatiFlags([]string{"--profile", "", "q?"}); err == nil {
		t.Error("empty --profile accepted")
	}
}
