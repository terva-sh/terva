package provider

import (
	"fmt"
	"net/http"
	"runtime"
	"strings"

	"github.com/google/uuid"

	"terva.sh/terva/packages/buildinfo"
)

// The opencode.ai Zen gateway, which serves both the `opencode` and
// `opencode-go` providers. Both speak OpenAI chat-completions, so the
// transport is a plain openaiClient; what they need on top is an identity
// on every request.
//
// The gateway rejects a request that carries no session header outright:
//
//	http 400 {"type":"error","error":{"type":"MissingSessionID","message":
//	"Error from provider (Console Go): Request is missing x-opencode-session
//	and cannot be routed efficiently."}}
//
// https://opencode.ai/docs/go/#where-can-i-use-it asks a client for two
// things: a stable session id per conversation in `x-opencode-session`, so
// the gateway can route a conversation's turns to one backend and keep its
// prompt cache warm, and a user agent naming the client rather than the
// HTTP library. terva sent neither. Go's default `Go-http-client/<ver>` is
// exactly the generic name that page tells clients not to send.

// opencodeSessionNamespace seeds the UUIDv5 derivation in opencodeSessionID.
// It keeps the derived ids in a space of terva's own, so an opencode session
// id can never collide with an unrelated identifier that happens to share a
// cache key string. codexSessionID derives from that same key under its own
// namespace, and the two results never meet.
var opencodeSessionNamespace = uuid.MustParse("b2f0c7d4-3a91-5e68-9c02-7d1e4a8b5f36")

// opencodeSessionID derives a conversation's `x-opencode-session` value from
// its prompt-cache key.
//
// Derived rather than minted, for the same reason codexSessionID is: the
// header only helps if it is STABLE across a conversation's dispatches. A
// fresh id per request satisfies the gateway's 400 while defeating the
// routing and cache locality the header exists to buy, and it looks like the
// fix in a header dump. cacheKey (core.Agent.cacheID) already guarantees the
// two properties needed: stable for the conversation's life, unique across
// concurrent ones. It also prefers the session's persisted meta UUID, so a
// --resume rejoins its own route rather than stranding it.
//
// UUIDv5 because cacheKey is not always UUID-shaped: legacy transcripts key
// on a file basename, live-only agents on "live-<uuid>". Hashing normalizes
// every shape to one the gateway certainly accepts, without inventing
// per-request entropy.
func opencodeSessionID(cacheKey string) string {
	return uuid.NewSHA1(opencodeSessionNamespace, []byte(cacheKey)).String()
}

// opencodeUserAgent names terva and its build, e.g.
// "terva/0.132.9 (linux amd64)". The version is the part that earns its
// keep: opencode's docs page tracks per-client fixes by release ("update to
// v0.81.6 or later"), so a version in the user agent is what lets them tell
// a build that sends the session header from one that does not. An unstamped
// build (SDK embedder, test) reports buildinfo's own "0.0.0" placeholder.
func opencodeUserAgent() string {
	v := buildinfo.Get().Version
	if v == "" {
		v = "0.0.0"
	}
	return fmt.Sprintf("terva/%s (%s %s)", v, runtime.GOOS, runtime.GOARCH)
}

// newOpenCodeClient builds the openaiClient behind both Zen providers,
// wired with the session header and terva's user agent.
//
// fallbackSession backs a request that arrives with no PromptCacheKey. That
// case is rare, because the agent loop never leaves the key empty and even a
// live-only conversation carries a synthetic one, but an embedder building a
// Request by hand can. Here an empty id is not the option it is for Codex:
// no header means a 400 and no answer at all. One id minted per client is
// the closest available stand-in for a conversation, since a client is built
// per provider binding rather than per request, and it is still stable
// rather than per-request random.
func newOpenCodeClient(name, apiKey, baseURL, fallbackBaseURL string) Client {
	if baseURL == "" {
		baseURL = fallbackBaseURL
	}
	fallbackSession := opencodeSessionID("terva-client-" + uuid.NewString())
	return &openaiClient{
		cred:          StaticCredential(apiKey),
		baseURL:       strings.TrimRight(baseURL, "/"),
		name:          name,
		userAgent:     opencodeUserAgent(),
		sessionHeader: "x-opencode-session",
		sessionID: func(cacheKey string) string {
			if cacheKey == "" {
				return fallbackSession
			}
			return opencodeSessionID(cacheKey)
		},
		http: &http.Client{Timeout: 0},
	}
}

// NewOpenCode is the opencode.ai Zen endpoint. Mixed APIs upstream; this
// constructor wires the openai-completions flavor only. Models that need
// the anthropic-messages flavor under the same provider should be built
// with NewAnthropicCompat against the same base URL, which would need the
// session header wired there too. No current model requires that.
func NewOpenCode(apiKey, baseURL string) Client {
	return newOpenCodeClient("opencode", apiKey, baseURL, "https://opencode.ai/zen/v1")
}

// NewOpenCodeGo is the opencode-go variant.
//
// Usage windows (/usage): the OpenCode Go plan has no usage/balance
// endpoint yet, and the Zen gateway does not return subscription-window
// headers, so this client implements no UsageReporter and /usage shows
// "doesn't report usage limits" for it. When OpenCode ships the endpoint
// (anomalyco/opencode#16017 — rolling/weekly/monthly windows), light it
// up by wrapping this client in a UsageReporter that fetches it; the
// dialog and status hint then work with no further changes.
func NewOpenCodeGo(apiKey, baseURL string) Client {
	return newOpenCodeClient("opencode-go", apiKey, baseURL, "https://opencode.ai/zen/go/v1")
}
