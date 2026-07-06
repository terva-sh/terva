# Image generation

terva can generate images with the built-in **`generate_image`** tool, backed by
a registry of image services you configure. It is **opt-in** and **off by
default** — with no `image` config block the tool does not exist.

This complements image *input* (vision): terva already reads image files and
renders images in the web client and TUI. `generate_image` is the other
direction — the model producing an image.

## Quick start

The lowest-friction setup is the OpenAI Images API. With an `OPENAI_API_KEY` in
your environment, enable image generation and terva uses it automatically:

```json
// $TERVA_HOME/config.json
{ "image": { "enabled": true } }
```

Now ask the agent to make an image. `generate_image` returns it inline (rendered
in the web client / TUI), and if you give it a path it also saves the file:

> "Generate a 1024×1024 placeholder hero image of a mountain lake and save it to
> `assets/hero.png`."

The tool spends money on hosted backends and calls an external service, so it is
**approval-gated** (it prompts before running) and never appears in plan mode.

## Configuring backends

For anything beyond the opportunistic OpenAI default, declare backends
explicitly. Each backend has a `protocol` (which adapter to use), an endpoint,
and defaults:

```json
{
  "image": {
    "backend": "openai",
    "backends": {
      "openai": {
        "protocol": "openai-images",
        "base_url": "https://api.openai.com/v1",
        "api_key": "sk-…",
        "model": "gpt-image-1",
        "size": "1024x1024"
      },
      "local": {
        "protocol": "openai-images",
        "base_url": "http://localhost:8080/v1",
        "model": "flux.1-dev",
        "negative_pipe": true
      }
    }
  }
}
```

- `backend` names the default the tool uses when a call doesn't specify one. With
  a single backend it is optional; with several you should set it (or the model
  must pass `backend`).
- `enabled: false` turns the whole feature off even with backends configured.

### Self-hosting

There is no single self-host standard for image generation (unlike
OpenAI-compatible chat), so backends are chosen by **protocol**:

| protocol | covers | status |
|---|---|---|
| `openai-images` | hosted OpenAI / Azure, **LocalAI** (fronts Stable Diffusion / SDXL / Flux on `/v1/images/generations`), and any OpenAI-compatible endpoint | **shipping** |
| `a1111` | AUTOMATIC1111 / Forge and forks (`/sdapi/v1/txt2img`) | **shipping** |
| `comfyui` | ComfyUI / SwarmUI (workflow graph) | **shipping** |

The `openai-images` adapter covers the easiest self-host path:
[LocalAI](https://localai.io/features/image-generation/) is a drop-in that serves
SD/SDXL/Flux on the same API — point a backend's `base_url` at it. Its
negative-prompt convention (`prompt|negative`) is enabled with
`"negative_pipe": true`.

The **`a1111`** protocol talks to a Stable Diffusion WebUI
(AUTOMATIC1111/Forge) directly — the huge existing self-host install base:

```json
{
  "image": {
    "backend": "sd",
    "backends": {
      "sd": {
        "protocol": "a1111",
        "base_url": "http://localhost:7860",
        "model": "sdxl.safetensors",
        "size": "1024x1024",
        "steps": 30,
        "sampler": "DPM++ 2M",
        "cfg_scale": 7.0
      }
    }
  }
}
```

`negative_prompt` is native (no pipe). `model` overrides the checkpoint,
`steps`/`sampler`/`cfg_scale` set sampling defaults (omit to let the server
choose), and `api_key` is sent as HTTP Basic auth for a WebUI started with
`--api-auth user:pass`.

The **`comfyui`** protocol runs a ComfyUI node graph. Because ComfyUI has no
fixed txt2img call, you supply a **workflow template** — export your graph with
ComfyUI's *Save (API Format)* button and put `{{prompt}}` (and optionally
`{{negative}}`) in the text node(s); everything else (checkpoint, size, steps,
seed, sampler) lives in the workflow you authored:

```json
{
  "image": {
    "backend": "comfy",
    "backends": {
      "comfy": {
        "protocol": "comfyui",
        "base_url": "http://localhost:8188",
        "workflow_file": "~/.config/terva/workflows/flux.json"
      }
    }
  }
}
```

Give either `workflow` (inline JSON) or `workflow_file` (a path). terva
substitutes the placeholders (escaping the prompt so quotes/newlines can't break
the JSON), queues the workflow (`/prompt`), polls `/history` for completion, and
fetches the outputs (`/view`). Size/steps/etc. from the tool call are ignored for
comfyui — they belong in the workflow.

For backends terva doesn't speak natively, you can also add an image-generation
MCP server.

## The `generate_image` tool

| argument | meaning |
|---|---|
| `prompt` | what to generate (required) |
| `path` | optional workspace-relative path to save to (e.g. `assets/hero.png`); omit to only display. With `n>1` an index is inserted before the extension (`hero-1.png`, `hero-2.png`) |
| `size` | e.g. `1024x1024`; defaults to the backend's configured size |
| `n` | number of images (default 1) |
| `backend` | override the default backend for this call |
| `negative_prompt` | what to avoid (only backends that support it) |

Files are written through the workspace sandbox, so a jailed session can only
write inside its working directory — the same rule as `write`.

## Notes

- **Cost & privacy.** Hosted image generation costs money per image, and your
  prompt (and any reference image) is sent to the configured service. Self-hosted
  backends keep it local.
## Planned

These are future niceties, not shipping yet:

- **Native model output** — a chat model emitting images itself (e.g. Gemini's
  image models, OpenAI's Responses image tool) as another backend behind the
  same tool, gated on the model's `image-output` capability. Today generation
  always goes through a backend endpoint.
- **Variations carousel** — generate several, view them, pick one to keep and
  regenerate the rest, in the web client. For now use `n` and ask the agent to
  regenerate.
- **Edits / img2img** — transform an existing image (OpenAI edits, a1111
  img2img, ComfyUI image input) via an optional input-image argument.
