package identity

import (
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestIdentityURLsAreWellFormed(t *testing.T) {
	// Channel-agnostic: this test must pass compiled from either
	// branch (Forgejo defaults on sothr-main, GitHub defaults on the
	// public release branch).
	for name, u := range map[string]string{
		"HomeURL":           HomeURL,
		"ReleasesPageURL":   ReleasesPageURL,
		"ReleasesAPILatest": ReleasesAPILatest,
		"ReleaseTagAPI":     ReleaseTagAPI("v0.0.1"),
		"RawDocURL":         RawDocURL("docs/providers.md"),
	} {
		parsed, err := url.Parse(u)
		if err != nil {
			t.Fatalf("%s does not parse: %v", name, err)
		}
		if parsed.Scheme != "https" {
			t.Errorf("%s is not https: %s", name, u)
		}
		if !strings.Contains(u, Owner+"/"+Repo) {
			t.Errorf("%s does not reference %s/%s: %s", name, Owner, Repo, u)
		}
	}
	if got := ReleaseTagAPI("v0.0.1"); !strings.HasSuffix(got, "/releases/tags/v0.0.1") {
		t.Errorf("ReleaseTagAPI shape changed: %s", got)
	}
	// Per-channel URL shapes, asserted against whichever channel this
	// binary was compiled for.
	switch Channel {
	case ChannelGitHub:
		if !strings.HasPrefix(ReleasesAPILatest, "https://api.github.com/repos/") {
			t.Errorf("github channel must use the api.github.com base: %s", ReleasesAPILatest)
		}
		if strings.Contains(RawDocURL("x"), "/raw/branch/") {
			t.Errorf("github raw URLs have no /branch/ segment: %s", RawDocURL("x"))
		}
	case ChannelForgejo:
		if !strings.Contains(ReleasesAPILatest, "/api/v1/repos/") {
			t.Errorf("forgejo channel must use the /api/v1 base: %s", ReleasesAPILatest)
		}
		if !strings.Contains(RawDocURL("x"), "/raw/branch/") {
			t.Errorf("forgejo raw URLs need the /branch/ segment: %s", RawDocURL("x"))
		}
	default:
		t.Fatalf("unknown channel %q", Channel)
	}
}

// TestConfigurePerChannel proves the cut's channel-block rewrite (and
// the -X escape hatch) produce a self-consistent identity on both
// channels. The forgejo subtest uses a placeholder host — this test
// ships on the public branch, so the real internal host must not
// appear here.
func TestConfigurePerChannel(t *testing.T) {
	saveChannel, saveOwner, saveHost, saveBranch := Channel, Owner, host, docBranch
	defer func() {
		Channel, Owner, host, docBranch = saveChannel, saveOwner, saveHost, saveBranch
		configure()
	}()

	t.Run("github", func(t *testing.T) {
		Channel, Owner, host, docBranch = ChannelGitHub, "terva-sh", "https://github.com", "release"
		configure()
		if got, want := ReleasesAPILatest, "https://api.github.com/repos/terva-sh/terva/releases/latest"; got != want {
			t.Errorf("ReleasesAPILatest = %s, want %s", got, want)
		}
		if got, want := ReleaseTagAPI("v0.3.0"), "https://api.github.com/repos/terva-sh/terva/releases/tags/v0.3.0"; got != want {
			t.Errorf("ReleaseTagAPI = %s, want %s", got, want)
		}
		if got, want := RawDocURL("docs/fork.md"), "https://github.com/terva-sh/terva/raw/release/docs/fork.md"; got != want {
			t.Errorf("RawDocURL = %s, want %s", got, want)
		}
		// The updater derives download URLs by flipping html_url's
		// /releases/tag/ to /releases/download/ (updatecmd.go). Prove
		// the GitHub html_url shape feeds that flip correctly.
		htmlURL := HomeURL + "/releases/tag/v0.3.0"
		flipped := strings.Replace(htmlURL, "/releases/tag/", "/releases/download/", 1)
		if want := "https://github.com/terva-sh/terva/releases/download/v0.3.0"; flipped != want {
			t.Errorf("download flip = %s, want %s", flipped, want)
		}
	})

	t.Run("forgejo", func(t *testing.T) {
		Channel, Owner, host, docBranch = ChannelForgejo, "owner", "https://forge.example.com", "main-ish"
		configure()
		if got, want := ReleasesAPILatest, "https://forge.example.com/api/v1/repos/owner/terva/releases/latest"; got != want {
			t.Errorf("ReleasesAPILatest = %s, want %s", got, want)
		}
		if got, want := RawDocURL("README.md"), "https://forge.example.com/owner/terva/raw/branch/main-ish/README.md"; got != want {
			t.Errorf("RawDocURL = %s, want %s", got, want)
		}
	})
}

// TestNoUpstreamURLsInGoSources walks every .go file in the repository
// and fails on any https URL that points at the upstream project. The
// Go module path legitimately keeps the upstream name (import paths
// and go.mod are not URLs and do not trip this), but a *URL* aimed at
// upstream means some binary self-reference bypassed this package —
// most likely reintroduced by an upstream merge. Route it through
// packages/agent/identity instead; upstream issue links belong in
// docs/ or commit messages, not in shipped sources.
func TestNoUpstreamURLsInGoSources(t *testing.T) {
	// Concatenated so this file doesn't match itself.
	upstreamOwner := "patric" + "eckhart"

	root := filepath.Join("..", "..", "..")
	var offenders []string

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "bin", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		scan := strings.HasSuffix(name, ".go") ||
			name == "install.sh" || name == "install.ps1" || name == ".goreleaser.yaml"
		if !scan {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(b), "\n") {
			hasURL := strings.Contains(line, "https://") || strings.Contains(line, "http://")
			if hasURL && strings.Contains(line, upstreamOwner) {
				rel, _ := filepath.Rel(root, path)
				offenders = append(offenders, rel+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(offenders) > 0 {
		t.Errorf("upstream URLs found outside packages/agent/identity:\n  %s",
			strings.Join(offenders, "\n  "))
	}
}

// TestNoStrayOldNameInGoSources is the rename's enforcement point
// (docs/plans/rename-terva.md, phase 2): every .go file must be free
// of old-name spellings except where they are deliberate —
//
//   - the Go module path, which tracks upstream until phase 3;
//   - lines carrying the `rename:keep` marker (compat seams: frozen
//     wire fields, TERVA_* fallbacks, dual-read lists, auth-bound
//     identifiers) — the same marker the translation script honors;
//   - grandfathered files that are bilingual or frozen by design.
//
// A failure here after an upstream merge means the translation script
// wasn't run (or a new compat seam is missing its marker).
func TestNoStrayOldNameInGoSources(t *testing.T) {
	// Concatenated so this test's own strings don't trip the scan.
	old := "z" + "ot"
	spellings := []string{old, "Z" + "ot", "Z" + "OT"}
	modulePath := "github.com/patric" + "eckhart/" + old

	grandfathered := []string{
		filepath.Join("packages", "envcompat"),         // bilingual by design
		filepath.Join("packages", "agent", "extproto"), // frozen wire + goldens
		filepath.Join("packages", "agent", "connproto"),
		filepath.Join("packages", "agent", "identity", "identity_test.go"), // this file
		filepath.Join("packages", "agent", "migrate"),                      // zot→terva migration engine + cmd: bilingual by design
		filepath.Join("packages", "agent", "modes", "migrate_dialog"),      // its TUI front-end
	}

	root := filepath.Join("..", "..", "..")
	var offenders []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "bin", "dist", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		for _, g := range grandfathered {
			if strings.HasPrefix(rel, g) {
				return nil
			}
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, "rename:keep") {
				continue
			}
			stripped := strings.ReplaceAll(line, modulePath, "")
			for _, sp := range spellings {
				if strings.Contains(stripped, sp) {
					offenders = append(offenders, rel+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
					break
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) > 0 {
		t.Errorf("old-name spellings outside the grandfather list (run scripts/rename-upstream.sh, or mark a deliberate compat seam with rename:keep):\n  %s",
			strings.Join(offenders, "\n  "))
	}
}

// TestInternalHostOnlyInIdentityDefaults keeps the maintainer's
// internal hostname out of Go sources everywhere except the identity
// channel-defaults block — the one region the release-cut rewrites for
// the public branch (docs/plans/release-process.md). Anywhere else and
// a public build could carry an internal URL the cut never touches.
// Trivially green on the public branch, where zero occurrences remain.
func TestInternalHostOnlyInIdentityDefaults(t *testing.T) {
	// Concatenated so this test doesn't match itself.
	internalHost := "local" + ".sothr.com"

	allowed := filepath.Join("packages", "agent", "identity", "identity.go")
	root := filepath.Join("..", "..", "..")
	var offenders []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "bin", "dist", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if rel == allowed || rel == filepath.Join("packages", "agent", "identity", "identity_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, internalHost) {
				offenders = append(offenders, rel+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) > 0 {
		t.Errorf("internal hostname outside the identity channel-defaults block:\n  %s",
			strings.Join(offenders, "\n  "))
	}
}
