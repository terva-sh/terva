# Native image output

Let the model **draw images inline in its own replies** — no separate image
endpoint, API key, or service. When it's on and you ask for a picture, the model
produces the image as part of its turn and it appears right in the conversation,
in both the TUI and the web client.

This uses the OpenAI **Responses** built-in `image_generation` tool over the
**Codex (ChatGPT) subscription** you already log into — so it reuses that
credential and spends against that subscription's usage, with nothing else to
wire up.

> **Not the same as `generate_image`.** The [`generate_image`
> tool](image-generation.md) calls a *separate* image endpoint (OpenAI Images, a
> self-hosted Stable Diffusion server, …) that you configure under `image`.
> Native output is the *model itself* drawing inline, configured under
> `native_output`. They're independent — enable either, both, or neither.

## Quick start

Native output is **off by default** (it spends money without a per-image
approval prompt, so it's opt-in). Turn it on in your config and use a Codex
model that supports it:

```jsonc
{
  "native_output": { "enabled": true }
}
```

Then log into the Codex subscription (`terva auth login openai`, choosing the
ChatGPT/Codex flow) and select a supported model (see below). Ask for an image —
"draw a red circle on a white background" — and it appears in the reply.

## Configuration

All fields live under the top-level `native_output` block:

| Field | Default | Meaning |
|---|---|---|
| `enabled` | `false` | Turn native image output on. |
| `size` | provider default | `1024x1024`, `1024x1536`, `1536x1024`, or `auto`. |
| `quality` | provider default | `low`, `medium`, `high`, or `auto`. Higher costs more. |
| `edit_history` | `1` | How many recent images stay **editable** — see below. |

```jsonc
{
  "native_output": {
    "enabled": true,
    "size": "1024x1024",
    "quality": "medium",
    "edit_history": 1
  }
}
```

### Editing images — and what `edit_history` costs

The model can **edit an image it already drew** ("now make the circle blue",
"add a moon") rather than starting over. For that to work, terva re-sends the
earlier image — *including its pixels* — back to the model on the next turn.

`edit_history` is how many of your **most recent** generated images stay
editable this way:

- **`1` (default)** — only the last image you drew can be edited. This is the
  common case ("edit the thing on screen") and the cheapest.
- **`0`** — generation only; images are never re-sent, so nothing is editable.
  Cheapest of all.
- **`N` (higher)** — the last N images stay editable. **This is not free:** each
  retained image is re-uploaded to the model *on every following turn* for as
  long as it's in the window, and those pixels are billed as image *input*
  tokens. Raising `edit_history` increases per-turn cost, latency, and context
  use by roughly one image apiece. Raise it only if you genuinely need to go
  back and edit older images; most people should leave it at `1`.

Older images (outside the window) still show in the transcript and on screen —
they just can't be handed back to the model as edit targets.

## Supported models

Native output only works on models that advertise the `image-output`
capability. Today that's the OpenAI **Codex subscription** models — every
mainline one was verified end-to-end:

- `gpt-5.6-sol`, `gpt-5.6-terra`, `gpt-5.6-luna`
- `gpt-5.5`, `gpt-5.4`, `gpt-5.4-mini`

(`gpt-5.3-codex-spark` is text-only and does not support it.) On any model
without the capability the setting simply does nothing — switching to a
supported model turns it on with no restart.

## Notes

- **Opt-in and cost.** Native images spend money and, unlike `generate_image`,
  are **not** individually approval-prompted (a drawn image isn't a tool call
  terva gates). The `enabled` toggle is the control. Usage shows up in the
  normal Codex usage windows (`/usage`).
- **Plan mode.** Native output is currently *not* suppressed in plan mode — if
  you enable it and work in plan mode, the model can still draw. Keep it off if
  that matters to you.
- Design record: `docs/proposals/native-image-output.md` (internal — not part of
  the published docs).
