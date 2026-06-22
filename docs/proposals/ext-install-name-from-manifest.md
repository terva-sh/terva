# Proposal — `terva ext install` should name the install dir from the manifest, not the source basename

- **Status:** Implemented (2026-06-22) — in `installOne`, plus a security
  hardening the original proposal missed: the install name (from the manifest
  OR an explicit override) is now validated as a single safe path component
  (`safeInstallName`), so an untrusted manifest/pack name can't escape the
  extensions dir via `..`/separators. An unsafe manifest name falls back to
  the source basename; an unsafe override errors.
- **Date:** 2026-06-22
- **Scope:** `packages/agent/extcmd.go` (`installOne`); small, self-contained.
- **Origin:** Hit while building `terva-ext-obsidian` (manifest name `obsidian`,
  repo dir `terva-ext-obsidian`). The extension installed to
  `extensions/terva-ext-obsidian` while terva keys it as `obsidian`, so the
  extension's `just install` has to rename the dir as a workaround. This affects
  every `terva-ext-<name>` repo, which is a natural naming convention.

## TL;DR

`terva ext install <path|url>` derives the install **directory name** from the
**source basename** (`filepath.Base`), but terva identifies an extension by its
**manifest `name`**. When those differ, the extension installs to a dir that
doesn't match its canonical identity. Make `installOne` name the install dir from
`extension.json`'s `name` field (when no explicit `nameOverride` is given),
falling back to the basename only if the manifest is missing/unreadable or
`name` is empty. This matches how `terva ext list`/`remove` already key
extensions and what `extmigrate.go` already calls the "canonical" dir.

## Problem

`installOne` (`packages/agent/extcmd.go`, ~L252) names the destination from the
source path basename:

- **Local path:** validates `<src>/extension.json` exists, then
  `name := filepath.Base(absSrc)` → `out = extensions/<name>`.
- **Git URL:** `name := strings.TrimSuffix(filepath.Base(src), ".git")` →
  clones into `extensions/<name>`, then validates `extension.json`.

So a repo whose directory name differs from its manifest `name` installs under
the *wrong* directory name. Concretely, `terva-ext-obsidian` (manifest
`"name": "obsidian"`) installs to `extensions/terva-ext-obsidian`, yet:

- `terva ext list` shows it as `obsidian` (the manifest name) — the dir is the
  odd one out.
- `terva ext remove obsidian` works by name, but the on-disk dir is
  non-canonical.
- `extmigrate.go` already treats the **manifest name** as the canonical dir name
  (`scanInstalledExtensions` reads `mf.Name`; the duplicate-migration logic
  renames non-canonical dirs to `extensions/<canonical>`). Installing by basename
  fights that.

The mismatch is common: mirror-friendly repos are frequently named
`terva-ext-<name>`, so without this fix *every* such extension needs a
post-install rename.

## Proposed change

In `installOne`, when `nameOverride == ""`, derive the install name from the
source's `extension.json` `name` field instead of the path basename. Fall back to
the basename only if the manifest can't be read or `name` is empty. Keep
`nameOverride` (and any future `--name` flag) taking precedence.

The `Manifest` type already exists with the field:

```go
// packages/agent/extdriver/extension.go
type Manifest struct {
    Name string `json:"name"`
    // ...
}
```

Read it the same way `scanInstalledExtensions` does
(`packages/agent/extmigrate.go`, ~L49):

```go
raw, err := os.ReadFile(filepath.Join(dir, "extension.json"))
var mf extdriver.Manifest
json.Unmarshal(raw, &mf) // use mf.Name when non-empty
```

A helper keeps both branches honest:

```go
// manifestName returns the declared extension name from <dir>/extension.json,
// or "" if it can't be read / is unset.
func manifestName(dir string) string {
    raw, err := os.ReadFile(filepath.Join(dir, "extension.json"))
    if err != nil {
        return ""
    }
    var mf extdriver.Manifest
    if json.Unmarshal(raw, &mf) != nil {
        return ""
    }
    return strings.TrimSpace(mf.Name)
}
```

### Local-path branch

`extension.json` is already stat-validated here. Read the manifest name from
`absSrc` and prefer it:

```go
name := nameOverride
if name == "" {
    name = manifestName(absSrc)        // canonical identity
}
if name == "" {
    name = filepath.Base(absSrc)       // fallback: legacy behavior
}
// (existing guards: reject ".", "..", "/", "")
out = filepath.Join(dest, name)
```

### Git-URL branch

The clone target is computed *before* the manifest is available, so clone first
(as today), then canonicalize after validation:

```go
// after the clone + extension.json validation succeed:
if nameOverride == "" {
    if mn := manifestName(out); mn != "" && mn != filepath.Base(out) {
        canonical := filepath.Join(dest, mn)
        if _, err := os.Stat(canonical); err == nil {
            _ = os.RemoveAll(out)
            return canonical, errExtAlreadyInstalled
        }
        if err := os.Rename(out, canonical); err != nil {
            return out, fmt.Errorf("canonicalize install dir: %w", err)
        }
        out = canonical
    }
}
```

`extmigrate.go` (~L160–192) already does an equivalent canonical-rename with an
`extension.json` re-validation — reuse or mirror that helper rather than
re-implementing, if convenient.

## Edge cases & compatibility

- **`nameOverride` precedence preserved.** An explicit name still wins; this only
  changes the *default*.
- **Fallback is the old behavior.** A source without a readable manifest name
  installs to the basename exactly as today — no regression for odd layouts.
- **Same-name collision is correct.** Two sources with the same manifest `name`
  are the *same* extension and should collide (`errExtAlreadyInstalled`).
- **Existing installs are untouched.** This only affects *new* installs. A
  previously basename-installed extension keeps working; the duplicate-migration
  path already canonicalizes it on the next relevant trigger.
- **`name` vs. dir drift after manual edits** is out of scope (the manifest is
  the source of truth on install).

## Testing

- Local install where `manifest.name != basename(src)` → dir is
  `extensions/<manifest.name>`.
- Local install where they match → unchanged.
- Source missing `extension.json` `name` (or unreadable) → falls back to
  basename.
- `nameOverride` set → wins regardless of manifest.
- Git-URL install with `name != cloned-basename` → cloned dir renamed to
  `extensions/<manifest.name>`; pre-existing canonical dir → `errExtAlreadyInstalled`
  and the temp clone is cleaned up.
- Re-install of the same extension from a differently-named source → detected as
  already installed.

## Downstream cleanup (after this lands)

`terva-ext-obsidian`'s `just install` currently installs, then locates the dir by
name and renames it to `extensions/obsidian`. Once this ships, that recipe
simplifies to: `terva ext install .`, locate the (now-canonical) dir by name,
copy the built binary in. (The binary-copy step stays because `ext install`
git-ignores the built binary.)
