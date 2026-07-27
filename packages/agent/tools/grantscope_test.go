package tools

import (
	"reflect"
	"regexp"
	"testing"
)

func displays(t *testing.T, cmd string) []string {
	t.Helper()
	scopes := BashGrantScopes(cmd)
	if scopes == nil {
		return nil
	}
	out := make([]string, len(scopes))
	for i, s := range scopes {
		out[i] = s.Display
	}
	return out
}

func TestBashGrantScopes(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want []string // displays; nil = no scoped option
	}{
		{"subcommand CLI takes two tokens", "git status", []string{"git status"}},
		{"flag is not a subcommand", "ls -la", []string{"ls"}},
		{"flag then subcommand still one token", "git -C /tmp status", []string{"git"}},
		{"compound derives one per command", "git status && ls -la | wc -l", []string{"git status", "ls", "wc"}},
		{"duplicates collapse", "git add -A && git add .", []string{"git add"}},
		{"wrapper anchors on the wrapper", "sudo rm -rf /tmp/x", []string{"sudo rm"}},
		{"path command", "./scripts/deploy.sh prod", []string{"./scripts/deploy.sh prod"}},
		{"command substitution poisons the line", "echo $(rm -rf /tmp/x)", nil},
		{"substitution in a later arg still poisons", "git commit -m \"$(date)\"", nil},
		{"assignment prefix refuses", "FOO=1 git status", nil},
		{"bare assignment alone has no command", "FOO=1", nil},
		{"quoted command word refuses", "'git' status", nil},
		{"expanded command word refuses", "$CMD status", nil},
		{"function body does not grant", "f() { rm -rf /; }; f", []string{"f"}},
		{"parameter expansion in later args is fine", "git checkout $BRANCH", []string{"git checkout"}},
		{"empty", "   ", nil},
		{"unparseable", "if then fi ((", nil},
		{"too many commands", "a && b && c && d && e", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := displays(t, tc.cmd)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("BashGrantScopes(%q) displays = %v, want %v", tc.cmd, got, tc.want)
			}
		})
	}
}

// The pattern half of the contract: anchored, quoted, and bounded by
// whitespace-or-end — `^git status` must not also match `git status.foo`,
// and QuoteMeta must keep a dotted command word from matching wildcards.
func TestBashGrantScopePatterns(t *testing.T) {
	scopes := BashGrantScopes("git status")
	if len(scopes) != 1 {
		t.Fatalf("scopes = %v, want exactly one", scopes)
	}
	re := regexp.MustCompile(scopes[0].Pattern)

	for _, m := range []string{"git status", "git status --short", "git status\t-sb"} {
		if !re.MatchString(m) {
			t.Errorf("pattern %q should match %q", scopes[0].Pattern, m)
		}
	}
	for _, m := range []string{"git status.foo", "git statusx", "xgit status", "echo git status"} {
		if re.MatchString(m) {
			t.Errorf("pattern %q must NOT match %q", scopes[0].Pattern, m)
		}
	}

	dotted := BashGrantScopes("./deploy.sh prod")
	if len(dotted) != 1 {
		t.Fatalf("dotted scopes = %v, want one", dotted)
	}
	dre := regexp.MustCompile(dotted[0].Pattern)
	if !dre.MatchString("./deploy.sh prod --now") {
		t.Error("quoted pattern should match its own command")
	}
	if dre.MatchString("./deployXsh prod") {
		t.Error("the dot must be QuoteMeta'd, not a wildcard")
	}
}
