// Package skills implements terva's reusable-instruction system.
//
// A skill is a per-folder SKILL.md file with a YAML frontmatter
// header. Skills live in well-known directories under the project or
// the user home; terva discovers them at startup, lists their names +
// one-line descriptions in the system prompt, and exposes a built-in
// "skill" tool the model uses to pull the full body on demand.
//
// The on-demand-load model keeps token usage cheap: only the
// short manifest goes into every request; the body is fetched as a
// tool result the one or two turns the model actually needs it.
//
// Discovery layout (priority order — first match wins per name):
//
//	./.terva/skills/<name>/SKILL.md            — project (native)
//	$TERVA_HOME/skills/<name>/SKILL.md         — global (native)
//	./.claude/skills/<name>/SKILL.md         — project (claude-compat)
//	~/.claude/skills/<name>/SKILL.md         — global (claude-compat)
//	./.agents/skills/<name>/SKILL.md         — project (agent-compat)
//	~/.agents/skills/<name>/SKILL.md         — global (agent-compat)
//
// The compat paths are deliberate: a SKILL.md written for any of
// the related ecosystems works in terva unchanged.
package skills

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"terva.sh/terva/packages/envcompat"
)

// Skill is one discovered SKILL.md file.
type Skill struct {
	// Name is the skill identifier — what the model uses when it
	// invokes the `skill` tool. Taken from the frontmatter `name`
	// field; falls back to the directory basename.
	Name string

	// Description is the one-line summary shown to the model in the
	// system-prompt manifest.
	Description string

	// Body is the markdown after the frontmatter. Returned as the
	// tool result when the model loads this skill.
	Body string

	// Path is the absolute path to the SKILL.md file.
	Path string

	// Source is a human-friendly label describing where the skill
	// came from ("project", "global", "project (claude)", etc.).
	// Shown in the /skills picker.
	Source string

	// Builtin marks skills that ship inside the terva binary. They are
	// fully active for the model (system-prompt manifest + skill
	// tool) but hidden from user-facing surfaces like the /skills
	// picker so users only see skills they actually installed or
	// shipped in their project.
	Builtin bool

	// AllowedTools, under lazy tool visibility (retro H2·b), names the tools
	// this skill depends on: loading the skill activates their capability
	// groups so they are advertised next turn (tool.go Execute →
	// Agent.ActivateGroupsForTools). This is strictly a VISIBILITY hint — it
	// never grants authority, so a revealed tool still faces its normal
	// permission/trust gate, and it is a no-op when lazy mode is off. Permissions
	// is parsed for forward-compatibility but not yet enforced.
	AllowedTools []string
	Permissions  map[string][]string
}

// VisibleSkills returns the subset of skills users should see in
// pickers, /skills, and other interactive surfaces. Built-ins are
// hidden because they're implementation detail; the model still
// loads them through the system-prompt manifest + the skill tool.
func VisibleSkills(in []*Skill) []*Skill {
	out := make([]*Skill, 0, len(in))
	for _, s := range in {
		if s == nil || s.Builtin {
			continue
		}
		out = append(out, s)
	}
	return out
}

// Discover returns the merged skill set. When includeUser is true,
// user-installed SKILL.md files are loaded before built-ins. Callers
// normally pass true; --no-skill skips discovery entirely before this
// function is called.
//
// trustProject gates the PROJECT-local skill dirs (./.terva|.claude|
// .agents/skills and extension-bundled skills under .terva/extensions).
// When false (an untrusted workspace — the default), those project
// dirs are dropped: a cloned repo cannot inject SKILL.md instructions
// into the model's prompt. Built-in, user, and global skills load
// regardless. See docs/plans/workspace-trust.md.
//
// First-match-wins per name; the order matches the priority list
// in the package doc (project-local before global before claude-
// compat before agents-compat, all before built-ins). That means a
// user-installed skill with the same name as a built-in shadows
// the built-in once includeUser is true.
//
// Errors per skill are returned alongside the partial result so a
// single broken file doesn't suppress the rest.
func Discover(tervaHome, cwd, userHome string, includeUser, trustProject bool) ([]*Skill, []error) {
	var errs []error
	seen := map[string]*Skill{}
	if includeUser {
		errs = append(errs, scanUserSkills(tervaHome, cwd, userHome, trustProject, seen)...)
	}
	// Built-ins fill in any name the user didn't already provide
	// (or every name, when includeUser is false).
	for _, s := range loadBuiltins() {
		if _, dup := seen[s.Name]; dup {
			continue
		}
		seen[s.Name] = s
	}
	out := make([]*Skill, 0, len(seen))
	for _, s := range seen {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, errs
}

// scanUserSkills walks the user-skill search dirs and populates
// `seen` with first-match-wins per name. Split out so Discover's
// includeUser=false path doesn't have to skip over a giant block.
func scanUserSkills(tervaHome, cwd, userHome string, trustProject bool, seen map[string]*Skill) []error {
	var errs []error
	for _, loc := range searchDirs(tervaHome, cwd, userHome, trustProject) {
		entries, err := os.ReadDir(loc.dir)
		if err != nil {
			continue // missing dir is fine
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			path := filepath.Join(loc.dir, e.Name(), "SKILL.md")
			s, err := load(path, loc.label)
			if err != nil {
				if !os.IsNotExist(err) {
					errs = append(errs, fmt.Errorf("%s: %w", path, err))
				}
				continue
			}
			if s.Name == "" {
				s.Name = e.Name()
			}
			if _, dup := seen[s.Name]; dup {
				continue // higher-priority location already won
			}
			seen[s.Name] = s
		}
	}
	return errs
}

// SystemPromptAddendum returns the text to append to the system
// prompt when at least one skill is loaded. Empty string if none.
//
// The format is deliberately compact: name, one-line description,
// and a source pointer telling the model where the full body
// lives. Built-in skills show "builtin" since their markdown is
// embedded in the terva binary and not on the filesystem; user
// skills show their SKILL.md path (shortened with ~ for HOME).
//
// Loading still goes through the `skill` tool with just the name.
// The pointer is there so the model can (a) mention the source
// honestly in explanations and (b) distinguish between built-ins
// and user-authored instruction sets when reasoning about trust.
func SystemPromptAddendum(skills []*Skill) string {
	if len(skills) == 0 {
		return ""
	}
	home, _ := os.UserHomeDir()
	var sb strings.Builder
	sb.WriteString("Available skills (call the `skill` tool with a name from this list to load its full instructions):\n")
	for _, s := range skills {
		desc := strings.TrimSpace(s.Description)
		if desc == "" {
			desc = "(no description)"
		}
		pointer := skillSourcePointer(s, home)
		fmt.Fprintf(&sb, "- %s [%s]: %s\n", s.Name, pointer, desc)
	}
	return sb.String()
}

// skillSourcePointer returns a short tag describing where a skill
// originates. Built-ins are tagged "builtin" because their markdown
// is embedded in the terva binary and not reachable through the
// filesystem. User skills are tagged with their SKILL.md path,
// collapsed to use ~ for the user home when possible.
func skillSourcePointer(s *Skill, home string) string {
	if s == nil {
		return "unknown"
	}
	if s.Builtin {
		return "builtin"
	}
	p := s.Path
	if p == "" {
		return "unknown"
	}
	if home != "" && strings.HasPrefix(p, home+string(filepath.Separator)) {
		return "~" + p[len(home):]
	}
	return p
}

// FindByName returns the skill with the given name, or nil.
func FindByName(skills []*Skill, name string) *Skill {
	for _, s := range skills {
		if s.Name == name {
			return s
		}
	}
	return nil
}

// ---- internals ----

type location struct {
	dir   string
	label string
}

// searchDirs lists the skill directories in priority order. trustProject
// gates every PROJECT-local (cwd-anchored) dir: when false, an untrusted
// workspace contributes no project SKILL.md and no extension-bundled
// skills (a cloned repo can't inject instructions into the prompt). The
// global/user dirs (tervaHome, ~/.claude, ~/.agents) are always
// included. See docs/plans/workspace-trust.md.
func searchDirs(tervaHome, cwd, userHome string, trustProject bool) []location {
	var out []location
	add := func(dir, label string) {
		if dir == "" {
			return
		}
		out = append(out, location{dir: dir, label: label})
	}
	if cwd != "" && trustProject {
		// Both project-dir spellings, new name first (the rename's
		// dual-read seam; see envcompat.ProjectDirNames).
		for _, dirName := range envcompat.ProjectDirNames() {
			add(filepath.Join(cwd, dirName, "skills"), "project")
		}
	}
	if tervaHome != "" {
		add(filepath.Join(tervaHome, "skills"), "global")
	}
	// Extension bundles: an installed, enabled extension may ship a
	// skills/ directory beside its extension.json (data-only bundle
	// contribution — see docs/extensions.md). Ranked after the user's
	// own dirs so a bundle can never shadow a deliberately-authored
	// skill, before the foreign-tool compat dirs. Project-ext bundles
	// are gated on trust (extensionSkillDirs honors trustProject); the
	// project extension wouldn't load untrusted anyway, so its skills
	// can't either.
	for _, dir := range extensionSkillDirs(tervaHome, cwd, trustProject) {
		add(dir, "extension")
	}
	if cwd != "" && trustProject {
		add(filepath.Join(cwd, ".claude", "skills"), "project (claude)")
	}
	if userHome != "" {
		add(filepath.Join(userHome, ".claude", "skills"), "global (claude)")
	}
	if cwd != "" && trustProject {
		add(filepath.Join(cwd, ".agents", "skills"), "project (agents)")
	}
	if userHome != "" {
		add(filepath.Join(userHome, ".agents", "skills"), "global (agents)")
	}
	return out
}

// extensionSkillDirs lists <extension>/skills for every enabled
// installed extension, global ($TERVA_HOME/extensions) before
// project (.terva/extensions, rename-aware spellings). Enabled-ness
// comes from a minimal read of each extension.json — a disabled
// extension contributes nothing, skills included.
// trustProject gates the project extension roots: an untrusted workspace
// contributes no project-ext-bundled skills (the project extension would
// not load there either). Global ($TERVA_HOME/extensions) bundles always
// contribute.
func extensionSkillDirs(tervaHome, cwd string, trustProject bool) []string {
	var roots []string
	if tervaHome != "" {
		roots = append(roots, filepath.Join(tervaHome, "extensions"))
	}
	if cwd != "" && trustProject {
		for _, dirName := range envcompat.ProjectDirNames() {
			roots = append(roots, filepath.Join(cwd, dirName, "extensions"))
		}
	}
	var out []string
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			extDir := filepath.Join(root, e.Name())
			mb, err := os.ReadFile(filepath.Join(extDir, "extension.json"))
			if err != nil {
				continue
			}
			var m struct {
				Enabled *bool `json:"enabled"`
			}
			if json.Unmarshal(mb, &m) != nil {
				continue
			}
			if m.Enabled != nil && !*m.Enabled {
				continue
			}
			skillsDir := filepath.Join(extDir, "skills")
			if st, err := os.Stat(skillsDir); err == nil && st.IsDir() {
				out = append(out, skillsDir)
			}
		}
	}
	return out
}

func load(path, source string) (*Skill, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	front, body := splitFrontmatter(string(raw))
	s := &Skill{
		Path:   path,
		Source: source,
		Body:   strings.TrimSpace(body),
	}
	parseFrontmatter(front, s)
	return s, nil
}

// splitFrontmatter returns (yamlBlock, restOfDocument) for a string
// whose first non-empty line is "---". If no frontmatter is present,
// returns ("", entireString).
func splitFrontmatter(raw string) (string, string) {
	rest := strings.TrimLeft(raw, " \t\r\n")
	if !strings.HasPrefix(rest, "---") {
		return "", raw
	}
	rest = strings.TrimPrefix(rest, "---")
	// Drop the trailing newline after the opening ---.
	if i := strings.IndexByte(rest, '\n'); i >= 0 {
		rest = rest[i+1:]
	}
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return "", raw // malformed; treat as no frontmatter
	}
	front := rest[:end]
	body := rest[end+len("\n---"):]
	body = strings.TrimLeft(body, " \t\r\n")
	return front, body
}

// frontmatter is the YAML head of a SKILL.md. terva recognises name,
// description, an allowed-tools list (either spelling), and a
// permissions map of tool -> patterns. AllowedTools drives lazy-visibility
// activation (see Skill.AllowedTools); Permissions is parsed for
// forward-compatibility but not yet enforced.
type frontmatter struct {
	Name            string              `yaml:"name"`
	Description     string              `yaml:"description"`
	AllowedTools    []string            `yaml:"allowed-tools"`
	AllowedToolsAlt []string            `yaml:"allowed_tools"`
	Permissions     map[string][]string `yaml:"permissions"`
}

// parseFrontmatter unmarshals the SKILL.md YAML head into s. Malformed
// frontmatter degrades gracefully — the skill still loads (its name
// falls back to the directory basename in scanUserSkills) rather than
// vanishing from discovery — matching the prior hand-parser, which
// silently ignored anything it couldn't read.
func parseFrontmatter(front string, s *Skill) {
	var fm frontmatter
	if err := yaml.Unmarshal([]byte(front), &fm); err != nil {
		return
	}
	s.Name = fm.Name
	s.Description = fm.Description
	if len(fm.AllowedTools) > 0 {
		s.AllowedTools = fm.AllowedTools
	} else {
		s.AllowedTools = fm.AllowedToolsAlt
	}
	s.Permissions = fm.Permissions
}
