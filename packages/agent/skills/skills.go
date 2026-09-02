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
//	(compiled into the binary)                 — built-in
//	<extension>/skills/<name>/SKILL.md         — extension bundle
//	./.claude/skills/<name>/SKILL.md         — project (claude-compat)
//	~/.claude/skills/<name>/SKILL.md         — global (claude-compat)
//	./.agents/skills/<name>/SKILL.md         — project (agent-compat)
//	~/.agents/skills/<name>/SKILL.md         — global (agent-compat)
//
// The compat paths are deliberate: a SKILL.md written for any of
// the related ecosystems works in terva unchanged.
//
// Built-ins sit deliberately in the MIDDLE of that ladder: below the
// native dirs, where a skill is authored for terva on purpose, and
// above the foreign-tool compat dirs, which terva merely reads. A
// ~/.claude/skills/handoff written against another runtime should not
// silently replace the handoff terva ships and documents. To override
// a built-in, write the skill natively (or pass --no-builtin-skills).
//
// Losing the bare name is not vanishing. Every skill also carries a
// namespace-qualified name — claude:handoff, ext:web:web-research —
// that resolves regardless of who won, and the winner keeps a list of
// what it shadowed so the collision is reportable instead of silent.
package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"terva.sh/terva/packages/agent/extroots"
	"terva.sh/terva/packages/envcompat"
)

// Skill namespaces. A namespace is a skill's SOURCE TIER — which
// ecosystem's directory the SKILL.md came out of — not a claim about
// its author. Qualifying a name with it ("claude:handoff") reaches a
// skill that lost the bare name to a higher tier.
const (
	// NamespaceTerva covers the native dirs: ./.terva/skills and
	// $TERVA_HOME/skills. As a lookup prefix it also falls through to
	// NamespaceBuiltin, so "terva:handoff" reaches the shipped skill
	// even when the user has not written one of their own.
	NamespaceTerva = "terva"
	// NamespaceBuiltin is the set compiled into the binary.
	NamespaceBuiltin = "builtin"
	// NamespaceClaude is the .claude/skills compat tier.
	NamespaceClaude = "claude"
	// NamespaceAgents is the .agents/skills compat tier.
	NamespaceAgents = "agents"
	// NamespaceExtPrefix prefixes an extension bundle's namespace; the
	// full namespace is "ext:<extension>", so the qualified name of a
	// bundled skill reads ext:<extension>:<skill>.
	NamespaceExtPrefix = "ext:"
)

// nameSep separates a namespace from a skill name in a qualified
// reference. Reserved: a SKILL.md may not put it in its own name.
const nameSep = ":"

// DefaultAlwaysOn names the skills terva pins into the system prompt when the
// operator has expressed no opinion. It is the fallback for a nil
// `always_on_skills` config value, and an explicit empty list overrides it.
//
// This is the single definition of that default. The config layer holds the
// operator's value and applies no policy, the build layer chooses between the
// two, and cmd/terva-ste-lint reads this list to decide which body it enrolls.
// A second copy of these names would drift.
//
// A pinned body costs roughly 1400 tokens in every request of every session,
// so an addition here is expensive. See docs/proposals/always-on-skills.md.
var DefaultAlwaysOn = []string{"house-style"}

// Skill is one discovered SKILL.md file.
type Skill struct {
	// Name is the skill identifier — what the model uses when it
	// invokes the `skill` tool. Taken from the frontmatter `name`
	// field; falls back to the directory basename.
	Name string

	// Namespace is the source tier this skill came from — one of the
	// Namespace* constants, or "ext:<extension>" for a bundle. It
	// prefixes Qualified(), which resolves even when a higher tier
	// won the bare Name.
	Namespace string

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

	// Project marks a skill that came from a cwd-anchored directory:
	// ./.terva/skills, ./.claude/skills, ./.agents/skills, or a project
	// extension bundle. A cloned repository controls those, so workspace
	// trust gates them at discovery, and always-on pinning refuses them
	// unless they shadow a name the operator listed. See ResolveAlwaysOn.
	Project bool

	// Pinned marks a skill whose BODY this session put in the system prompt,
	// rather than only its description in the manifest. Set by
	// ResolveAlwaysOn, which is the one place that decides it, and read by
	// user-facing surfaces so a pinned skill does not look like an ordinary
	// one that the model simply never loads.
	Pinned bool

	// Builtin marks skills that ship inside the terva binary. They are
	// fully active for the model (system-prompt manifest + skill
	// tool) but hidden from user-facing surfaces like the /skills
	// picker so users only see skills they actually installed or
	// shipped in their project.
	Builtin bool

	// Shadowed lists the same-named skills this one beat, in ladder
	// order. Set on the WINNER only. They are absent from the model's
	// manifest — two entries whose descriptions differ in nuance make
	// the choice a coin flip — but each stays addressable by its
	// Qualified() name and visible in the /skills picker.
	Shadowed []*Skill

	// ShadowedBy points at the skill that took this one's bare name.
	// Set on the LOSER only; nil for an active skill.
	ShadowedBy *Skill

	// DisableModelInvocation keeps a skill out of the system-prompt
	// manifest, so the model never selects it on its own. It stays
	// loadable through the `skill` tool, which is how /skill's
	// prime-the-editor flow reaches it — the flag scopes model
	// DISCOVERY, not the load path.
	DisableModelInvocation bool

	// ArgumentHint is the frontmatter one-liner describing what a
	// skill wants as its argument. Shown beside the name in the
	// `/skill <name>` completion popup and the /skills picker.
	ArgumentHint string

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

// Qualified returns the namespace-qualified reference for this skill —
// "claude:handoff", "ext:web:web-research". It always resolves through
// Resolve, whether or not the skill won its bare name.
func (s *Skill) Qualified() string {
	if s == nil {
		return ""
	}
	if s.Namespace == "" {
		return s.Name
	}
	return s.Namespace + nameSep + s.Name
}

// Ref is the reference a surface should PRINT for this skill — and the
// one it must accept back. A skill that still holds its bare name shows
// it; one that lost it shows the qualified name, because the bare form
// now resolves to whatever beat it. Every surface that renders a skill
// identifier goes through here so none of them can print a string that
// does not resolve.
func (s *Skill) Ref() string {
	if s == nil {
		return ""
	}
	if s.ShadowedBy != nil {
		return s.Qualified()
	}
	return s.Name
}

// VisibleSkills returns the skills users should see in pickers,
// /skills, completions, and other interactive surfaces.
//
// Built-ins ARE included. They used to be hidden as implementation
// detail, which stopped being true twice over: four of them are
// standing workflows a person invokes by hand (handoff,
// init-workspace, retrospective, troubleshoot-terva), and `/skill
// <name>` has always resolved them — so omitting them here made the
// surfaces understate what the user could actually type. Worse, a
// picker that tags a collision "shadowed by builtin" while refusing to
// show any builtin names a tier the user cannot see, which is only
// half an answer to the "where did my skill go?" question the row
// exists to answer.
//
// Shadowed skills are flattened in beside the winner that beat them:
// a skill that lost its bare name is not a skill that vanished, and
// the picker is where a user finds out a collision happened at all.
// Each carries ShadowedBy so the surface can name what beat it.
func VisibleSkills(in []*Skill) []*Skill {
	out := make([]*Skill, 0, len(in))
	keep := func(s *Skill) {
		if s == nil {
			return
		}
		out = append(out, s)
	}
	for _, s := range in {
		if s == nil {
			continue
		}
		keep(s)
		for _, sh := range s.Shadowed {
			keep(sh)
		}
	}
	// The user's own skills first, terva's built-ins after: the first
	// screen of the picker stays the set the user installed, and the
	// thirteen that ship in the binary do not push it off the top.
	// Name-primary within each group so a shadowed entry sorts next to
	// the winner that took its name rather than drifting off under its
	// namespace.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Builtin != out[j].Builtin {
			return !out[i].Builtin
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Namespace < out[j].Namespace
	})
	return out
}

// ModelVisible returns the skills that belong in the system-prompt
// manifest: the active set minus any that opted out of model
// discovery. Shadowed skills are already excluded — they are not in
// the active slice to begin with.
func ModelVisible(in []*Skill) []*Skill {
	out := make([]*Skill, 0, len(in))
	for _, s := range in {
		if s == nil || s.DisableModelInvocation {
			continue
		}
		out = append(out, s)
	}
	return out
}

// Collisions returns every active skill that shadowed at least one
// same-named skill, for the /skills picker and `terva doctor`. The
// winner is the element; what it beat hangs off .Shadowed.
func Collisions(in []*Skill) []*Skill {
	var out []*Skill
	for _, s := range in {
		if s != nil && len(s.Shadowed) > 0 {
			out = append(out, s)
		}
	}
	return out
}

// MissingFrom lists the skills in `in` whose qualified name `other` does not
// have, sorted. It backs the "added" / "removed" halves a reload reports: call
// it once each way round.
//
// Compared by qualified name rather than bare name, so a project skill and a
// built-in that answer to the same word are never mistaken for one another —
// swapping one for the other IS a change worth reporting.
func MissingFrom(in, other []*Skill) []string {
	have := make(map[string]bool, len(other))
	for _, s := range other {
		if s != nil {
			have[s.Qualified()] = true
		}
	}
	var out []string
	for _, s := range in {
		if s != nil && !have[s.Qualified()] {
			out = append(out, s.Qualified())
		}
	}
	sort.Strings(out)
	return out
}

// Gate is extroots.Gate — what this workspace is allowed to contribute.
// Aliased rather than redeclared so skills, lore and the extension scanner
// cannot drift into three shapes of the same answer, which is the failure this
// whole seam exists to prevent.
type Gate = extroots.Gate

// Discover returns the merged skill set. When includeUser is true,
// user-installed SKILL.md files are loaded; includeBuiltin controls
// the skills compiled into the binary. Callers normally pass true for
// both; --no-skill skips discovery entirely before this function is
// called.
//
// gate.TrustProject gates the PROJECT-local skill dirs (./.terva|.claude|
// .agents/skills and extension-bundled skills under .terva/extensions).
// When false (an untrusted workspace — the default), those project
// dirs are dropped: a cloned repo cannot inject SKILL.md instructions
// into the model's prompt. Built-in, user, and global skills load
// regardless. See docs/plans/workspace-trust.md.
//
// gate.Disabled drops every skill shipped by an extension the operator has
// switched off. It used to be absent, so a disabled extension went on
// injecting its instructions after its tools and personas had gone.
//
// First-match-wins per name, in the ladder order of discoveryTiers.
// The loser is NOT discarded: it is attached to the winner's Shadowed
// list, keeps its Qualified() name, and stays reachable through
// Resolve — so a collision is reportable rather than silent.
//
// Errors per skill are returned alongside the partial result so a
// single broken file doesn't suppress the rest.
func Discover(tervaHome, cwd, userHome string, includeUser, includeBuiltin bool, gate Gate) ([]*Skill, []error) {
	var errs []error
	var candidates []*Skill
	for _, t := range discoveryTiers(tervaHome, cwd, userHome, includeUser, includeBuiltin, gate) {
		if t.builtin {
			candidates = append(candidates, loadBuiltins()...)
			continue
		}
		found, tierErrs := scanTier(t)
		candidates = append(candidates, found...)
		errs = append(errs, tierErrs...)
	}

	seen := map[string]*Skill{}
	out := make([]*Skill, 0, len(candidates))
	for _, s := range candidates {
		if winner, dup := seen[s.Name]; dup {
			s.ShadowedBy = winner
			winner.Shadowed = append(winner.Shadowed, s)
			continue
		}
		seen[s.Name] = s
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, errs
}

// SystemPromptAddendum returns the text to append to the system
// prompt when at least one model-visible skill is loaded. Empty
// string if none.
//
// The format is deliberately compact: name, one-line description,
// and a source pointer telling the model where the full body
// lives. Built-in skills show "builtin" since their markdown is
// embedded in the terva binary and not on the filesystem; user
// skills show their SKILL.md path (shortened with ~ for HOME).
//
// Only ACTIVE skills are listed, under their BARE names. A shadowed
// skill is deliberately absent: two entries named handoff whose
// descriptions differ only in nuance turn selection into a coin
// flip. It stays reachable by its qualified name — the `skill`
// tool's not-found message teaches that syntax at the moment it is
// needed, which costs nothing on the turns it is not.
//
// Loading still goes through the `skill` tool with just the name.
// The pointer is there so the model can (a) mention the source
// honestly in explanations and (b) distinguish between built-ins
// and user-authored instruction sets when reasoning about trust.
func SystemPromptAddendum(skills []*Skill) string {
	listed := ModelVisible(skills)
	if len(listed) == 0 {
		return ""
	}
	home, _ := os.UserHomeDir()
	var sb strings.Builder
	sb.WriteString("Available skills (call the `skill` tool with a name from this list to load its full instructions):\n")
	for _, s := range listed {
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

// FindByName returns the ACTIVE skill with the given name, or nil.
// Exact match on the bare name; use Resolve for the namespace-aware,
// case-insensitive lookup the user-facing surfaces want.
func FindByName(skills []*Skill, name string) *Skill {
	for _, s := range skills {
		if s.Name == name {
			return s
		}
	}
	return nil
}

// Resolve looks up a skill by bare name ("handoff") or by
// namespace-qualified name ("claude:handoff", "ext:web:web-research"),
// case-insensitively. active is the winner slice from Discover;
// shadowed skills are reached through their winners' Shadowed lists.
//
// A bare name resolves to whichever tier won it, which after the
// ladder ordering means: a natively-authored skill, else the built-in,
// else the highest-ranked foreign one. Qualifying is how you reach
// past that. "terva:" is an alias GROUP covering the native tier and
// then the built-ins, so terva:handoff resolves whether or not the
// user wrote a native handoff of their own.
//
// Returns nil when nothing matches.
func Resolve(active []*Skill, name string) *Skill {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	ns, bare, qualified := splitQualified(name)
	if !qualified {
		for _, s := range active {
			if strings.EqualFold(s.Name, bare) {
				return s
			}
		}
		return nil
	}
	tiers := []string{ns}
	if strings.EqualFold(ns, NamespaceTerva) {
		tiers = append(tiers, NamespaceBuiltin)
	}
	for _, want := range tiers {
		for _, s := range active {
			if hit := matchInFamily(s, bare, want); hit != nil {
				return hit
			}
		}
	}
	return nil
}

// matchInFamily checks a winner and everything it shadowed for a
// (name, namespace) match. Winner first, then the shadowed list in
// ladder order, so the highest-ranked member of a namespace wins when
// several tiers share it (project (claude) before global (claude)).
func matchInFamily(winner *Skill, bare, namespace string) *Skill {
	if winner == nil {
		return nil
	}
	if strings.EqualFold(winner.Name, bare) && strings.EqualFold(winner.Namespace, namespace) {
		return winner
	}
	for _, sh := range winner.Shadowed {
		if sh != nil && strings.EqualFold(sh.Name, bare) && strings.EqualFold(sh.Namespace, namespace) {
			return sh
		}
	}
	return nil
}

// splitQualified splits a reference into (namespace, name). Extension
// namespaces carry their own separator ("ext:web:web-research" is
// namespace "ext:web", name "web-research"), so the leading segment is
// re-joined when it is the ext prefix. A reference with no separator —
// or a dangling one ("ext:web", "claude:") — is reported unqualified
// and falls back to a bare lookup.
func splitQualified(name string) (namespace, bare string, ok bool) {
	i := strings.Index(name, nameSep)
	if i < 0 {
		return "", name, false
	}
	head, rest := name[:i], name[i+1:]
	if strings.EqualFold(head, strings.TrimSuffix(NamespaceExtPrefix, nameSep)) {
		j := strings.Index(rest, nameSep)
		if j <= 0 || j == len(rest)-1 {
			return "", name, false
		}
		return NamespaceExtPrefix + rest[:j], rest[j+1:], true
	}
	if head == "" || rest == "" {
		return "", name, false
	}
	return head, rest, true
}

// ---- internals ----

// tier is one rung of the discovery ladder: a directory to scan, or
// the embedded built-in set.
type tier struct {
	dir       string // "" when builtin is set
	label     string // human-facing Source
	namespace string
	builtin   bool
	// project marks a cwd-anchored rung. It drives Skill.Project, which
	// gates always-on pinning. Do not infer this from label: label is a
	// display string, and a wording change must not move a gate.
	project bool
}

// discoveryTiers lists the discovery rungs in priority order.
//
// trustProject gates every PROJECT-local (cwd-anchored) rung: when
// false, an untrusted workspace contributes no project SKILL.md and no
// extension-bundled skills (a cloned repo can't inject instructions
// into the prompt). The global/user dirs (tervaHome, ~/.claude,
// ~/.agents) are always included. See docs/plans/workspace-trust.md.
//
// The built-in rung sits between the native dirs and everything else.
// Above it: skills written FOR terva, where shadowing a built-in is a
// deliberate act. Below it: extension bundles, which ride along with
// an install, and the foreign-tool compat dirs, which are shared with
// a different runtime and were not authored against terva's built-ins
// at all. --no-builtin-skills removes the rung entirely, handing the
// bare names back to the tiers underneath.
func discoveryTiers(tervaHome, cwd, userHome string, includeUser, includeBuiltin bool, gate Gate) []tier {
	trustProject := gate.TrustProject
	var out []tier
	add := func(dir, label, namespace string, project bool) {
		if dir == "" {
			return
		}
		out = append(out, tier{dir: dir, label: label, namespace: namespace, project: project})
	}
	if includeUser {
		if cwd != "" && trustProject {
			// Both project-dir spellings, new name first (the rename's
			// dual-read seam; see envcompat.ProjectDirNames).
			for _, dirName := range envcompat.ProjectDirNames() {
				add(filepath.Join(cwd, dirName, "skills"), "project", NamespaceTerva, true)
			}
		}
		if tervaHome != "" {
			add(filepath.Join(tervaHome, "skills"), "global", NamespaceTerva, false)
		}
	}
	if includeBuiltin {
		out = append(out, tier{label: "built-in", namespace: NamespaceBuiltin, builtin: true})
	}
	if !includeUser {
		return out
	}
	// Extension bundles: an installed, enabled extension may ship a
	// skills/ directory beside its extension.json (data-only bundle
	// contribution — see docs/extensions.md). Ranked after the user's
	// own dirs and the built-ins so a bundle can never shadow either,
	// before the foreign-tool compat dirs. Project-ext bundles are
	// gated on trust (extensionSkillTiers honors the gate); the
	// project extension wouldn't load untrusted anyway, so its skills
	// can't either.
	out = append(out, extensionSkillTiers(tervaHome, cwd, gate)...)
	if cwd != "" && trustProject {
		add(filepath.Join(cwd, ".claude", "skills"), "project (claude)", NamespaceClaude, true)
	}
	if userHome != "" {
		add(filepath.Join(userHome, ".claude", "skills"), "global (claude)", NamespaceClaude, false)
	}
	if cwd != "" && trustProject {
		add(filepath.Join(cwd, ".agents", "skills"), "project (agents)", NamespaceAgents, true)
	}
	if userHome != "" {
		add(filepath.Join(userHome, ".agents", "skills"), "global (agents)", NamespaceAgents, false)
	}
	return out
}

// scanTier reads one directory rung: every subdirectory holding a
// SKILL.md becomes a candidate, tagged with the rung's label and
// namespace. A missing directory is not an error.
func scanTier(t tier) ([]*Skill, []error) {
	entries, err := os.ReadDir(t.dir)
	if err != nil {
		return nil, nil // missing dir is fine
	}
	var errs []error
	var out []*Skill
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(t.dir, e.Name(), "SKILL.md")
		s, err := load(path, t.label, t.namespace)
		if err != nil {
			if !os.IsNotExist(err) {
				errs = append(errs, fmt.Errorf("%s: %w", path, err))
			}
			continue
		}
		if clean := sanitizeName(s.Name); clean != s.Name {
			errs = append(errs, fmt.Errorf("%s: skill name %q contains %q, which is reserved to separate a namespace from a name; loaded as %q", path, s.Name, nameSep, clean))
			s.Name = clean
		}
		if s.Name == "" {
			s.Name = sanitizeName(e.Name())
		}
		s.Project = t.project
		out = append(out, s)
	}
	return out, errs
}

// extensionSkillTiers lists <extension>/skills for every enabled
// installed extension, global ($TERVA_HOME/extensions) before
// project (.terva/extensions, rename-aware spellings).
//
// Enabled-ness is extroots' answer, not one of three: the manifest's own
// `enabled` flag AND the operator's resolved disable_extensions. Skills used to
// honour only the first, so switching an extension off left it injecting
// instructions into the prompt after its tools and personas had gone.
//
// gate.TrustProject gates the project extension roots: an untrusted workspace
// contributes no project-ext-bundled skills (the project extension would not
// load there either). Global ($TERVA_HOME/extensions) bundles always
// contribute, subject to the same disable list.
func extensionSkillTiers(tervaHome, cwd string, gate Gate) []tier {
	var out []tier
	for _, r := range extroots.Enabled(tervaHome, cwd, gate) {
		skillsDir, ok := r.SubDir("skills")
		if !ok {
			continue
		}
		out = append(out, tier{
			dir:     skillsDir,
			label:   "extension",
			project: r.Project,
			// 🪤 The DIRECTORY name, not extroots' preferred name. Persona
			// namespaces by the manifest name; changing either here would
			// rename live refs, so the mismatch is recorded in extroots.Root
			// rather than resolved on the way past.
			namespace: NamespaceExtPrefix + sanitizeName(r.DirName),
		})
	}
	return out
}

// sanitizeName strips the namespace separator out of a skill or
// extension name. ':' is reserved so a qualified reference always
// parses as (namespace, name) and can never be ambiguous with a skill
// literally named "claude:handoff".
func sanitizeName(name string) string {
	return strings.ReplaceAll(name, nameSep, "-")
}

func load(path, source, namespace string) (*Skill, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	front, body := splitFrontmatter(string(raw))
	s := &Skill{
		Path:      path,
		Source:    source,
		Namespace: namespace,
		Body:      strings.TrimSpace(body),
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
// description, an allowed-tools list (either spelling), a permissions
// map of tool -> patterns, an argument hint, and the
// disable-model-invocation opt-out. AllowedTools drives lazy-visibility
// activation (see Skill.AllowedTools); Permissions is parsed for
// forward-compatibility but not yet enforced.
//
// Both the hyphen and underscore spellings are accepted for the
// multi-word keys: SKILL.md files travel between ecosystems that
// disagree about which is canonical, and a key terva silently ignores
// is a behaviour the author thought they had configured.
type frontmatter struct {
	Name                      string              `yaml:"name"`
	Description               string              `yaml:"description"`
	AllowedTools              []string            `yaml:"allowed-tools"`
	AllowedToolsAlt           []string            `yaml:"allowed_tools"`
	Permissions               map[string][]string `yaml:"permissions"`
	ArgumentHint              string              `yaml:"argument-hint"`
	ArgumentHintAlt           string              `yaml:"argument_hint"`
	DisableModelInvocation    bool                `yaml:"disable-model-invocation"`
	DisableModelInvocationAlt bool                `yaml:"disable_model_invocation"`
}

// parseFrontmatter unmarshals the SKILL.md YAML head into s. Malformed
// frontmatter degrades gracefully — the skill still loads (its name
// falls back to the directory basename in scanTier) rather than
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
	if fm.ArgumentHint != "" {
		s.ArgumentHint = fm.ArgumentHint
	} else {
		s.ArgumentHint = fm.ArgumentHintAlt
	}
	s.DisableModelInvocation = fm.DisableModelInvocation || fm.DisableModelInvocationAlt
}
