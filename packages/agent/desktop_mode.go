//go:build terva_desktop && terva_web

package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"terva.sh/terva/packages/i18n"
	"time"

	"github.com/terva-sh/tuohi"
	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/desktop"
	"terva.sh/terva/packages/agent/web"
)

const desktopArtifact = true

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
	if err := desktopHomeAvailable(); err != nil {
		return err
	}
	if err := prepareDesktopBackend(); err != nil {
		return err
	}
	app := &tuohi.App{Name: "terva", Icon: web.DesktopIcon(), Exit: true}
	if err := checkDesktopBackend(app.Backend()); err != nil {
		return err
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return err
	}
	args.WebToken = hex.EncodeToString(secret[:])
	args.WebAddr = net.JoinHostPort("127.0.0.1", strconv.Itoa(args.DesktopPort))
	window := &desktopWindow{app: app}
	return desktop.Run(ctx, window, args.WebToken, func(ctx context.Context, ready func(string)) error {
		return runWebServer(ctx, args, version, func(network, addr string) {
			ready("http://" + addr)
		})
	})
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

type desktopWindow struct {
	app  *tuohi.App
	view *tuohi.View
}

func (w *desktopWindow) Show(origin, token string) error {
	w.view = &tuohi.View{URL: origin + "/auth", Title: "terva", Frame: true, Width: 1200, Height: 800}
	w.view.Ready = func() {
		quotedOrigin, _ := json.Marshal(origin)
		quotedToken, _ := json.Marshal(token)
		// Native evaluation fills the existing token form. No token
		// enters a URL, a binding, or browser localStorage.
		w.view.Eval("if (location.origin === " + string(quotedOrigin) + " && location.pathname === '/auth') { const input = document.querySelector('input[name=token]'); if (input) { input.type = 'hidden'; input.autocomplete = 'off'; input.value = " + string(quotedToken) + "; input.form.requestSubmit(); } }")
	}
	return w.app.Show(w.view)
}

func (w *desktopWindow) Wait() error { return w.app.Wait() }
func (w *desktopWindow) Quit()       { w.app.Quit() }
