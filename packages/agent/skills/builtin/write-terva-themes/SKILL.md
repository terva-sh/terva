---
name: write-terva-themes
description: Help the user create, install, or package terva themes, including theme-only extensions.
---

# Writing terva themes

Use this skill when the user asks for help creating, editing,
installing, debugging, or packaging a terva color theme. Read this skill
before generating a theme file or advising on theme extension layout.

## What a terva theme is

A terva theme is a JSON file that overrides any subset of terva's built-in
light/dark theme values. Theme files are intentionally permissive:
nothing is required. A file may contain only colors, only spinner
settings, only syntax colors, or only metadata. Missing values always
inherit from the built-in detected light/dark default.

User themes are discovered from:

```text
$TERVA_HOME/themes/*.json
```

Extension themes are discovered in-place from loaded extension dirs:

```text
$TERVA_HOME/extensions/<extension>/theme.json
$TERVA_HOME/extensions/<extension>/themes/theme.json
<project>/.terva/extensions/<extension>/theme.json
<project>/.terva/extensions/<extension>/themes/theme.json
```

terva does **not** copy extension themes into `$TERVA_HOME/themes`; extension
owned themes stay in the extension directory. The settings picker stores
an absolute path for extension-owned themes and loads that file directly.

## Minimal examples

### Empty / metadata-only theme

Valid; inherits all colors/spinner/syntax from terva defaults.

```json
{
  "name": "my-theme",
  "description": "Metadata only; all visuals inherit terva defaults."
}
```

### Change one color for both light and dark

```json
{
  "name": "pink-accent",
  "colors": {
    "accent": 204
  }
}
```

### Change one color per mode

```json
{
  "name": "split-accent",
  "colors": {
    "dark": { "accent": 204 },
    "light": { "accent": 161 }
  }
}
```

### Spinner-only theme

Top-level spinner overrides are valid and apply to both modes.

```json
{
  "name": "custom-spinner",
  "description": "Only changes the busy spinner.",
  "spinner_frames": ["◢", "◣", "◤", "◥"],
  "spinner_messages": ["working"],
  "spinner_interval_ms": 120
}
```

### Dark-only theme also works in light terminals

If `colors.light` is missing, terva applies `colors.dark` overrides on
top of the built-in light default when running in light mode. The
inverse is also true: if dark is missing but light exists, light
settings are used on dark defaults.

```json
{
  "name": "custom-spinner",
  "description": "An alternative spinner for terva that only displays a single spinner text.",
  "colors": {
    "dark": {
      "spinner_frames": ["◢", "◣", "◤", "◥"],
      "spinner_messages": ["working"],
      "spinner_interval_ms": 120
    }
  }
}
```

## Full theme shape

All fields below are optional.

```json
{
  "name": "my-theme",
  "description": "Shown in /settings → color theme.",
  "color_descriptions": {
    "accent": "Optional documentation for humans. terva ignores this object."
  },
  "colors": {
    "dark": {
      "fg": 253,
      "muted": 244,
      "accent": 111,
      "user": 180,
      "user_bubble_bg": "#42454b",
      "user_bubble_fg": 248,
      "assistant": 117,
      "tool": 114,
      "tool_out": 245,
      "error": 203,
      "warning": 214,
      "spinner": 183,
      "selection_bg": 24,
      "selection_fg": 231,
      "spinner_frames": ["⠋", "⠙", "⠚", "⠞", "⠖", "⠦", "⠴", "⠲", "⠳", "⠓"],
      "spinner_messages": ["thinking", "working"],
      "spinner_interval_ms": 80,
      "syntax_base_style": "monokai",
      "syntax": {
        "keyword": "#81a1c1 bold",
        "keyword_constant": "#81a1c1",
        "keyword_declaration": "#81a1c1",
        "keyword_namespace": "#81a1c1",
        "keyword_reserved": "#81a1c1 bold",
        "keyword_type": "#88c0d0",
        "name_builtin": "#88c0d0",
        "name_function": "#8fbcbb",
        "name_class": "#a3be8c bold",
        "name_decorator": "#b48ead",
        "literal_string": "#a3be8c",
        "literal_string_escape": "#bf616a",
        "literal_number": "#d08770",
        "comment": "#616e88 italic",
        "comment_preproc": "#b48ead",
        "operator": "#eceff4",
        "punctuation": "#d8dee9",
        "text": "#e5e9f0"
      }
    },
    "light": {
      "fg": 236,
      "muted": 244,
      "accent": 33
    }
  }
}
```

You may also put overrides directly at the top level, or directly
under `colors`, when they should apply to both modes:

```json
{
  "name": "tiny",
  "accent": 204,
  "colors": {
    "spinner_messages": ["shipping"]
  }
}
```

## Color fields

- `fg` — default foreground text.
- `muted` — secondary text, dividers, gutters, inactive hints.
- `accent` — prompt bar, bullets, links, headings, active markers.
- `user` — user role label color; mostly compatibility.
- `user_bubble_bg` — background behind user message rows.
- `user_bubble_fg` — foreground inside user message rows.
- `assistant` — assistant/terva accent and spinner text.
- `tool` — tool names, success marks, diff additions.
- `tool_out` — plain tool-output text.
- `error` — errors, refused calls, diff deletions.
- `warning` — warnings and high context-usage state.
- `spinner` — reserved spinner color slot.
- `selection_bg` — highlighted row background.
- `selection_fg` — highlighted row foreground.

Most color fields are xterm-256 indexes (`0`–`255`).

`user_bubble_bg` supports richer terminal color forms:

```json
254
"#42454b"
{ "mode": "256", "index": 254 }
{ "mode": "ansi", "index": 100 }
{ "mode": "rgb", "r": 66, "g": 69, "b": 75 }
```

## Spinner fields

Spinner settings can appear at top level, under `colors`, or under
`colors.dark` / `colors.light`.

- `spinner_frames` — list of frame strings. Use single-cell glyphs
  when possible so status-bar alignment stays clean.
- `spinner_messages` — list of messages; terva chooses one per turn.
- `spinner_interval_ms` — frame interval in milliseconds; must be
  positive. Missing/invalid falls back to 80ms.

Example:

```json
{
  "spinner_frames": ["◐", "◓", "◑", "◒"],
  "spinner_messages": ["shipping pixels", "warming edge cache"],
  "spinner_interval_ms": 120
}
```

## Syntax fields

Syntax highlighting uses Chroma style entries. Values may include
attributes after the color, such as `bold`, `italic`, or `underline`.

Common fields:

- `keyword`
- `keyword_constant`
- `keyword_declaration`
- `keyword_namespace`
- `keyword_reserved`
- `keyword_type`
- `name_builtin`
- `name_function`
- `name_class`
- `name_decorator`
- `literal_string`
- `literal_string_escape`
- `literal_number`
- `comment`
- `comment_preproc`
- `operator`
- `punctuation`
- `text`

Example:

```json
{
  "colors": {
    "dark": {
      "syntax_base_style": "monokai",
      "syntax": {
        "keyword": "#f05b8d",
        "name_function": "#b675f1",
        "literal_string": "#58c760",
        "comment": "#a1a1a1 italic"
      }
    }
  }
}
```

## Installing a user theme

```bash
mkdir -p "$TERVA_HOME/themes"
cp my-theme.json "$TERVA_HOME/themes/my-theme.json"
```

Then open `/settings` and choose **color theme**. terva switches theme
immediately and persists the selection in `$TERVA_HOME/config.json`.

If a selected theme file is deleted, terva resets the setting to the
built-in auto/default theme.

## Theme-only extensions

A terva extension can exist only to ship a theme. No slash command,
subprocess, or executable is required when the extension contains a
valid theme file.

Layout:

```text
$TERVA_HOME/extensions/my-theme-extension/
├── extension.json
└── theme.json
```

or:

```text
$TERVA_HOME/extensions/my-theme-extension/
├── extension.json
└── themes/
    └── theme.json
```

`extension.json` for a theme-only extension:

```json
{
  "name": "my-theme-extension",
  "version": "1.0.0",
  "description": "Ships a terva color theme",
  "enabled": true
}
```

No `exec` is needed when `theme.json` or `themes/theme.json` exists.
If `exec` is present, terva treats it as a normal extension too.

In `/settings → color theme`, extension themes show source info in
the description, e.g. `from extension my-theme-extension — ...`.

## Recommendations

- Prefer short descriptions; settings wraps them, but concise is
  easier to scan.
- Keep spinner frames single-width when possible.
- For theme packages, use a unique extension name so the source label
  is clear.
- For user-editable themes, keep comments out of JSON; JSON comments
  are not supported.
- Validate with `python3 -m json.tool theme.json` before installing.
