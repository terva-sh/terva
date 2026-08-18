# terva themes

terva themes are JSON files that override any subset of the built-in
light/dark TUI theme. Nothing is required: a theme can change one
color, only the spinner, only syntax highlighting, or all of them.
Missing values inherit from terva's built-in default for the detected
terminal background.

## Built-in themes

| name | what it is |
|---|---|
| `auto` | detect the terminal background, use the matching default (this is the default) |
| `dark` / `light` | the built-in defaults, pinned regardless of detection |
| `dark-daltonized` / `light-daltonized` | the defaults with every red/green semantic distinction re-based on a blue/orange axis (deuteranopia-friendly): diff additions and git `+` read blue, errors and `-` read vermillion, and the status-bar meters climb a cyan → amber → magenta ramp |
| `daltonized` | picks `dark-daltonized` or `light-daltonized` by the detected background, like `auto` |

## Where themes live

User themes are discovered from:

```text
$TERVA_HOME/themes/*.json
```

Open `/settings` and choose **color theme** to switch. Changes are
saved in `$TERVA_HOME/config.json` and apply immediately. If the selected
file is deleted later, terva resets the setting to the built-in auto
(default) theme.

Theme files bundled with extensions are discovered in-place from loaded
extension directories:

```text
$TERVA_HOME/extensions/<extension>/theme.json
$TERVA_HOME/extensions/<extension>/themes/theme.json
<project>/.terva/extensions/<extension>/theme.json
<project>/.terva/extensions/<extension>/themes/theme.json
```

terva does **not** copy extension themes into `$TERVA_HOME/themes`; extension
owned themes stay in the extension directory. The settings picker shows
source info such as `from extension my-theme-extension`.

## Minimal themes

All of these are valid.

Metadata only:

```json
{
  "name": "my-theme",
  "description": "Metadata only; all visuals inherit terva defaults."
}
```

One shared color for both light and dark terminals:

```json
{
  "name": "pink-accent",
  "colors": {
    "accent": 204
  }
}
```

One color per mode:

```json
{
  "name": "split-accent",
  "colors": {
    "dark": { "accent": 204 },
    "light": { "accent": 161 }
  }
}
```

Spinner-only:

```json
{
  "name": "custom-spinner",
  "description": "Only changes the busy spinner.",
  "spinner_frames": ["◢", "◣", "◤", "◥"],
  "spinner_messages": ["working"],
  "spinner_interval_ms": 120
}
```

Dark-only themes still work on light terminals. If `colors.light` is
missing, terva applies `colors.dark` overrides on top of the built-in
light default. The inverse also works.

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

## Full shape

All fields are optional.

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
      "background": "#0b1020",
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
      "spinner_messages": ["thinking", "{verb} the {noun}"],
      "spinner_interval_ms": 80,
      "greetings": ["{verb} the {noun}.", "point me at anything."],
      "flavor_verbs": ["harness", "wrangle", "tame"],
      "flavor_nouns": ["wildcard", "chaos", "entropy"],
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

You may also put overrides directly at the top level or directly under
`colors` when they should apply to both modes:

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

Most color fields are xterm-256 indexes (`0`–`255`).

- `fg` — default foreground text.
- `muted` — secondary text, dividers, gutters, inactive hints.
- `accent` — prompt bar, bullets, links, headings, active markers.
- `background` — optional full-row TUI background. If missing, terva uses the terminal's existing background. Experimental: terminal background colors can vary by emulator and scrollback behavior; for the most reliable result, change your terminal background color in your terminal settings instead.
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
- `ghost` — an offered next line in the composer that the user has not
  accepted. It sits where their own text goes, so it has to read as an
  offer rather than as typing: pick a shade that recedes from `fg`, which
  means darker on a dark theme and lighter on a light one. Recede, but not
  all the way: a shade that sinks toward the terminal's own background
  reads as nothing at all, and an offer nobody notices is an offer nobody
  presses Tab for. Aim for the half of the range nearer `fg` than the
  background. Missing falls back to `muted`.
- `meter_low`, `meter_mid`, `meter_high` — the status bar's staged
  meter ramp (context window, subscription usage): the whole meter
  takes `meter_low` below 70% consumed, `meter_mid` from 70, and
  `meter_high` from 90. Stages rather than gradients on purpose — the
  hue jump at a threshold is the signal. Missing values fall back to
  `muted`/`warning`/`error`.
- `status_colors` — per-segment recoloring for the status bar, an
  object keyed by segment ID (`cwd`, `git`, `edits`, `model`,
  `persona`, `thinking`, `tokens`, `cost`, `context`, `usage`,
  `swarm`, `session`, `clock`, `tags`, `bridge`, `ext`, `replay`,
  `tasks`). Segments not named keep the muted default. Themes only
  restyle the bar — which segments render, and in what order, is
  `status_line` in `config.json`, never the theme.

```json
{
  "name": "colorful-bar",
  "colors": {
    "dark": {
      "meter_low": 44, "meter_mid": 214, "meter_high": 201,
      "status_colors": { "cwd": 81, "git": 179, "cost": 114 }
    }
  }
}
```

`background` and `user_bubble_bg` support richer terminal color forms:

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

- `spinner_frames` — list of frame strings. Single-cell glyphs keep
  status-bar alignment clean.
- `spinner_messages` — list of messages; terva picks one per turn.
  Templated — see Flavor fields.
- `spinner_interval_ms` — frame interval in milliseconds. Missing or
  invalid falls back to 80ms.

## Flavor fields

The startup headline and the spinner's working lines are **templates**, not
fixed strings: a template may embed `{verb}` and `{noun}`, each filled with an
independent random pick from the theme's word pools. A plain string with no
placeholder passes through unchanged, so fixed and generated lines can sit in
the same list. Overriding these three lists rethemes terva's whole voice
without a rebuild. Like the spinner fields, they can appear at top level, under
`colors`, or under `colors.dark` / `colors.light`.

- `greetings` — taglines for the startup headline: the text after the `i'm …`
  prefix (`i'm terva.` on the help/usage screen, `i'm <persona>.` in the
  interactive welcome banner). terva picks one at random per launch. With none
  configured it falls back to a plain line.
- `flavor_verbs` — the `{verb}` pool. Keep them imperative (`harness`, not
  `harnessing`) so they read in a greeting.
- `flavor_nouns` — the `{noun}` pool. Keep them bare so `the {noun}` stays
  grammatical.

```json
{
  "name": "nautical",
  "greetings": ["{verb} the {noun}.", "here to {verb} the {noun}."],
  "flavor_verbs": ["chart", "moor", "fathom"],
  "flavor_nouns": ["tide", "shoal", "reckoning"]
}
```

An empty or missing pool leaves its `{token}` verbatim, so define both pools if
your templates use both.

## Syntax fields

Syntax highlighting uses Chroma style entries. Values may include
attributes after the color, such as `bold`, `italic`, or `underline`.

Supported syntax override keys:

```text
keyword, keyword_constant, keyword_declaration, keyword_namespace,
keyword_reserved, keyword_type, name_builtin, name_function,
name_class, name_decorator, literal_string, literal_string_escape,
literal_number, comment, comment_preproc, operator, punctuation, text
```

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

## Theme-only extensions

An extension can exist only to ship a theme. No slash command,
subprocess, or executable is required when the extension contains a
valid theme file.

```text
$TERVA_HOME/extensions/my-theme-extension/
├── extension.json
└── theme.json
```

`extension.json`:

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

## Validate

terva theme files are plain JSON, not JSONC. Validate before installing:

```bash
python3 -m json.tool theme.json >/dev/null
```
