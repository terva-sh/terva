package build

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/extdriver"
	"terva.sh/terva/packages/testsupport"
)

// TestNewExtensionManagerAppliesTheConfiguredHelloTimeout: the whole reason
// NewExtensionManager exists is to carry config-sourced settings into a driver
// that deliberately cannot read config itself.
func TestNewExtensionManagerAppliesTheConfiguredHelloTimeout(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	if err := config.SaveConfig(config.Config{ExtensionHelloTimeout: 45}); err != nil {
		t.Fatalf("save config: %v", err)
	}
	mgr := NewExtensionManager(home, "", "0.0.0-test", "anthropic", "claude-opus-4-7", NonInteractiveExtHooks{})
	if got := mgr.HelloTimeout(); got != 45*time.Second {
		t.Errorf("HelloTimeout = %s, want 45s — the config value never reached the driver", got)
	}
}

// TestHelloTimeoutFallsBackToTheDefault covers both "key absent" and "value
// nonsense": neither may leave the driver with a zero deadline, which would
// make every spawn fail instantly.
func TestHelloTimeoutFallsBackToTheDefault(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  config.Config
	}{
		{"unset", config.Config{}},
		{"zero", config.Config{ExtensionHelloTimeout: 0}},
		{"negative", config.Config{ExtensionHelloTimeout: -5}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := testsupport.TempDir(t)
			t.Setenv("TERVA_HOME", home)
			if err := config.SaveConfig(tc.cfg); err != nil {
				t.Fatalf("save config: %v", err)
			}
			mgr := NewExtensionManager(home, "", "0.0.0-test", "anthropic", "claude-opus-4-7", NonInteractiveExtHooks{})
			if got := mgr.HelloTimeout(); got != extdriver.DefaultHelloTimeout {
				t.Errorf("HelloTimeout = %s, want the %s default", got, extdriver.DefaultHelloTimeout)
			}
		})
	}
}

// TestHelloTimeoutIsClamped: raising the deadline for a slow build is the
// point; a stray extra digit turning terva into something that looks hung is
// not. The clamp is the difference between a knob and a footgun.
func TestHelloTimeoutIsClamped(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	if err := config.SaveConfig(config.Config{ExtensionHelloTimeout: 86400}); err != nil {
		t.Fatalf("save config: %v", err)
	}
	mgr := NewExtensionManager(home, "", "0.0.0-test", "anthropic", "claude-opus-4-7", NonInteractiveExtHooks{})
	if got := mgr.HelloTimeout(); got != helloTimeoutCeiling {
		t.Errorf("HelloTimeout = %s, want the %s ceiling", got, helloTimeoutCeiling)
	}
}

// TestHostsBuildExtensionManagersThroughBuild is what keeps NewExtensionManager
// from becoming a convention nobody follows. extensions.New takes no config, so
// a host that calls it directly gets a manager with the DEFAULT hello timeout
// no matter what the user configured — a silent, per-host divergence that no
// other test would notice. Every production caller must go through this
// package; tests may construct bare managers freely.
func TestHostsBuildExtensionManagersThroughBuild(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	selfDir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	var offenders []string
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "dist", "vendor":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		if filepath.Dir(abs) == selfDir {
			return nil // this package is where the wrapping happens
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(body), "extensions.New(") {
			rel, _ := filepath.Rel(root, path)
			offenders = append(offenders, rel)
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk: %v", walkErr)
	}
	if len(offenders) > 0 {
		t.Errorf("these hosts call extensions.New directly and so silently ignore extension_hello_timeout; use build.NewExtensionManager instead: %v", offenders)
	}
}
