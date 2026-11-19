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

// The top of each limit of a routine. A member's ask and a default of a kind
// have the same top, so every default is a value that a routine can store.
// packages/agent/talkoot names these for a routine.
const (
	TalkootRoutineMaxFollowUps      = 100
	TalkootRoutineMaxReviewsPerDay  = 1000
	TalkootRoutineMaxSpendUSDPerDay = 10000
	TalkootRoutineMaxEnd            = 100000
)

// TalkootRoutineDefaults holds the defaults of one kind of routine: the six
// limits that a card hides when a routine does not change them.
type TalkootRoutineDefaults struct {
	// EndAfterRuns and EndAfterDays are the end condition. Zero is no end.
	EndAfterRuns   int
	EndAfterDays   int
	SpendUSDPerDay float64
	ReviewsPerDay  int
	FollowUps      int
	// CatchUp says whether a missed window gets one late run.
	CatchUp bool
}

// The kinds of a routine, as packages/agent/talkoot names them.
const (
	routineWatcher = "watcher"
	routineBrief   = "brief"
	routineTask    = "task"
)

// builtinRoutineDefaults are the defaults of a kind that the user
// configuration leaves out.
//
// 🔑 A watcher and a task catch up, and a brief does not. A check that the
// daemon missed still has to run one time. A brief that arrives six hours
// late is worse than no brief (decision 0029 rule 7).
//
// Only a task asks for a review or sets a follow-up by default. No kind has
// an end by default, because a person deletes a routine from its list.
var builtinRoutineDefaults = map[string]TalkootRoutineDefaults{
	routineWatcher: {SpendUSDPerDay: 1, CatchUp: true},
	routineBrief:   {SpendUSDPerDay: 1},
	routineTask:    {SpendUSDPerDay: 5, ReviewsPerDay: 2, FollowUps: 2, CatchUp: true},
}

// TalkootRoutinePolicy says whether a talkoot member may ask for a routine,
// and the defaults of each kind.
type TalkootRoutinePolicy struct {
	On bool
	// Max caps the routines of one talkoot.
	Max int
	// Kinds holds the defaults of every kind, the built-in ones filled in.
	Kinds map[string]TalkootRoutineDefaults
	// QuestionWait is how long a question waits for a person in a chain
	// that a routine began. Read it through Wait, which has a value when
	// routines are off too. A key with a problem in another field keeps the
	// wait that it set.
	QuestionWait time.Duration
	// Problem says why the key is set and routines are still off. It is
	// empty when the key is unset, and when routines are on.
	Problem string
}

// The wait for a person in a chain that a routine began: the default, and the
// two ends of what the key may set.
//
// The default covers an evening away from the chat. A run that waits holds
// its routine, so the default also leaves a routine with one run a day free
// before its next time.
const (
	DefaultTalkootQuestionWait = 4 * time.Hour
	MinTalkootQuestionWait     = time.Minute
	MaxTalkootQuestionWait     = 7 * 24 * time.Hour
)

// Wait returns how long a question waits for a person in a chain that a
// routine began.
//
// 🔑 It has a value when routines are off. A person can turn routines off
// while a chain of one still works, and that chain's question still needs a
// deadline.
func (p TalkootRoutinePolicy) Wait() time.Duration {
	if p.QuestionWait > 0 {
		return p.QuestionWait
	}
	return DefaultTalkootQuestionWait
}

// TalkootRoutines reads the talkoot_routines key from the user layer. Read
// live per call, so an edit applies to the next card without a restart.
//
// 🚨 Every doubt reads as off. A default is authority that a card hides, so a
// kind the daemon does not know, or a negative limit, turns routines off and
// Problem says why. It never becomes a built-in default in silence.
func TalkootRoutines() TalkootRoutinePolicy {
	cfg, err := LoadConfig()
	if err != nil || cfg.TalkootRoutines == nil {
		return TalkootRoutinePolicy{}
	}
	if cfg.TalkootEnabled == nil || !*cfg.TalkootEnabled {
		return TalkootRoutinePolicy{Problem: "talkoot_routines is set, and talkoot_enabled is off"}
	}
	return talkootRoutinePolicy(*cfg.TalkootRoutines)
}

func talkootRoutinePolicy(c TalkootRoutinesConfig) (p TalkootRoutinePolicy) {
	// 🚨 The wait is read first, and it stays beside a problem in any other
	// field. A chain of a routine can still work while the key turns
	// routines off. A person who set a short wait and then mistyped a limit
	// must not get the default wait in silence.
	wait := DefaultTalkootQuestionWait
	if c.QuestionWait != "" {
		d, err := time.ParseDuration(c.QuestionWait)
		if err != nil || d < MinTalkootQuestionWait || d > MaxTalkootQuestionWait {
			return TalkootRoutinePolicy{Problem: fmt.Sprintf("talkoot_routines.question_wait %q is not a duration from one minute to 7 days, such as 90m or 8h", c.QuestionWait)}
		}
		wait = d
	}
	defer func() { p.QuestionWait = wait }()
	if c.Max < 1 {
		return TalkootRoutinePolicy{Problem: fmt.Sprintf("talkoot_routines.max is %d, and routines need 1 or more", c.Max)}
	}
	kinds := make(map[string]TalkootRoutineDefaults, len(builtinRoutineDefaults))
	for kind, d := range builtinRoutineDefaults {
		kinds[kind] = d
	}
	for kind, k := range c.Kinds {
		d, ok := kinds[kind]
		if !ok {
			return TalkootRoutinePolicy{Problem: fmt.Sprintf("talkoot_routines.kinds names %q, and the kinds are watcher, brief and task", kind)}
		}
		// 🚨 A default has the top that an ask has. An approval gives a
		// routine each default that its ask left out. A default above the
		// top would store a routine that no later read accepts.
		for _, n := range []struct {
			field string
			from  *int
			to    *int
			max   int
		}{
			{"end_after_runs", k.EndAfterRuns, &d.EndAfterRuns, TalkootRoutineMaxEnd},
			{"end_after_days", k.EndAfterDays, &d.EndAfterDays, TalkootRoutineMaxEnd},
			{"reviews_per_day", k.ReviewsPerDay, &d.ReviewsPerDay, TalkootRoutineMaxReviewsPerDay},
			{"follow_ups", k.FollowUps, &d.FollowUps, TalkootRoutineMaxFollowUps},
		} {
			if n.from == nil {
				continue
			}
			if *n.from < 0 || *n.from > n.max {
				return TalkootRoutinePolicy{Problem: fmt.Sprintf("talkoot_routines.kinds.%s.%s is %d, and it must be from 0 to %d", kind, n.field, *n.from, n.max)}
			}
			*n.to = *n.from
		}
		if v := k.SpendUSDPerDay; v != nil {
			// The comparison is written so that a value which is no number
			// fails it too.
			if !(*v >= 0 && *v <= TalkootRoutineMaxSpendUSDPerDay) {
				return TalkootRoutinePolicy{Problem: fmt.Sprintf("talkoot_routines.kinds.%s.spend_usd_per_day is %v, and it must be from 0 to %d", kind, *v, TalkootRoutineMaxSpendUSDPerDay)}
			}
			d.SpendUSDPerDay = *v
		}
		if k.CatchUp != nil {
			d.CatchUp = *k.CatchUp
		}
		kinds[kind] = d
	}
	return TalkootRoutinePolicy{On: true, Max: c.Max, Kinds: kinds}
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
