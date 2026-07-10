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

// skillsForSession re-discovers the visible skills for a session's workspace,
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
	full, _ := skills.Discover(config.TervaHome(), s.cwd, userHome, s.args.WithSkills, s.trusted.Load())
	if refresh && s.skillTool != nil {
		s.skillTool.SetSkills(full)
	}
	return skills.VisibleSkills(full)
}

// SkillSnapshot re-discovers the visible skills for the /skills picker (reflects
// edits made this session); nil pre-login or under --no-skill.
func (w *Workspace) SkillSnapshot(sess string) []*skills.Skill {
	return w.skillsForSession(sess, false)
}

// ReloadSkills re-discovers and swaps the session's live catalog so a
// session-authored skill resolves by name (bound to the /skills reload key).
func (w *Workspace) ReloadSkills(sess string) []*skills.Skill {
	return w.skillsForSession(sess, true)
}

// SessionSkills returns the session's live in-memory visible catalog — the
// cheap per-render source for `/skill <name>` completions (no disk rescan).
func (w *Workspace) SessionSkills(sess string) []*skills.Skill {
	s := w.existing(sess)
	if s == nil || s.args.NoSkill || s.skillTool == nil {
		return nil
	}
	return skills.VisibleSkills(s.skillTool.Skills())
}
