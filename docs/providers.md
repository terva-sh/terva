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
are at a browser on that machine, but it binds to loopback on a random
port, so it is unreachable from anywhere else. The paste box is the
headless path.

**OpenAI Compatible** needs a base URL and a default model id as well, so
`/login` gives it a small form instead of a single box: base URL, default
model id, an optional API key (most local servers ignore it), and an
optional default context window. Tab and shift-tab move between the fields,
enter submits. The endpoint is probed before it is stored.

**Subscription (OAuth).** The provider's callback URL is pinned to
`localhost` by the provider's own client registration, so terva cannot move
it to a reachable address, so the browser on your laptop will fail to load it.
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

`/usage` shows where you stand against a subscription's usage windows, the
5-hour and weekly budgets with how much is consumed and when each resets, plus
any pay-as-you-go credits. The status bar also shows a compact `weekly 88%` hint
for the busiest window once it crosses 80%, colored yellow (≥80) then red (≥90);
below that it stays out of the way.

This works wherever the provider puts usage data on the wire terva already talks
to, or hangs it off a cheap endpoint beside it. Today:

- **OpenAI Codex** returns its subscription window state as response headers on
  every request, so `/usage` is accurate, costs no extra calls, and refreshes
  each turn.
- **Every OpenAI-shaped provider** (openai, groq, xai, ollama, the compatibles)
  reports whatever `x-ratelimit-*` windows its responses carry, on the same free
  ride.
- **Anthropic** returns its subscription windows (5h, weekly, overage) as
  `anthropic-ratelimit-unified-*` headers on every request, read the same way.
- **A gateway in front of subscription credentials** (CLIProxyAPI, LiteLLM, a
  corporate router) that forwards the upstream vendor's headers untouched lights
  up too, on either wire. Both `openai-compatible` and `anthropic-compatible`
  read all three header families (`x-codex-*`, `anthropic-ratelimit-unified-*`
  and `x-ratelimit-*`), whichever the answering vendor sent, so a Codex-backed
  model served over Chat Completions still shows its weekly window. The meters
  describe whichever account served that turn; a gateway rotating several
  credentials shows them in turn, with no indication which. Cache accounting
  follows the same rule: a translated Claude response that reports its cache
  write as `prompt_tokens_details.cache_write_tokens` (or
  `cached_creation_tokens`) is priced at the model's cache-write rate, and the
  written tokens come off the uncached input count.
- **OpenRouter** and **DeepSeek** ship no window headers but do have a balance
  endpoint, so terva polls it lazily (cached, off the hot path) and renders the
  answer as pay-as-you-go credits: OpenRouter's key limit/remaining plus lifetime
  spend, DeepSeek's account balance.

Providers that don't expose usage data show `<provider> doesn't report usage
limits` in `/usage`, and no status-bar hint. This includes OpenCode Go for now:
it has no usage/balance endpoint yet ([anomalyco/opencode#16017](https://github.com/anomalyco/opencode/issues/16017)).
The mechanism is a generic `provider.UsageReporter` capability: any provider
lights up `/usage` automatically once its client implements it, with no harness
changes. See `docs/plans/archive/usage-windows.md`.

### Client identity (`providers.<id>.client_identity`)

By default terva names itself on every request. On the Codex subscription wire
that means `originator: terva` and a `terva (<os> <arch>)` user-agent.

You can make terva present OpenAI's own Codex CLI instead. This is off by
default, and it is a user-layer setting only.

The easiest route is `/settings`, under **Provider**, as **Codex client
identity**. The row is there whatever provider the session runs on, so you can
set what your next Codex session sends without starting one first. It governs
`openai-codex` alone, and the row's note says so when you are on another
provider. Changing it writes your user config and applies to new sessions, not
to the one you are in.

The same setting by hand:

```json
{
  "providers": {
    "openai-codex": { "client_identity": "native" }
  }
}
```

With `native`, all three Codex request paths (the responses stream, the
server-side `/compact` call, and the `/wham` account endpoints) send
`originator: codex_cli_rs` and a `codex_cli_rs/<version>` user-agent. The
version comes from `codex --version` on this machine, with a compiled baseline
as its floor. terva runs that probe in a goroutine only when the setting is on.
The first call returns the baseline immediately while the worker reads the cache.
Later calls use the cached version, including during a refresh.

Claude Code version detection for Anthropic OAuth uses the same background cache.
Each CLI has a cache file under `$TERVA_HOME/cli-versions/` and a ten-minute TTL.
Processes share an OS lock and check the TTL again after they acquire it, so
concurrent launches do not repeat the scan. Failed checks retain the last
successful version and count toward the TTL. Without a usable cache directory,
terva keeps the baseline and retries cache access after ten minutes.

Read this before you switch it on.

**It is impersonation.** A third party is told a different client is calling.
`/status` grows an `identity` row naming every provider you have switched, and
that row is there so the choice cannot sit unread in a config file.

**It has no measured benefit.** terva's own header decomposition against
`chatgpt.com/backend-api/codex/responses` scored `originator` and `user-agent` at
1/4 each against a 0/4 baseline. The header that carried the whole effect was
`session-id`, at 25/30 against 3/18, and terva has sent that since v0.131.5 with
no impersonation at all. The setting exists because the cache cliff costs real
money and an operator asked to be able to try the remaining variable, not
because it is known to work. `docs/reviews/2026-08-04-gpt56-post-compaction-cache-collapse.md`
holds the measurements.

A project's `.terva/config.json` cannot set this. The field is absent from the
project config schema, so a cloned repository cannot decide how terva identifies
itself.

`client_identity` takes `""` (the default) or `"native"`. Any other value keeps
the default. Only `openai-codex` implements it today.

### Activation continuation (`providers.<id>.activation_continuation`)

Activation continuation is an engine feature: when the agent activates a tool
group and then finishes its reply, terva continues the turn by itself so the
new tools are live. It is on by default wherever lazy tools are on. This key
scopes that decision to one provider.

**The default does not change.** A provider you have never configured inherits
the global `engine_features.activation_continuation` exactly as before, and
setting the key on one provider does not touch any other.

The easiest route is `/settings`, under **Provider**, as **Activation
continuation for this provider**. The row configures whichever provider the
current session runs on, and its note names that provider. A change applies
live to sessions on that provider. A session mid-turn picks it up on its next
message, because the loop reads the setting once per turn.

The same setting by hand:

```json
{
  "providers": {
    "openai-codex": { "activation_continuation": "off" }
  }
}
```

`activation_continuation` takes `""` (the default, which inherits the global),
`"on"`, or `"off"`. Any other value inherits, so a typo in a hand-edited config
falls back to the default rather than to the quieter behavior.

**What turning it off buys, and what it does not.** Activating a tool group
invalidates the cached prompt from the tools rung down, so the next request
re-reads that prompt at full price. Continuation makes that request happen
immediately. Off, it waits for your next message, and if you change direction
instead of continuing, it never happens on that prompt at all. The saving is at
most one request per activation.

It does **not** fix a sustained cache collapse, and it should not be reached for
as a remedy for one. A 56-session sweep found that `activate_tools` marks the
affected sessions rather than causing them: 30 sessions on providers that report
cache writes took 17 activations and produced zero collapses, and 31 of 49
collapse runs have no activation to account for them. Section 15 of
`docs/reviews/2026-08-04-gpt56-post-compaction-cache-collapse.md` holds the
measurements. The setting exists because that one request is expensive on some
wires and an operator asked to be able to defer it.

A project's `.terva/config.json` cannot set this, for the same reason it cannot
set `client_identity`: the field is absent from the project config schema, so a
cloned repository cannot decide how a session runs.

## API-key providers

These providers can use environment variables. Simple API-key providers can
also be configured through `/login`. Providers that require extra cloud setup
show instructions and should be configured with environment variables. Two rows
below are *not* offered in the `/login` picker and are reached another way:
`openai-responses` (opt in with `--provider openai-responses`; it reuses your
OpenAI key from the environment) and `ollama` (a local server, with no key at all).

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
| Anthropic Compatible (local/custom) | — (use `/login` or `--base-url`) | `anthropic-compatible` |

Example:

```bash
export OPENROUTER_API_KEY=...
terva --provider openrouter
```

> **Google is API key only.** Google issues no OAuth tokens for consumer Gemini
> Advanced or Google One AI Premium subscriptions, so there is no "log in with
> your Google subscription" flow. Programmatic access needs either an AI Studio
> API key (the `google` provider) or a Vertex AI service-account credential
> (`google-vertex`, below). Google is therefore absent from the
> `/login subscription` list entirely: pick it under `/login` -> api key, which
> probes `/v1beta/models` once and stores the key under `google`.

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
model, so name the one you pulled:

```bash
ollama pull qwen3.5:4b
terva --provider ollama --model qwen3.5:4b
```

A remote or authenticated instance takes the usual flags
(`--base-url https://my-server.example/v1 --api-key <token>`), and `--insecure`
is permitted here for a self-signed endpoint. Pin models and context windows in
`models.json` under the `ollama` provider; see
[models.md](models.md#local-models-with-ollama).

## OpenAI-compatible endpoints (local and custom servers)

The `openai-compatible` provider points terva at any server that speaks the
OpenAI chat-completions protocol: LM Studio, vLLM, llama.cpp's server, LocalAI,
Ollama's `/v1` endpoint, or a hosted gateway. Unlike the `ollama` provider it
has no fixed base URL and is configured through `/login`.

### Logging in

Run `/login` and choose **OpenAI Compatible (local/custom)**, in the terminal or
in the web panel's **Providers** pane, which serve the same form. terva collects:

- **name**: *optional, and the one that matters if you run more than one server.*
  Name it and the endpoint becomes **its own provider**, kept alongside your
  others; leave it empty and it goes into the single shared `openai-compatible`
  slot, **replacing whatever was there**. See [Several at once](#several-endpoints-at-once).
- **base url**: where requests go, e.g. `http://localhost:1234/v1`. terva lists
  the endpoint's models once to confirm it's reachable.
- **default model id**: the model selected after login, e.g. `qwen2.5-coder`.
  Required for the shared slot; **not needed for a named endpoint**, which
  discovers the models the server serves.
- **default context window**: applied to discovered models the server doesn't
  describe a size for (optional; leave blank if unsure).

Tab and shift-tab move between fields; enter submits. The same fields are
served as a browser form on the terva host, if you prefer to fill them in
there, but that page is loopback-only, so on a remote host the TUI form is
the one that works.

These are stored in `$TERVA_HOME/auth.json` under `openai-compatible`. The API key
is optional, and most local servers ignore it.

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

### Several endpoints at once

**Name the endpoint when you log in.** A named endpoint is registered as its own
provider under that name, with its own `/v1/models` discovery and its own rows in
`/model`, and it does not touch any other. This is how you run a small model on
one machine for titles and a large one on another for the work:

```
/login → OpenAI Compatible → name: workshop-3090   base url: http://3090.box:8000/v1
/login → OpenAI Compatible → name: little-box      base url: http://mini.box:1234/v1
```

Named endpoints are recorded in `$TERVA_HOME/config.json` under `endpoints`, so you
can also write them by hand:

```json
{
  "endpoints": {
    "workshop-3090": { "baseUrl": "http://3090.box:8000/v1", "contextWindow": 131072 },
    "little-box":    { "baseUrl": "http://mini.box:1234/v1",  "apiKeyEnv": "LITTLE_BOX_KEY" }
  }
}
```

Each entry's `api` field picks the protocol it speaks: `"openai"` (the default,
and what every entry written before this field existed means) or `"anthropic"`.
See [Anthropic-compatible endpoints](#anthropic-compatible-endpoints) for the
latter's extra settings.

Keys are **never** stored in `config.json`. Give a key at login and it goes into
`auth.json` under the endpoint's name; or point `apiKeyEnv` at an environment
variable. Most local servers want no key at all.

Endpoints are a **user-config concept**: a project's `.terva/config.json` cannot
define one, because it could otherwise point the agent at a server of the
project's choosing. Adding or editing an endpoint re-runs discovery on the next
launch.

The web panel's **Providers** pane lists your named endpoints and can forget one
(its definition *and* its key; a sign-out would only clear the key and leave the
server configured).

**Leaving the name empty** puts the endpoint in the single shared
`openai-compatible` slot in `auth.json`, which holds exactly one: logging in
without a name a second time **overwrites the first**. That slot is fine for a
single server and is what `--base-url` and the CLI flags below drive.

#### The older route: per-model baseUrl in models.json

Predates named endpoints and still works. Each model entry pins its own `baseUrl`,
so many endpoints coexist in `/model`, but you must hand-list every model, because
there is no discovery:

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

Don't hand-write that from scratch. Run `terva models init` to drop a starter
`models.json` (with two example endpoints) at `$TERVA_HOME/models.json`, edit the
ids/URLs, then run `terva --list-models` to confirm the entries load (they show
`source: user`). `terva models init` refuses to overwrite an existing file unless
you pass `--force`. A per-launch `--base-url` override also works for a one-off
without touching the stored login.

**Prefer named endpoints when a server should list its own models**: name it at
`/login` and there is nothing to hand-list. The `models.json` pattern above shows
only the entries you write out yourself. The two coexist, so there is no forced
migration, and nothing breaks if you keep both. The difference is that a named
endpoint **discovers** its models where `models.json` only shows what you
hand-listed.

To convert an existing multi-`baseUrl` `models.json`, lift and trim:

1. `terva models endpoints` scans it for distinct `openai-compatible` base URLs
   and prints a ready-to-paste `endpoints` block, one named endpoint per URL,
   naming each from its host, plus the `models.json` entries that become
   redundant once discovery covers them. It changes nothing.
2. `terva models endpoints --apply` writes those endpoints into `config.json`.
   Additive: it never clobbers an endpoint you already defined, and never
   touches `models.json`.
3. Relaunch so each endpoint discovers its models, then **trim** `models.json`
   by hand. Delete the entries discovery now covers, but keep any you rely on
   for exact `contextWindow` / `maxTokens` / capability overrides, which
   discovery cannot infer. Anything you keep still wins over the catalog and the
   endpoint's `/v1/models`.

If you set a key via `apiKeyEnv` for a migrated endpoint, point it at the
variable holding that endpoint's token. `models.json` had no key field, so a
previously keyless local server needs nothing.

## Anthropic-compatible endpoints

The `anthropic-compatible` provider is the twin of `openai-compatible` for the
other wire terva speaks: point it at any server that implements Anthropic's
**Messages API**, whether LiteLLM in `anthropic` mode, a Bedrock/Vertex shim, a
corporate gateway in front of Claude, or a local router.

Everything above applies unchanged: run `/login`, choose **Anthropic Compatible
(local/custom)**, and give it a base URL and a default model id. **Name it** and
it becomes its own provider kept alongside your others; leave the name empty and
it goes into the single shared `anthropic-compatible` slot, replacing whatever
was there. Models are discovered from `GET {base-url}/v1/models` on every launch,
and the key is optional.

> **Base URL:** give the server's root, **not** its `/v1`. terva appends
> `/v1/messages` itself, so `http://localhost:4000/v1` becomes
> `http://localhost:4000/v1/v1/messages`, a 404 from an otherwise correct
> gateway. (The OpenAI form is the opposite: it wants the `/v1`.)

### Wire settings

Four settings exist because a real class of Messages-compatible server fails
*every* turn without them, with an error that names none of them. All are
optional and all have working defaults, so fill one in only when your server
needs it.

| Setting | Default | When you need it |
|---|---|---|
| `anthropic-version` | `2023-06-01` | The gateway is pinned to a different API version. |
| `anthropic-beta` | *(none)* | A feature has to be opted into, e.g. `context-1m-2025-08-07`. Comma-separated. |
| Auth style | `x-api-key` | Set `bearer` when the server wants `Authorization: Bearer` instead, which is common on gateways that present an OpenAI-style front door. |
| Prompt caching | `on` | Turn it **off** for a server that validates the request body strictly and rejects Anthropic's `cache_control` field. |

For a named endpoint these live in `config.json` beside the rest of its
definition:

```json
{
  "endpoints": {
    "claude-gw": {
      "baseUrl": "http://gw.box:4000",
      "api": "anthropic",
      "contextWindow": 200000,
      "anthropicVersion": "2023-06-01",
      "anthropicBeta": "context-1m-2025-08-07",
      "authStyle": "bearer",
      "disableCaching": false,
      "apiKeyEnv": "CLAUDE_GW_KEY"
    }
  }
}
```

For the shared slot they are captured at `/login` and stored in `auth.json`
alongside the base URL and model.

### From the CLI

```bash
terva --provider anthropic-compatible \
  --model claude-sonnet-4.5 \
  --base-url http://localhost:4000 \
  --api-key optional-token   # omit for keyless local servers
```

`--insecure` is permitted here, as for `openai-compatible` and `ollama`, for a
self-signed endpoint with an explicit `--base-url`.

### Sizing

Unlike Chat Completions, the Messages API **requires** `max_tokens` on every
request. A discovered model therefore gets a default response cap rather than
letting the server choose, and models the baked catalog has never heard of get
your configured default context window. Pin exact values per model in
`models.json` under the endpoint's own provider id:

```json
{
  "providers": {
    "claude-gw": {
      "models": [
        { "id": "claude-sonnet-4.5", "contextWindow": 200000, "maxTokens": 64000 }
      ]
    }
  }
}
```

## Context window and max response tokens

What `contextWindow` and `maxTokens` control, and how to pin them per model, are
in [models.md](models.md#where-context-windows-come-from). They are properties of
the model rather than of the endpoint, which is the only reason they are not
here.

One thing is this page's: for the `openai-compatible` provider the login form
sets a *default* context window only, applied to discovered models the server
does not describe a size for. It cannot set `maxTokens` at all.

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

`$TERVA_HOME/models.json` adds private models, deployment aliases, local servers,
or gateways that are not in the built-in catalog, and its entries override
built-in ones with the same provider and model id.

**The field reference lives in [models.md](models.md#custom-models)**, because
every field describes a model rather than a way of reaching one. One tag is the
exception, and it is below.

### Capability tags

`capabilities` marks what a model can do, when terva can't know on its
own. Keys it understands today: `image-input` (vision, defaulting to
**true** when unset), `image-output` (image generation, defaulting to
false; consumed today by the `/model` picker's capability filter), and
`reasoning` (an alias for the top-level `reasoning` field). Unknown keys
load with a warning so a file written for a newer terva still works here.

The **true** default applies to the built-in catalog (its models are
known vision-capable). Models found by **discovery** are different: a
standard `/v1/models` list carries no modality data, so terva infers
`image-input` from the model id. Known vision families (Claude 3+,
GPT‑4o/5, Gemini, and local names like `llava`/`*-vl`/`pixtral`) keep
vision; anything unrecognized is treated as **text-only**. That's
deliberately conservative: handing an image to a text-only model is a
hard `400` ("does not support image inputs", common on gateways like
opencode‑go), while a vision model given text-only input just proceeds.
If discovery guesses wrong for a model you know takes images, assert it
explicitly (below); your `models.json` entry wins.

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
`:` token filters by capability: `:img` (also `:image`, `:images`,
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

The local API-key form belongs to the provider and login attempt selected in
terva. Open the URL from the current `/login` dialog. A bookmarked form or a
bare `/apikey?provider=...` URL cannot start a new attempt. The server checks
the loopback host, browser origin, and a per-attempt token before it probes or
accepts a key. Canceling a login invalidates its browser form. Submitting a
key directly also retires browser alternatives for that provider.

The API-key server no longer accepts `/callback` requests. OAuth continues to
use the separate provider callback servers. Code that embeds the key-form
server can call `Server.BeginAPIKey` to create a form; `Manager.StartAPIKey`
does this for normal logins. Remote `CompleteAPIKey` remains available without
a loopback browser form.

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
