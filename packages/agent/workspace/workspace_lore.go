package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/lore"
)

// The lore inspector + editor surface: lists the session's authored keyword-
// triggered context entries, and lets the operator create/edit/delete USER lore
// ($TERVA_HOME/lore), keyed by name→slug filename. Writes re-discover the pane
// live; the actual per-turn INJECTION is baked at session build, so edits take
// effect on NEW sessions (documented in the pane). Only user entries the web
// manages (a $TERVA_HOME/lore/<slug>.md exists) are editable; ext-bundle, card,
// and hand-authored/other-tier entries are read-only.

// loreSnapshot returns a race-safe copy of the discovered entries (read by the
// surface list, the pane, and mutated by reloadLore).
func (s *wsSession) loreSnapshot() []lore.Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loreEntries
}

// loreView builds the lore pane from the discovered entries. CanProject offers
// project-scope authoring only in a trusted workspace (project lore is
// trust-gated on load).
func (s *wsSession) loreView() *ctrlproto.LoreView {
	v := &ctrlproto.LoreView{CanProject: s.trusted.Load()}
	// The last turn's activation trace, keyed the way loreFiredOf resolves a
	// source (source, else name), so a triggered entry can be annotated with
	// whether it fired, why, and whether the budget dropped it.
	s.mu.Lock()
	rec := s.loreFired
	s.mu.Unlock()
	trace := map[string]build.LoreFired{}
	if rec != nil {
		for _, f := range rec.Get() {
			trace[loreTraceKey(f.Name, f.Source)] = f
		}
	}
	for _, e := range s.loreSnapshot() {
		scope := s.loreEntryScope(e.Name)
		entry := ctrlproto.LoreEntry{
			Name:     e.Name,
			Keys:     e.Keys,
			Constant: e.Constant,
			Source:   e.Source,
			Content:  e.Content,
			Editable: scope != "",
			Scope:    scope,
		}
		if f, ok := trace[loreTraceKey(e.Name, e.Source)]; ok {
			entry.Fired = true
			entry.MatchedKeys = f.Keys
			entry.DroppedForBudget = f.Dropped
		}
		v.Entries = append(v.Entries, entry)
	}
	return v
}

// loreTraceKey is the identity a loreView entry and an activation-trace entry
// join on — the display source, falling back to the name (matching loreFiredOf).
func loreTraceKey(name, source string) string {
	if source != "" {
		return source
	}
	return name
}

// loreAction creates/edits ("save") or removes ("delete") a user lore entry,
// then re-discovers so the pane reflects it. Injection updates on new sessions.
func (s *wsSession) loreAction(action string, args map[string]string) error {
	switch action {
	case "save":
		name := strings.TrimSpace(args["name"])
		if name == "" {
			return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "save: missing name")
		}
		scope, err := s.loreScope(args["scope"])
		if err != nil {
			return err
		}
		constant := args["constant"] == "true"
		var keys []string
		for k := range strings.SplitSeq(args["keys"], ",") {
			if k = strings.TrimSpace(k); k != "" {
				keys = append(keys, k)
			}
		}
		if strings.TrimSpace(args["content"]) == "" {
			return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "save: content is required")
		}
		if !constant && len(keys) == 0 {
			return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "save: a non-constant entry needs at least one key")
		}
		if err := saveLoreEntry(s.cwd, scope, name, keys, constant, args["content"]); err != nil {
			return ctrlproto.Errorf(ctrlproto.CodeInternal, "save: %v", err)
		}
	case "delete":
		name := strings.TrimSpace(args["name"])
		if name == "" {
			return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "delete: missing name")
		}
		scope, err := s.loreScope(args["scope"])
		if err != nil {
			return err
		}
		if err := deleteLoreEntry(s.cwd, scope, name); err != nil {
			return ctrlproto.Errorf(ctrlproto.CodeInternal, "delete: %v", err)
		}
	default:
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "unknown lore action %q", action)
	}
	s.reloadLore()
	s.broadcast(ctrlproto.SurfaceUpdatedEvent("lore"))
	return nil
}

// reloadLore re-discovers lore for the pane after an edit and re-wires the
// agent's per-turn context provider so KEYWORD-triggered lore takes effect on the
// next turn (live). CONSTANT lore is baked into the system prompt at build, so it
// stays new-session — deliberately, to keep the provider's prompt cache warm.
func (s *wsSession) reloadLore() {
	args := s.argsSnapshot()
	var entries []lore.Entry
	if !args.NoLore {
		if cfg, _ := config.LoadConfig(); cfg.Lore == nil || *cfg.Lore {
			entries, _, _ = lore.Discover(config.TervaHome(), s.cwd, s.trusted.Load())
		}
	}
	s.mu.Lock()
	s.loreEntries = entries
	s.mu.Unlock()

	// Live triggered-lore: a fresh Resolve rebuilds loreTriggered from the edited
	// files; swap its per-turn provider onto the running agent — AND its
	// side-effect-free peek twin, so the /context size view reflects the edited
	// lore too (not the stale pre-edit set). Best-effort — skipped if Resolve
	// fails (e.g. no credential in a test).
	if s.agent != nil {
		if rr, err := build.Resolve(args, true); err == nil {
			s.agent.SetContextProvider(rr.PerTurnContext(s.agent))
			s.agent.SetContextProviderPeek(rr.PerTurnContextPeek(s.agent))
			s.rewireTail(rr)
		}
	}
}

// rewireTail re-points the retained per-turn tail record handles at rr's fresh
// records — a new Resolve behind a swapped context provider mints new note /
// user-persona / activation-trace records — and re-seeds the live-authored ones
// (note, user-persona description) from the durable meta. Without the re-seed a
// tail rebuild would orphan the author's note the surface keeps writing to
// (the pre-4b bug this centralizes the fix for): the surface would write the
// old record while the tail read the new empty one. Call it right after
// installing rr's PerTurnContext. The caller must NOT hold s.mu.
func (s *wsSession) rewireTail(rr build.Resolved) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if nr := rr.Note(); nr != nil {
		nr.Set(s.sess.Meta.Note)
		s.note = nr
	}
	if ur := rr.User(); ur != nil {
		ur.Set(s.sess.Meta.UserDescription)
		s.user = ur
	}
	s.loreFired = rr.LoreFired()
}

// loreDir is the directory the web manages entries in, per scope: user
// ($TERVA_HOME/lore) or project (<cwd>/.terva/lore).
func loreDir(cwd, scope string) string {
	if scope == "project" {
		return filepath.Join(cwd, ".terva", "lore")
	}
	return filepath.Join(config.TervaHome(), "lore")
}

// loreFile is the path the web manages an entry at: <loreDir>/<slug>.md.
func loreFile(cwd, scope, name string) string {
	return filepath.Join(loreDir(cwd, scope), loreSlug(name)+".md")
}

// loreEntryScope reports which tier a web-managed file backs this entry — project
// (trusted only) wins over user, matching the discovery precedence. "" = not a
// web-managed entry (read-only).
func (s *wsSession) loreEntryScope(name string) string {
	if s.trusted.Load() {
		if _, err := os.Stat(loreFile(s.cwd, "project", name)); err == nil {
			return "project"
		}
	}
	if _, err := os.Stat(loreFile(s.cwd, "user", name)); err == nil {
		return "user"
	}
	return ""
}

// loreScope validates a requested edit scope: "project" needs a trusted
// workspace; anything else resolves to "user".
func (s *wsSession) loreScope(want string) (string, error) {
	if want == "project" {
		if !s.trusted.Load() {
			return "", ctrlproto.Errorf(ctrlproto.CodeUnsupported, "project lore needs a trusted workspace")
		}
		return "project", nil
	}
	return "user", nil
}

// loreSlug derives a stable filename stem from an entry name.
func loreSlug(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "entry"
	}
	return out
}

type loreFrontmatterOut struct {
	Name     string   `yaml:"name"`
	Keys     []string `yaml:"keys,omitempty"`
	Constant bool     `yaml:"constant,omitempty"`
}

// saveLoreEntry writes (or overwrites) a lore entry in the given scope,
// validating it through the real parser before touching disk. Atomic.
func saveLoreEntry(cwd, scope, name string, keys []string, constant bool, content string) error {
	front, err := yaml.Marshal(loreFrontmatterOut{Name: name, Keys: keys, Constant: constant})
	if err != nil {
		return err
	}
	doc := "---\n" + string(front) + "---\n\n" + strings.TrimRight(content, "\n") + "\n"
	if _, ok, err := lore.ParseEntry(doc, "web"); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("entry is disabled or invalid")
	}
	if err := os.MkdirAll(loreDir(cwd, scope), 0o755); err != nil {
		return err
	}
	path := loreFile(cwd, scope, name)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(doc), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// deleteLoreEntry removes a web-managed lore entry by name from the given scope.
func deleteLoreEntry(cwd, scope, name string) error {
	path := loreFile(cwd, scope, name)
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("not a web-managed lore entry")
	}
	return os.Remove(path)
}
