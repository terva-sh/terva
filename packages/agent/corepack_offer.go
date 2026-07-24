package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"
	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/privfs"
)

// First-run offer: when an interactive session starts with no extensions
// installed, terva offers (once) to install the built-in core pack. The
// shell installer (install.sh) is the wrong place for this — `curl | sh`
// has no TTY and silently fetching executables there would be a surprise
// — so the consent prompt lives at the first interactive run instead.
// See docs/plans/extension-packs.md.

// maybeOfferCorePack prompts to install the built-in core pack and, on
// acceptance, installs it into the global extensions dir so the
// subsequent Discover picks it up the same session. It is heavily gated
// (see corePackOfferAllowed) and asks at most once. A no-op on any
// non-interactive host or non-TTY stdin. Call BEFORE Discover.
func maybeOfferCorePack(args build.Args) {
	if !stdinIsTTY() || !corePackOfferAllowed(args) {
		return
	}
	// Load the embedded pack first; a broken embed must never break
	// startup, so bail silently if it won't resolve.
	p, _, err := resolvePack("core")
	if err != nil {
		return
	}

	fmt.Fprintf(os.Stderr, "\nNo extensions installed yet. terva ships a core extension pack:\n%s\nInstall it now? [y/N] ", p.summary())
	var resp string
	_, _ = fmt.Scanln(&resp)
	// Mark offered on either answer, so the prompt never repeats.
	_ = markCorePackOffered()
	if !strings.EqualFold(strings.TrimSpace(resp), "y") {
		fmt.Fprintln(os.Stderr, "skipped; install it later with `terva ext pack install core`")
		return
	}
	// The offer itself is the consent, so install without re-prompting.
	if err := p.install(true); err != nil {
		fmt.Fprintln(os.Stderr, "core pack:", err)
	}
}

// corePackOfferAllowed is the non-TTY half of the offer gate (split out
// so it's testable without a terminal): offer only when extensions aren't
// disabled for this run, the user hasn't opted out, we haven't already
// asked, and nothing is installed globally.
func corePackOfferAllowed(args build.Args) bool {
	if args.NoExt {
		return false
	}
	if corePackOfferDisabled() {
		return false
	}
	if corePackAlreadyOffered() {
		return false
	}
	return globalExtensionsEmpty()
}

// stdinIsTTY reports whether stdin is an interactive terminal, so piped
// or CI input never triggers the prompt.
func stdinIsTTY() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// corePackOfferDisabled reads the user config opt-out. A missing/unreadable
// config is treated as "not disabled" (offer allowed).
func corePackOfferDisabled() bool {
	c, err := config.LoadConfig()
	if err != nil {
		return false
	}
	return c.DisableCorePackOffer
}

// globalExtensionsEmpty reports whether the global extensions dir holds no
// installed extension (no subdir with an extension.json). A missing dir
// counts as empty.
func globalExtensionsEmpty() bool {
	dir := filepath.Join(config.TervaHome(), "extensions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return true
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, e.Name(), "extension.json")); err == nil {
			return false
		}
	}
	return true
}

// corePackOfferSentinel is the marker recording that the first-run offer
// has been made, so it's a one-time prompt regardless of the answer.
func corePackOfferSentinel() string {
	return filepath.Join(config.TervaHome(), ".core-pack-offered")
}

func corePackAlreadyOffered() bool {
	_, err := os.Stat(corePackOfferSentinel())
	return err == nil
}

func markCorePackOffered() error {
	if err := privfs.MkdirAll(config.TervaHome()); err != nil {
		return err
	}
	return os.WriteFile(corePackOfferSentinel(), []byte("1\n"), 0o644)
}
