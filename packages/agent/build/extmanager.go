package build

import (
	"time"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/extensions"
)

// helloTimeoutCeiling caps the configured hello deadline. An operator raising
// it for a slow build is the point; an operator typing a stray extra digit and
// then wondering why terva appears to hang is not. Lives here rather than in
// the driver so the driver stays a mechanism with no policy in it.
const helloTimeoutCeiling = 10 * time.Minute

// NewExtensionManager constructs an extension manager with every
// config-derived runtime setting already applied. It is the ONLY way a host
// should build one: extensions and extdriver are deliberately config-free
// (they take injected values and never read the file), so anything sourced
// from config must be applied by a layer that can see both — and a layer that
// is "one line each host remembers to write" is a line some host eventually
// won't. TestHostsBuildExtensionManagersThroughBuild enforces it.
//
// The arguments mirror extensions.New exactly, so adopting it is a rename at
// the call site.
func NewExtensionManager(tervaHome, cwd, tervaVersion, provider, model string, hooks extensions.HostHooks) *extensions.Manager {
	mgr := extensions.New(tervaHome, cwd, tervaVersion, provider, model, hooks)
	mgr.SetHelloTimeout(configuredHelloTimeout())
	return mgr
}

// configuredHelloTimeout reads extension_hello_timeout (seconds) from the user
// config, clamped to the ceiling. Returns 0 — "use extdriver's default" — when
// the key is unset or the config can't be read: a missing or corrupt config
// must never be the reason extensions stop loading.
func configuredHelloTimeout() time.Duration {
	cfg, err := config.LoadConfig()
	if err != nil || cfg.ExtensionHelloTimeout <= 0 {
		return 0
	}
	v := time.Duration(cfg.ExtensionHelloTimeout) * time.Second
	if v > helloTimeoutCeiling {
		return helloTimeoutCeiling
	}
	return v
}
