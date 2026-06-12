// Package identity pins every URL through which a terva binary refers to
// its own project home: the release feed the updater polls, the asset
// downloads it installs, the changelog endpoint, and the docs links the
// TUI prints.
//
// This repository is a hard fork of upstream zot // rename:keep — historical
// (github.com/patriceckhart/zot), renamed terva. Since phase 3 of the // rename:keep — historical
// rename the module path is the fork-owned vanity import
// terva.sh/terva (upstream merges are translated by
// scripts/rename-upstream.sh, which absorbs the merge tax the old
// keep-upstream's-path decision existed to avoid). The *binary* must
// still never phone home to upstream: before this package existed,
// `terva update` on a tagged fork build would have replaced the
// binary with upstream's release and verified it against upstream's
// checksums.txt. Every self-referential URL therefore routes through
// here, and identity_test.go fails if a hardcoded upstream URL ever
// reappears in Go sources (e.g. reintroduced by an upstream merge).
//
// Dual-channel: terva ships from two hosts — the maintainer's Forgejo
// (day-to-day, sothr-main) and the public GitHub mirror (the curated
// release branch; see docs/plans/release-process.md). A binary must
// self-update from the channel it was built for, so the values below
// are vars, not consts: the channel-defaults block is the ONE region
// the release-cut rewrites on the public branch, and all four vars are
// also -ldflags -X settable (derived URLs are computed in init, which
// runs after link-time injection).
package identity

import (
	"os"

	"terva.sh/terva/packages/envcompat"
)

// The two release channels. Channel selects the API shape: Forgejo
// serves the GitHub-compatible release JSON under /api/v1/repos/...,
// GitHub serves it under api.github.com/repos/...; raw-file URLs
// differ too (/raw/branch/<branch>/ vs /raw/<branch>/).
const (
	ChannelForgejo = "forgejo"
	ChannelGitHub  = "github"
)

// --- channel defaults (the release-cut rewrites this block; scripts/release.sh) ---
var (
	// Channel is the release channel this build self-updates from.
	Channel = ChannelGitHub

	// Owner identifies the repo owner on the channel's host.
	Owner = "terva-sh"

	// host is the web host serving the repo and its releases.
	host = "https://github.com"

	// docBranch is the branch raw-doc links point at: the curated
	// public release branch.
	docBranch = "release"
)

// --- end channel defaults ---

// Repo is the repository name; identical on every channel.
var Repo = "terva"

// Derived URLs, computed from the channel defaults by configure().
var (
	// HomeURL is the project's web home.
	HomeURL string

	// ReleasesPageURL is the human-facing releases page.
	ReleasesPageURL string

	// ReleasesAPILatest returns the newest published release. The
	// release JSON carries the same fields on both channels (tag_name,
	// html_url, body), and both serve assets from the same
	// /releases/download/<tag>/<file> layout GoReleaser publishes — so
	// the update path is channel-agnostic once these URLs are set.
	ReleasesAPILatest string

	releasesAPI string
	rawDocBase  string
)

func init() { configure() }

// configure computes the derived URLs from the channel defaults. Split
// out of init so tests can flip the channel vars and recompute.
func configure() {
	HomeURL = host + "/" + Owner + "/" + Repo
	ReleasesPageURL = HomeURL + "/releases"
	switch Channel {
	case ChannelGitHub:
		releasesAPI = "https://api.github.com/repos/" + Owner + "/" + Repo + "/releases"
		rawDocBase = HomeURL + "/raw/" + docBranch + "/"
	default:
		releasesAPI = host + "/api/v1/repos/" + Owner + "/" + Repo + "/releases"
		rawDocBase = HomeURL + "/raw/branch/" + docBranch + "/"
	}
	ReleasesAPILatest = releasesAPI + "/latest"
}

// ReleaseTagAPI returns the API URL for a single tagged release.
func ReleaseTagAPI(tag string) string { return releasesAPI + "/tags/" + tag }

// RawDocURL returns the raw-file URL for a repo path on the channel's
// doc branch.
func RawDocURL(path string) string { return rawDocBase + path }

// ReleaseHostToken returns the token to send with requests to the
// release host, or "" for anonymous access. TERVA_RELEASE_TOKEN always
// wins. The per-host fallback follows the channel: FORGEJO_TOKEN on
// the Forgejo channel, GITHUB_TOKEN on the GitHub channel — a GitHub
// credential is never sent to the Forgejo host, and vice versa.
func ReleaseHostToken() string {
	if tok := envcompat.Get("RELEASE_TOKEN"); tok != "" {
		return tok
	}
	if Channel == ChannelGitHub {
		return os.Getenv("GITHUB_TOKEN")
	}
	return os.Getenv("FORGEJO_TOKEN")
}
