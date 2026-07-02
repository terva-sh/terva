package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"terva.sh/terva/packages/agent/tools"
)

// loadProjectCast reads a project-declared cast from <cwd>/.terva/cast.json — a
// JSON object mapping actor NAME → REF (a persona name or a character-card
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

// mergedCastRefs is the effective cast declaration (NAME → REF) for a run: the
// trusted project's .terva/cast.json overlaid by --cast, so an explicit launch
// flag overrides the project file per name. Empty when nothing declares a cast.
// Entries with an empty name OR ref are dropped from both sources (the --cast
// parser already rejects them; a malformed cast.json must not slip through and
// become a dispatchable, identity-less actor).
func mergedCastRefs(args Args, cwd string, trusted bool) map[string]string {
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

// castSkinActive reports whether this session may carry the play cast skin —
// the actor_spawn tool and the cast addendum that advertises it. --play only,
// and never under --no-tools (which suppresses ALL tools, so the session must
// not be handed — or told about — the one surviving tool). The single gate for
// both injection sites, the twin of hasBaseWorkspaceTools for the coding skin.
func castSkinActive(args Args) bool {
	return args.Experience == ExperiencePlay && !args.NoTools
}

// castAddendum is the play director's system-prompt block: it advertises the
// declared cast to the model — so dispatch doesn't depend on the model noticing
// the actor_spawn tool in its list — and gives pacing guidance (the soft
// interaction budget). Emitted only in --play with a non-empty cast.
func castAddendum(cast map[string]string) string {
	names := make([]string, 0, len(cast))
	for n := range cast {
		names = append(names, n)
	}
	sort.Strings(names)

	var sb strings.Builder
	sb.WriteString("You have a CAST you can give voice to with the actor_spawn tool. ")
	sb.WriteString("Call actor_spawn(actor, situation) to have a cast member respond in character to a situation you describe; it returns their line for you to weave into the scene as the narrator. ")
	sb.WriteString("Dispatch by NAME only, from this cast:\n")
	for _, n := range names {
		sb.WriteString("  - " + n + "\n")
	}
	sb.WriteString("You remain the narrator and the source of truth about the world — the cast lends voices, not authority. ")
	sb.WriteString("Bring an actor in for beats that matter (a greeting, a confrontation, a revelation), not for every line — a scene needs only a voice or two per exchange.")
	return sb.String()
}

// buildActorCast resolves the --cast declaration (NAME=REF) into the actor_spawn
// tool's cast: each REF is a persona name or a character-card path. Card paths
// are absolutized against the launch cwd so the dispatched child (which runs in
// the same cwd) loads the same file. Refs are validated NOW: a typo'd persona
// or a missing card file fails at launch naming the offending NAME=REF, rather
// than opaquely mid-scene ("the actor exited before responding") the first time
// the director voices that actor. Returns nil for an empty declaration.
func buildActorCast(raw map[string]string, cwd string) (map[string]tools.CastMember, error) {
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
// .json/.png) rather than a persona name.
func looksLikeCardPath(ref string) bool {
	return strings.ContainsAny(ref, `/\`) || strings.HasSuffix(ref, ".png") || strings.HasSuffix(ref, ".json")
}
