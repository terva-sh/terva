package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/identity"
)

// runUpdateCommand dispatches `terva update`. Returns (handled=true, err)
// if rawArgs starts with "update"; otherwise (handled=false, nil) so
// the main router falls through to the regular flag parser. Mirrors
// the shape of runBotCommand / runExtCommand on purpose: identical
// dispatch shape keeps the router in cli.go uniform.
//
// The command:
//
//  1. Resolves the latest release tag via the fork's release API
//     (same code path the in-TUI update banner uses, so we never
//     disagree about "what is latest").
//  2. Picks the asset matching the current GOOS/GOARCH using the
//     name template defined in .goreleaser.yaml.
//  3. Downloads checksums.txt and the asset to a temp directory.
//  4. Verifies the asset's sha256 against checksums.txt.
//  5. Extracts the terva binary from the archive.
//  6. Atomically replaces the running binary with the new one.
//
// Refuses to operate on dev builds (version == "0.0.0") because there
// is no meaningful "is newer" comparison and we'd happily downgrade a
// freshly-compiled local binary back to whatever's on GitHub.
func runUpdateCommand(rawArgs []string, version string) (handled bool, err error) {
	if len(rawArgs) == 0 || rawArgs[0] != "update" {
		return false, nil
	}
	// Accept --help/-h for parity with the rest of the CLI.
	for _, a := range rawArgs[1:] {
		switch a {
		case "-h", "--help", "help":
			printUpdateHelp()
			return true, nil
		case "--check":
			return true, runUpdateCheck(version)
		default:
			printUpdateHelp()
			return true, fmt.Errorf("unknown update flag: %s", a)
		}
	}
	return true, runUpdate(version)
}

func printUpdateHelp() {
	fmt.Fprintln(os.Stderr, `terva update — replace the current terva binary with the latest release

usage:
  terva update           download and install the newest release
  terva update --check   show what update is available; install nothing
  terva update --help    show this help

notes:
  * The binary must be writable by the current user. On a system-wide
    install (e.g. /usr/local/bin/terva owned by root) re-run with sudo.
  * Dev builds (version 0.0.0) are refused — they typically come from
    'go install' or a local 'make build' and shouldn't be silently
    replaced with a release binary.
  * Honours $TERVA_RELEASE_TOKEN (or $FORGEJO_TOKEN) if set, so
    private-repo releases work.
  * After the binary is installed, every extension under
    $TERVA_HOME/extensions/ that is a git checkout is fast-forward
    pulled (no merge, no rebase). Dirty worktrees are stashed and
    restored. Extensions without a .git directory, disabled
    extensions, and pulls that fail (offline, diverged, etc.) are
    skipped per-extension and never abort the overall update. terva
    does NOT run any build step after pulling — authors are expected
    to commit a working binary, or you can rebuild manually and
    /reload-ext.`)
}

// runUpdateCheck just prints what would happen without doing the
// download. Useful as a sanity probe and as something the user can
// pipe into scripts.
func runUpdateCheck(version string) error {
	if version == "" || version == "dev" || version == "0.0.0" {
		fmt.Println("terva: dev build (version 0.0.0) — `terva update` is disabled")
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tag, url, err := fetchLatestRelease(ctx)
	if err != nil {
		return fmt.Errorf("query latest release: %w", err)
	}
	latest := strings.TrimPrefix(tag, "v")
	current := versionOnly(version)
	if !versionLess(current, latest) {
		fmt.Printf("terva %s is up to date (latest: %s)\n", current, latest)
		return nil
	}
	fmt.Printf("terva %s -> %s available\n  release: %s\n  run 'terva update' to install\n", current, latest, url)
	return nil
}

// runUpdate is the meat of `terva update`.
func runUpdate(version string) error {
	if version == "" || version == "dev" || version == "0.0.0" {
		return errors.New("dev build (version 0.0.0): `terva update` is disabled. Build a release tag or download from " + identity.ReleasesPageURL)
	}
	current := versionOnly(version)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	fmt.Println("terva update: querying latest release...")
	tag, releaseURL, err := fetchLatestRelease(ctx)
	if err != nil {
		return fmt.Errorf("query latest release: %w", err)
	}
	latest := strings.TrimPrefix(tag, "v")

	if !versionLess(current, latest) {
		fmt.Printf("terva %s is already up to date.\n", current)
		return nil
	}
	fmt.Printf("terva update: %s -> %s\n", current, latest)
	fmt.Printf("terva update: release page %s\n", releaseURL)

	// Pick the archive matching this platform.
	assetName, archiveFmt, err := releaseAssetName(latest)
	if err != nil {
		return err
	}
	fmt.Printf("terva update: target asset %s\n", assetName)

	// We assume the standard GoReleaser layout (served identically by
	// GitHub and Forgejo): assets live under
	//   <host>/<owner>/<repo>/releases/download/<tag>/<file>
	base := strings.TrimSuffix(releaseURL, "/")
	// releaseURL points at /releases/tag/<tag>; flip it to /releases/download/<tag>
	base = strings.Replace(base, "/releases/tag/", "/releases/download/", 1)

	assetURL := base + "/" + assetName
	sumsURL := base + "/checksums.txt"

	tmp, err := os.MkdirTemp("", "terva-update-")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	// Best effort: remove the temp dir at the end. If extraction
	// fails midway we leave it behind on purpose for diagnosis;
	// users can clear /tmp themselves.
	defer func() { _ = os.RemoveAll(tmp) }()

	fmt.Println("terva update: downloading checksums.txt...")
	sumsPath := filepath.Join(tmp, "checksums.txt")
	if err := downloadFile(ctx, sumsURL, sumsPath); err != nil {
		return fmt.Errorf("download checksums: %w", err)
	}
	wantSum, err := lookupChecksum(sumsPath, assetName)
	if err != nil {
		return err
	}

	fmt.Println("terva update: downloading archive...")
	archivePath := filepath.Join(tmp, assetName)
	if err := downloadFile(ctx, assetURL, archivePath); err != nil {
		return fmt.Errorf("download archive: %w", err)
	}

	fmt.Println("terva update: verifying checksum...")
	gotSum, err := sha256File(archivePath)
	if err != nil {
		return fmt.Errorf("hash archive: %w", err)
	}
	if !strings.EqualFold(gotSum, wantSum) {
		return fmt.Errorf("checksum mismatch for %s: got %s, want %s", assetName, gotSum, wantSum)
	}

	fmt.Println("terva update: extracting...")
	extractDir := filepath.Join(tmp, "extracted")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		return fmt.Errorf("mkdir extract: %w", err)
	}
	if err := extractArchive(archivePath, archiveFmt, extractDir); err != nil {
		return fmt.Errorf("extract archive: %w", err)
	}

	// Accept either binary name: the rename bridge
	// (docs/plans/rename-terva.md, phase 1) — an installed terva must be
	// able to self-update into an archive whose member is `terva`, so
	// this updater ships BEFORE any archive containing the new name.
	newBin := ""
	for _, name := range []string{"terva", "zot"} { // rename:keep — updater bridge
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		candidate := filepath.Join(extractDir, name)
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			newBin = candidate
			break
		}
	}
	if newBin == "" {
		return fmt.Errorf("extracted archive does not contain a terva or terva binary under %s", extractDir)
	}

	curBin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve current binary path: %w", err)
	}
	// Resolve symlinks so 'terva' on $PATH that points at /usr/local/bin
	// gets actually replaced rather than us writing into the symlink
	// target's directory while leaving the link stale.
	if resolved, err := filepath.EvalSymlinks(curBin); err == nil {
		curBin = resolved
	}

	fmt.Printf("terva update: replacing %s\n", curBin)
	if err := replaceBinary(curBin, newBin); err != nil {
		return fmt.Errorf("replace binary: %w", err)
	}
	fmt.Printf("terva update: installed %s\n", latest)

	// Best-effort: also refresh installed extensions that live in
	// git checkouts. Failures here are advisory and never abort the
	// overall update — the binary swap already succeeded.
	updateAllExtensions(TervaHome())

	return nil
}

// releaseAssetName returns the archive filename for the current
// platform and the format (tar.gz / zip) used to extract it. Must
// stay in sync with archives.name_template in .goreleaser.yaml:
//
//	{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}
func releaseAssetName(version string) (name, format string, err error) {
	goos := runtime.GOOS
	goarch := runtime.GOARCH
	switch goos {
	case "linux", "darwin":
		// supported
	case "windows":
		// supported
	default:
		return "", "", fmt.Errorf("unsupported OS for terva update: %s (download manually from the release page)", goos)
	}
	switch goarch {
	case "amd64", "arm64":
		// supported
	default:
		return "", "", fmt.Errorf("unsupported CPU arch for terva update: %s", goarch)
	}
	if goos == "windows" && goarch == "arm64" {
		return "", "", errors.New("windows/arm64 release artifacts are not published; download manually")
	}
	ext := "tar.gz"
	if goos == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("terva_%s_%s_%s.%s", version, goos, goarch, ext), ext, nil
}

// downloadFile fetches url to dst, streaming through io.Copy so big
// archives don't balloon memory.
func downloadFile(ctx context.Context, url, dst string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	if tok := identity.ReleaseHostToken(); tok != "" {
		req.Header.Set("authorization", "Bearer "+tok)
	}
	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		return err
	}
	return nil
}

// lookupChecksum parses a GoReleaser checksums.txt file and returns
// the sha256 hex for the named asset. Format is:
//
//	<sha256>  <filename>
//
// one entry per line, two spaces between columns.
func lookupChecksum(path, asset string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read checksums: %w", err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if fields[1] == asset {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("checksum for %s not listed in checksums.txt", asset)
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// extractArchive shells out to the system tar / unzip rather than
// pulling in a Go archive lib. Reasoning: terva already depends on
// system tar implicitly in a few places, every supported platform
// ships tar (BSD tar on macOS handles gzip natively, GNU tar on
// Linux, bsdtar on Windows 10+), and the dependency-free release
// archives are simple enough that we don't need format-detection
// gymnastics.
func extractArchive(archive, format, dst string) error {
	switch format {
	case "tar.gz":
		cmd := exec.Command("tar", "-xzf", archive, "-C", dst)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("tar: %v: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	case "zip":
		// PowerShell ships everywhere on supported Windows
		// versions and Expand-Archive doesn't need elevation.
		ps := fmt.Sprintf("Expand-Archive -LiteralPath %q -DestinationPath %q -Force", archive, dst)
		cmd := exec.Command("powershell", "-NoProfile", "-Command", ps)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("powershell Expand-Archive: %v: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	default:
		return fmt.Errorf("unknown archive format: %s", format)
	}
}

// replaceBinary writes the new binary in place of the old one,
// preserving the old binary's permissions. On Unix we rename
// in-place (which works while the binary is running because the
// kernel keeps the in-memory inode alive until the process exits).
// On Windows the running .exe is locked, so we rename it aside
// first, then move the new one in.
func replaceBinary(cur, newBin string) error {
	info, err := os.Stat(cur)
	if err != nil {
		return fmt.Errorf("stat current binary: %w", err)
	}
	mode := info.Mode().Perm()
	if mode == 0 {
		mode = 0o755
	}

	if runtime.GOOS == "windows" {
		// Rename current aside so we can drop the new one in.
		bak := cur + ".old"
		// Clean up any stale .old from a previous update.
		_ = os.Remove(bak)
		if err := os.Rename(cur, bak); err != nil {
			return fmt.Errorf("rename current to .old: %w", err)
		}
		if err := os.Rename(newBin, cur); err != nil {
			// Try to put the old one back.
			_ = os.Rename(bak, cur)
			return fmt.Errorf("install new binary: %w", err)
		}
		// The .old file is locked until this process exits; leave
		// it behind. Next `terva update` cleans it up via the
		// os.Remove(bak) above.
		return nil
	}

	// Unix: atomic rename if we're on the same filesystem.
	if err := os.Rename(newBin, cur); err == nil {
		_ = os.Chmod(cur, mode)
		return nil
	}
	// Cross-fs (temp dir on tmpfs vs binary on a different mount)
	// — fall back to copy + chmod + remove.
	if err := copyFile(newBin, cur); err != nil {
		return fmt.Errorf("copy new binary into place: %w", err)
	}
	_ = os.Chmod(cur, mode)
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// versionOnly strips the build metadata that main.go appends to the
// version string, so "0.0.5 (43da5e5, 2026-05-12)" becomes "0.0.5".
func versionOnly(v string) string {
	if i := strings.IndexAny(v, " ("); i > 0 {
		return strings.TrimSpace(v[:i])
	}
	return v
}
