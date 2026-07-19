package worktree

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestSlugify(t *testing.T) {
	cases := []struct{ in, want string }{
		{"feat-auth", "feat-auth"},
		{"Feat/Smoke Test", "feat-smoke-test"},
		{"  Trim Me  ", "trim-me"},
		{"a___b...c:::d", "a-b-c-d"},
		{"---leading-and-trailing---", "leading-and-trailing"},
		{"UPPER", "upper"},
		{"with.dots/and/slashes", "with-dots-and-slashes"},
		{"!!!", ""},
		{"", ""},
		{"héllo wörld", "hllo-wrld"}, // non-ASCII letters are dropped
	}
	for _, c := range cases {
		if got := slugify(c.in); got != c.want {
			t.Errorf("slugify(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSlugifyTruncates(t *testing.T) {
	long := strings.Repeat("a", maxNameLen+50)
	got := slugify(long)
	if len([]rune(got)) > maxNameLen {
		t.Errorf("slugify did not truncate to %d: got %d", maxNameLen, len([]rune(got)))
	}
	// A trailing dash created by truncation must be trimmed.
	if strings.HasSuffix(got, "-") {
		t.Errorf("truncated slug should not end in a dash: %q", got)
	}
}

func TestRepoKeyFormat(t *testing.T) {
	k := repoKey("/home/me/projects/myrepo/.git")
	if !strings.HasPrefix(k, "myrepo-") {
		t.Errorf("expected readable prefix, got %q", k)
	}
	// prefix-<10 hex>
	suffix := strings.TrimPrefix(k, "myrepo-")
	if len(suffix) != 10 {
		t.Errorf("expected 10-hex suffix, got %q (%d)", suffix, len(suffix))
	}
	// Stable + collision-resistant: same input → same key, different → different.
	if repoKey("/home/me/projects/myrepo/.git") != k {
		t.Error("repoKey not stable for identical input")
	}
	if repoKey("/home/me/projects/other/.git") == k {
		t.Error("repoKey collided across different repos")
	}
	// A repo whose name slugs to empty still produces a usable key.
	if got := repoKey("/!!!/.git"); !strings.HasPrefix(got, "repo-") {
		t.Errorf("empty-name repo should fall back to repo-… prefix, got %q", got)
	}
}

func TestLastPathElem(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/a/b/c", "c"},
		{"/a/b/c/", "c"},
		{`C:\a\b\rogue`, "rogue"}, // git reports forward slashes, but be defensive
		{"single", "single"},
		{"/trailing///", "trailing"},
	}
	for _, c := range cases {
		if got := lastPathElem(c.in); got != c.want {
			t.Errorf("lastPathElem(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParsePorcelain(t *testing.T) {
	out := strings.Join([]string{
		"worktree /repo/main",
		"HEAD aaaa1111",
		"branch refs/heads/main",
		"",
		"worktree /repo/wt/feat",
		"HEAD bbbb2222",
		"branch refs/heads/wt/feat",
		"",
		"worktree /repo/detached",
		"HEAD cccc3333",
		"detached",
		"",
		"worktree /repo/bare",
		"bare",
		"",
		"worktree /repo/prunable-wt",
		"HEAD dddd4444",
		"branch refs/heads/gone",
		"prunable gitdir file points to non-existent location",
		"",
	}, "\n")

	m := parsePorcelain(out)
	if len(m) != 5 {
		t.Fatalf("want 5 worktrees, got %d: %+v", len(m), m)
	}
	main := m["/repo/main"]
	if main.Head != "aaaa1111" || main.Branch != "main" || main.Detached || main.Bare {
		t.Errorf("main parsed wrong: %+v", main)
	}
	feat := m["/repo/wt/feat"]
	if feat.Branch != "wt/feat" { // refs/heads/ stripped, internal slash kept
		t.Errorf("feat branch parsed wrong: %+v", feat)
	}
	det := m["/repo/detached"]
	if !det.Detached || det.Branch != "" {
		t.Errorf("detached parsed wrong: %+v", det)
	}
	bare := m["/repo/bare"]
	if !bare.Bare {
		t.Errorf("bare parsed wrong: %+v", bare)
	}
	pr := m["/repo/prunable-wt"]
	if !pr.Prunable {
		t.Errorf("prunable parsed wrong: %+v", pr)
	}
}

func TestDeriveStatus(t *testing.T) {
	now := time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-time.Hour).Format(time.RFC3339)
	old := now.Add(-48 * time.Hour).Format(time.RFC3339)

	m := &Manager{now: func() time.Time { return now }, ttl: defaultClaimTTL}

	tests := []struct {
		name        string
		claim       *Claim
		current     string
		alive       bool
		wantState   string
		wantClaimBy string
		wantStale   bool
	}{
		{"nil claim", nil, "s1", true, "available", "", false},
		{"self", &Claim{SessionID: "s1", PID: 9, ClaimedAt: fresh}, "s1", true, "claimed", "self", false},
		{"other live", &Claim{SessionID: "s2", PID: 9, ClaimedAt: fresh}, "s1", true, "claimed", "s2", false},
		{"other pid dead", &Claim{SessionID: "s2", PID: 9, ClaimedAt: fresh}, "s1", false, "available", "", true},
		{"other ttl expired", &Claim{SessionID: "s2", PID: 9, ClaimedAt: old}, "s1", true, "available", "", true},
		{"unparseable ts", &Claim{SessionID: "s2", PID: 9, ClaimedAt: "garbage"}, "s1", true, "available", "", true},
		{"empty session not self", &Claim{SessionID: "", PID: 9, ClaimedAt: fresh}, "", true, "claimed", "(no session)", false},
		{"empty session stale", &Claim{SessionID: "", PID: 9, ClaimedAt: fresh}, "s1", false, "available", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m.pidAlive = func(int) bool { return tc.alive }
			st := m.deriveStatus(tc.claim, tc.current)
			if st.State != tc.wantState || st.ClaimedBy != tc.wantClaimBy || st.Stale != tc.wantStale {
				t.Errorf("deriveStatus = %+v, want state=%s claimedBy=%s stale=%v",
					st, tc.wantState, tc.wantClaimBy, tc.wantStale)
			}
			if tc.wantStale && st.StaleReason == "" {
				t.Error("stale status must carry a reason")
			}
			if tc.claim != nil && tc.claim.SessionID == "" && st.StaleReason != "" && !strings.Contains(st.StaleReason, "(no session)") {
				t.Errorf("empty-session stale reason should name the owner: %q", st.StaleReason)
			}
		})
	}
}

func TestPidAliveReal(t *testing.T) {
	if !pidAlive(os.Getpid()) {
		t.Error("current process should be alive")
	}
	if pidAlive(0) || pidAlive(-1) {
		t.Error("non-positive pids must not be considered alive")
	}
	// A reaped child should read as gone (ESRCH). Tolerate pid reuse to stay
	// non-flaky.
	cmd := exec.Command("sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn child: %v", err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Wait() // reap it
	if pidAlive(pid) {
		t.Skipf("pid %d appears reused; skipping dead-pid assertion", pid)
	}
}
