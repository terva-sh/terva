//go:build terva_desktop && terva_web

package agent

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
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

func TestDesktopArchiveSurvivesSelfUpdate(t *testing.T) {
	name, _, err := releaseAssetName("1.2.3")
	if err != nil || !strings.HasPrefix(name, "terva-desktop_1.2.3_") {
		t.Fatalf("desktop updater chose %q: %v", name, err)
	}
	dir := testsupport.TempDir(t)
	archive := filepath.Join(dir, "desktop.tar.gz")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	body := []byte("desktop binary fixture")
	if err := tw.WriteHeader(&tar.Header{Name: "terva-desktop", Mode: 0755, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "extracted")
	if err := os.Mkdir(out, 0700); err != nil {
		t.Fatal(err)
	}
	if err := extractArchive(archive, "tar.gz", out); err != nil {
		t.Fatal(err)
	}
	bin, _ := findArtifactBinary(out, "linux")
	if bin == "" {
		t.Fatal("desktop archive was not recognized")
	}
	current := filepath.Join(dir, "installed")
	if err := os.WriteFile(current, []byte("old binary"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := replaceBinary(current, bin); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(current)
	if err != nil || string(got) != string(body) {
		t.Fatalf("update installed %q: %v", got, err)
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
