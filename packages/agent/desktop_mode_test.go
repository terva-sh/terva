//go:build terva_desktop && terva_web

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/desktop"
	"terva.sh/terva/packages/agent/workspace"
	"terva.sh/terva/packages/relaunch"
	"terva.sh/terva/packages/testsupport"
)

func TestDesktopAndWebShareHomeOwnership(t *testing.T) {
	home := filepath.Join(testsupport.TempDir(t), "new-home")
	t.Setenv("TERVA_HOME", home)
	guard, err := acquireWebHome()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(guard.Release)
	args := build.Args{CWD: home, WebAddr: "127.0.0.1:0", AllowWebLogin: true, NoExt: true}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := runWebMode(ctx, args, "test"); err == nil || !strings.Contains(err.Error(), "already owns") {
		t.Fatalf("web ignored a preparing owner: %v", err)
	}
	args.Desktop = true
	if err := runDesktopMode(ctx, args, "test"); err == nil || !strings.Contains(err.Error(), "already owns") {
		t.Fatalf("desktop ignored a preparing owner: %v", err)
	}
	guard.Release()
	args.Desktop = false
	done := make(chan error, 1)
	stopped := make(chan struct{})
	go func(serverArgs build.Args) { defer close(stopped); done <- runWebMode(ctx, serverArgs, "test") }(args)
	t.Cleanup(func() { cancel(); <-stopped })
	for {
		if _, ready := config.ReadListenRecord(); ready {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("web startup failed: %v", err)
		case <-ctx.Done():
			t.Fatal("web did not bind")
		case <-time.After(10 * time.Millisecond):
		}
	}
	args.Desktop = true
	if err := runDesktopMode(ctx, args, "test"); err == nil || !strings.Contains(err.Error(), "already owns") {
		t.Fatalf("desktop started beside web: %v", err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("web shutdown failed: %v", err)
	}
	guard, err = acquireWebHome()
	if err != nil {
		t.Fatalf("web retained ownership after shutdown: %v", err)
	}
	guard.Release()
}

func TestDesktopFreshHomeAndRelaunch(t *testing.T) {
	if !relaunch.Supported() {
		t.Skip("self-restart is disabled on this platform")
	}
	if os.Getenv("TERVA_DESKTOP_LOCK_PROBE") == "1" {
		guard, err := acquireWebHome()
		if err != nil {
			t.Fatal(err)
		}
		defer guard.Release()
		if relaunch.Handoff("HOME_LOCK") == "replacement" {
			fmt.Println("home lock reacquired after exec")
			return
		}
		fmt.Println("fresh home lock held before exec")
		relaunch.SetHandoff("HOME_LOCK", "replacement")
		relaunch.Enable()
		if err := relaunch.Trigger("home lock regression"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Second)
		t.Fatal("process image was not replaced")
	}
	dir := testsupport.TempDir(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(dir, "relaunch-probe")
	if err := os.WriteFile(probe, data, 0755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, probe, "-test.run=^TestDesktopFreshHomeAndRelaunch$")
	cmd.Env = append(os.Environ(), "TERVA_DESKTOP_LOCK_PROBE=1", "TERVA_HOME="+filepath.Join(dir, "fresh-home"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("relaunch probe failed: %v\n%s", err, out)
	}
	for _, marker := range []string{"fresh home lock held before exec", "home lock reacquired after exec"} {
		if !strings.Contains(string(out), marker) {
			t.Fatalf("missing %q in probe output: %s", marker, out)
		}
	}
}

// The desktop window ships in the full build, so a desktop install updates
// from the full archive.
func TestDesktopUpdatesFromTheFullArchive(t *testing.T) {
	name, _, err := releaseAssetName("1.2.3")
	if err != nil || !strings.HasPrefix(name, "terva_1.2.3_") {
		t.Fatalf("desktop updater chose %q: %v", name, err)
	}
}

func TestDesktopHomeConflictAndRecovery(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	if err := desktopHomeAvailable(); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	rec := config.ListenRecord{Endpoint: config.EndpointForListener("tcp", ln.Addr().String()), Heartbeat: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)}
	write := func() {
		data, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(config.ListenRecordPath(), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	if err := desktopHomeAvailable(); err == nil {
		t.Fatal("accepted live daemon with stale heartbeat")
	}
	ln.Close()
	if err := desktopHomeAvailable(); err != nil {
		t.Fatalf("dead daemon blocked recovery: %v", err)
	}
	rec.Heartbeat = time.Now().UTC().Format(time.RFC3339)
	write()
	if err := desktopHomeAvailable(); err == nil {
		t.Fatal("accepted fresh daemon heartbeat")
	}
}

func TestDesktopAppStartPage(t *testing.T) {
	origin := "http://127.0.0.1:9134"
	if got := desktopLoginURL(origin, desktopApps[""].page); got != origin+"/auth" {
		t.Fatalf("panel login moved: %s", got)
	}
	got := desktopLoginURL(origin, desktopApps["talkoot"].page)
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	// The native token fill runs only on /auth, so the query must not move it.
	if u.Path != "/auth" || u.Query().Get("next") != "/talkoot/" {
		t.Fatalf("talkoot window opens on %s", got)
	}
	if desktopApps["talkoot"].title != "terva Talkoot" {
		t.Fatalf("talkoot window title %q", desktopApps["talkoot"].title)
	}
}

// The page posts a notification through tervaNotify, because the webview
// denies the web API. It goes out under the window's name, clipped.
func TestDesktopNotifyBinding(t *testing.T) {
	var got []string
	b := desktopBindings("terva Talkoot", func(name, title, message string) error {
		got = append(got, name, title, message)
		return nil
	})
	fn, ok := b["tervaNotify"].(func(string, string) error)
	if !ok {
		t.Fatalf("tervaNotify is %T", b["tervaNotify"])
	}
	if err := fn(" crew ", strings.Repeat("é", 500)); err != nil {
		t.Fatal(err)
	}
	if got[0] != "terva Talkoot" || got[1] != "crew" {
		t.Fatalf("posted %q", got[:2])
	}
	if r := []rune(got[2]); len(r) != desktopNoticeMax || r[len(r)-1] != '…' {
		t.Fatalf("message kept %d runes", len(r))
	}
}

// A launcher starts the window in a directory with no teams and no checkout.
// The picker's choice becomes the workspace, the next launch reopens it, and
// a team made there binds to it.
func TestTheDesktopWindowServesThePickedDirectory(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"talkoot_enabled": true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	start := testsupport.TempDir(t)
	chosen := testsupport.TempDir(t)
	statePath := filepath.Join(home, desktop.StateFile)

	args := build.Args{CWD: start}
	if src := desktopWorkspace(&args, statePath, func() string { return chosen }); src != desktop.SourcePicked || !desktop.SameDir(args.CWD, chosen) {
		t.Fatalf("first launch serves %s (%s), want the picked %s", args.CWD, src, chosen)
	}
	again := build.Args{CWD: start}
	if src := desktopWorkspace(&again, statePath, func() string { t.Fatal("the picker opened on a second launch"); return "" }); src != desktop.SourceRemembered || !desktop.SameDir(again.CWD, chosen) {
		t.Fatalf("second launch serves %s (%s), want the remembered %s", again.CWD, src, chosen)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)
	ws, err := workspace.NewWorkspace(build.Args{
		Provider: "openai-compatible", BaseURL: srv.URL, APIKey: "k", Model: "fake-model",
		CWD: again.CWD, NoExt: true, NoMCP: true,
	}, "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	pv, err := ws.PreviewTalkoot(t.Context(), ctrlproto.TalkootPreviewParams{Template: "coding", ID: "team"})
	if err != nil {
		t.Fatal(err)
	}
	if !desktop.SameDir(pv.Home, chosen) {
		t.Fatalf("a new team binds to %s, want the picked %s", pv.Home, chosen)
	}
	if _, err := ws.CreateTalkoot(t.Context(), ctrlproto.TalkootCreateParams{ID: "team", Template: "coding", Digest: pv.Digest}); err != nil {
		t.Fatal(err)
	}
	if !desktopHoldsTeam(chosen) || desktopHoldsTeam(start) {
		t.Fatal("the created team does not name the picked directory as its home")
	}
}

func TestDesktopTalkootNeedsTheFlag(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	args := build.Args{CWD: home, Desktop: true, DesktopApp: "talkoot", NoExt: true}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := runDesktopMode(ctx, args, "test"); err == nil || !strings.Contains(err.Error(), "talkoot_enabled") {
		t.Fatalf("desktop opened Talkoot while it is off: %v", err)
	}
}
