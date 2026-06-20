package agent

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// An extension pack is a hosted manifest naming a set of extensions to
// install in one shot. terva does NOT carry per-platform binaries or
// checksums — a pack is a list of *sources*, and each extension's own
// launcher (typically a run.sh that compiles or downloads a release
// binary) owns its bring-up. `pack install` just fans out over the same
// install path a manual `ext install` uses. See
// docs/plans/extension-packs.md.

// packSchemaV1 is the manifest's format discriminator. A mismatched
// schema is rejected rather than guessed at.
const packSchemaV1 = "terva-extension-pack/v1"

// maxPackManifestBytes caps a fetched/read manifest so a hostile or
// mistaken URL can't stream an unbounded body into memory.
const maxPackManifestBytes = 256 * 1024

// corePackJSON is the built-in "core" pack, version-locked to this
// binary so the first-run offer works offline. Only the per-entry source
// clones touch the network. The real public URLs/refs are curated in
// packs/core.json.
//
//go:embed packs/core.json
var corePackJSON []byte

// Pack is a parsed extension-pack manifest.
type Pack struct {
	Schema      string      `json:"schema"`
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	Version     string      `json:"version,omitempty"`
	Extensions  []PackEntry `json:"extensions"`
}

// PackEntry is one extension in a pack. Source is required; the rest
// mirror what `ext install` already understands, plus an optional ref.
type PackEntry struct {
	Name        string `json:"name,omitempty"`
	Source      string `json:"source"`
	Ref         string `json:"ref,omitempty"` // branch or tag; empty = repo default HEAD
	Description string `json:"description,omitempty"`
}

// entryName is the dir name / reporting label for an entry: the explicit
// name, else the source basename (minus a trailing .git).
func (e PackEntry) entryName() string {
	if e.Name != "" {
		return e.Name
	}
	return strings.TrimSuffix(filepath.Base(e.Source), ".git")
}

// validate checks a parsed pack before any install side effects.
func (p Pack) validate() error {
	if p.Schema != packSchemaV1 {
		return fmt.Errorf("unsupported pack schema %q (want %q)", p.Schema, packSchemaV1)
	}
	if len(p.Extensions) == 0 {
		return errors.New("pack has no extensions")
	}
	for i, e := range p.Extensions {
		if strings.TrimSpace(e.Source) == "" {
			return fmt.Errorf("pack entry %d (%s): source is required", i, e.entryName())
		}
	}
	return nil
}

// extPackInstall handles `terva ext pack [install] [pack] [--yes]`. The
// pack arg resolves to the built-in core pack (default), an https URL, or
// a local file. A non-built-in pack is confirmed before installing
// unless --yes; bulk-installing executables from a hosted list is a
// supply-chain act, so the prompt is the consent.
func extPackInstall(args []string) error {
	// Tolerate an optional leading "install" subverb (the only verb in
	// v1), so `ext pack install core`, `ext pack core`, and bare
	// `ext pack` all work.
	if len(args) > 0 && args[0] == "install" {
		args = args[1:]
	}

	yes := false
	packArg := ""
	for _, a := range args {
		switch {
		case a == "--yes" || a == "-y":
			yes = true
		case a == "help" || a == "-h" || a == "--help":
			fmt.Fprintln(os.Stderr, "usage: terva ext pack install [core | https://… | path.json] [--yes]")
			return nil
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown flag %q", a)
		case packArg == "":
			packArg = a
		default:
			return fmt.Errorf("unexpected argument %q", a)
		}
	}
	if packArg == "" {
		packArg = "core" // default to the built-in core pack
	}

	builtin := isBuiltinPackArg(packArg)
	p, source, err := resolvePack(packArg)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "pack: %s (%s)\n", dashIfEmpty(p.Name), source)
	// A built-in pack ships with terva and is trusted; skip its prompt.
	return p.install(yes || builtin)
}

func isBuiltinPackArg(arg string) bool {
	return arg == "core" || arg == "builtin" || arg == "builtin:core"
}

// resolvePack turns a pack argument into a validated Pack plus a
// human-readable source label. Accepts the built-in core pack, an https
// URL, or a local file path.
func resolvePack(arg string) (Pack, string, error) {
	var (
		raw   []byte
		label string
		err   error
	)
	switch {
	case arg == "":
		return Pack{}, "", errors.New("usage: terva ext pack install [core | https://… | path.json]")
	case isBuiltinPackArg(arg):
		raw, label = corePackJSON, "built-in core pack"
	case strings.HasPrefix(arg, "builtin:"):
		return Pack{}, "", fmt.Errorf("unknown built-in pack %q (only \"core\" exists)", strings.TrimPrefix(arg, "builtin:"))
	case strings.HasPrefix(arg, "http://"):
		return Pack{}, arg, fmt.Errorf("refusing insecure pack URL %q; use https://", arg)
	case strings.HasPrefix(arg, "https://"):
		raw, err = fetchPackURL(arg)
		label = arg
	default:
		raw, err = readPackFile(arg)
		label = arg
	}
	if err != nil {
		return Pack{}, label, err
	}
	var p Pack
	if err := json.Unmarshal(raw, &p); err != nil {
		return Pack{}, label, fmt.Errorf("parse pack manifest: %w", err)
	}
	if err := p.validate(); err != nil {
		return Pack{}, label, err
	}
	return p, label, nil
}

// fetchPackURL GETs a pack manifest over HTTPS with a short timeout and a
// hard size cap.
func fetchPackURL(url string) ([]byte, error) {
	return fetchPackWith(&http.Client{Timeout: 15 * time.Second}, url)
}

// fetchPackWith is the client-injectable core of fetchPackURL so tests
// can supply an httptest server's TLS-trusting client.
func fetchPackWith(client *http.Client, url string) ([]byte, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetch pack: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch pack: HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxPackManifestBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read pack body: %w", err)
	}
	if len(raw) > maxPackManifestBytes {
		return nil, fmt.Errorf("pack manifest exceeds %d bytes", maxPackManifestBytes)
	}
	return raw, nil
}

// readPackFile reads a local pack manifest with the same size cap.
func readPackFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%s is a directory, not a pack manifest", path)
	}
	if info.Size() > maxPackManifestBytes {
		return nil, fmt.Errorf("pack manifest exceeds %d bytes", maxPackManifestBytes)
	}
	return os.ReadFile(path)
}

// install fans out over the pack's entries, one clone/copy each, and
// reports a per-entry + summary result. A single failed entry does not
// abort the rest (mirroring Discover's per-extension error model); an
// already-present extension is skipped, not failed. yes skips the
// confirmation prompt (set for a built-in pack, the first-run offer, or
// --yes).
func (p Pack) install(yes bool) error {
	fmt.Fprint(os.Stderr, p.summary())
	if !yes {
		fmt.Fprintf(os.Stderr, "install these %d extension(s)? [y/N] ", len(p.Extensions))
		var resp string
		_, _ = fmt.Scanln(&resp)
		if !strings.EqualFold(strings.TrimSpace(resp), "y") {
			fmt.Fprintln(os.Stderr, "aborted")
			return nil
		}
	}

	var installed, skipped, failed int
	for _, e := range p.Extensions {
		out, err := installOne(e.Source, e.Ref, e.Name)
		switch {
		case errors.Is(err, errExtAlreadyInstalled):
			skipped++
			fmt.Fprintf(os.Stderr, "  skip   %s (already installed at %s)\n", e.entryName(), out)
		case err != nil:
			failed++
			fmt.Fprintf(os.Stderr, "  FAIL   %s: %v\n", e.entryName(), err)
		default:
			installed++
			fmt.Fprintf(os.Stderr, "  ok     %s -> %s\n", e.entryName(), out)
		}
	}
	fmt.Fprintf(os.Stderr, "\n%d installed, %d skipped, %d failed\n", installed, skipped, failed)
	if failed > 0 {
		return fmt.Errorf("%d extension(s) failed to install", failed)
	}
	return nil
}

// summary renders the pack's description and one line per entry for the
// pre-install confirmation.
func (p Pack) summary() string {
	var b strings.Builder
	if p.Description != "" {
		fmt.Fprintf(&b, "%s\n", p.Description)
	}
	for _, e := range p.Extensions {
		ref := e.Ref
		if ref == "" {
			ref = "default"
		}
		fmt.Fprintf(&b, "  %-16s %s  (%s)\n", e.entryName(), e.Source, ref)
	}
	return b.String()
}
