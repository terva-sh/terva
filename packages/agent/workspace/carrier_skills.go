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
// never cost the user their prompt cache.
func (w *Workspace) ReloadSkills(sess string) []*skills.Skill {
	return w.skillsForSession(sess, true)
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
