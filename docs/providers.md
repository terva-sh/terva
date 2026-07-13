# terva providers

terva ships with built-in providers and a model catalog. You can select models
with `/model`, list them with `terva --list-models`, and add private models in
`$TERVA_HOME/models.json`.

## Login methods

Use `/login` in interactive mode.

- `api key`: stores an API key in `$TERVA_HOME/auth.json` when the provider uses a normal key.
- `subscription`: stores OAuth credentials for subscription-backed providers.

Use `/logout` to remove stored credentials.

Some providers need more than a single pasted key. For those providers,
`/login` shows setup instructions instead of opening a localhost browser form.
This avoids broken browser flows in SSH, containers, and `kubectl exec`
sessions.

Setup-instruction providers:

- Amazon Bedrock
- Google Vertex AI
- Cloudflare Workers AI
- Cloudflare AI Gateway
- Azure OpenAI Responses

## Logging in on a headless machine

Both login methods work over plain SSH, in a container, or anywhere else
with no browser on the terva host. `/login` opens a local page as a
convenience, but never depends on it.

**API key.** Paste the key straight into the `/login` dialog and press
enter. The key is checked against the provider before it is stored, so a
mistyped key is rejected there and then rather than failing later on the
first request. The local page is offered as well, and still works if you
are at a browser on that machine — but it binds to loopback on a random
port, so it is unreachable from anywhere else. The paste box is the
headless path.

**OpenAI Compatible** needs a base URL and a default model id as well, so
`/login` gives it a small form instead of a single box: base URL, default
model id, an optional API key (most local servers ignore it), and an
optional default context window. Tab and shift-tab move between the fields,
enter submits. The endpoint is probed before it is stored.

**Subscription (OAuth).** The provider's callback URL is pinned to
`localhost` by the provider's own client registration, so terva cannot move
it to a reachable address — the browser on your laptop will fail to load it.
That is expected and harmless: the authorization code is in the failed URL.
Copy the whole URL out of the browser's address bar and paste it back into
the `/login` dialog. It also accepts a bare code or `code#state`.

Anthropic additionally offers a variant that redirects to its own console
instead of a local port, so no local server is involved at all.

## Subscription providers

These providers support subscription login:

| Provider | Notes |
| --- | --- |
| Anthropic | Claude Pro/Max OAuth credentials. |
| OpenAI Codex | ChatGPT Plus/Pro Codex subscription route. Separate from the OpenAI API-key provider. |
| Kimi | Kimi subscription login. |
| GitHub Copilot | GitHub Copilot token flow. |

OAuth tokens are stored in `$TERVA_HOME/auth.json` and refreshed when refresh is
available.

### Usage limits (`/usage`)

`/usage` shows where you stand against a subscription's usage windows — the
5-hour and weekly budgets, with how much is consumed and when each resets — plus
any pay-as-you-go credits. The status bar also shows a compact `weekly 88%` hint
for the busiest window once it crosses 80%, colored yellow (≥80) then red (≥90);
below that it stays out of the way.

This works wherever the provider puts usage data on the wire terva already talks
to, or hangs it off a cheap endpoint beside it. Today:

- **OpenAI Codex** returns its subscription window state as response headers on
  every request — so `/usage` is accurate, costs no extra calls, and refreshes
  each turn.
- **Every OpenAI-shaped provider** (openai, groq, xai, ollama, the compatibles)
  reports whatever `x-ratelimit-*` windows its responses carry, on the same free
  ride.
- **OpenRouter** and **DeepSeek** ship no window headers but do have a balance
  endpoint, so terva polls it lazily (cached, off the hot path) and renders the
  answer as pay-as-you-go credits: OpenRouter's key limit/remaining plus lifetime
  spend, DeepSeek's account balance.

Providers that don't expose usage data show `<provider> doesn't report usage
limits` in `/usage`, and no status-bar hint. This includes OpenCode Go for now:
it has no usage/balance endpoint yet ([anomalyco/opencode#16017](https://github.com/anomalyco/opencode/issues/16017)).
The mechanism is a generic `provider.UsageReporter` capability — any provider
lights up `/usage` automatically once its client implements it, with no harness
changes. See `docs/plans/archive/usage-windows.md`.

## API-key providers

These providers can use environment variables. Simple API-key providers can
also be configured through `/login`. Providers that require extra cloud setup
show instructions and should be configured with environment variables. Two rows
below are *not* offered in the `/login` picker and are reached another way:
`openai-responses` (opt in with `--provider openai-responses`; it reuses your
OpenAI key from the environment) and `ollama` (a local server — no key at all).

| Provider | Environment variable | Stored key |
| --- | --- | --- |
| Anthropic | `ANTHROPIC_API_KEY` | `anthropic` |
| OpenAI | `OPENAI_API_KEY` | `openai` |
| OpenAI Responses | `OPENAI_API_KEY` | `openai-responses` |
| Kimi | `KIMI_API_KEY` or `MOONSHOT_API_KEY` | `kimi` |
| Google Gemini | `GEMINI_API_KEY` or `GOOGLE_API_KEY` | `google` |
| DeepSeek | `DEEPSEEK_API_KEY` | `deepseek` |
| Moonshot AI | `MOONSHOT_API_KEY` | `moonshotai` |
| Moonshot AI China | `MOONSHOT_API_KEY` | `moonshotai-cn` |
| Groq | `GROQ_API_KEY` | `groq` |
| xAI | `XAI_API_KEY` | `xai` |
| Cerebras | `CEREBRAS_API_KEY` | `cerebras` |
| Together AI | `TOGETHER_API_KEY` | `together` |
| Hugging Face | `HF_TOKEN` | `huggingface` |
| OpenRouter | `OPENROUTER_API_KEY` | `openrouter` |
| Mistral | `MISTRAL_API_KEY` | `mistral` |
| ZAI | `ZAI_API_KEY` | `zai` |
| Xiaomi MiMo | `XIAOMI_API_KEY` | `xiaomi` |
| Xiaomi Token Plan Amsterdam | `XIAOMI_TOKEN_PLAN_AMS_API_KEY` | `xiaomi-token-plan-ams` |
| Xiaomi Token Plan China | `XIAOMI_TOKEN_PLAN_CN_API_KEY` | `xiaomi-token-plan-cn` |
| Xiaomi Token Plan Singapore | `XIAOMI_TOKEN_PLAN_SGP_API_KEY` | `xiaomi-token-plan-sgp` |
| MiniMax | `MINIMAX_API_KEY` | `minimax` |
| MiniMax China | `MINIMAX_CN_API_KEY` or `MINIMAX_API_KEY` | `minimax-cn` |
| Fireworks | `FIREWORKS_API_KEY` | `fireworks` |
| Vercel AI Gateway | `AI_GATEWAY_API_KEY` | `vercel-ai-gateway` |
| OpenCode Zen | `OPENCODE_API_KEY` | `opencode` |
| OpenCode Go | `OPENCODE_API_KEY` | `opencode-go` |
| GitHub Copilot token | `COPILOT_GITHUB_TOKEN` or `GITHUB_COPILOT_TOKEN` | `github-copilot` |
| Cloudflare Workers AI | `CLOUDFLARE_API_KEY` | `cloudflare-workers-ai` |
| Cloudflare AI Gateway | `CLOUDFLARE_API_KEY` | `cloudflare-ai-gateway` |
| Azure OpenAI Responses | `AZURE_OPENAI_API_KEY` | `azure-openai-responses` |
| Ollama (local) | — (no key; `--base-url` for a remote host) | `ollama` |
| OpenAI Compatible (local/custom) | — (use `/login` or `--base-url`) | `openai-compatible` |

Example:

```bash
export OPENROUTER_API_KEY=...
terva --provider openrouter
```

## Cloud providers

### Amazon Bedrock

Bedrock is configured with AWS credentials, not a generic terva API-key entry.
Use one of these credential sources:

```bash
# AWS profile
export AWS_PROFILE=your-profile

# IAM access keys
export AWS_ACCESS_KEY_ID=AKIA...
export AWS_SECRET_ACCESS_KEY=...
export AWS_SESSION_TOKEN=... # only for temporary credentials

# Bedrock API key bearer token
export AWS_BEARER_TOKEN_BEDROCK=bedrock-api-key-...

# Region
export AWS_REGION=us-east-1
```

ECS task roles, IRSA, and other AWS SDK credential-chain sources are also
supported.

Example:

```bash
AWS_BEARER_TOKEN_BEDROCK=bedrock-api-key-... AWS_REGION=us-east-1 \
  terva --provider amazon-bedrock --model anthropic.claude-sonnet-4-5-20250929-v1:0
```

Some Bedrock models require regional inference-profile IDs for on-demand
throughput, such as `us.` or `eu.` prefixed model IDs. terva rewrites known
families automatically where possible. Explicit profile IDs and ARNs are left
unchanged.

### Google Vertex AI

Vertex can use a Google API key when available:

```bash
export GOOGLE_CLOUD_API_KEY=...
terva --provider google-vertex
```

For service-account or application-default credentials, set the standard
Google environment variables used by your deployment.

### Cloudflare AI Gateway

Cloudflare AI Gateway needs a Cloudflare token plus account and gateway IDs:

```bash
export CLOUDFLARE_API_KEY=...
export CLOUDFLARE_ACCOUNT_ID=...
export CLOUDFLARE_GATEWAY_ID=...
terva --provider cloudflare-ai-gateway
```

### Cloudflare Workers AI

Workers AI needs a Cloudflare token and account ID:

```bash
export CLOUDFLARE_API_KEY=...
export CLOUDFLARE_ACCOUNT_ID=...
terva --provider cloudflare-workers-ai
```

### Azure OpenAI Responses

```bash
export AZURE_OPENAI_API_KEY=...
export AZURE_OPENAI_BASE_URL=https://your-resource.openai.azure.com
export AZURE_OPENAI_API_VERSION=2024-02-01 # optional
terva --provider azure-openai-responses
```

If your Azure deployment names differ from terva model IDs, add model overrides
in `$TERVA_HOME/models.json`.

## Ollama (local)

The `ollama` provider is the batteries-included local path: it speaks the same
OpenAI chat-completions wire, but defaults its base URL to
`http://localhost:11434` and needs no credential at all. There is no default
model — name the one you pulled:

```bash
ollama pull qwen3.5:4b
terva --provider ollama --model qwen3.5:4b
```

A remote or authenticated instance takes the usual flags
(`--base-url https://my-server.example/v1 --api-key <token>`), and `--insecure`
is permitted here for a self-signed endpoint. Pin models and context windows in
`models.json` under the `ollama` provider — see
[models.md](models.md#local-models-with-ollama).

## OpenAI-compatible endpoints (local and custom servers)

The `openai-compatible` provider points terva at any server that speaks the
OpenAI chat-completions protocol: LM Studio, vLLM, llama.cpp's server, LocalAI,
Ollama's `/v1` endpoint, or a hosted gateway. Unlike the `ollama` provider it
has no fixed base URL and is configured through `/login`.

### Logging in

Run `/login` and choose **OpenAI Compatible (local/custom)**. terva shows a
form collecting three things (plus an optional API key):

- **base url** — where requests go, e.g. `http://localhost:1234/v1`. terva lists
  the endpoint's models once to confirm it's reachable.
- **default model id** — the model selected after login, e.g. `qwen2.5-coder`.
- **default context window** — applied to discovered models the server doesn't
  describe a size for (optional; leave blank if unsure).

Tab and shift-tab move between fields; enter submits. The same fields are
served as a browser form on the terva host, if you prefer to fill them in
there — but that page is loopback-only, so on a remote host the TUI form is
the one that works.

These are stored in `$TERVA_HOME/auth.json` under `openai-compatible`. The API key
is optional — most local servers ignore it.

You can also configure it entirely from the CLI:

```bash
terva --provider openai-compatible \
  --model qwen2.5-coder \
  --base-url http://localhost:1234/v1 \
  --api-key optional-token   # omit for keyless local servers
```

### Model discovery

On every launch (and right after login) terva lists `GET {base-url}/models` and
adds every model the server reports to the `/model` picker. This is deliberately
**not** cache-gated, because a local server's loaded model set changes often.
Embeddings, rerankers, and audio models are filtered out.

The standard `/v1/models` response only carries model ids, so context sizes are
best-effort: terva reads common non-standard hints (vLLM's `max_model_len`, some
gateways' `context_length` / `context_window`) when present, and otherwise
applies your **default context window**. For exact per-model sizing, pin the
model in `models.json` (see below).

### One endpoint per login; use models.json for several

`/login` stores exactly **one** openai-compatible endpoint. The base URL,
default model, optional key, and default context window live under a single
`openai-compatible` entry in `auth.json`, so logging in to a second endpoint
**overwrites the first** — there is no second slot and no per-endpoint naming.

To use several endpoints at once, register them in `models.json` instead. Each
model entry pins its own `baseUrl`, so many endpoints coexist in `/model`
simultaneously and none clobbers another:

```json
{
  "providers": {
    "openai-compatible": {
      "models": [
        { "id": "qwen-local",   "baseUrl": "http://localhost:1234/v1", "contextWindow": 131072 },
        { "id": "llama-server", "baseUrl": "http://localhost:8000/v1", "contextWindow": 8192 }
      ]
    }
  }
}
```

Don't hand-write that from scratch — run `terva models init` to drop a starter
`models.json` (with two example endpoints) at `$TERVA_HOME/models.json`, edit the
ids/URLs, then run `terva --list-models` to confirm the entries load (they show
`source: user`). `terva models init` refuses to overwrite an existing file unless
you pass `--force`. A per-launch `--base-url` override also works for a one-off
without touching the stored login.

**Prefer named endpoints when a server should list its own models.** The
`models.json` pattern above only shows the entries you hand-list; a *named
endpoint* (defined in `config.json` under `endpoints`) becomes its own provider
that runs `/v1/models` discovery, so you don't pre-register each model. The two
coexist, so there's no forced migration. To convert an existing multi-`baseUrl`
`models.json`, run `terva models endpoints` — it prints a ready-to-paste
`endpoints` block (or `--apply` writes it to `config.json`) and flags the
now-redundant `models.json` entries for you to trim. See
[models.md](models.md#openai-compatible-endpoints-lm-studio-vllm-llamacpp-gateways)
for the named-endpoints reference and the full lift-and-trim path.

## Context window and max response tokens

Two fields control sizing for a model, and both matter most for local/custom
models where terva has no built-in catalog entry:

- **`contextWindow`** — the model's total token budget. Drives the status-bar
  context gauge and the automatic-compaction trigger. If it's `0`/unknown,
  auto-compaction is disabled and the gauge drops the percentage, falling back
  to a plain token count (safe, but you lose the usage-versus-limit readout).
- **`maxTokens`** — the maximum tokens terva requests for a single response (sent
  as `max_tokens` / `max_completion_tokens`). Leave it out to let the server use
  its own default.

For the `openai-compatible` provider the login form sets only the *default*
context window. To set precise per-model values — including `maxTokens`, which
the form does not capture — add the model to `$TERVA_HOME/models.json`:

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
        },
        {
          "id": "llama-3.1-8b",
          "contextWindow": 8192,
          "maxTokens": 2048,
          "baseUrl": "http://localhost:1234/v1"
        }
      ]
    }
  }
}
```

Entries in `models.json` take precedence over both the baked-in catalog and the
values discovered from `/v1/models`, so they're the source of truth when a
server under-reports (or misreports) its limits. `baseUrl` is optional here —
when omitted, the model uses the base URL stored at login.

## Auth file

Credentials are stored in `$TERVA_HOME/auth.json` with user-only permissions
when terva creates the file.

Example:

```json
{
  "anthropic": { "api_key": "sk-ant-..." },
  "openai": { "api_key": "sk-..." },
  "google": { "api_key": "..." },
  "additional_api_key_creds": {
    "openrouter": { "api_key": "..." },
    "mistral": { "api_key": "..." }
  }
}
```

The top-level keys are used for providers with dedicated credential fields.
Other API-key providers are stored under `additional_api_key_creds`. Prefer
`/login` so terva writes the correct schema.

## Custom providers and models

Use `$TERVA_HOME/models.json` for private models, deployment aliases, local
servers, or OpenAI-compatible gateways that are not in the built-in catalog.
User entries override built-in entries with the same provider and model ID.

Supported fields per model: `id` (required), `name`, `reasoning`,
`contextWindow`, `desiredContextWindow`, `maxTokens`, `temperature`, `baseUrl`,
`priceInput`, `priceOutput`, `priceCacheRead`, `priceCacheWrite`,
`capabilities`. `desiredContextWindow` is an optional smaller working window
that only moves the auto-compaction thresholds (compact earlier without
pretending the model is smaller); `temperature` (0–2) is the model's default
sampling temperature when no `--temperature` flag is given. See
[Context window and max response tokens](#context-window-and-max-response-tokens)
for what `contextWindow` and `maxTokens` control and a full
`openai-compatible` example.

### Capability tags

`capabilities` marks what a model can do, when terva can't know on its
own. Keys it understands today: `image-input` (vision — defaults to
**true** when unset), `image-output` (image generation — defaults to
false; consumed today by the `/model` picker's capability filter), and
`reasoning` (an alias for the top-level `reasoning` field). Unknown keys
load with a warning so a file written for a newer terva still works here.

The **true** default applies to the built-in catalog (its models are
known vision-capable). Models found by **discovery** are different: a
standard `/v1/models` list carries no modality data, so terva infers
`image-input` from the model id — known vision families (Claude 3+,
GPT‑4o/5, Gemini, and local names like `llava`/`*-vl`/`pixtral`) keep
vision; anything unrecognized is treated as **text-only**. That's
deliberately conservative: handing an image to a text-only model is a
hard `400` ("does not support image inputs", common on gateways like
opencode‑go), while a vision model given text-only input just proceeds.
If discovery guesses wrong for a model you know takes images, assert it
explicitly (below) — your `models.json` entry wins.

The one most people need: a local model served **without a vision
projector**. Mark it text-only and terva drops image attachments at the
request boundary (with a visible note) instead of letting the server
400 on every turn after a screenshot enters the transcript:

```json
{
  "providers": {
    "openai-compatible": {
      "models": [
        {
          "id": "qwen2.5-coder-32b",
          "baseUrl": "http://localhost:1234/v1",
          "capabilities": { "image-input": false }
        }
      ]
    }
  }
}
```

The legacy spelling `"input": ["text"]` / `"input": ["text","image"]`
means the same thing; an explicit `capabilities` key wins over it.
Capability tags follow the same precedence as everything else in
`models.json`: your entry beats the built-in catalog, which beats
live discovery (OpenRouter's modality data is folded in
automatically). In the `/model` picker, `◈` marks vision models and a
`:` token filters by capability — `:img` (also `:image`, `:images`,
`:vision`), `:reasoning` (also `:reason`, `:thinking`), and `:imggen`
(also `:imagegen`, `:image-gen`, `:image-out`, `:imageout`);
`terva --list-models` shows a `vision` column.

```json
{
  "providers": {
    "openai-compatible": {
      "models": [
        {
          "id": "my-local-model",
          "contextWindow": 32768,
          "maxTokens": 4096,
          "baseUrl": "http://localhost:1234/v1"
        }
      ]
    }
  }
}
```

## Credential resolution

For each request, terva checks credentials in this order:

1. Explicit CLI key, such as `--api-key`.
2. Provider-specific environment variables.
3. `$TERVA_HOME/auth.json`.
4. Custom provider credentials from `$TERVA_HOME/models.json`, when configured.

Bedrock then uses the AWS SDK credential chain for the actual request.

## The /login flow in detail

- **API key**: a small local web server starts on `127.0.0.1:<free-port>`, your browser opens a form, you pick a provider from the API-key provider list, paste the key, and terva saves it to `auth.json` if accepted. Providers with a lightweight model-list endpoint are probed before saving; provider backends that need extra project/account env vars are saved directly. The list is not quite every provider id: `openai-responses` and `ollama` are absent by design (see [API-key providers](#api-key-providers)).
- **Subscription**: use your Claude Pro/Max, ChatGPT Plus/Pro, Kimi Code, or GitHub Copilot subscription. DeepSeek and Google Gemini do **not** have a subscription login path. For those, use the API-key flow.
  - Anthropic and OpenAI pin the browser callback to fixed provider-specific ports (`localhost:53692` for Anthropic, `localhost:1455` for OpenAI) because those are the only ports their auth servers will redirect to.
  - Anthropic uses the Claude Code OAuth flow. Messages go to `api.anthropic.com` with a bearer token and the Claude Code identity headers.
  - OpenAI uses the Codex CLI OAuth flow. Messages go to `chatgpt.com/backend-api/codex/responses` with the `chatgpt-account-id` extracted from the returned id_token.
  - Kimi uses the Kimi Code device-code OAuth flow. terva opens the verification URL, polls until you approve it in the browser, then sends messages to `api.kimi.com/coding/v1` with the Kimi Code identity headers.
  - GitHub Copilot uses GitHub's device-code login flow. terva stores the GitHub access token and exchanges it for short-lived Copilot inference tokens on demand.

> **Note on subscription login.** The OAuth client IDs used are the ones published in Anthropic's Claude Code CLI, OpenAI's Codex CLI, Kimi Code CLI, and GitHub Copilot's device-code flow. Reusing them from a third-party tool may be against their terms of service and may be revoked at any time. Use it at your own risk; the API-key flow is the safe default.

### Token refresh

OAuth access tokens are short-lived (Anthropic ~8h, OpenAI ~30d; Kimi and GitHub Copilot also use refresh/exchange flows). terva refreshes or exchanges them automatically:

- At every credential lookup, terva checks the stored `expiry` and, if past it (with a 60s safety margin), hits the provider's `oauth/token` endpoint with the stored `refresh_token`, persists the new `access_token`, `refresh_token`, and `expiry` back to `auth.json`, and hands the fresh token to the client.
- The telegram bridge additionally refreshes once per turn so a bot that runs for days keeps working without manual intervention.
- If the refresh itself fails (the `refresh_token` was revoked, or the account was logged out everywhere), the error bubbles up to the caller: the TUI shows it in the status line, the bot replies with it in your DM. Run `/login` to get a fresh token pair.
