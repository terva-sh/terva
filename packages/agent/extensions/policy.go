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
