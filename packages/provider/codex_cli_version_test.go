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

// The native user-agent claims the host's installed version only when it is
// newer than the baseline, and the baseline when the host gives none.
func TestTheCodexUserAgentClaimsTheInstalledVersionAboveTheFloor(t *testing.T) {
	for _, c := range []struct {
		name      string
		installed func() string
		want      string
	}{
		{"no host version", nil, codexCLIVersion},
		{"not known yet", func() string { return "" }, codexCLIVersion},
		{"older install", func() string { return "0.1.0" }, codexCLIVersion},
		{"newer install", func() string { return "99.1.2" }, "99.1.2"},
	} {
		var opts []CodexOption
		if c.installed != nil {
			opts = append(opts, WithCodexCLIVersion(c.installed))
		}
		cl := NewOpenAICodexSource(StaticCredential("t"), "a", "", append(opts, WithCodexClientIdentity(CodexIdentityNative))...).(*codexClient)
		if _, ua := cl.identityHeaders(); ua != "codex_cli_rs/"+c.want {
			t.Errorf("%s: user-agent %q, want codex_cli_rs/%s", c.name, ua, c.want)
		}
	}
}

// The user-agent must carry a version, because a bare "codex_cli_rs" is not
// what the measured arm sent and a version-less client is a shape nobody has
// observed the backend accept.
func TestCodexNativeUserAgentCarriesAVersion(t *testing.T) {
	ua := (&codexClient{catalogRef: catalogRef{testReg}}).codexNativeUserAgent()
	if !regexp.MustCompile(`^codex_cli_rs/\d+\.\d+\.\d+$`).MatchString(ua) {
		t.Fatalf("codexNativeUserAgent() = %q, want codex_cli_rs/<triple>", ua)
	}
}
