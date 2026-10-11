package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

func init() {
	if os.Getenv("TERVA_TEST_AWS_CLI") != "1" {
		return
	}
	if strings.Join(os.Args[1:], " ") != "configure export-credentials --profile work --format process --no-cli-auto-prompt --no-cli-pager" || os.Getenv("AWS_CLI_AUTO_PROMPT") != "off" || os.Getenv("AWS_PAGER") != "" || os.Getenv("AWS_EC2_METADATA_DISABLED") != "true" {
		os.Exit(11)
	}
	// Getwd reports the resolved directory, /private/var on macOS, so compare
	// both sides after symlinks resolve.
	wd, _ := os.Getwd()
	wd, _ = filepath.EvalSymlinks(wd)
	home, _ := filepath.EvalSymlinks(os.Getenv("TERVA_TEST_AWS_HOME"))
	if wd == "" || wd != home {
		os.Exit(12)
	}
	switch os.Getenv("TERVA_TEST_AWS_MODE") {
	case "failure":
		fmt.Fprint(os.Stderr, "SYNTHETIC-SECRET-DO-NOT-REPORT")
		os.Exit(1)
	case "malformed":
		fmt.Print("SYNTHETIC-SECRET-DO-NOT-REPORT")
	case "oversized":
		fmt.Print(strings.Repeat("x", awsProfileOutputLimit+1))
	case "expiry":
		fmt.Print(`{"Version":1,"AccessKeyId":"FIXTUREAK","SecretAccessKey":"FIXTURESK","Expiration":"not-a-date"}`)
	case "sleep":
		time.Sleep(30 * time.Second)
	default:
		fmt.Print(`{"Version":1,"AccessKeyId":"FIXTUREAK","SecretAccessKey":"FIXTURESK","SessionToken":"FIXTUREST","Expiration":"2099-01-01T00:00:00Z"}`)
	}
	os.Exit(0)
}

func installAWSCLIHelper(t *testing.T) {
	t.Helper()
	dir := testsupport.TempDir(t)
	home := testsupport.TempDir(t)
	name := "aws"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(dst, src)
	closeErr := dst.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("TERVA_TEST_AWS_CLI", "1")
	t.Setenv("TERVA_TEST_AWS_HOME", home)
	t.Setenv("TERVA_TEST_AWS_MODE", "")
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(home, "absent-config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(home, "absent-credentials"))
}

func TestAWSProfileCLIExecutionAndRedaction(t *testing.T) {
	installAWSCLIHelper(t)
	keys, expiry, err := exportAWSProfile(context.Background(), "work")
	if err != nil || keys.AccessKeyID != "FIXTUREAK" || keys.SessionToken != "FIXTUREST" || expiry.IsZero() {
		t.Fatalf("synthetic CLI: keys=%v expiry=%v err=%v", keys, expiry, err)
	}
	for _, mode := range []string{"failure", "malformed", "oversized", "expiry"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("TERVA_TEST_AWS_MODE", mode)
			_, _, err := exportAWSProfile(context.Background(), "work")
			if err == nil || strings.Contains(err.Error(), "SYNTHETIC-SECRET") || strings.Contains(err.Error(), "FIXTURESK") {
				t.Fatalf("expected sanitized failure, got %v", err)
			}
		})
	}
	t.Setenv("TERVA_TEST_AWS_MODE", "sleep")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, _, err := exportAWSProfile(ctx, "work"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout: %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("CLI cancellation did not return within its bound")
	}
}

func TestAWSProfileCLIRequiresProfileAndExecutable(t *testing.T) {
	t.Setenv("PATH", testsupport.TempDir(t))
	for _, profile := range []string{"", "work", "bad\nprofile"} {
		if _, _, err := exportAWSProfile(context.Background(), profile); err == nil {
			t.Fatalf("profile %q unexpectedly resolved", profile)
		}
	}
}

func TestAWSProfileRefreshAndConcurrentReuse(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	var calls atomic.Int32
	s := &awsProfileSource{profile: "work", now: func() time.Time { return now }}
	s.export = func(context.Context, string) (provider.BedrockSigningCredentials, time.Time, error) {
		call := calls.Add(1)
		return provider.BedrockSigningCredentials{AccessKeyID: fmt.Sprint(call), SecretAccessKey: "fixture-secret"}, now.Add(time.Minute), nil
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			keys, err := s.credentials(context.Background())
			if err != nil || keys.AccessKeyID != "1" {
				t.Errorf("concurrent reuse: keys=%v err=%v", keys, err)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("concurrent callers exported %d times", calls.Load())
	}
	now = now.Add(49 * time.Second)
	keys, err := s.credentials(context.Background())
	if err != nil || keys.AccessKeyID != "2" || calls.Load() != 2 {
		t.Fatalf("refresh before expiry: keys=%v calls=%d err=%v", keys, calls.Load(), err)
	}
}

func TestAWSProfileRefreshWaitIsCancelable(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	s := &awsProfileSource{profile: "work", now: time.Now, export: func(ctx context.Context, _ string) (provider.BedrockSigningCredentials, time.Time, error) {
		close(started)
		select {
		case <-release:
			return provider.BedrockSigningCredentials{AccessKeyID: "fixture", SecretAccessKey: "fixture"}, time.Now().Add(time.Hour), nil
		case <-ctx.Done():
			return provider.BedrockSigningCredentials{}, time.Time{}, ctx.Err()
		}
	}}
	owner := make(chan error, 1)
	go func() { _, err := s.credentials(context.Background()); owner <- err }()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := s.credentials(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting caller: %v", err)
	}
	close(release)
	if err := <-owner; err != nil {
		t.Fatal(err)
	}
}

func TestAWSProfileRejectsExpiredExport(t *testing.T) {
	now := time.Now()
	s := &awsProfileSource{profile: "work", now: func() time.Time { return now }, export: func(context.Context, string) (provider.BedrockSigningCredentials, time.Time, error) {
		return provider.BedrockSigningCredentials{AccessKeyID: "fixture", SecretAccessKey: "fixture"}, now.Add(-time.Second), nil
	}}
	if _, err := s.credentials(context.Background()); err == nil {
		t.Fatal("expired exported keys were accepted")
	}
}

func TestAWSProfileFailureCooldownAndStaticRefresh(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	calls := 0
	s := &awsProfileSource{profile: "work", now: func() time.Time { return now }, export: func(context.Context, string) (provider.BedrockSigningCredentials, time.Time, error) {
		calls++
		if calls == 1 {
			return provider.BedrockSigningCredentials{}, time.Time{}, errors.New("synthetic failure")
		}
		return provider.BedrockSigningCredentials{AccessKeyID: fmt.Sprint(calls), SecretAccessKey: "fixture"}, time.Time{}, nil
	}}
	for range 2 {
		if _, err := s.credentials(context.Background()); err == nil {
			t.Fatal("failed export returned credentials")
		}
	}
	if calls != 1 {
		t.Fatalf("failed export repeated %d times", calls)
	}
	now = now.Add(6 * time.Second)
	if _, err := s.credentials(context.Background()); err != nil {
		t.Fatal(err)
	}
	now = now.Add(6 * time.Minute)
	if keys, err := s.credentials(context.Background()); err != nil || keys.AccessKeyID != "3" {
		t.Fatalf("static credentials did not refresh: keys=%v err=%v", keys, err)
	}
}

func TestAWSProfileInternalTimeoutCooldown(t *testing.T) {
	now := time.Now()
	calls := 0
	s := &awsProfileSource{profile: "work", now: func() time.Time { return now }, export: func(context.Context, string) (provider.BedrockSigningCredentials, time.Time, error) {
		calls++
		return provider.BedrockSigningCredentials{}, time.Time{}, fmt.Errorf("AWS profile refresh: %w", context.DeadlineExceeded)
	}}
	for range 2 {
		if _, err := s.credentials(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("internal timeout lost its identity: %v", err)
		}
	}
	if calls != 1 {
		t.Fatal("internal timeout bypassed the failure cooldown")
	}
	now = now.Add(6 * time.Second)
	_, _ = s.credentials(context.Background())
	if calls != 2 {
		t.Fatal("internal timeout prevented a retry after cooldown")
	}
}

func TestAWSProfileCallerCancellationDoesNotCoolDown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	s := &awsProfileSource{profile: "work", now: time.Now, export: func(ctx context.Context, _ string) (provider.BedrockSigningCredentials, time.Time, error) {
		calls++
		if calls == 1 {
			cancel()
			return provider.BedrockSigningCredentials{}, time.Time{}, ctx.Err()
		}
		return provider.BedrockSigningCredentials{AccessKeyID: "fixture-key", SecretAccessKey: "fixture-secret"}, time.Time{}, nil
	}}
	if _, err := s.credentials(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("caller cancellation: %v", err)
	}
	if _, err := s.credentials(context.Background()); err != nil || calls != 2 {
		t.Fatal("caller cancellation prevented another caller from refreshing")
	}
}
