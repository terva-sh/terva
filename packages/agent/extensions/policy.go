package extensions

// Host load-policy state: which extensions config forbids loading, and
// the Workspace Trust verdict for the cwd. Both gate discovery before
// the wire driver ever spawns a subprocess, so they live in the
// integration layer rather than in extdriver. SetContextDisabled (the
// model-context opt-out) is promoted from the embedded Driver, since
// that filtering is dep-free and lives with the context aggregation.

// SetDisabledExtensions records which extensions must not be loaded at
// all (from the resolved user ∪ project config). MUST be called before
// Discover / LoadExplicit — loadOne consults it to skip spawning a
// disabled extension entirely.
func (m *Manager) SetDisabledExtensions(names []string) {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		if n != "" {
			set[n] = true
		}
	}
	m.mu.Lock()
	m.disabledExtensions = set
	m.mu.Unlock()
}

// extensionLoadDisabled reports whether an extension name is in the
// load-disable set.
func (m *Manager) extensionLoadDisabled(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.disabledExtensions[name]
}

// SetAllowedExtensions records a per-run allowlist (the --extensions
// flag): when non-empty, only DISCOVERED extensions whose manifest name
// is in the set are loaded. Explicit --ext paths bypass it (pointing at
// a directory is already explicit consent — the flow used when
// developing an extension), and the disable list still subtracts, so
// the allowlist is restrict-only: it can narrow a run, never resurrect
// something config or trust has turned off. Empty or nil means no
// allowlist (everything installed loads, as before). MUST be called
// before Discover.
func (m *Manager) SetAllowedExtensions(names []string) {
	var set map[string]bool
	if len(names) > 0 {
		set = make(map[string]bool, len(names))
		for _, n := range names {
			if n != "" {
				set[n] = true
			}
		}
	}
	m.mu.Lock()
	m.allowedExtensions = set
	m.mu.Unlock()
}

// extensionAllowlisted reports whether an extension name passes the
// per-run allowlist (vacuously true when no allowlist is set).
func (m *Manager) extensionAllowlisted(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.allowedExtensions == nil || m.allowedExtensions[name]
}

// SetProjectTrusted records the Workspace Trust verdict for the
// manager's cwd. When false (the default — a fresh Manager is
// untrusted), searchDirs drops the project-local extension roots so a
// cloned/untrusted repo's extensions are never discovered or spawned.
// MUST be called before Discover. See docs/plans/workspace-trust.md.
func (m *Manager) SetProjectTrusted(trusted bool) {
	m.mu.Lock()
	m.projectTrusted = trusted
	m.mu.Unlock()
}

// isProjectTrusted reports the current trust verdict under the lock.
func (m *Manager) isProjectTrusted() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.projectTrusted
}

// SetAdopt configures the global extensions a project-scoped agent re-adopts:
// root is the user's global extensions dir, names are the ones to pull from it.
// Discover loads them only when the project is trusted (they're project-declared
// content) and only for names the project doesn't already provide. MUST be
// called before Discover. Empty names disables adoption.
func (m *Manager) SetAdopt(root string, names []string) {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		// adopt_extensions is project-supplied (untrusted) config. Reject any
		// entry that isn't a bare extension name, so a crafted "../../evil" or
		// absolute path can't escape the global extensions root in Discover.
		// This is the authoritative guard (Discover re-checks defensively).
		if isPlainExtensionName(n) {
			set[n] = true
		}
	}
	m.mu.Lock()
	m.adoptRoot = root
	m.adoptNames = set
	m.mu.Unlock()
}

// adoptTargets returns the global extensions-root and the names to adopt from
// it, but ONLY when the project is trusted (adopted extensions are
// project-declared, so they obey the same trust gate). Returns ("", nil) when
// adoption is off or untrusted.
func (m *Manager) adoptTargets() (root string, names []string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.projectTrusted || m.adoptRoot == "" || len(m.adoptNames) == 0 {
		return "", nil
	}
	names = make([]string, 0, len(m.adoptNames))
	for name := range m.adoptNames {
		names = append(names, name)
	}
	return m.adoptRoot, names
}
