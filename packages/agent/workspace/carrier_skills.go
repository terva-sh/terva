package workspace

import (
	"os"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/skills"
)

// Carrier skill accessors: the /skills picker, /skill <name> priming, and the
// `/skill <name>` argument completions read the CURRENT session's skill set.
// Skills are per-session (each wsSession owns a *skills.Tool over its resolved
// cwd + trust verdict), so these resolve the live session each call — the same
// in-process seam as ExtensionThemes / CarrierExtHost. Skill EXECUTION is the
// model's daemon-side `skill` tool and is unaffected; this is the interactive
// discovery/refresh surface the legacy entry wired directly.

// These accessors hand back the FULL active catalog — built-ins included, each
// winner carrying what it shadowed. Filtering is the RENDERING surface's job
// (skills.VisibleSkills), because the two consumers want different sets: a
// picker should hide built-ins and show collisions, while `/skill <name>` must
// resolve against everything the model can actually load, built-ins included.
// Handing a pre-filtered list to both made /skill unable to name a built-in.

// skillsForSession re-discovers the active skills for a session's workspace,
// honoring --no-skill / --with-skills and the session's live trust verdict.
// When refresh is set it swaps the discovered set into the session's live skill
// tool so a newly added/edited SKILL.md becomes loadable by name WITHOUT
// rebuilding the system prompt (which would discard the request-prefix cache).
func (w *Workspace) skillsForSession(sess string, refresh bool) []*skills.Skill {
	s := w.existing(sess)
	if s == nil || s.args.NoSkill {
		return nil
	}
	userHome, _ := os.UserHomeDir()
	trusted := s.trusted.Load()
	full, _ := skills.Discover(config.TervaHome(), s.cwd, userHome, s.args.WithSkills, !s.args.NoBuiltinSkills,
		skills.Gate{TrustProject: trusted, Disabled: config.ResolveConfig(s.cwd, trusted).Config.DisableExtensions})
	if refresh {
		if st := s.liveSkillTool(); st != nil {
			st.SetSkills(full)
		}
	}
	return full
}

// liveSkillTool returns the skill tool the MODEL currently holds, read from the
// agent's live registry rather than from a pointer cached at session build.
//
// Derived, not stored, and deliberately so. Every rebuildTools installs a
// registry that Resolve minted fresh — skill tool included — so a cached
// pointer silently becomes an orphan at the first rebuild. The host then
// reloads into a catalog the model cannot read, while the picker keeps looking
// correct because it rescans disk on every call. Deriving removes that
// invariant instead of maintaining it. Agent.LookupTool takes the agent's own
// lock, so this is safe against a concurrent rebuild.
//
// nil is a normal answer: no skills discovered, --no-skill, --no-tools, a
// chat/play session, or a bare fixture with no agent.
func (s *wsSession) liveSkillTool() *skills.Tool {
	if s == nil || s.agent == nil {
		return nil
	}
	t, ok := s.agent.LookupTool("skill")
	if !ok {
		return nil
	}
	st, _ := t.(*skills.Tool)
	return st
}

// SkillSnapshot re-discovers the active skills for the /skills picker and
// /skill resolution (reflects edits made this session); nil pre-login or
// under --no-skill.
func (w *Workspace) SkillSnapshot(sess string) []*skills.Skill {
	return w.skillsForSession(sess, false)
}

// ReloadSkills re-discovers and swaps the session's live catalog so a
// session-authored skill resolves by name (bound to the /skills reload key).
//
// Catalog only: it never rebuilds the system prompt, so opening a picker can
// never cost the user their prompt cache. /reload-skills wants the other
// trade-off — see ReloadSkillsAndPrompt.
func (w *Workspace) ReloadSkills(sess string) []*skills.Skill {
	return w.skillsForSession(sess, true)
}

// SkillReloadStats reports what a /reload-skills actually did, so a front end
// can say something more useful than "done" — and so the one case that costs
// the user something (a dropped prompt cache) is stated rather than silent.
type SkillReloadStats struct {
	// Available counts what a person sees listed, matching the /skills picker's
	// number rather than the larger set the model can load by name.
	Available int
	// Added and Removed are qualified names, so a project skill and a built-in
	// sharing a bare name are never mistaken for one another.
	Added   []string
	Removed []string
	// PromptRebuilt records that the manifest changed and the next turn
	// therefore starts uncached.
	PromptRebuilt bool
}

// ReloadSkillsAndPrompt is the /reload-skills path: re-run discovery, swap the
// live catalog, and rebuild the system prompt ONLY when the manifest the model
// reads actually changed.
//
// The two halves are deliberately separate, because they have very different
// costs. Swapping the catalog is free and immediately makes a skill loadable by
// name. Rebuilding the prompt discards the provider's cached request prefix, so
// the next turn re-reads the whole transcript uncached. That is worth paying
// when a skill APPEARED — the model will not reach for what it cannot see
// listed — and wasted when the author merely edited a body, which is the common
// beat of the authoring loop.
//
// SystemPromptAddendum is exactly the text the model sees, which makes diffing
// it the precise test rather than an approximation: a changed name or
// description rebuilds, a changed body does not.
func (w *Workspace) ReloadSkillsAndPrompt(sess string) SkillReloadStats {
	s := w.existing(sess)
	if s == nil || s.args.NoSkill {
		return SkillReloadStats{}
	}
	var before []*skills.Skill
	if st := s.liveSkillTool(); st != nil {
		before = st.Skills()
	}
	after := w.skillsForSession(sess, true)

	stats := SkillReloadStats{
		Available: len(skills.VisibleSkills(after)),
		Added:     skills.MissingFrom(after, before),
		Removed:   skills.MissingFrom(before, after),
	}
	if skills.SystemPromptAddendum(before) != skills.SystemPromptAddendum(after) {
		// Re-runs discovery a second time inside Resolve. That is the honest
		// price of reusing the one code path that builds a prompt, and it beats
		// a bespoke prompt-patching seam that could drift from it.
		s.rebuildTools("skill-reload")
		stats.PromptRebuilt = true
	}
	return stats
}

// SessionSkills returns the session's live in-memory catalog — the cheap
// per-render source for `/skill <name>` completions (no disk rescan).
func (w *Workspace) SessionSkills(sess string) []*skills.Skill {
	s := w.existing(sess)
	if s == nil || s.args.NoSkill {
		return nil
	}
	st := s.liveSkillTool()
	if st == nil {
		return nil
	}
	return st.Skills()
}
