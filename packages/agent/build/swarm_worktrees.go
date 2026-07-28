package build

// ResolveSwarmWorktrees rode in gate.go, whose header called it "the headless
// confirm gate and the swarm-worktree flag resolution" — two unrelated things
// in one file, which only became visible when the gate left for package
// permissions and tried to take this with it. Worktree isolation is a swarm
// wiring decision, not a permission.

// ResolveSwarmWorktrees decides whether per-agent swarm worktree
// isolation is on. The --swarm-worktrees flag (flagOverride, non-nil
// when the flag was given) wins over the user config's swarm_worktrees
// (cfg). nil/absent in both means off — today's behavior. Mirrors the
// bool-pointer precedence used for the other swarm/picker settings.
func ResolveSwarmWorktrees(flagOverride, cfg *bool) bool {
	if flagOverride != nil {
		return *flagOverride
	}
	return cfg != nil && *cfg
}
