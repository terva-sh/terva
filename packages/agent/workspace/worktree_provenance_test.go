package workspace

import (
	"strings"
	"testing"
)

// TestWorktreeProvenanceRender pins the one-line operator record (retro H5·ux,
// Phase 7 §7a): present git facts appear, the trust verdict and reason always
// show, and a restricted worktree — and only a restricted one — carries the
// exact-path, never-auto-applied grant hint.
func TestWorktreeProvenanceRender(t *testing.T) {
	tests := []struct {
		name    string
		prov    worktreeProvenance
		want    []string // substrings that must be present
		wantNot []string // substrings that must be absent
	}{
		{
			name: "trusted with full facts shows no grant hint",
			prov: worktreeProvenance{
				Repo: "terva", Path: "/wt/a", Branch: "feat/x", Base: "sothr-main",
				Commit: "abc1234", Trusted: true, Reason: "explicit trust entry",
			},
			want:    []string{"terva", "/wt/a", "branch feat/x @ abc1234", "forked from sothr-main", "TRUSTED", "explicit trust entry"},
			wantNot: []string{"terva trust", "RESTRICTED"},
		},
		{
			name: "restricted carries the exact-path grant hint",
			prov: worktreeProvenance{
				Path: "/wt/b", Branch: "feat/y", Commit: "def5678",
				Trusted: false, Reason: "no trust entry",
			},
			want:    []string{"/wt/b", "branch feat/y @ def5678", "RESTRICTED", "terva trust /wt/b", "never auto-trusted"},
			wantNot: []string{"— TRUSTED"},
		},
		{
			name: "inherited --parent trust is surfaced as the reason",
			prov: worktreeProvenance{
				Repo: "terva", Path: "/wt/c", Trusted: true,
				Reason: "trusted via --parent entry /repo",
			},
			want:    []string{"TRUSTED", "trusted via --parent entry /repo"},
			wantNot: []string{"terva trust"},
		},
		{
			name: "missing git facts degrade gracefully (no empty parens)",
			prov: worktreeProvenance{
				Path: "/wt/d", Trusted: false, Reason: "no trust entry",
			},
			want:    []string{"worktree /wt/d", "RESTRICTED", "terva trust /wt/d"},
			wantNot: []string{"()", "branch ", "forked from"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.prov.Render()
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("Render() = %q, missing %q", got, w)
				}
			}
			for _, w := range tc.wantNot {
				if strings.Contains(got, w) {
					t.Errorf("Render() = %q, should not contain %q", got, w)
				}
			}
		})
	}
}

func TestRepoNameFromURL(t *testing.T) {
	tests := map[string]string{
		// Userinfo is spelled "forge@" rather than the conventional
		// "git@": the release scrub blocks that ssh-remote prefix
		// across the whole public tree. Same userinfo-stripping path.
		"ssh://forge@git.example.com:2222/terva-sh/terva.git": "terva",
		"git@github.com:owner/repo.git":                       "repo",
		"https://example.com/a/b/c":                           "c",
		"https://example.com/a/b/c/":                          "c",
		"terva":                                               "terva",
		"":                                                    "",
	}
	for in, want := range tests {
		if got := repoNameFromURL(in); got != want {
			t.Errorf("repoNameFromURL(%q) = %q, want %q", in, got, want)
		}
	}
}
