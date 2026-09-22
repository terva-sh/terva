# Models and providers in practice

Choosing models, fallback behavior, custom catalogs, and
per-provider notes. Login flows, endpoints, and the models.json
reference live in [providers.md](providers.md).

## Providers

terva's built-in provider catalog includes:

- **Subscription-capable**: Anthropic Claude Pro/Max (`anthropic`), OpenAI Codex / ChatGPT Plus/Pro (`openai-codex`), Kimi Code (`kimi`), GitHub Copilot (`github-copilot`).
- **Direct API providers**: Anthropic, OpenAI Chat Completions, OpenAI Responses, DeepSeek, Google Gemini, Kimi/Moonshot, Moonshot CN, Groq, Cerebras, xAI, Together AI, Hugging Face Router, OpenRouter, Mistral, Z.AI, Xiaomi/MiMo token-plan regions, MiniMax global/CN, Fireworks, Vercel AI Gateway, OpenCode/OpenCode Go.
- **Cloud/platform providers**: Amazon Bedrock, Google Vertex AI, Azure OpenAI, Cloudflare Workers AI, Cloudflare AI Gateway.
- **Local/compatible**: Ollama, plus the first-class `openai-compatible` and `anthropic-compatible` providers for any server speaking OpenAI Chat Completions (LM Studio, vLLM, llama.cpp, gateways) or the Anthropic Messages API (LiteLLM in anthropic mode, a gateway in front of Claude, a local router), configured via `/login` with auto-discovery of the endpoint's models.

`/model` only shows models from providers that are currently available from env vars, `auth.json`, Kimi CLI fallback, local Ollama, or a configured `openai-compatible` / `anthropic-compatible` endpoint. Getting a credential in place for any of them is [providers.md](providers.md); this page assumes you already can reach the provider and is about which model you run on it.

## Models

`--list-models` or the `/model` picker shows the full catalog across all built-in providers. The sources:

- **User**: your `models.json` entries, which take precedence over everything below.
- **Live**: IDs discovered from `GET /v1/models` using your stored API key (cached for 6h in `$TERVA_HOME/models-cache.json`, refreshed in the background on startup; entries loaded from that cache show `cache`).
- **Catalog**: models baked into terva, covering Claude, GPT/Codex, Gemini/Gemma, Kimi/Moonshot, DeepSeek, Groq-hosted Llama/Gemma/Compound, OpenRouter-routed models, Bedrock model ids, Vertex model ids, Azure OpenAI deployments, Copilot models, and other provider-specific catalog entries.
- **Speculative**: IDs that appear in the upstream generator but aren't live on the public API yet. They'll 404 today and start working the moment the provider ships them.

## Choosing and sizing a model

Everything here is cross-provider: it applies to whatever model you picked.

### Where context windows come from

A provider's `/v1/models` says *which* models exist but not how big their
context is. The OpenAI list schema carries only `{id, object, created,
owned_by}` and no limits. So live discovery can name a model it has no numbers
for, and a model the baked catalog has never heard of falls back to a
conservative default. That is how a million-token model can end up budgeted
as 32k, compacting a conversation that has barely started.

The catalog is therefore the source of truth for limits and prices, and it is
kept in step with [models.dev](https://models.dev), the community catalog
several of these gateways publish from, at **build time**, so the shipped
binary needs no network to know how big a model's context is:

```bash
just models-check    # report drift; non-zero exit when the catalog is behind
just models-sync     # apply it, then review the diff
```

The sync fills gaps rather than replacing wholesale: models.dev is not a
superset of what every provider serves, and the catalog carries hand-curated
limits for some models it has never heard of. Those are kept. Where both have
a value and they disagree, the sync takes models.dev's, because it tracks
upstream and a hand-typed constant does not. The diff is there to be reviewed.

Models that *neither* source can describe are reported by name and left out of
the catalog, so they fall back to the conservative default rather than a
fabricated number. Give one a real window with a `models.json` entry (below).

The full list is long, so `--list-models=FILTER` narrows it. This is the flow
for finding the `--provider`/`--model` pair to pin a bot to:

```bash
terva --list-models=available        # only providers your credentials can use right now
terva --list-models=live+            # provider-reported + your own entries; no catalog noise
terva --list-models=user             # just your models.json entries
terva --list-models=available,live+  # terms AND together
```

A bare source name matches exactly (`live` deliberately folds in
`cache`, the same listing one run removed); `source+` means
"that tier and above" in the order user > live/cache > catalog >
speculative; `available` keeps only providers whose credentials
resolve right now (keyless providers like ollama count). Note that
`available` is about the PROVIDER: an authenticated provider's catalog
and speculative entries stay visible, because you can pin them.

The context meter in the status line uses the model's advertised context window to show how much of it your last turn consumed.

Two fields carry the sizing, and both matter most for a local or custom model
terva has no catalog entry for:

- **`contextWindow`** is the model's total token budget. It drives the
  status-bar context gauge and the automatic-compaction trigger. At `0` or
  unknown, auto-compaction is disabled and the gauge drops the percentage,
  falling back to a plain token count. That is safe, but you lose the
  usage-versus-limit readout.
- **`maxTokens`** is the most terva will request for a single response, sent as
  `max_tokens` or `max_completion_tokens`. Leave it out to let the server use
  its own default.

Set them per model in `models.json` (below). Entries there beat both the baked-in
catalog and whatever `/v1/models` reported, so they are the fix when a server
under-reports or misreports its limits.

### Model fallback (rescue)

When a turn fails because of a recoverable provider error (an expired token `401`, permission denied `403`, rate limit `429`, provider outage `502`/`503`/`504`, or a transient network failure), terva opens a **rescue** picker over the chat instead of just painting a red banner.

The picker is the same vertical list / fuzzy filter UI as `/model`, but it only shows models from providers you're currently logged in to (env vars, `auth.json`, Kimi CLI fallback, ollama). The failed model is excluded. Press `↑`/`↓` to choose, `enter` to retry the **same prompt** on the new model, `esc` to dismiss.

Before the actual provider request fires, the OpenAI / Anthropic / Kimi / DeepSeek / Google / OpenAI-Codex clients also do up to two silent retries with short backoff (250ms, 750ms) on `502`/`503`/`504` and connection-reset / EOF-before-headers errors. Most edge-proxy blips disappear without you ever seeing the rescue picker.

A rescue retry always **drops launch-time `--api-key` and `--base-url`** before rebuilding the agent. Those overrides are usually the reason the rescue triggered (bad key, typo'd base URL, corporate gateway only valid for the originally-picked provider), so the retry re-resolves credentials from env vars / `auth.json` / provider defaults instead. Use `/model` if you want overrides to stick.

No configuration is required: the candidate list is built dynamically from your active credentials. Bad-request / context-length / serialization errors are NOT routed to the rescue picker, because switching models won't fix them; those still surface as a normal error.

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

Supported fields per model: `id` (required), `name`, `reasoning`, `contextWindow`, `desiredContextWindow`, `maxTokens`, `temperature`, `defaultReasoning`, `reasoningEfforts`, `capabilities`, `baseUrl`, `priceInput`, `priceOutput`, `priceCacheRead`, `priceCacheWrite`, `priceOutputImage`. `name` is what to call the model on screen **in place of its id** (see [Renaming a model](#renaming-a-model)); `contextWindow` is the model's total token budget (the hard ceiling: it drives the context gauge and clamps `maxTokens`); `desiredContextWindow` is an optional smaller *working* window that moves the auto-compaction thresholds only, so you can compact earlier on a large-window model, say to stay under a long-context pricing surcharge, without pretending the model is smaller; `maxTokens` is the cap on a single response; `temperature` (0–2) is the model's default sampling temperature, used when no `--temperature` flag is given and ignored for adaptive-thinking models (which reject sampling params); `defaultReasoning` is the thinking level for this model, and a value you set here outranks the global **Thinking** setting, unlike the one terva ships in its own catalog, which yields to it (full order in [CLI](cli.md#per-session-thinking)); `reasoningEfforts` is the list of `reasoning_effort` values this model actually accepts (`["none", "low", "medium", "xhigh"]`, say; see [Declaring which efforts a model accepts](#declaring-which-efforts-a-model-accepts)); `capabilities` is an object of explicit capability assertions (`image-input`, `image-output`, `reasoning`) that override what terva would otherwise infer (`{"image-input": false}` on a vision-less local model, say). These are editable in-app from the `/model` picker with `Ctrl+E`; `defaultReasoning` is a picker there rather than a text box, offering the levels **this** model can actually tell apart (several rungs reach some models as one wire value, and it does not offer both), and the row is absent for a model that takes no thinking setting at all. Several are especially worth setting for local / OpenAI-compatible models that aren't in the built-in catalog; see [Local models](#local-models-with-ollama).

Prices are USD per 1M tokens. `priceOutputImage` is the separate output rate for **image** tokens, for models that bill a generated picture differently from the text beside it. Gemini's nano-banana family charges $3/1M for text but $60/1M for images on the same model, in the same response. Leave it unset (0) for every ordinary model: output is then billed at the single `priceOutput` rate exactly as before. When it is set, terva splits the response's output tokens by modality and bills each part at its own rate.

A few retired provider keys are still accepted and rewritten to their current names: `anthropic-messages` maps to `anthropic`, `moonshot-ai` and `kimi-code` map to `kimi`, and `deepseek-chat` and `deepseek-ai` map to `deepseek`. Everything else is used exactly as written: `openai-responses`, `openai-codex`, `moonshot`, `groq`, `openrouter`, `github-copilot`, `amazon-bedrock`, `google-vertex`, `azure-openai-responses`, `fireworks`, `vercel-ai-gateway`, `mistral`, `xai` and the rest are live provider ids or aliases, so a block keyed with one applies to that provider and no other.

User-defined models show `source: user` in `--list-models` and take precedence over both the baked-in catalog and live-discovered models. Missing or invalid files are silently ignored.

### Renaming a model

Local model ids get long: `hf.co/unsloth/Qwen3-Coder-30B-A3B-Instruct-GGUF:Q4_K_XL` is wider than most status bars. `name` gives the model a label to wear instead:

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

Set it by hand, or in-app: `/model` → `Ctrl+E` → **display name** in the terminal, or the ⚙ on a row in the web picker. Either way it lands in `models.json` and applies immediately, with no restart.

The name replaces the id in the status bar, in the `/model` picker's first column, and on the web's model button. It does **not** replace it in `/status`, which shows `name (id)`: that view exists to tell you what you are actually talking to.

Renaming is presentational only. The **id remains the identity**: it is what goes to the provider, what `--model` matches, what sessions record, and what a `models.json` entry is keyed by. Both spellings search in the picker, so a renamed model is still findable by its id.

Only names *you* set displace an id. Built-in catalog models keep showing their ids in these places, because a catalog name (`Claude Sonnet 4.5 (latest)`) is longer than the id it would replace.

Names are single-line plain text: escape sequences and control characters are stripped, runs of whitespace collapse, and anything past 64 characters is trimmed. A name is rendered straight into a terminal, so an ESC in one would repaint the frame rather than just look wrong. terva warns on load when it has to adjust a name you wrote by hand.

### Editing a model's settings

Per-model overrides (**context window**, **desired context window**, **max
tokens**, **temperature**, **base URL**) live in `$TERVA_HOME/models.json` and are
editable from either frontend:

- **TUI**: open `/model`, highlight a model, and edit it.
- **Web**: open the model picker and press the ⚙ on the model's row.

Both write the same `models.json` and both are driven by one descriptor in the
daemon, so neither can drift onto a different idea of what a setting means.

An **empty box means inherit**, taking whatever terva knows about that model, so the
default is shown as placeholder text rather than filled in. Clearing a box removes
that override; **Reset to defaults** removes the model's entry outright.

`desiredContextWindow` is the one whose name does not explain it: it is not the
model's ceiling but the *working* window that drives auto-condensing, so you can
keep a large-window model and still condense earlier.

Changing a setting takes effect immediately; a session sitting on that model is
re-resolved, which is what lets a changed base URL actually reach the new machine.

### Hiding models you never use

A gateway like OpenRouter serves several hundred models. Favourites fix half the
problem, pinning the handful you love to the top, but you still scroll past
the other 290 to reach anything unpinned. Hiding is the other half: favourites
raise a few models, hiding lowers the many.

- **TUI**: open `/model`, highlight a model, press **ctrl+k**. Type **`:hidden`**
  to list what you have hidden, and ctrl+k there puts one back.
- **Web**: open the model picker and press the **◌** on the model's row. A
  **Show hidden (N)** switch at the foot of the picker reveals them, dimmed, with
  **⊘** to restore one.

(It is ctrl+k rather than the more obvious ctrl+h because a terminal sends ctrl+h
as the same byte as Backspace, so that binding would fire while you typed in the
filter box.)

**Hiding is about choosing, not about capability.** The catalogue is untouched:
a hidden model keeps its context window and cost data, gauges and budgets still
work, and a session already running on one carries on to the end. What changes is
what the pickers offer you next.

One case is deliberately an error rather than a guess. If you hide the model that
is also your **configured default**, terva refuses to start and says so, because
only you can decide which half you meant. An explicit `--model` still works, and
so does resuming a session that is already on it. Refusing to reopen a
conversation over a picker preference would be destructive out of all proportion.

#### The rules behind it

Hiding writes to `hidden_models` in `$TERVA_HOME/config.json`. Unlike
`favorite_models`, **order matters: last match wins**. Entries are
`provider/id` keys, `*` matches any run of characters (slashes included), and a
`!` prefix un-hides:

```json
{
  "hidden_models": [
    "openrouter/*",
    "!openrouter/anthropic/claude-*",
    "openrouter/anthropic/claude-2*"
  ]
}
```

That reads: hide everything OpenRouter serves, keep Anthropic's models, but not
the old Claude 2 ones. This is what makes a 300-model provider tractable without
listing the 290 you did not want, which is a list that would rot every time the
provider added a model.

Because `*` crosses slashes, `openrouter/*` matches ids that contain their own
slash, such as `openrouter/anthropic/claude-sonnet-4.5`.

The per-model toggles maintain this file for you, and un-hiding a model that a
**pattern** covers appends an explicit `!` exception rather than deleting your
pattern, because the pattern is still doing its job for every other model it matches.
The web picker shows which rule hid a model when you hover its ⊘.

#### Hiding duplicate variants

OpenRouter is the provider these rules were written for, and its cheapest win
needs no curation at all. It lists `:free` and `:batch` variants beside the
plain id, so `openai/gpt-4o` appears three times. Two rules remove every one of
them:

```json
{
  "hidden_models": [
    "openrouter/*:free",
    "openrouter/*:batch"
  ]
}
```

At the time of writing that hides 59 of 396 models and loses nothing: every
suffixed id has a plain base id that is also listed, so no model becomes
unreachable.

**Confirm that before you hide by pattern.** The tempting next rule is
`openrouter/*-preview`, and it is a trap. Half of those ids have no
non-preview counterpart, so it would hide the only route to
`google/gemini-3-flash-preview`, `google/gemini-3.1-pro-preview` and
`qwen/qwen3.6-max-preview`, all current models, removed by a rule that looked
like tidying. Before adopting a pattern, list what it matches and check each one has
a sibling you are keeping.

Expect this to trim the list, not transform it. OpenRouter's bulk is superseded
versions *inside* the big vendors (`gpt-3.5-turbo` and `gpt-4-turbo` sit beside
`gpt-4o`), and no pattern expresses "older", so the picker's type-to-filter
stays the way to reach a specific model quickly.

Unlike the opencode-go list below, these counts are not checked by a test:
OpenRouter's models are discovered live and have no baked catalog to compare
against. Re-derive them from the picker rather than trusting the numbers here.

#### When there is no pattern to write

Not every provider's clutter has a shape a glob can catch. OpenCode Go serves 37
models while its landing page promotes 28. The extra nine are seven superseded
releases (`glm-5`, where `glm-5.1` through `5.3` are current; `kimi-k2.5`, where
`k2.6` and `k3` are), plus a preview and an experimental vision build. Nothing
about an id says "older", so this one is a plain enumeration:

```json
{
  "hidden_models": [
    "opencode-go/deepseek-v4-flash-vision-exp",
    "opencode-go/glm-5",
    "opencode-go/grok-4.5",
    "opencode-go/hy3-preview",
    "opencode-go/kimi-k2.5",
    "opencode-go/mimo-v2-omni",
    "opencode-go/mimo-v2-pro",
    "opencode-go/minimax-m2.5",
    "opencode-go/qwen3.5-plus"
  ]
}
```

All nine still work: they are served, they keep their real context windows, and
`--model` reaches them. This only trims the picker to the lineup OpenCode
currently promotes.

Treat it as a snapshot, not a rule. Where `openrouter/*` keeps working as that
gateway adds models, this list goes stale the moment OpenCode retires one or
ships another, and nothing will tell you it has. That is the trade an
enumeration makes.

## Per-provider notes

What each provider does differently once you are reaching it. Getting a
credential to it in the first place is [providers.md](providers.md).

### GPT-6 Astra

OpenAI began a staged GPT-6 Astra rollout on September 3, 2026. The built-in
catalog lets an entitled account select `gpt-6-astra` through either
Responses-backed route:

```bash
# OpenAI API key
terva --provider openai-responses --model gpt-6-astra

# ChatGPT subscription
terva --provider openai-codex --model gpt-6-astra
```

Catalog presence does not grant model access. OpenAI started with Trusted
Access Program enterprises and said API and ChatGPT plan access would follow.
An account without Astra access can keep using another catalog model until its
entitlement arrives.

Astra is intentionally absent from the plain `openai` provider. That provider
uses Chat Completions. OpenAI lists Chat Completions as an Astra endpoint, but
[Astra tool calling requires the Responses API](https://developers.openai.com/api/docs/guides/latest-model?model=gpt-6-astra).
Exposing Astra on `openai` would therefore select a model that cannot run
terva's tools.

The catalog records Astra's 1,050,000-token hard limit and 128,000-token output
limit. It uses 272,000 tokens as the default working window because OpenAI's
long-context rates start above that point. Input and cache rates double above
272,000 input tokens, while output costs 1.5 times the short-context rate. The
short-context prices are $10 input, $1 cached input, $12.50 cache write, and $50
output per million tokens. See OpenAI's
[Astra model page](https://developers.openai.com/api/docs/models/gpt-6-astra)
and [API pricing](https://developers.openai.com/api/docs/pricing).

Astra accepts `low`, `medium`, `high`, `xhigh`, and `max` reasoning efforts.
terva sends its `max` rung without clamping for Astra. Native image output
remains unasserted until the ChatGPT subscription route can be live-tested.

### GPT-6 Sol and GPT-6 Luna

OpenAI released GPT-6 Sol and GPT-6 Luna on September 22, 2026, in the API
and for ChatGPT Plus, Pro, Business, Enterprise, and Edu subscriptions. Sol is
the general and coding model. OpenAI positions Luna for high-volume tasks with
a clear goal, such as summarizing, extraction, and quick answers. Both are in
the catalog on the same two Responses-backed routes as Astra:

```bash
# OpenAI API key
terva --provider openai-responses --model gpt-6-luna

# ChatGPT subscription
terva --provider openai-codex --model gpt-6-luna
```

Sol is the default model for both `openai-codex` and `openai-responses`, so
either provider starts on `gpt-6-sol` when you give no `--model`. Before
September 22, `openai-codex` defaulted to `gpt-5.5` and `openai-responses` to
`gpt-5`. On an API key, Sol costs more input than `gpt-5` did ($2 against
$1.25 per million tokens), at the same $10 output. To keep the old default,
pass the old model for your provider:

```bash
terva --provider openai-codex --model gpt-5.5
terva --provider openai-responses --model gpt-5
```

Neither model is on the plain `openai` provider, for the reason Astra is not.
On Chat Completions, OpenAI allows function calling only at reasoning effort
`none`, so every other thinking level would run without terva's tools.

Both models share Astra's limits: a 1,050,000-token hard limit, a
128,000-token output limit, and a 272,000-token default working window where
long-context pricing starts. Above 272,000 input tokens, input and cache rates
double for the whole request, and output costs 1.5 times the short-context
rate. Short-context prices per million tokens:

| Model | Input | Cached input | Cache write | Output |
|---|---:|---:|---:|---:|
| `gpt-6-sol` | $2 | $0.20 | $2.50 | $10 |
| `gpt-6-luna` | $0.10 | $0.01 | $0.125 | $0.50 |

See OpenAI's [Sol](https://developers.openai.com/api/docs/models/gpt-6-sol)
and [Luna](https://developers.openai.com/api/docs/models/gpt-6-luna) model
pages.

Both accept efforts from `none` through `max`, and terva sends its `max` rung
natively. Native image output is unasserted, as on Astra.

The catalog also declares those efforts for an `openai-compatible` gateway that
serves the same ids. Discovery copies the list from the built-in rows (see
[Declaring which efforts a model accepts](#declaring-which-efforts-a-model-accepts)),
so on such a gateway `off` sends `reasoning_effort: "none"` rather than leaving
the field out, and `maximum` and `max` reach `xhigh` and `max` rather than
stopping at `high`. The `none` matters for a gateway that passes Chat
Completions straight through to OpenAI: an omitted effort means `medium` there,
and Sol and Luna accept tool calls on Chat Completions only at `none`. Any
thinking level above `off` still cannot call tools through such a gateway. A
gateway that converts requests to the Responses API has no such limit. Astra's
list has no `none`, so `off` still omits the field for Astra. A `models.json`
entry for the id that sets `reasoningEfforts` replaces the inherited list.

On the Codex subscription, the
[swarm tier ladder](#swarm-sub-agent-tiers-weak--medium--strong--cheap) now
resolves to GPT-6 first: `cheap` is Luna at low thinking, `weak` is Luna at
medium, `medium` is Sol at low, and `strong` is Sol at high. Luna replaced
the mini as the `cheap` rung because its $0.50 output rate undercuts the
mini's $4.50.

A tier resolves against terva's catalog, not against what your account can
use. The GPT-6 rows are always in the catalog, so the ladder always picks
GPT-6, even on an account that OpenAI has not yet given GPT-6. A sub-agent on
such an account fails when the request reaches OpenAI. GPT-5.6 is listed
behind GPT-6 only so that the ladder still resolves if a later catalog drops
the GPT-6 rows. To keep a rung on GPT-5.6, pin it by id in `swarm_tiers`.

### Kimi Code

terva has built-in Kimi support through Kimi Code, which speaks the **Anthropic Messages API** rather than OpenAI chat-completions, so terva drives it with the same client it uses for Claude.

```bash
terva --provider kimi
```

By default this uses:

- model: `kimi-for-coding`
- base URL: `https://api.kimi.com/coding` (no `/v1` suffix; the Anthropic client appends `/v1/messages` itself)

Credential lookup order for Kimi:

1. `--api-key`
2. `KIMI_API_KEY`
3. `MOONSHOT_API_KEY`
4. `$TERVA_HOME/auth.json`
5. the official Kimi Code CLI token at `~/.kimi/credentials/kimi-code.json`, unless disabled by `/logout kimi`

Use `/login` for either API-key login or Kimi Code subscription login. The subscription flow uses Kimi Code's device-code OAuth flow: terva opens the verification URL, waits for browser approval, stores the token in `auth.json`, and refreshes it automatically.

Direct Moonshot API keys are a *different* provider, `moonshotai`, because the Moonshot platform endpoint is OpenAI-compatible where Kimi Code is Anthropic-shaped:

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

> **Text only at the wire level.** DeepSeek's chat-completions endpoint currently rejects the multimodal content schema (`unknown variant image_url, expected text`), so the V4 catalog rows are tagged `image-input: false`. The drop is **capability-keyed, not provider-keyed**: for any model without the image-input capability, whether a DeepSeek V4 row, a vision-less local GGUF, or a `{"image-input": false}` entry in your `models.json`, terva silently drops `ImageBlock` parts from outgoing user/tool messages and keeps only the text. Switching back to a vision-capable model (Claude, GPT-5, Gemini) re-sends the image normally because the session file still stores it.

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

- model: `gemini-flash-latest`
- base URL: `https://generativelanguage.googleapis.com`

The default is deliberately a *rolling alias*. A pinned id goes stale the moment Google retires it, and a retired default fails a first run before it sends a token, which is exactly what `gemini-2.5-pro` did once it became unavailable to new keys. `gemini-flash-latest` only ever moves forward. It resolves to `gemini-3.7-flash` today; a response's `modelVersion` field tells you what it points at now.

The catalog spans the 3.x line (`gemini-3.1-pro-preview`, `gemini-3.5-flash`, `gemini-3.6-flash`, `gemini-3.7-flash`, the Flash-Lite models and previews), the rolling `gemini-flash-latest` / `gemini-flash-lite-latest` aliases, and the open Gemma models. It moves with every Google release, so run `terva --list-models` for the current set. Live discovery against `/v1beta/models` adds anything else your key can see.

The Gemini 2.x and 2.0 families are **gone** from the catalog: the API now answers 404 for all of them (`gemini-2.5-*` with "no longer available to new users", `gemini-2.0-*` with "no longer available"). If your key predates the change and still has 2.5 access, add the ids back through `models.json`, where a user entry wins over the built-in catalog.

Getting a key in place, and the order terva looks for one, are in
[providers.md](providers.md#api-key-providers).

> **Free-tier rate limits.** AI Studio's free tier has tight per-minute and per-day caps that vary by model: the Pro models are the strictest (a few requests per minute, ~50 per day), Flash and Flash-Lite are far more generous. If a Pro turn 429s with `"You exceeded your current quota"` while Flash on the same key still works, you've hit the Pro free-tier RPD. Either switch to Flash for agent loops, or [enable billing](https://aistudio.google.com/app/apikey) on your AI Studio project to flip the same key from free to pay-as-you-go pricing.

Thinking levels (`--thinking off|minimum|low|medium|high|maximum|max`, also configurable in `/settings` as **Thinking**) map differently per generation. Budget-based providers use roughly 1k/2k/8k/16k/32k thinking tokens for minimum/low/medium/high/maximum, with provider/model caps applied. Gemini 3.x uses the `thinkingLevel` enum (`MINIMAL`/`LOW`/`MEDIUM`/`HIGH`), with the Pro models pinned to `LOW` minimum and `HIGH` for any "medium" or higher request. The rolling `-latest` aliases are treated as 3.x, since they always point at a current model. Effort-based OpenAI-compatible chat providers map minimum to `low`, low/medium directly, and high/maximum to `high`; the Codex/Responses backend maps maximum to `xhigh` where supported. `max` is a seventh tier *above* `maximum`, opt-in on purpose: it is sent natively only where the model has an effort above `xhigh` (GPT-5.6, GPT-6, adaptive-thinking Claude) and is clamped back to the `maximum` effort everywhere else. `off` sends no reasoning config, unless the model declares a `none` effort (see [Declaring which efforts a model accepts](#declaring-which-efforts-a-model-accepts)). 2.0-family Gemini models have no thinking config at all.

You can add additional Gemini model IDs to `models.json` under the `google` provider.

#### Persisting reasoning summaries

**Default: off.** Set it in `/settings` as **Record thinking** (the same row appears in the web settings pane), or set `"reasoning_summary"` in `config.json` to `"auto"`, `"concise"`, or `"detailed"`. It asks the model for a human-readable summary of its reasoning and keeps it in the session record alongside the opaque reasoning payload. Changing it applies live to every open session and becomes the default for new ones. This exists for reviewing unattended runs: without it a transcript shows *what* an agent did with no trace of *why*, and intent has to be reconstructed from the shape of the tool calls. Any unrecognized value is treated as off.

Recorded thinking is also **shown**, which is the point of recording it somewhere you will look. In the TUI each reply that carries one renders a muted `▸ thinking` line above the prose; `ctrl+o` opens it, the same key that expands tool results and the compaction summary. In the web panel it is a `▸ thinking` chevron on the message, opened per message. Both are collapsed by default, so a recorded session reads exactly as it did before until you ask for the reasoning behind a particular reply. With **Record thinking** off there is no readable text to show and no control appears. The live line that streams during a turn is a separate, deliberately ephemeral thing (**Show thinking**), and it is still discarded when the turn ends.

Four reasoning wires implement this today, and they divide by whether the provider waits to be asked. `openai-codex` sends a summary only when terva requests one, so the setting simply decides whether to ask. `anthropic`, `google` and the OpenAI-compatible chat backends that emit `reasoning_content` (DeepSeek, Kimi, and similar) all send readable reasoning unbidden, so for them the setting decides what is *kept*: with **Record thinking** off, Anthropic's block is dropped outright, and the Gemini and chat summaries are blanked while each keeps the opaque half it needs to replay.

Those four wires cover more than four provider ids, because several providers reuse a wire rather than implementing their own. `azure` and `github-copilot` are the OpenAI chat client under different credentials and base URLs, and `google-vertex` is the Gemini client under a different name, so all three behave exactly as the wire they ride, this setting included. `bedrock` captures no reasoning at all and is unaffected. The practical rule: if your provider shows you thinking, **Record thinking** governs whether that thinking is written down.

There is one deliberate exemption. A chat backend whose thinking channel never closes puts the entire *reply* into `reasoning_content` and leaves the visible content empty; terva promotes that text back into the answer on the following request. Where such a turn has no other substance, with no visible text and no tool calls, the summary is the reply rather than deliberation, and it is kept regardless of the setting. Blanking it there would not quiet the turn, it would delete it, leaving a hole in the history exactly where the answer had been.

Anthropic is the one provider where the setting also changes what is *sent*, and it is worth understanding why. A Codex summary exists only because terva asked for one, so declining to record it is a matter of not asking. Anthropic sends thinking the moment thinking is enabled, with no way to decline it, and seals each block with a signature over its own text. Text and signature are one object: a block with the text blanked is not a quieter block, it is one Anthropic will reject. So with **Record thinking** off, terva drops the block outright, which is exactly where this provider stood before it captured thinking at all. With it on, the block is kept whole and replayed on following requests, which is the form Anthropic asks for beside a tool call.

Reasoning never crosses providers. Each block records which wire it came from, and each provider replays only its own, so a `/model` switch mid-session leaves the earlier turns' reasoning in the transcript for you to read while sending none of it onward. That matters in both directions: one vendor's opaque payload is meaningless to another and is rejected outright, and Anthropic's readable thinking is the model's unabridged deliberation, which has no business being replayed to a different vendor as that model's own prior words. A block from a provider terva does not recognize is replayed nowhere, which is also what keeps a session written by a newer terva safe to open in an older one.

What goes back is the provider's own opaque half, never the readable one. Gemini is the clearest case: it seals a text-only answer with a trailing thought signature, and terva returns that signature and leaves the thought summary in the transcript, where it costs nothing to re-send and buys the model nothing.

Two kinds of Anthropic block are kept regardless of the setting, because neither contains anything to withhold: blocks Anthropic redacted itself, and the signed-but-textless blocks that **adaptive-thinking models** (Opus 4.7+, Sonnet 5, Fable 5 and 5.1) produce. Adaptive models do not stream their reasoning at all: they think, sign the result, and send no readable text, so the block is pure replay state and keeping it costs nothing in privacy while preserving the form Anthropic asks for. Adaptive models are also the only Anthropic models that report a separate thinking-token count, which is what the reasoning figure in `/usage` reflects for them; budget-thinking models fold thinking into their output tokens with no breakdown.

Three things worth knowing before enabling it:

- **It is written to disk, and it can quote.** A summary of a turn spent reading mail can quote that mail. Tool results in the same session file already carry comparable content, so this is not a new class of exposure, but it does make the file more quotable, so account for it before widening who reads transcripts.
- **The cost is on the input side, not generation.** The summary rides inside the reasoning tokens you already pay for, and a turn where the model does no reasoning emits no summary at all. What accrues is context: summaries persist into the transcript, so they are re-sent with it. terva deliberately does *not* replay the summary back to the provider as part of the reasoning item, because the encrypted payload is what the model consumes, and that keeps the per-turn replay cost unchanged. Anthropic is the exception, and unavoidably so: its signature seals the thinking text, so a replayed block carries the text with it.
- **It is a readable record, not a visible one.** Summaries land in the session JSONL for after-the-fact review; the TUI and web UI do not display them. The one place a summary surfaces is an ACP client repainting a resumed session, and only for a turn that produced no visible text of its own; otherwise the turn repaints as the model's actual answer, unchanged.

### Local models with ollama

For reaching an ollama server at all, local or remote, see
[providers.md](providers.md#ollama-local). This section is about the models you
run on it.

Ollama reports the models you have pulled, but not their limits, so a pulled
model arrives with no context window. Pin the ones you use in `models.json` and
you also stop needing flags every time:

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

For a server that is not ollama, see the next section: the `ollama` provider is
fixed to one base URL, and `openai-compatible` is the one that is not.

### OpenAI-compatible endpoints (LM Studio, vLLM, llama.cpp, gateways)

Configuring one of these endpoints, naming it so several coexist, and where its
key is kept are all in
[providers.md](providers.md#openai-compatible-endpoints-local-and-custom-servers).
This section is about the two things that are properties of the **model** rather
than of the server: its size, and which reasoning efforts it accepts.

Discovery names a model without describing it, which is where both problems come
from.

**Context size and max response tokens.** The standard `/v1/models` response only lists model ids, so terva can't always know a model's limits. It reads non-standard hints when the server provides them (vLLM's `max_model_len`, some gateways' `context_length`), and otherwise uses the default context window you set at login. For exact per-model values, `maxTokens` included since the login form does not capture it, pin the model in `models.json`:

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

`contextWindow` is the total token budget (drives the context gauge and auto-compaction); `maxTokens` is the cap on a single response (`max_tokens`). `models.json` values win over both the catalog and whatever `/v1/models` reports, so they're the fix when a server under-reports its limits. A `"capabilities"` map tags what the model can do, most usefully `{"image-input": false}` for a local model without vision, so terva drops image attachments with a note instead of letting the server reject every turn (see [providers.md](providers.md#capability-tags)). See [providers.md](providers.md#openai-compatible-endpoints-local-and-custom-servers) for the full reference.

#### Declaring which efforts a model accepts

The `reasoning_effort` enum is a property of the **model**, not of the wire. OpenAI's own guide says "some models support only a subset of these values": `gpt-5.5` takes `none`/`low`/`medium`/`high`/`xhigh` and rejects `minimal` and `max`, while a local qwen server may take `low`/`medium`/`xhigh` and reject `high` with an HTTP 400 on every turn. terva cannot interrogate an endpoint for its enum, so by default it guesses conservatively, which is why `minimum` is sent as `low` and both top rungs collapse onto `high`.

`reasoningEfforts` replaces the guess with a fact:

```json
{
  "providers": {
    "openai-compatible": {
      "models": [
        {
          "id": "qwen3.8-27b-abl",
          "reasoning": true,
          "reasoningEfforts": ["none", "low", "medium", "xhigh"]
        }
      ]
    }
  }
}
```

**Leaving it out changes nothing.** An undeclared model behaves exactly as it always has, so this can never cost you a rung you had before. Only a model that declares its efforts is held to them.

Once declared, two things change. Each thinking rung aims at what it *means* and is then bent onto the nearest value you listed, so `maximum` reaches the `xhigh` above rather than being pre-clamped to a `high` this model would reject. And a rung never crosses the off boundary in either direction: a thinking level is never quietly answered with `none`, and `off` is never bent up into thinking.

Declaring `none` also fixes a subtler trap. terva's `off` normally *omits* the field, which leaves the server free to apply its own default. On a server whose default is `xhigh`, "off" silently bought the **most** expensive setting available. Listing `none` lets terva say so explicitly. A model that has no `none` keeps omitting the field, which is the honest answer: such a model reasons whatever terva does.

The `/reasoning` ladder reads the same declaration as the request builder, so what the dialog shows you is what the wire sends.

The built-in GPT-6 rows declare OpenAI's published sets, so an `openai-compatible` gateway that serves `gpt-6-sol`, `gpt-6-luna`, or `gpt-6-astra` inherits them at discovery with no `models.json` entry. See [GPT-6 Sol and GPT-6 Luna](#gpt-6-sol-and-gpt-6-luna).

## Swarm sub-agent tiers (weak / medium / strong / cheap)

When auto-swarm is on, the agent picks a `tier` for each background sub-agent it spawns. A tier always resolves **for the host's own provider** (a sub-agent stays on the provider you're using), and each provider maps the tiers to its own models.

There are two axes, and they answer different questions:

- **`weak` / `medium` / `strong`, how capable.** Ordered, and **capped at the host model's tier**: a weak host can't spawn a strong child, so delegation stays cheaper than doing the work yourself.
- **`cheap`, how much it costs.** Deliberately *outside* that ordering, and **never capped**. It exists because capability stopped being a matter of which model: on a recent series the better "medium" is usually the largest model thinking a little, and a small model earns its place thinking *hard*. Once the ladder says that, none of its three rungs answers "keep this cheap", so a caller that cares about spend rather than strength had nothing to ask for. Reach for `cheap` in test runs and bulk passes.

terva ships a built-in mapping per provider:

| provider | weak | medium | strong | cheap |
|---|---|---|---|---|
| Anthropic | haiku, thinking high | opus, thinking low | opus, thinking high | haiku, thinking minimum |
| OpenAI (Codex subscription) | luna, thinking medium | sol, thinking low | sol, thinking high | luna, thinking low |
| GitHub Copilot | haiku | sonnet | opus | haiku, thinking minimum |
| Google | flash-lite | flash | pro | flash-lite, thinking minimum |
| OpenAI | nano | mini | the plain flagship | nano, thinking minimum |
| DeepSeek | flash | — | pro | flash, thinking minimum |
| Kimi | K2 | — | K3 | K2, thinking minimum |

**A rung is a model *and* an effort.** Anthropic and Codex are effort ladders: medium and strong are the same model at two thinking levels. That is not a shortcut. It is what those ladders should say, and a table that could only name model families said something else confidently. Where two rungs share a model, their efforts must reach it as genuinely different values on the wire, or the rungs would be one sub-agent with two labels; a guard checks exactly that.

Because most of a provider's catalog then matches no rung at all, the host cap falls back to **price**: a host that isn't itself a rung is ranked at the dearest rung that costs no more than it does. So a Sonnet host still can't spawn an Opus child, even though Sonnet is no longer a rung. (Skipped where prices are all zero, as on a subscription provider: treating "free" as equal would silently switch the cap off.)

Most rows are **family names**, matched as substrings, so they survive version bumps (`claude-opus-4-5` → `4-8`) with no edits. Two rows are not:

- **DeepSeek and Kimi have no middle model to point at**, so their ladder has two rungs. A `tier: weak` spawn gets the cheap model, which is the point; but a two-rung ladder is not a ladder, so it does **not** satisfy raati rigor level 1 (see [raati.md](raati.md)).
- **Codex names its generations sol / terra / luna**, which say nothing about capability and don't recur, so its rungs list actual model ids newest-first and fall through to the previous generation. That row wants a touch when a new generation lands.

A resolved tier never picks a *speculative* catalog entry, meaning a model terva knows about from the vendor's CLI but that isn't live on their API yet. Those 404 today, and a tier is dispatched, not suggested. Pin one by id in `swarm_tiers` if you want it anyway.

**Every other provider, gateways like opencode-go, OpenRouter and LiteLLM included, has no built-in mapping**, so `tier` is ignored there and sub-agents fall back to the full host model (which is why a `tier: weak` spawn on opencode-go still costs host-model price). To get cheap tiers on those providers, configure them yourself.

**See what resolves today, and what to set:**

```bash
terva models tiers          # per logged-in provider: what each tier resolves to
terva models tiers --all    # include providers you're not logged into
```

Providers with no mapping are flagged, with a ready-to-paste config block and candidate model ids from that provider's catalog.

**Configure** per provider in `$TERVA_HOME/config.json` under `swarm_tiers` (user config only; a project's `.terva/config.json` cannot redirect sub-agent model selection):

```json
{
  "swarm_tiers": {
    "opencode-go": {
      "weak":   "minimax-m3",
      "medium": "glm-5.2",
      "strong": "kimi-k2.7-code",
      "cheap":  { "model": "minimax-m3", "reasoning": "minimum" }
    }
  }
}
```

The ids must be real models in that provider's catalog (`terva models tiers` flags ones that aren't). Your entries **override** the built-in guesses; a **partial** map is fine, and any tier you leave out falls back to the built-in guess for that provider, then to the host model. The host-cap rule still applies: when terva can identify the host model's own tier (by your configured ids or the built-in families), it never resolves a *stronger* tier than the host.

### A rung can name a thinking level instead of a model

Strength is not only a matter of *which* model. A reasoning model with thinking off is meaningfully cheaper and faster than the same model at high, which is the only ladder available on a provider that ships one good model and no cheap sibling. Write a rung as an object to say so:

```json
{
  "swarm_tiers": {
    "kimi": {
      "weak":   { "model": "k3", "reasoning": "off" },
      "medium": { "model": "k3", "reasoning": "medium" },
      "strong": { "model": "k3", "reasoning": "high" }
    }
  }
}
```

`reasoning` takes the same values as `--reasoning` (`off`, `minimum`, `low`, `medium`, `high`, `maximum`) and is passed to the sub-agent as that flag. The bare-string form is unchanged and still means "this model, its own default effort".

**Naming only an effort is legal** and useful, because the rung borrows its built-in model:

```json
{ "swarm_tiers": { "anthropic": { "weak": { "reasoning": "off" } } } }
```

resolves weak to Haiku with thinking off. Repeating the id would only invite it to drift from the built-in one.

Two consequences worth knowing:

- **The host cap stops applying to a one-model ladder.** The cap exists to stop a weak host reaching for a stronger *model*; when one id sits on several rungs, ranking the host is ambiguous by construction, so terva declines to rank it and every tier resolves uncapped. Without that, a `k3` host would rank as the weak rung and *every* spawn would land on thinking-off.
- **The built-in table never names an effort.** terva recognises model families; it does not guess how hard you want your sub-agents to think.

A thinking ladder counts as a full ladder for raati rigor level 1, with one restriction, covered in [raati.md](raati.md).
