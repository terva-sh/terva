package external

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/chat"
)

// verbTimeout bounds the non-interactive verbs (configured, status).
// setup and reset run interactively on the user's tty, unbounded.
const verbTimeout = 5 * time.Second

// NewService builds the chat.Service for one manifest file. The
// lifecycle hooks map to argv invocations of the connector
// executable, git-credential-helper style: `<exec> setup` / `status`
// / `reset` run interactively; `<exec> configured` is a cheap exit-
// code probe; only `<exec> run` (spawned by the proxy) speaks the
// protocol.
//
// Hooks re-read the manifest on every call so a dev connector author
// can edit exec/args between invocations without relinking.
func NewService(manifestPath string, dev bool) (chat.Service, error) {
	m, _, err := LoadManifest(manifestPath)
	if err != nil {
		return chat.Service{}, err
	}
	name := m.Name
	return chat.Service{
		Name: name,
		Dev:  dev,
		Configured: func(tervaHome string) bool {
			m, dir, err := LoadManifest(manifestPath)
			if err != nil {
				return false
			}
			ctx, cancel := context.WithTimeout(context.Background(), verbTimeout)
			defer cancel()
			cmd := verbCmd(ctx, m, dir, "configured")
			return cmd.Run() == nil
		},
		NewConnector: func(tervaHome string, warn func(string)) (chat.Connector, chat.Pairing, error) {
			m, dir, err := LoadManifest(manifestPath)
			if err != nil {
				return nil, chat.Pairing{}, err
			}
			p := NewProxy(m, dir, tervaHome, warn)
			pairing := chat.Pairing{
				AllowedUserID: loadPairing(tervaHome, m.Name),
				Save: func(userID string) error {
					return savePairing(tervaHome, m.Name, userID)
				},
			}
			return p, pairing, nil
		},
		Setup: func(tervaHome string) error {
			m, dir, err := LoadManifest(manifestPath)
			if err != nil {
				return err
			}
			cmd := verbCmd(context.Background(), m, dir, "setup")
			cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
			return cmd.Run()
		},
		StatusText: func(tervaHome string) (string, error) {
			return statusText(manifestPath, tervaHome, dev)
		},
		Reset: func(tervaHome string) (string, error) {
			return reset(manifestPath, tervaHome)
		},
	}, nil
}

// verbCmd builds `<exec> <manifest args...> <verb>` rooted at the
// manifest dir.
func verbCmd(ctx context.Context, m Manifest, dir, verb string) *exec.Cmd {
	argv := append(append([]string{}, m.Args...), verb)
	cmd := exec.CommandContext(ctx, resolveExec(m.Exec, dir), argv...)
	cmd.Dir = dir
	return cmd
}

// statusText renders the host-side block (manifest, exec, pairing,
// dev/link provenance) and appends whatever `<exec> status` reports.
func statusText(manifestPath, tervaHome string, dev bool) (string, error) {
	m, dir, err := LoadManifest(manifestPath)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	tag := "external"
	if dev {
		tag = "external, dev"
	}
	fmt.Fprintf(&b, "%s connector (%s)", m.Name, tag)
	if m.Version != "" {
		fmt.Fprintf(&b, " v%s", m.Version)
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "manifest:     %s", manifestPath)
	if target, err := os.Readlink(manifestPath); err == nil {
		fmt.Fprintf(&b, " -> %s (linked)", target)
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "exec:         %s\n", resolveExec(m.Exec, dir))
	if paired := loadPairing(tervaHome, m.Name); paired != "" {
		fmt.Fprintf(&b, "paired with:  user id %s\n", paired)
	} else {
		b.WriteString("paired with:  (unpaired — send /start from the chat to claim)\n")
	}
	fmt.Fprintf(&b, "log file:     %s\n", filepath.Join(tervaHome, "logs", "connector-"+m.Name+".log"))

	ctx, cancel := context.WithTimeout(context.Background(), verbTimeout)
	defer cancel()
	out, err := verbCmd(ctx, m, dir, "status").CombinedOutput()
	if text := strings.TrimSpace(string(out)); err == nil && text != "" {
		fmt.Fprintf(&b, "--- reported by the connector ---\n%s", text)
	} else if err != nil {
		fmt.Fprintf(&b, "connector status verb failed: %v", err)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// reset clears everything the host holds for the connector — pairing
// state and, for a linked manifest, the symlink itself — after giving
// the connector a chance to wipe its own credentials via `<exec>
// reset`. The verb is best-effort: a deleted dev tree must not make
// the link impossible to remove.
func reset(manifestPath, tervaHome string) (string, error) {
	var removed []string
	if m, dir, err := LoadManifest(manifestPath); err == nil {
		cmd := verbCmd(context.Background(), m, dir, "reset")
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if rerr := cmd.Run(); rerr != nil {
			fmt.Fprintf(os.Stderr, "connector reset verb failed (continuing host-side cleanup): %v\n", rerr)
		}
		if p := pairingPath(tervaHome, m.Name); fileExists(p) {
			if err := removePairing(tervaHome, m.Name); err != nil {
				return strings.Join(removed, ", "), err
			}
			removed = append(removed, p)
		}
	} else {
		fmt.Fprintf(os.Stderr, "connector manifest unreadable (continuing host-side cleanup): %v\n", err)
	}

	// Remove the symlink for linked manifests under $TERVA_HOME — the
	// `terva bot link` counterpart. Installed (regular-file) manifests
	// are left alone: reset forgets credentials, not the install.
	if isUnder(manifestPath, ConnectorsDir(tervaHome)) {
		if fi, err := os.Lstat(manifestPath); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			if err := os.Remove(manifestPath); err != nil {
				return strings.Join(removed, ", "), err
			}
			removed = append(removed, manifestPath)
		}
	}
	return strings.Join(removed, ", "), nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func isUnder(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Discover loads every enabled manifest under $TERVA_HOME/connectors.
// Returns the services plus one error per unloadable entry (a single
// bad manifest doesn't hide the others).
func Discover(tervaHome string) ([]chat.Service, []error) {
	entries, err := os.ReadDir(ConnectorsDir(tervaHome))
	if err != nil {
		return nil, nil // no connectors dir is the common case
	}
	var (
		svcs []chat.Service
		errs []error
	)
	for _, e := range entries {
		manifestPath := filepath.Join(ConnectorsDir(tervaHome), e.Name(), "connector.json")
		if _, err := os.Lstat(manifestPath); err != nil {
			continue // not a connector dir; ignore strays quietly
		}
		m, _, err := LoadManifest(manifestPath)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", manifestPath, err))
			continue
		}
		if !m.IsEnabled() {
			continue
		}
		// The dir name keys the install; a manifest claiming a
		// different name would scatter state across two dirs.
		if m.Name != e.Name() {
			errs = append(errs, fmt.Errorf("%s: manifest name %q does not match directory %q", manifestPath, m.Name, e.Name()))
			continue
		}
		svc, err := NewService(manifestPath, false)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", manifestPath, err))
			continue
		}
		svcs = append(svcs, svc)
	}
	return svcs, errs
}

// RegisterDiscovered folds discovered external connectors into the
// chat-service registry. Compiled-in connectors win name conflicts —
// a dropped manifest must not be able to silently shadow the built-in
// telegram transport.
func RegisterDiscovered(tervaHome string) []error {
	svcs, errs := Discover(tervaHome)
	for _, svc := range svcs {
		if _, exists := chat.Lookup(svc.Name); exists {
			errs = append(errs, fmt.Errorf("connector %q: a connector with this name is compiled into terva; ignoring the external one", svc.Name))
			continue
		}
		chat.Register(svc)
	}
	return errs
}

// RegisterManifest loads ONE manifest as a dev connector for this
// invocation (--connector-manifest). Unlike discovery, an explicit
// manifest may shadow a compiled-in service of the same name — the
// --ext precedent: explicit beats implicit. Returns the service name.
func RegisterManifest(manifestPath string) (string, error) {
	svc, err := NewService(manifestPath, true)
	if err != nil {
		return "", err
	}
	chat.Register(svc)
	return svc.Name, nil
}
