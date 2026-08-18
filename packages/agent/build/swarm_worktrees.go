package build

import "terva.sh/terva/packages/agent/config"

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

// SwarmWorktreesActive is ResolveSwarmWorktrees with the config read for you —
// the form every caller that holds Args actually wants.
//
// Two places need this answer and must never disagree: the workspace, which
// installs the lease hook, and the system prompt, which tells the model where
// its sub-agents' files will be. A second inline LoadConfig at the prompt site
// would be one edit away from drifting from the one that leases.
//
// An unreadable config degrades to the flag alone, which is the same reading
// the workspace's `uc, _ :=` already takes: the explicit --swarm-worktrees is
// never lost to a broken file.
func SwarmWorktreesActive(args Args) bool {
	uc, _ := config.LoadConfig()
	return ResolveSwarmWorktrees(args.SwarmWorktrees, uc.SwarmWorktrees)
}
