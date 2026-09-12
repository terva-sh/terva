package provider

import (
	"regexp"
	"testing"
)

func TestParseCodexVersion(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"codex-cli 0.153.4", "0.153.4", true},
		{"codex-cli 0.153.4\n", "0.153.4", true},
		{"0.153.4", "0.153.4", true},
		{"codex-cli 0.153.4 (rust)", "0.153.4", true},
		// Refused rather than guessed at: a two-part version is not a triple,
		// and the floor comparison needs three parts to be meaningful.
		{"codex-cli 0.153", "", false},
		{"", "", false},
		{"command not found", "", false},
		{"codex-cli unknown", "", false},
	}
	for _, c := range cases {
		got, ok := parseCodexVersion(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("parseCodexVersion(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

// Floor, never ceiling. Every way the probe can disappoint must land on the
// compiled baseline, because the alternative is claiming a version terva made
// up.
func TestPickCodexCLIVersionFloorsAtBaseline(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"newer install wins", "codex-cli 99.1.2", "99.1.2"},
		{"older install floors", "codex-cli 0.1.0", codexCLIVersion},
		{"same version floors", "codex-cli " + codexCLIVersion, codexCLIVersion},
		{"unparseable floors", "codex-cli unknown", codexCLIVersion},
		{"empty floors", "", codexCLIVersion},
		// The shape of a probe that ran but failed: no binary, a panic, a
		// usage message. None of it is a version.
		{"error text floors", "codex: command not found", codexCLIVersion},
	}
	for _, c := range cases {
		if got := pickCodexCLIVersion(c.in); got != c.want {
			t.Errorf("%s: pickCodexCLIVersion(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// Numeric per part, so 0.99.0 does not beat 0.153.4 lexically. This is the
// comparison the floor rests on, and the Anthropic probe found the same trap
// with 2.1.67 against 2.1.267.
func TestPickCodexCLIVersionComparesNumerically(t *testing.T) {
	if got := pickCodexCLIVersion("codex-cli 0.99.0"); got != codexCLIVersion {
		t.Errorf("pickCodexCLIVersion(0.99.0) = %q, want the baseline %q — a lexical "+
			"compare would have accepted 0.99.0 over 0.153.4", got, codexCLIVersion)
	}
}

// The baseline itself must stay a parseable triple, or the floor comparison
// silently degrades.
func TestBaselineCodexCLIVersionIsATriple(t *testing.T) {
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(codexCLIVersion) {
		t.Fatalf("codexCLIVersion = %q is not a dotted version triple", codexCLIVersion)
	}
}

// Whatever the probe finds on the machine running the tests, the result must be
// a dotted triple and never older than the compiled baseline.
func TestEffectiveCodexCLIVersionNeverBelowBaseline(t *testing.T) {
	got := effectiveCodexCLIVersion()
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(got) {
		t.Fatalf("effectiveCodexCLIVersion() = %q is not a dotted version triple", got)
	}
	if versionTripleNewer(codexCLIVersion, got) {
		t.Fatalf("effectiveCodexCLIVersion() = %q is older than the baseline %q", got, codexCLIVersion)
	}
	t.Logf("probe resolved %q (baseline %q)", got, codexCLIVersion)
}

// The user-agent must carry a version, because a bare "codex_cli_rs" is not
// what the measured arm sent and a version-less client is a shape nobody has
// observed the backend accept.
func TestCodexNativeUserAgentCarriesAVersion(t *testing.T) {
	ua := codexNativeUserAgent()
	if !regexp.MustCompile(`^codex_cli_rs/\d+\.\d+\.\d+$`).MatchString(ua) {
		t.Fatalf("codexNativeUserAgent() = %q, want codex_cli_rs/<triple>", ua)
	}
}
