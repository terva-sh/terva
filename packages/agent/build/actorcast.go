package build

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/i18n"
)

// loadProjectCast reads a project-declared cast from <cwd>/.terva/cast.json — a
// JSON object mapping actor NAME → REF (a Persona name or a character-card
// path), the same shape as --cast. Honored ONLY in a trusted project (like the
// rest of .terva/), so a cloned repo can't seed a cast into an untrusted run.
// A missing or malformed file yields nil (the file is optional).
func loadProjectCast(cwd string, trusted bool) map[string]string {
	if !trusted {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(cwd, ".terva", "cast.json"))
	if err != nil {
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return nil
	}
	return m
}

// MergedCastRefs is the effective cast declaration (NAME → REF) for a run: the
// trusted project's .terva/cast.json overlaid by --cast, so an explicit launch
// flag overrides the project file per name. Empty when nothing declares a cast.
// Entries with an empty name OR ref are dropped from both sources (the --cast
// parser already rejects them; a malformed cast.json must not slip through and
// become a dispatchable, identity-less actor).
func MergedCastRefs(args Args, cwd string, trusted bool) map[string]string {
	out := map[string]string{}
	add := func(k, v string) {
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if k != "" && v != "" {
			out[k] = v
		}
	}
	for k, v := range loadProjectCast(cwd, trusted) {
		add(k, v)
	}
	for k, v := range args.Cast {
		add(k, v)
	}
	return out
}

// CastSkinActive reports whether this session may carry the play cast skin —
// the actor_spawn tool and the cast addendum that advertises it. --play only,
// and never under --no-tools (which suppresses ALL tools, so the session must
// not be handed — or told about — the one surviving tool). The single gate for
// both injection sites, the twin of HasBaseWorkspaceTools for the coding skin.
func CastSkinActive(args Args) bool {
	return args.Experience == ExperiencePlay && !args.NoTools
}

// castAddendum is the play director's proactive-use NUDGE (Toggle 2, shared with
// auto-swarm): the pacing disposition a bare tool wouldn't give. The mechanics
// (how actor_spawn works) live in its Description, and the cast roster lives in
// its `actor` schema enum, so this no longer advertises the list — it only
// nudges pacing + keeps the narrator's authority. Emitted only in --play with a
// non-empty cast AND when the nudge is on (see Resolve).
func castAddendum() string {
	return i18n.P("play.cast.addendum",
		"You can give voice to your cast with the actor_spawn tool — its `actor` options are the members you can call. Bring an actor in for beats that matter (a greeting, a confrontation, a revelation), not for every line — a scene needs only a voice or two per exchange. You remain the narrator and the source of truth about the world; the cast lends voices, not authority.")
}

// BuildActorCast resolves the --cast declaration (NAME=REF) into the actor_spawn
// tool's cast: each REF is a Persona name or a character-card path. Card paths
// are absolutized against the launch cwd so the dispatched child (which runs in
// the same cwd) loads the same file. Refs are validated NOW: a typo'd Persona
// or a missing card file fails at launch naming the offending NAME=REF, rather
// than opaquely mid-scene ("the actor exited before responding") the first time
// the director voices that actor. Returns nil for an empty declaration.
func BuildActorCast(raw map[string]string, cwd string) (map[string]tools.CastMember, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	cast := make(map[string]tools.CastMember, len(raw))
	for name, ref := range raw {
		ref = strings.TrimSpace(ref)
		if looksLikeCardPath(ref) {
			p := ref
			if !filepath.IsAbs(p) {
				p = filepath.Join(cwd, p)
			}
			if _, err := os.Stat(p); err != nil {
				return nil, fmt.Errorf("cast %s=%s: %w", name, ref, err)
			}
			cast[name] = tools.CastMember{Card: p}
		} else {
			if _, err := ResolvePersona(ref); err != nil {
				return nil, fmt.Errorf("cast %s=%s: %w", name, ref, err)
			}
			cast[name] = tools.CastMember{Persona: ref}
		}
	}
	return cast, nil
}

// looksLikeCardPath reports whether a --cast REF names a card file (a path, or a
// .json/.png) rather than a Persona name.
func looksLikeCardPath(ref string) bool {
	return strings.ContainsAny(ref, `/\`) || strings.HasSuffix(ref, ".png") || strings.HasSuffix(ref, ".json")
}
