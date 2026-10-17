//go:build terva_desktop && terva_web

package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"terva.sh/terva/packages/i18n"
	"time"

	"github.com/terva-sh/tuohi"
	"github.com/terva-sh/tuohi/dialog"
	"github.com/terva-sh/tuohi/notify"
	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/desktop"
	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/agent/web"
)

func runDesktopMode(ctx context.Context, args build.Args, version string) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	if os.Getenv("LISTEN_FDS") != "" {
		return i18n.Errorf("terva desktop cannot use inherited listeners")
	}
	lock, err := acquireWebHome()
	if err != nil {
		return err
	}
	defer lock.Release()
	cfg, err := config.LoadConfig()
	if err != nil {
		return err
	}
	if cfg.WebOIDC != nil {
		return i18n.Errorf("terva desktop cannot use web_oidc; use a separate TERVA_HOME without daemon authentication configuration")
	}
	// 🔑 The daemon mounts /talkoot/ only while Talkoot is on, so a window
	// opened without it would land on a page that is not there.
	if args.DesktopApp == "talkoot" && (cfg.TalkootEnabled == nil || !*cfg.TalkootEnabled) {
		return i18n.Errorf("terva desktop --desktop-app talkoot needs Talkoot on: turn on \"Talkoot teams\" in settings, or set \"talkoot_enabled\": true in %s", config.ConfigPath())
	}
	if err := desktopHomeAvailable(); err != nil {
		return err
	}
	if err := prepareDesktopBackend(); err != nil {
		return err
	}
	start := desktopApps[args.DesktopApp]
	app := &tuohi.App{Name: start.title, Icon: web.DesktopIcon(), Exit: true}
	if err := checkDesktopBackend(app.Backend()); err != nil {
		return err
	}
	// 🚨 The picker runs after the backend check on purpose. tuohi's dialog
	// loads GTK3 unless GTK4 is already mapped, and only Backend() maps it, so
	// an earlier picker would put GTK3 into a GTK4 process.
	statePath := filepath.Join(config.TervaHome(), desktop.StateFile)
	if args.DesktopApp == "talkoot" {
		desktopWorkspace(&args, statePath, pickDesktopWorkspace)
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return err
	}
	args.WebToken = hex.EncodeToString(secret[:])
	port, busy := desktop.PickPort(statePath, args.DesktopPort, desktop.PortFree)
	if busy != 0 {
		fmt.Fprintln(os.Stderr, i18n.T("terva desktop: port %d is busy, so this window uses a new port and the app's saved preferences start fresh", busy))
	}
	args.WebAddr = net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	window := &desktopWindow{app: app, page: start.page, title: start.title}
	return desktop.Run(ctx, window, args.WebToken, func(ctx context.Context, ready func(string)) error {
		return runWebServer(ctx, args, version, func(network, addr string) {
			// A lost record costs the next launch its preferences, never this
			// one its window, so the error is dropped.
			if _, p, err := net.SplitHostPort(addr); err == nil {
				if n, err := strconv.Atoi(p); err == nil {
					_ = desktop.RememberPort(statePath, n)
				}
			}
			ready("http://" + addr)
		})
	})
}

// desktopWorkspace points args.CWD at the directory the Talkoot window serves,
// and remembers it for the next launch. A fallback to the start directory is
// not remembered, so the next launch asks again.
func desktopWorkspace(args *build.Args, statePath string, pick func() string) desktop.Source {
	dir, src := desktop.ChooseWorkspace(statePath, args.CWD, args.CWDExplicit, desktopHoldsTeam, pick)
	args.CWD = dir
	switch src {
	case desktop.SourceFallback:
		fmt.Fprintln(os.Stderr, i18n.T("terva desktop: no workspace was chosen, so the window serves %s", dir))
	default:
		_ = desktop.RememberWorkspace(statePath, dir)
		fmt.Fprintln(os.Stderr, i18n.T("terva desktop: serving the teams of %s", dir))
	}
	return src
}

// desktopHoldsTeam reports whether a talkoot roster names dir as its home.
// It reads the rosters without validating them, because one broken roster
// still says where the person works.
func desktopHoldsTeam(dir string) bool {
	ids, err := talkoot.List()
	if err != nil {
		return false
	}
	for _, id := range ids {
		path := filepath.Join(talkoot.Dir(), id, talkoot.FileName)
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if r, err := talkoot.Parse(raw, path); err == nil && desktop.SameDir(r.Home, dir) {
			return true
		}
	}
	return false
}

// pickDesktopWorkspace asks the person for the directory their teams work in.
// It returns "" when they cancel or no picker can open.
func pickDesktopWorkspace() string {
	home, _ := os.UserHomeDir()
	paths, err := dialog.Open(dialog.Options{
		Type:      dialog.TypeDirectory,
		Title:     i18n.T("Choose the directory your Talkoot teams work in"),
		Directory: home,
	})
	if err != nil || len(paths) == 0 {
		return ""
	}
	return paths[0]
}

func desktopHomeAvailable() error {
	data, err := os.ReadFile(config.ListenRecordPath())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var rec config.ListenRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return i18n.Errorf("terva desktop cannot read daemon discovery: %w", err)
	}
	// A reachable stale record still names a live owner. A fresh heartbeat
	// also excludes startup when its listener is temporarily unreachable.
	live := rec.FreshAt(time.Now())
	if !live {
		network, address := "tcp", ""
		if strings.HasPrefix(rec.Endpoint, "unix:") {
			network, address = "unix", strings.TrimPrefix(rec.Endpoint, "unix:")
		} else if u, err := url.Parse(rec.Endpoint); err == nil {
			address = u.Host
		}
		if address != "" {
			if conn, err := net.DialTimeout(network, address, time.Second); err == nil {
				conn.Close()
				live = true
			}
		}
	}
	if live {
		return i18n.Errorf("terva desktop refuses a TERVA_HOME with a live daemon; stop it or use a separate TERVA_HOME (attachment will come later)")
	}
	return nil
}

// desktopApps maps --desktop-app to the page the window opens on and its
// title. The empty name is the control panel.
var desktopApps = map[string]struct{ page, title string }{
	"":        {"/", "terva"},
	"talkoot": {"/talkoot/", "terva Talkoot"},
}

// desktopLoginURL is the login page that sends the window on to page once the
// token form is submitted.
func desktopLoginURL(origin, page string) string {
	if page == "/" {
		return origin + "/auth"
	}
	return origin + "/auth?next=" + url.QueryEscape(page)
}

type desktopWindow struct {
	app   *tuohi.App
	view  *tuohi.View
	page  string
	title string
}

// 🔑 The view sets no Title. A Go title would win over the page's, and the
// Talkoot app names the open team and its state in document.title, as it
// does in a browser tab. App.Name holds w.title until the page names itself.
func (w *desktopWindow) Show(origin, token string) error {
	w.view = &tuohi.View{URL: desktopLoginURL(origin, w.page), Frame: true, Width: 1200, Height: 800, Bind: desktopBindings(w.title, notify.Show)}
	w.view.Ready = func() {
		quotedOrigin, _ := json.Marshal(origin)
		quotedToken, _ := json.Marshal(token)
		// Native evaluation fills the existing token form. No token
		// enters a URL, a binding, or browser localStorage.
		w.view.Eval("if (location.origin === " + string(quotedOrigin) + " && location.pathname === '/auth') { const input = document.querySelector('input[name=token]'); if (input) { input.type = 'hidden'; input.autocomplete = 'off'; input.value = " + string(quotedToken) + "; input.form.requestSubmit(); } }")
	}
	return w.app.Show(w.view)
}

// desktopNoticeMax caps each part of a notification the page asks for, in
// runes. The app sends a team name and one short line, never a card's body.
const desktopNoticeMax = 200

// desktopBindings is what the window hands its pages. tuohi denies the web
// Notification API on every engine, so the page asks Go to post one through
// tervaNotify. tuohi installs a binding in trusted documents only, the
// daemon's own origin.
func desktopBindings(app string, show func(name, title, message string) error) map[string]any {
	return map[string]any{
		"tervaNotify": func(title, message string) error {
			return show(app, clipRunes(title, desktopNoticeMax), clipRunes(message, desktopNoticeMax))
		},
	}
}

func clipRunes(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return string(r)
}

func (w *desktopWindow) Wait() error { return w.app.Wait() }
func (w *desktopWindow) Quit()       { w.app.Quit() }
