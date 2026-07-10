package raati

// Convening profiles: named, user-authored bundles a convening surface
// (the raati_convene tool, the web form, the CLI) selects BY NAME. The
// split is the trust design (docs/proposals/raati-deliberation.md,
// "Convening profiles"): the caller — possibly the very agent whose
// plan is being judged — picks which profile; only the user's config
// says what a profile means. Seat composition therefore never crosses
// the call boundary: a profile may pin the panel, a call may not.

import (
	"fmt"
	"sort"
	"strings"

	"terva.sh/terva/packages/i18n"
)

// Profile is one convening bundle. Every field is a DEFAULT for the
// convening call: anything the call itself specifies wins — except
// Seats, which no call may supply. String fields stay raw here and are
// validated where they are consumed (ParseClass, ParseSeatOrder, the
// inquire-mode switch), so a typo'd profile errors the convening with
// a real message instead of silently vanishing at config load.
type Profile struct {
	// Description says what the profile is FOR — it is the selection
	// signal, enumerated verbatim wherever profiles are picked.
	Description string
	// Level is the rigor level (0-2). Pointer: nil leaves the level to
	// the call, while an explicit 0 pins the cheap correlated panel on
	// purpose.
	Level *int
	// AutoLevel resolves the rigor at convene time to the highest level
	// the user's config actually supports (2 with a complete
	// raati.level2, else 1 with a full tier ladder, else 0), capped at
	// AutoCeiling — "auto:1" collapses a profile to one provider's
	// ladder even when cross-provider seats exist. Rigor then upgrades
	// the day the config does, with no profile edits. Ignored when
	// Level or Seats say otherwise.
	AutoLevel   bool
	AutoCeiling int
	// Seats pins the panel explicitly, one binding per seat — a
	// per-profile pool that implies level 2 and wins over the config's
	// raati.level2 when this profile convenes.
	Seats []Binding
	// SeatOrder / SeatMap override the config's pool→seat policy.
	SeatOrder string
	SeatMap   []int
	// Class is the default decision class (advisory | gate | veto).
	Class string
	// SingleRound / Inquire / Converge default the deliberation shape.
	// Inquire is the mode string ("record" | "convener"); surfaces
	// without a convener to ask degrade it the same way they degrade
	// an explicit convener request.
	SingleRound *bool
	Inquire     string
	Converge    *bool
}

// BuiltinProfiles is the shipped set — the reason a fresh install's
// raati is useful before anyone writes config. All four are SHAPE
// profiles (class, rounds, inquiry, convergence): none pin seats,
// because terva can't know what providers a user has. The three
// serious ones ride auto level, so their rigor climbs the day the
// config gains a tier ladder or raati.level2 — no profile edits. A
// user profile with the same name replaces a built-in wholesale, and
// raati.builtin_profiles=false hides the whole set.
func BuiltinProfiles() map[string]Profile {
	zero := 0
	yes := true
	return map[string]Profile{
		"triage": {
			Description: "fast gut-check for low-stakes calls — one blind round on this session's binding; correlated and cheap, never a gate",
			Level:       &zero,
			Class:       "advisory",
			SingleRound: &yes,
		},
		"counsel": {
			Description: "the workhorse for real decisions — blind round, cross-examination, panel questions answered from the record, converges if a verdict flips",
			AutoLevel:   true,
			AutoCeiling: 2,
			Class:       "advisory",
			Inquire:     "record",
			Converge:    &yes,
		},
		"code-review": {
			Description: "merge/ship/release checks — unanimity or it fails closed, questions answered from the record; refuses to run correlated (wants a tier ladder or raati.level2)",
			AutoLevel:   true,
			AutoCeiling: 2,
			Class:       "gate",
			Inquire:     "record",
			Converge:    &yes,
		},
		"ethics": {
			Description: "people, policy, and data-use calls — the benevolence seat may block, and the panel may ask the convener what the record can't answer",
			AutoLevel:   true,
			AutoCeiling: 2,
			Class:       "veto",
			Inquire:     "convener",
			Converge:    &yes,
		},
	}
}

// ProfileFor resolves a profile by name. Unknown names error with the
// configured list, so a caller (human or agent) can self-correct
// without digging through config.
func ProfileFor(profiles map[string]Profile, name string) (Profile, error) {
	if p, ok := profiles[name]; ok {
		return p, nil
	}
	if len(profiles) == 0 {
		return Profile{}, i18n.Errorf("no convening profiles configured (user config raati.profiles)")
	}
	return Profile{}, i18n.Errorf("unknown convening profile %q — configured: %s", name, strings.Join(ProfileNames(profiles), ", "))
}

// ProfileNames returns the configured profile names sorted, for stable
// enumeration in tool descriptions, error messages, and form lists.
func ProfileNames(profiles map[string]Profile) []string {
	names := make([]string, 0, len(profiles))
	for n := range profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// EffectiveLevel is the rigor level a profile convenes at when the
// call names none: an explicit Level wins, pinned Seats imply level 2,
// and an empty profile leaves the level-0 default alone. Auto profiles
// report nothing here — they need the config picture; see PickLevel.
func (p Profile) EffectiveLevel() (int, bool) {
	if p.Level != nil {
		return *p.Level, true
	}
	if len(p.Seats) > 0 {
		return 2, true
	}
	return 0, false
}

// PickLevel is EffectiveLevel plus auto resolution: highest is the top
// rigor level the caller's config supports right now (the surface
// computes it from its tiers/level2 picture). viaAuto marks a level the
// profile resolved automatically — the honesty hook: a gate that
// auto-resolves to the correlated level refuses rather than quietly
// gating on theater, while the same level asked for EXPLICITLY is the
// trust root's deliberate call.
func (p Profile) PickLevel(highest int) (level int, ok bool, viaAuto bool) {
	if lvl, ok := p.EffectiveLevel(); ok {
		return lvl, true, false
	}
	if !p.AutoLevel {
		return 0, false, false
	}
	if highest > p.AutoCeiling {
		highest = p.AutoCeiling
	}
	if highest < 0 {
		highest = 0
	}
	return highest, true, true
}

// ValidSeats checks a pinned panel before it seats a convening: the
// profile must pin every seat, each fully. Checked where Seats are
// adopted, so the error talks about the profile — the generic pool
// resolver's messages blame raati.level2, which would mislead here.
func (p Profile) ValidSeats(want int) error {
	if len(p.Seats) != want {
		return i18n.Errorf("the profile pins %d seat(s) but the panel has %d — pin the whole panel or none", len(p.Seats), want)
	}
	for i, b := range p.Seats {
		if b.Provider == "" || b.Model == "" {
			return i18n.Errorf("profile seat %d needs both provider and model", i+1)
		}
	}
	return nil
}

// ProfileLine renders the one-line enumeration form: "name — description".
func ProfileLine(name string, p Profile) string {
	if strings.TrimSpace(p.Description) == "" {
		return name
	}
	return fmt.Sprintf("%s — %s", name, p.Description)
}
