package config

import (
	"fmt"
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

// TalkootChat reads the talkoot_chat key from the user layer. It returns nil
// when the key is unset, when it names no talkoot, or when Talkoot is off: a
// bridge to a team that the daemon does not load serves nothing.
func TalkootChat() *TalkootChatConfig {
	cfg, err := LoadConfig()
	if err != nil || cfg.TalkootChat == nil || cfg.TalkootChat.Talkoot == "" {
		return nil
	}
	if cfg.TalkootEnabled == nil || !*cfg.TalkootEnabled {
		return nil
	}
	c := *cfg.TalkootChat
	return &c
}

// DefaultTalkootWakeDelay is the longest delay of a wake when the
// talkoot_wakes key names none.
const DefaultTalkootWakeDelay = 24 * time.Hour

// TalkootWakePolicy is the talkoot_wakes key as the daemon applies it.
type TalkootWakePolicy struct {
	// On reports whether a member may set a wake.
	On bool
	// PerMember caps the open wakes of one member.
	PerMember int
	// MaxDelay caps the delay of one wake.
	MaxDelay time.Duration
	// Problem says why the key is set and wakes are still off. It is empty
	// when the key is unset, and when wakes are on.
	Problem string
}

// TalkootWakes reads the talkoot_wakes key from the user layer. Read live per
// call, so an edit applies to the next wake without a restart.
//
// 🚨 Every doubt reads as off. A cap that does not parse must not become no
// cap, so a bad max_delay turns wakes off and Problem says why.
func TalkootWakes() TalkootWakePolicy {
	cfg, err := LoadConfig()
	if err != nil || cfg.TalkootWakes == nil {
		return TalkootWakePolicy{}
	}
	if cfg.TalkootEnabled == nil || !*cfg.TalkootEnabled {
		return TalkootWakePolicy{Problem: "talkoot_wakes is set, and talkoot_enabled is off"}
	}
	return talkootWakePolicy(*cfg.TalkootWakes)
}

func talkootWakePolicy(c TalkootWakesConfig) TalkootWakePolicy {
	if c.PerMember < 1 {
		return TalkootWakePolicy{Problem: fmt.Sprintf("talkoot_wakes.per_member is %d, and wakes need 1 or more", c.PerMember)}
	}
	max := DefaultTalkootWakeDelay
	if c.MaxDelay != "" {
		d, err := time.ParseDuration(c.MaxDelay)
		if err != nil || d < time.Minute {
			return TalkootWakePolicy{Problem: fmt.Sprintf("talkoot_wakes.max_delay %q is not a duration of one minute or more, such as 90m or 24h", c.MaxDelay)}
		}
		max = d
	}
	return TalkootWakePolicy{On: true, PerMember: c.PerMember, MaxDelay: max}
}

// TalkootEnabled reads the talkoot_enabled flag from the user layer. Read live
// per call like ExternalWorkersEnabled, so an edit applies without a restart.
func TalkootEnabled() bool {
	cfg, err := LoadConfig()
	if err != nil {
		return false
	}
	return cfg.TalkootEnabled != nil && *cfg.TalkootEnabled
}

// AWSCLIProfileEnabled reports user consent for the Bedrock CLI adapter.
func AWSCLIProfileEnabled() bool {
	cfg, err := LoadConfig()
	return err == nil && cfg.Providers["amazon-bedrock"].AWSCLICredentials
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
