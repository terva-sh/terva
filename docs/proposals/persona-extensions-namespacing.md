# Proposal — extension personas + namespacing (persona phase 3)

- **Status:** **Phase A implemented** (2026-06-30) — namespacing + global
  extension personas + visible provenance shipped on `feat/persona-swarm-dispatch`
  (commit `6c12602`, docs `0e7c645`). Phase B (project-scoped extension personas,
  trust-gated, plus the project `disable_extensions` list) is not yet implemented.
- **Date:** 2026-06-30
- **Scope:** `packages/agent/persona.go` (a `Namespace` field, qualified-name
  resolution, precedence-by-qualified-name dedup), a new `extensionPersonaDirs`
  mirroring `skills.extensionSkillDirs`, provenance in `terva persona list` +
  the dispatch roster, and docs. No protocol/SDK change (Phase A).
- **Origin:** Extends persona v1 (shipped, `ba16277`) and phase-2 dispatch
  (`docs/proposals/persona-swarm-dispatch.md`). Lets an extension ship a
  dispatchable specialist persona — e.g. a web-research extension shipping a
  `deep-researcher` — with namespaced, visible provenance and user override.

## TL;DR

Extensions contribute personas the way they already contribute **skills**: a
bundle `personas/` directory, found by a **static disk scan** — no protocol, no
running subprocess. Personas gain a **namespace = their grouping** (a team
subdir, or the extension name), surfaced as `namespace:name`. That one model
delivers collision-safety, user override, and **visible provenance**. A disabled
extension contributes nothing, the same as its skills.

## Mechanism: bundle dir, not protocol

There is **no** `register_skill`/`register_persona` frame; bundle `skills/` on
disk is the only way extensions contribute skills, via a pure static scan
(`skills.extensionSkillDirs`, `packages/agent/skills/skills.go:296`). A
`register_persona` frame would be net-new *and* hit the same startup-timing
circularity that ruled extension-registration out for the core persona (the
extension handshakes *after* the agent exists; persona resolves at sub-agent
startup). So personas mirror the skill precedent: an **`extensionPersonaDirs`**
that scans `<extroot>/<ext>/personas`, gated like bundle skills.

## Namespacing — the unifying model

A persona's **namespace is its grouping**, derived uniformly from where it
lives:

| path | namespace | qualified name |
|---|---|---|
| `personas/mieli.md` | — | `mieli` |
| `personas/review-crew/vartija.md` | `review-crew` | `review-crew:vartija` |
| `extensions/zot-web/personas/deep-researcher.md` | `zot-web` | `zot-web:deep-researcher` |

The extension name is **just a namespace**, exactly like a team subdir — no
special case. Separator is **`:`** (not `/`, which already means "this is a file
path" in `--persona`).

**Resolution is back-compatible.** A bare name still resolves (phase-1's
`--persona vartija` keeps working); the qualified form disambiguates:
- `--persona zot-web:deep-researcher` — exact qualified match.
- `--persona deep-researcher` — bare match across namespaces, by precedence.
- `--persona ./x.md` — a path (unchanged).

**Precedence + override.** The library tiers are **user on-disk
(`$TERVA_HOME/personas/**`) > extension bundles > embedded crew**, deduped **by
qualified name**. So a user overrides an extension persona by dropping
`$TERVA_HOME/personas/zot-web/deep-researcher.md` — same namespace (mirrored as a
subdir), same qualified name, higher tier → it shadows the extension's, exactly
like overriding a built-in. Two extensions both shipping `deep-researcher` never
collide (`zot-web:deep-researcher` vs `other:deep-researcher`).

**Embedded teams namespace too** (uniformity): the crew becomes
`review-crew:vartija` etc. Bare `vartija` still resolves. This is a visible
change from phase-1's flat names — intentional.

**Bare-name ambiguity:** resolve by precedence tier; **warn** when two personas
in the *same* tier share a bare name. The qualified form is the disambiguator.

## Visible provenance (required)

Personas surface **where they come from**, everywhere they appear:

- `Persona.Namespace` + `Persona.Source` — `"embedded:…"`, an on-disk path, or
  `"ext:<name>:…"`.
- **`terva persona list`** gains a source view — the qualified name plus where it
  was loaded from, and a note when a user persona shadows an extension/built-in:

  ```
  NAME                       SPECIALTY          SOURCE
  review-crew:vartija        security review    built-in
  zot-web:deep-researcher    web research       ext:zot-web
  zot-web:deep-researcher    web research       $TERVA_HOME/personas/zot-web/deep-researcher.md (overrides ext:zot-web)
  ```
- **The dispatch roster** (auto-swarm addendum) annotates each entry with its
  origin, so the coordinator *and* anyone reading the prompt see it:
  `- zot-web:deep-researcher — web research — good for: web-research (via zot-web)`.

## Trust & disable — follow skills, then close the gap

`extensionPersonaDirs` mirrors `extensionSkillDirs`:
- **Project gating:** the `cwd/.terva/extensions` roots are only scanned when the
  workspace is **trusted** (Phase B; Phase A is `$TERVA_HOME/extensions` only).
- **Disabled extensions contribute nothing:** read each `extension.json`'s
  `enabled` flag and skip disabled extensions — same as skills, satisfying "when
  the extension is disabled its personas don't load" for the normal `/extensions`
  toggle.

One honest divergence from skills: the bundle-skill path honors the manifest
`enabled` flag but **not** the config `disable_extensions` list. For personas
that gap bites harder — a config-disabled extension's persona would be
dispatchable while its *tools* don't load (disable blocks spawn), yielding a
deep-researcher with no web tools. So we **close it for personas**: honor the
**user** `disable_extensions` list in Phase A (readable from config without
cwd), the **project** list in Phase B.

## The zot-web flow (confirmed end to end)

A dispatched swarm sub-agent loads extensions (`setupNonInteractiveExtensions`,
`swarm_agent.go:48`) → it gets zot-web's **tools** (spawned, merged registry),
and because persona discovery is a static disk scan it also resolves zot-web's
**`deep-researcher` charter**. Capability + identity from one extension, both
present in the sub-agent. A coordinator sees `zot-web:deep-researcher` in the
roster and dispatches `swarm_spawn(persona="zot-web:deep-researcher", task=…)`.

## `extensionPersonaDirs` (sketch)

Mirror `skills.extensionSkillDirs` (`skills.go:296`):

```go
// extensionPersonaDirs returns (namespace, dir) pairs for each enabled,
// non-config-disabled extension that ships a personas/ directory. Phase A:
// $TERVA_HOME/extensions only; Phase B adds trusted cwd/.terva/extensions.
func extensionPersonaDirs(tervaHome string, userDisabled map[string]bool) []extPersonaDir {
	root := filepath.Join(tervaHome, "extensions")
	entries, _ := os.ReadDir(root)
	var out []extPersonaDir
	for _, e := range entries {
		ext := e.Name()
		if userDisabled[ext] {
			continue
		}
		extDir := filepath.Join(root, ext)
		var m struct{ Enabled *bool `json:"enabled"` }
		mb, err := os.ReadFile(filepath.Join(extDir, "extension.json"))
		if err != nil || (json.Unmarshal(mb, &m) == nil && m.Enabled != nil && !*m.Enabled) {
			continue
		}
		pdir := filepath.Join(extDir, "personas")
		if st, err := os.Stat(pdir); err == nil && st.IsDir() {
			out = append(out, extPersonaDir{namespace: ext, dir: pdir})
		}
	}
	return out
}
```

Then in persona discovery: read user personas (namespace = subdir), then
extension personas (namespace = ext name), then embedded; tag each with
`Namespace` + `Source`; dedup by qualified name keeping the highest tier.

## Phasing

- **Phase A:** namespacing (the load-bearing refactor) + **global** extension
  personas + provenance surfaces + user-config disable. No protocol/SDK/cwd
  context. *Medium* — bigger than phase-2a because it touches the persona
  naming/dedup core.
- **Phase B:** **project** extension personas (trust-gated) + project
  `disable_extensions` — threads cwd/trust/disable through the persona API.

## Implementation sketch

1. `persona.go`: add `Persona.Namespace`; compute it from the source grouping in
   `parsePersona`/the readers; add a `Qualified()` helper (`ns + ":" + name`,
   bare when ns==""). Rework `loadPersonaByName` to match qualified-or-bare and
   `AllPersonas` to dedup by qualified name across the three tiers (user, ext,
   embedded).
2. `extensionPersonaDirs` (+ wire into the readers); `Source` = `ext:<name>`.
3. `persona list`/`autoSwarmAddendum` roster: show qualified name + source/origin;
   `personaRoster` annotates `(via <ext>)`.
4. Tests: namespace derivation; qualified + bare resolution; user-overrides-ext
   by qualified name; disabled/absent extension contributes nothing; provenance
   strings; bare-name ambiguity warns.
5. Docs: bundle `personas/` convention in `docs/extensions.md` (beside the
   `skills/` section), the `write-terva-persona` skill, and the persona-format
   proposal.

## Decisions resolved (2026-06-30)

1. **Mechanism** — bundle `personas/` dir (A), confirmed by the absence of any
   register frame.
2. **Namespacing** — uniform: namespace = grouping dir / ext name; qualified
   `ns:name` with `:`; bare names back-compatible; embedded teams namespaced too.
3. **Override/precedence** — user > extension > embedded, deduped by qualified
   name; user mirrors the namespace to shadow.
4. **Provenance** — visible everywhere (`persona list` source view, roster
   `(via <ext>)`, `Persona.Namespace`/`Source`).
5. **Disable** — follow skills (manifest `enabled`) and additionally honor the
   user `disable_extensions` list; project list is Phase B.

## Open questions

1. Bare-name ambiguity *across tiers* is resolved by precedence; do we also want
   a hard error (vs warn) when the user explicitly bare-names something that's
   ambiguous within the top tier?
2. Should a persona's frontmatter be allowed to *declare* a namespace (override
   the dir-derived one), or is the directory the sole source of truth? Lean:
   directory only (no self-declared namespace), to keep provenance honest.
3. Nested grouping under an extension (`extensions/zot/personas/team/x.md`) —
   flatten to `zot:x`, or `zot/team:x`? Lean: namespace is the extension name
   only; deeper subdirs are organizational, name stays `zot:x`.
