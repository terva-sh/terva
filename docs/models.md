# Models and providers in practice

Choosing models, fallback behavior, custom catalogs, and
per-provider notes. Login flows, endpoints, and the models.json
reference live in [providers.md](providers.md).

## Providers

terva's built-in provider catalog includes:

- **Subscription-capable**: Anthropic Claude Pro/Max (`anthropic`), OpenAI Codex / ChatGPT Plus/Pro (`openai-codex`), Kimi Code (`kimi`), GitHub Copilot (`github-copilot`).
- **Direct API providers**: Anthropic, OpenAI Chat Completions, OpenAI Responses, DeepSeek, Google Gemini, Kimi/Moonshot, Moonshot CN, Groq, Cerebras, xAI, Together AI, Hugging Face Router, OpenRouter, Mistral, Z.AI, Xiaomi/MiMo token-plan regions, MiniMax global/CN, Fireworks, Vercel AI Gateway, OpenCode/OpenCode Go.
- **Cloud/platform providers**: Amazon Bedrock, Google Vertex AI, Azure OpenAI, Cloudflare Workers AI, Cloudflare AI Gateway.
- **Local/compatible**: Ollama, plus the first-class `openai-compatible` provider for any OpenAI-compatible server (LM Studio, vLLM, llama.cpp, gateways) configured via `/login` with auto-discovery of the endpoint's models.

Use `/login` to store API keys or subscription credentials. `/model` only shows models from providers that are currently available from env vars, `auth.json`, Kimi CLI fallback, local Ollama, or a configured `openai-compatible` endpoint.

## Models

`--list-models` or the `/model` picker shows the full catalog across all built-in providers. The sources:

- **User**: your `models.json` entries — they take precedence over everything below.
- **Live**: IDs discovered from `GET /v1/models` using your stored API key (cached for 6h in `$TERVA_HOME/models-cache.json`, refreshed in the background on startup; entries loaded from that cache show `cache`).
- **Catalog**: models baked into terva, covering Claude, GPT/Codex, Gemini/Gemma, Kimi/Moonshot, DeepSeek, Groq-hosted Llama/Gemma/Compound, OpenRouter-routed models, Bedrock model ids, Vertex model ids, Azure OpenAI deployments, Copilot models, and other provider-specific catalog entries.
- **Speculative**: IDs that appear in the upstream generator but aren't live on the public API yet. They'll 404 today and start working the moment the provider ships them.

### Where context windows come from

A provider's `/v1/models` says *which* models exist but not how big their
context is — the OpenAI list schema carries only `{id, object, created,
owned_by}`, no limits. So live discovery can name a model it has no numbers
for, and a model the baked catalog has never heard of falls back to a
conservative default. That is how a million-token model can end up budgeted
as 32k, compacting a conversation that has barely started.

The catalog is therefore the source of truth for limits and prices, and it is
kept in step with [models.dev](https://models.dev) — the community catalog
several of these gateways publish from — at **build time**, so the shipped
binary needs no network to know how big a model's context is:

```bash
just models-check    # report drift; non-zero exit when the catalog is behind
just models-sync     # apply it, then review the diff
```

The sync fills gaps rather than replacing wholesale: models.dev is not a
superset of what every provider serves, and the catalog carries hand-curated
limits for some models it has never heard of. Those are kept. Where both have
a value and they disagree, the sync takes models.dev's — it tracks upstream, a
hand-typed constant does not — and the diff is there to be reviewed.

Models that *neither* source can describe are reported by name and left out of
the catalog, so they fall back to the conservative default rather than a
fabricated number. Give one a real window with a `models.json` entry (below).

The full list is long, so `--list-models=FILTER` narrows it — the flow
for finding the `--provider`/`--model` pair to pin a bot to:

```bash
terva --list-models=available        # only providers your credentials can use right now
terva --list-models=live+            # provider-reported + your own entries; no catalog noise
terva --list-models=user             # just your models.json entries
terva --list-models=available,live+  # terms AND together
```

A bare source name matches exactly (`live` deliberately folds in
`cache` — it's the same listing, one run removed); `source+` means
"that tier and above" in the order user > live/cache > catalog >
speculative; `available` keeps only providers whose credentials
resolve right now (keyless providers like ollama count). Note that
`available` is about the PROVIDER: an authenticated provider's catalog
and speculative entries stay visible, because you can pin them.

The context meter in the status line uses the model's advertised context window to show how much of it your last turn consumed.

### Model fallback (rescue)

When a turn fails because of a recoverable provider error — expired token (`401`), permission denied (`403`), rate limit (`429`), provider outage (`502`/`503`/`504`), or a transient network failure — terva opens a **rescue** picker over the chat instead of just painting a red banner.

The picker is the same vertical list / fuzzy filter UI as `/model`, but it only shows models from providers you're currently logged in to (env vars, `auth.json`, Kimi CLI fallback, ollama). The failed model is excluded. Press `↑`/`↓` to choose, `enter` to retry the **same prompt** on the new model, `esc` to dismiss.

Before the actual provider request fires, the OpenAI / Anthropic / Kimi / DeepSeek / Google / OpenAI-Codex clients also do up to two silent retries with short backoff (250ms, 750ms) on `502`/`503`/`504` and connection-reset / EOF-before-headers errors. Most edge-proxy blips disappear without you ever seeing the rescue picker.

A rescue retry always **drops launch-time `--api-key` and `--base-url`** before rebuilding the agent. Those overrides are usually the reason the rescue triggered (bad key, typo'd base URL, corporate gateway only valid for the originally-picked provider), so the retry re-resolves credentials from env vars / `auth.json` / provider defaults instead. Use `/model` if you want overrides to stick.

No configuration is required — the candidate list is built dynamically from your active credentials. Bad-request / context-length / serialization errors are NOT routed to the rescue picker, because switching models won't fix them; those still surface as a normal error.

### Custom models

Place a `models.json` in `$TERVA_HOME` (macOS: `~/Library/Application Support/terva/`, Linux: `~/.local/state/terva/`) to add models that aren't in the baked-in catalog or to override existing entries. Run `terva models init` to scaffold a starter file at that path (it refuses to overwrite an existing one unless you pass `--force`), then edit it:

```json
{
  "providers": {
    "openai": {
      "models": [
        {
          "id": "gpt-5.5",
          "name": "GPT-5.5",
          "reasoning": true,
          "contextWindow": 400000,
          "maxTokens": 128000,
          "temperature": 0.7
        }
      ]
    }
  }
}
```

Supported fields per model: `id` (required), `name`, `reasoning`, `contextWindow`, `desiredContextWindow`, `maxTokens`, `temperature`, `capabilities`, `baseUrl`, `priceInput`, `priceOutput`, `priceCacheRead`, `priceCacheWrite`. `name` is what to call the model on screen **in place of its id** — see [Renaming a model](#renaming-a-model); `contextWindow` is the model's total token budget (the hard ceiling: it drives the context gauge and clamps `maxTokens`); `desiredContextWindow` is an optional smaller *working* window that moves the auto-compaction thresholds only — compact earlier on a large-window model, e.g. to stay under a long-context pricing surcharge, without pretending the model is smaller; `maxTokens` is the cap on a single response; `temperature` (0–2) is the model's default sampling temperature, used when no `--temperature` flag is given and ignored for adaptive-thinking models (which reject sampling params); `capabilities` is an object of explicit capability assertions — `image-input`, `image-output`, `reasoning` — that override what terva would otherwise infer (`{"image-input": false}` on a vision-less local model, say). These are editable in-app from the `/model` picker with `Ctrl+E`. Several are especially worth setting for local / OpenAI-compatible models that aren't in the built-in catalog — see [Local models](#local-models-with-ollama).

Provider keys are normalized: `openai-codex` and `openai-responses` map to `openai`, `anthropic-messages` maps to `anthropic`, `moonshot`, `moonshot-ai`, and `kimi-code` map to `kimi`, and `deepseek-chat` and `deepseek-ai` map to `deepseek`. Built-in provider ids such as `groq`, `openrouter`, `github-copilot`, `amazon-bedrock`, `google-vertex`, `azure-openai-responses`, `fireworks`, `vercel-ai-gateway`, `mistral`, and `xai` can also be used directly.

User-defined models show `source: user` in `--list-models` and take precedence over both the baked-in catalog and live-discovered models. Missing or invalid files are silently ignored.

### Renaming a model

Local model ids get long — `hf.co/unsloth/Qwen3-Coder-30B-A3B-Instruct-GGUF:Q4_K_XL` is wider than most status bars. `name` gives the model a label to wear instead:

```json
{
  "providers": {
    "ollama": {
      "models": [
        { "id": "hf.co/unsloth/Qwen3-Coder-30B-A3B-Instruct-GGUF:Q4_K_XL", "name": "Qwen Coder" }
      ]
    }
  }
}
```

Set it by hand, or in-app: `/model` → `Ctrl+E` → **display name** in the terminal, or the ⚙ on a row in the web picker. Either way it lands in `models.json` and applies immediately — no restart.

The name replaces the id in the status bar, in the `/model` picker's first column, and on the web's model button. It does **not** replace it in `/status`, which shows `name (id)` — that view exists to tell you what you are actually talking to.

Renaming is presentational only. The **id remains the identity**: it is what goes to the provider, what `--model` matches, what sessions record, and what a `models.json` entry is keyed by. Both spellings search in the picker, so a renamed model is still findable by its id.

Only names *you* set displace an id. Built-in catalog models keep showing their ids in these places, because a catalog name (`Claude Sonnet 4.5 (latest)`) is longer than the id it would replace.

Names are single-line plain text: escape sequences and control characters are stripped, runs of whitespace collapse, and anything past 64 characters is trimmed — a name is rendered straight into a terminal, so an ESC in one would repaint the frame rather than just look wrong. terva warns on load when it has to adjust a name you wrote by hand.

### Kimi Code

terva has built-in Kimi support through Kimi Code, which speaks the **Anthropic Messages API** (not OpenAI chat-completions) — terva drives it with the same client it uses for Claude.

```bash
terva --provider kimi
```

By default this uses:

- model: `kimi-for-coding`
- base URL: `https://api.kimi.com/coding` (no `/v1` suffix — the Anthropic client appends `/v1/messages` itself)

Credential lookup order for Kimi:

1. `--api-key`
2. `KIMI_API_KEY`
3. `MOONSHOT_API_KEY`
4. `$TERVA_HOME/auth.json`
5. the official Kimi Code CLI token at `~/.kimi/credentials/kimi-code.json`, unless disabled by `/logout kimi`

Use `/login` for either API-key login or Kimi Code subscription login. The subscription flow uses Kimi Code's device-code OAuth flow: terva opens the verification URL, waits for browser approval, stores the token in `auth.json`, and refreshes it automatically.

Direct Moonshot API keys are a *different* provider — `moonshotai` — because the Moonshot platform endpoint is OpenAI-compatible where Kimi Code is Anthropic-shaped:

```bash
terva --provider moonshotai --model kimi-k2.6 --api-key "$MOONSHOT_API_KEY"   # base URL defaults to https://api.moonshot.ai/v1
```

You can add additional Kimi model IDs to `models.json` under the `kimi` provider, and Moonshot ones under `moonshotai`.

### DeepSeek

terva has built-in DeepSeek support through DeepSeek's OpenAI-compatible chat API.

```bash
terva --provider deepseek
```

By default this uses:

- model: `deepseek-v4-pro`
- base URL: `https://api.deepseek.com/v1`

Catalog ships with `deepseek-v4-pro` (reasoning) and `deepseek-v4-flash`. These are exactly the IDs returned by `GET https://api.deepseek.com/models` today. You can add additional model IDs to `models.json` under the `deepseek` provider.

Credential lookup order for DeepSeek:

1. `--api-key`
2. `DEEPSEEK_API_KEY`
3. `$TERVA_HOME/auth.json`

Use `/login` and pick **api key** to paste a DeepSeek key. terva probes `/v1/models` once and stores the key under `deepseek` in `auth.json`.

> **Auth model: API key only.** DeepSeek does not offer a subscription OAuth flow. The `/login subscription` step lists only Anthropic, OpenAI Codex, Kimi, and GitHub Copilot; DeepSeek shows up only under `/login → api key`.

> **Text only at the wire level.** DeepSeek's chat-completions endpoint currently rejects the multimodal content schema (`unknown variant image_url, expected text`), so the V4 catalog rows are tagged `image-input: false`. The drop is **capability-keyed, not provider-keyed**: for any model without the image-input capability — a DeepSeek V4 row, a vision-less local GGUF, a `{"image-input": false}` entry in your `models.json` — terva silently drops `ImageBlock` parts from outgoing user/tool messages and keeps only the text. Switching back to a vision-capable model (Claude, GPT-5, Gemini) re-sends the image normally because the session file still stores it.

For a custom-compatible endpoint (mirror, gateway, self-host):

```bash
terva --provider deepseek --base-url https://my-deepseek-mirror.example.com/v1 --api-key "$DEEPSEEK_API_KEY"
```

### Google Gemini

terva has built-in Google Gemini support through the [AI Studio Generative Language API](https://aistudio.google.com/).

```bash
terva --provider google
```

By default this uses:

- model: `gemini-2.5-pro`
- base URL: `https://generativelanguage.googleapis.com`

The catalog spans the 3.x line (`gemini-3-pro-preview`, `gemini-3.1-pro-preview`, `gemini-3.5-flash`, the Flash-Lite previews), the rolling `gemini-flash-latest` / `gemini-flash-lite-latest` aliases, the 2.x line (`gemini-2.5-pro`, `gemini-2.5-flash`, `gemini-2.5-flash-lite`, `gemini-2.0-flash`, `gemini-2.0-flash-lite`), and the open Gemma models. It moves with every Google release — run `terva --list-models` for the current set. Live discovery against `/v1beta/models` adds anything else your key can see.

Credential lookup order for Google:

1. `--api-key`
2. `GEMINI_API_KEY`
3. `GOOGLE_API_KEY`
4. `$TERVA_HOME/auth.json`

Use `/login` and pick **api key** to paste an AI Studio key. terva probes `/v1beta/models` once and stores the key under `google` in `auth.json`.

> **Auth model: API key only.** Google does not issue OAuth tokens for consumer Gemini Advanced / Google One AI Premium subscriptions, so there is no "log in with your Google subscription" flow. Programmatic access requires either an AI Studio API key (this provider) or a Vertex AI / GCP service-account credential (not yet wired up in terva). Google is therefore absent from the `/login subscription` list entirely; pick it under `/login → api key`.

> **Free-tier rate limits.** AI Studio's free tier has tight per-minute and per-day caps that vary by model: `gemini-2.5-pro` is the strictest (a few requests per minute, ~50 per day), Flash and Flash-Lite are far more generous. If a Pro turn 429s with `"You exceeded your current quota"` while Flash on the same key still works, you've hit the Pro free-tier RPD. Either switch to Flash for agent loops, or [enable billing](https://aistudio.google.com/app/apikey) on your AI Studio project to flip the same key from free to pay-as-you-go pricing (`$1.25/M` input, `$10/M` output for Pro).

Reasoning levels (`--reasoning off|minimum|low|medium|high|maximum|max`, also configurable in `/settings` as **thinking level**) map differently per generation. Budget-based providers use roughly 1k/2k/8k/16k/32k thinking tokens for minimum/low/medium/high/maximum, with provider/model caps applied (Gemini 2.5 Pro caps at 32k; Flash at 24k). Gemini 3.x uses the `thinkingLevel` enum (`MINIMAL`/`LOW`/`MEDIUM`/`HIGH`), with Gemini-3-Pro pinned to `LOW` minimum and `HIGH` for any "medium" or higher request. Effort-based OpenAI-compatible chat providers map minimum to `low`, low/medium directly, and high/maximum to `high`; the Codex/Responses backend maps maximum to `xhigh` where supported. `max` is a seventh tier *above* `maximum`, opt-in on purpose: it is sent natively only where the model has an effort above `xhigh` (GPT-5.6, adaptive-thinking Claude) and is clamped back to the `maximum` effort everywhere else. `off` sends no reasoning config. 2.0-family Gemini models have no thinking config at all.

You can add additional Gemini model IDs to `models.json` under the `google` provider.

#### Persisting reasoning summaries

**Default: off.** Set it in `/settings` as **Record thinking** (the same row appears in the web settings pane), or set `"reasoning_summary"` in `config.json` to `"auto"`, `"concise"`, or `"detailed"`. It asks the model for a human-readable summary of its reasoning and keeps it in the session record alongside the opaque reasoning payload. Changing it applies live to every open session and becomes the default for new ones. This exists for reviewing unattended runs: without it a transcript shows *what* an agent did with no trace of *why*, and intent has to be reconstructed from the shape of the tool calls. Any unrecognized value is treated as off.

Only the `openai-codex` path implements this today; other providers ignore the setting. Anthropic thinking blocks are still dropped before they become content, deliberately, and are not covered by it. Note also that OpenAI-compatible chat providers that emit `reasoning_content` (DeepSeek, Kimi, and similar) already persist readable reasoning unconditionally — this setting is not what governs them.

Three things worth knowing before enabling it:

- **It is written to disk, and it can quote.** A summary of a turn spent reading mail can quote that mail. Tool results in the same session file already carry comparable content, so this is not a new class of exposure, but it does make the file more quotable — account for it before widening who reads transcripts.
- **The cost is on the input side, not generation.** The summary rides inside the reasoning tokens you already pay for, and a turn where the model does no reasoning emits no summary at all. What accrues is context: summaries persist into the transcript, so they are re-sent with it. terva deliberately does *not* replay the summary back to the provider as part of the reasoning item — the encrypted payload is what the model consumes — which keeps the per-turn replay cost unchanged.
- **It is a readable record, not a visible one.** Summaries land in the session JSONL for after-the-fact review; the TUI and web UI do not display them. The one place a summary surfaces is an ACP client repainting a resumed session, and only for a turn that produced no visible text of its own — otherwise the turn repaints as the model's actual answer, unchanged.

### Local models with ollama

terva works with [ollama](https://ollama.com) out of the box. Ollama serves an OpenAI-compatible API locally, so any model you have pulled works with terva.

Quick start:

```bash
ollama pull qwen3.5:4b
terva --provider ollama --model qwen3.5:4b
```

That's it. No API key needed for local models. terva defaults to `http://localhost:11434`.

For a remote ollama instance or one behind auth:

```bash
terva --provider ollama --model llama3 --base-url https://my-server.com/v1 --api-key my-token
```

You can also add models to your `models.json` so you don't need flags every time:

```json
{
  "providers": {
    "ollama": {
      "models": [
        {
          "id": "qwen3.5:4b",
          "name": "Qwen 3.5 4B",
          "contextWindow": 32768,
          "maxTokens": 8192
        }
      ]
    }
  }
}
```

The `ollama` provider uses the OpenAI chat completions protocol internally, so it also works with any OpenAI-compatible server (vLLM, LM Studio, LocalAI, etc.).

### Editing a model's settings

Per-model overrides — **context window**, **desired context window**, **max
tokens**, **temperature**, **base URL** — live in `$TERVA_HOME/models.json` and are
editable from either frontend:

- **TUI**: open `/model`, highlight a model, and edit it.
- **Web**: open the model picker and press the ⚙ on the model's row.

Both write the same `models.json` and both are driven by one descriptor in the
daemon, so neither can drift onto a different idea of what a setting means.

An **empty box means inherit** — whatever terva knows about that model — so the
default is shown as placeholder text rather than filled in. Clearing a box removes
that override; **Reset to defaults** removes the model's entry outright.

`desiredContextWindow` is the one whose name does not explain it: it is not the
model's ceiling but the *working* window that drives auto-condensing, so you can
keep a large-window model and still condense earlier.

Changing a setting takes effect immediately; a session sitting on that model is
re-resolved, which is what lets a changed base URL actually reach the new machine.

### OpenAI-compatible endpoints (LM Studio, vLLM, llama.cpp, gateways)

For local servers that aren't Ollama — or any hosted OpenAI-compatible gateway — use the first-class `openai-compatible` provider. Unlike `ollama` it has no fixed base URL: you configure the endpoint through `/login`.

Run `/login`, pick **OpenAI Compatible (local/custom)**, and enter the base URL, a default model id, and (optionally) a default context window. The API key is optional — most local servers ignore it. terva then lists `GET {base-url}/models` on every launch and adds every model the server serves to the `/model` picker, so you don't pre-register them.

**Name it to keep several.** A named endpoint (the optional first field at `/login`, in the terminal or the web panel's Providers pane) is registered as **its own provider** under that name, with its own `/v1/models` discovery — so a small local model for titles and a big one on another box coexist in `/model`. Named endpoints live in `config.json` under `endpoints`; their keys, if any, live in `auth.json` under the same name, and a named endpoint needs no default model id.

**Leave the name empty and it is the shared slot.** `/login` then stores a single `openai-compatible` endpoint, so logging in to a second one without a name overwrites the first.

**Several backends at once — named endpoints.** Define them in `config.json` under `endpoints`; each becomes its **own provider** (the key is the provider id) with its own `/v1/models` discovery and its own row in the `/model` picker — so models from different machines don't pile into one `openai-compatible` list:

```json
{
  "endpoints": {
    "box-a": { "baseUrl": "http://box-a:8000/v1", "contextWindow": 32768 },
    "gw":    { "baseUrl": "https://gw.internal/v1", "apiKeyEnv": "GW_KEY" }
  }
}
```

The key is **optional** (most local servers need none). It is **never** stored in `config.json`: set `apiKeyEnv` to read it from the environment, or store it in `auth.json` under the endpoint name. Endpoints are a user-config concept — a project's `.terva/config.json` can't define one (it could otherwise point the agent at an arbitrary server). Adding or editing an endpoint re-runs discovery on the next launch. (The older `models.json` per-model `baseUrl` override still works for pinning an individual model to a one-off URL.)

**Migrating from the `models.json` `baseUrl` pattern.** Named endpoints and the per-model `baseUrl` override coexist — nothing breaks if you keep both, so there's no forced cutover. The difference is that a named endpoint **discovers** its models (`/v1/models`), where `models.json` only shows the entries you hand-list. The lift-and-trim path:

1. `terva models endpoints` scans your `models.json` for distinct `openai-compatible` base URLs and prints a ready-to-paste `endpoints` block (one named endpoint per URL, naming each from its host), plus the `models.json` entries that become redundant once discovery covers them. It changes nothing.
2. `terva models endpoints --apply` writes those endpoints into `config.json` for you (additive — it never clobbers an endpoint you already defined, and never touches `models.json`).
3. Relaunch so each endpoint discovers its models, then **trim** `models.json` by hand: delete the entries now covered by discovery, but keep any you rely on for exact `contextWindow` / `maxTokens` / capability overrides (discovery can't infer those). Anything you keep still wins over the catalog and the endpoint's `/v1/models`.

If you set a key via `apiKeyEnv` for a migrated endpoint, point it at the variable holding that endpoint's token — `models.json` had no key field, so a previously keyless local server needs nothing.

CLI equivalent:

```bash
terva --provider openai-compatible \
  --model qwen2.5-coder \
  --base-url http://localhost:1234/v1
```

**Context size and max response tokens.** The standard `/v1/models` response only lists model ids, so terva can't always know a model's limits. It reads non-standard hints when the server provides them (vLLM's `max_model_len`, some gateways' `context_length`), and otherwise uses the default context window you set at login. For exact per-model values — including `maxTokens`, which the login form does not capture — pin the model in `models.json`:

```json
{
  "providers": {
    "openai-compatible": {
      "models": [
        {
          "id": "qwen2.5-coder-32b",
          "name": "Qwen2.5 Coder 32B (local)",
          "contextWindow": 131072,
          "maxTokens": 8192,
          "baseUrl": "http://localhost:1234/v1"
        }
      ]
    }
  }
}
```

`contextWindow` is the total token budget (drives the context gauge and auto-compaction); `maxTokens` is the cap on a single response (`max_tokens`). `models.json` values win over both the catalog and whatever `/v1/models` reports, so they're the fix when a server under-reports its limits. A `"capabilities"` map tags what the model can do — most usefully `{"image-input": false}` for a local model without vision, so terva drops image attachments with a note instead of letting the server reject every turn (see [providers.md](providers.md#capability-tags)). See [providers.md](providers.md#openai-compatible-endpoints-local-and-custom-servers) for the full reference.

## Swarm sub-agent tiers (weak / medium / strong)

When auto-swarm is on, the agent can pick a model *strength* for each background sub-agent it spawns via a `tier` of `weak`, `medium`, or `strong` — so routine sub-tasks run on a cheap model and only the hard ones use a strong one. A tier always resolves **for the host's own provider** (a sub-agent stays on the provider you're using) and is **capped at the host model's tier**: a weak host can't spawn a strong child. Tiers are a per-provider concept — each provider maps `weak`/`medium`/`strong` to its own models.

terva ships a built-in mapping for **Anthropic only** (`weak`→haiku, `medium`→sonnet, `strong`→opus — these family names are unambiguous, so they survive version bumps). **Every other provider — including gateways like opencode-go, OpenRouter, and LiteLLM — has no built-in mapping**, so `tier` is ignored there and sub-agents fall back to the full host model (which is why a `tier: weak` spawn on opencode-go still costs host-model price). To get cheap tiers on those providers, configure them yourself.

**See what resolves today, and what to set:**

```bash
terva models tiers          # per logged-in provider: the weak/medium/strong model each resolves to
terva models tiers --all    # include providers you're not logged into
```

Providers with no mapping are flagged, with a ready-to-paste config block and candidate model ids from that provider's catalog.

**Configure** per provider in `$TERVA_HOME/config.json` under `swarm_tiers` (user config only — a project's `.terva/config.json` cannot redirect sub-agent model selection):

```json
{
  "swarm_tiers": {
    "opencode-go": {
      "weak":   "minimax-m3",
      "medium": "glm-5.2",
      "strong": "kimi-k2.7-code"
    }
  }
}
```

The ids must be real models in that provider's catalog (`terva models tiers` flags ones that aren't). Your entries **override** the built-in guesses; a **partial** map is fine — any tier you leave out falls back to the built-in guess for that provider, then to the host model. The host-cap rule still applies: when terva can identify the host model's own tier (by your configured ids or the built-in families), it never resolves a *stronger* tier than the host.
