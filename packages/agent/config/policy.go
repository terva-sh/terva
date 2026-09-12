package config

import (
	"strings"
	"time"

	"terva.sh/terva/packages/core"
)

// Policy derived from config. These read config and nothing else, so they
// belong beside it rather than in whatever host happened to need them first.

// AutoCompactPolicy reads the live `auto_compact` knob from config for
// core's threshold checks. Called per check (like AutoSwarmEnabled), so
// a config edit applies to the running session without a rebuild. The
// raw string passes through; core validates and falls back to "steps"
// on anything unknown.
func AutoCompactPolicy() core.AutoCompactMode {
	cfg, err := LoadConfig()
	if err != nil {
		return core.AutoCompactSteps
	}
	return core.AutoCompactMode(strings.ToLower(strings.TrimSpace(cfg.AutoCompact)))
}

// AutoSwarmEnabled reads the current auto-swarm flag from config.
// Used by the swarm_spawn tool at call time to gate execution.
func AutoSwarmEnabled() bool {
	cfg, err := LoadConfig()
	if err != nil {
		return false
	}
	return cfg.AutoSwarmEnabled != nil && *cfg.AutoSwarmEnabled
}

// TicketsEnabled reports whether the ticket_* tools may register for cwd.
// It reads the merged view, so an explicit false on either the user layer or
// the project layer turns them off. The project layer is restrict-only (see
// ResolveConfig), so a repository can refuse the tools for its own directory
// and can never grant them back.
//
// trustProject is deliberately false here: the ticket key is not trust-gated,
// for the DisableMCP reason, and the fields ResolveConfig does gate on trust
// (context files, provider, model) are not read from this view.
func TicketsEnabled(cwd string) bool {
	eff := ResolveConfig(cwd, false)
	return eff.Config.Tickets == nil || *eff.Config.Tickets
}

// ExternalWorkersEnabled reads the current external-workers flag from config.
// Used by the swarm_spawn backend gate at spawn time, live per call like
// AutoSwarmEnabled, so a config edit applies without restarting the session.
func ExternalWorkersEnabled() bool {
	cfg, err := LoadConfig()
	if err != nil {
		return false
	}
	return cfg.ExternalWorkersEnabled != nil && *cfg.ExternalWorkersEnabled
}

// DefaultSwarmRetentionDays is how long a finished swarm agent's record stays
// in the live tree when swarm_retention_days is not set.
//
// A week, because the record's value decays fast but not instantly: a person
// who wants to read what a sub-agent did wants it the same day or the next
// one, and the archive keeps the record afterwards rather than destroying it.
const DefaultSwarmRetentionDays = 7

// SwarmRetentionAge is how old a finished agent must be before the retention
// sweep archives it. Zero means the sweep is off, which is what a configured
// zero or negative value asks for.
//
// Read live per call, like AutoSwarmEnabled, so a config edit applies without
// restarting the session. A config that fails to load returns the default
// rather than zero: an unreadable file is not a request to disable anything.
func SwarmRetentionAge() time.Duration {
	days := DefaultSwarmRetentionDays
	if cfg, err := LoadConfig(); err == nil && cfg.SwarmRetentionDays != nil {
		days = *cfg.SwarmRetentionDays
	}
	if days <= 0 {
		return 0
	}
	return time.Duration(days) * 24 * time.Hour
}

// AutoSwarmNudgeEnabled reports whether the proactive-delegation nudge (the
// swarm system addendum) should be injected. Independent of AutoSwarmEnabled
// and defaults ON (nil = true), so enabling auto-swarm keeps today's behavior;
// set auto_swarm_nudge=false to keep the tool but drop the nudge.
func AutoSwarmNudgeEnabled() bool {
	cfg, err := LoadConfig()
	if err != nil {
		return true
	}
	return cfg.AutoSwarmNudge == nil || *cfg.AutoSwarmNudge
}
