package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// ServerCompactor is implemented by clients whose backend compacts the
// transcript itself, rather than leaving the client to summarize it.
//
// Probed as an optional interface through clientAs rather than declared as a
// ClientCapabilities bool, because the capability and the call are the same
// fact: a client that can answer the question implements the method, and there
// is no way to assert one without providing the other. clientAs walks the
// wrapper chain (renamedClient, pollingUsageClient), so a wrapped client still
// reports honestly — the failure mode a bare type assertion on the outer client
// would reintroduce.
type ServerCompactor interface {
	// CompactServerSide asks the backend to compact req.Messages and returns
	// the replacement transcript. The result is meant to REPLACE the messages
	// it was given, not to be appended to them.
	//
	// The Usage is the compaction call's own spend, and it is returned rather
	// than dropped because the request is transcript-sized and billed like any
	// other. A server-side compaction that reported nothing would read in an A/B
	// against the client summarizers as FREE, which is the one number that would
	// make the comparison say the opposite of the truth.
	CompactServerSide(ctx context.Context, req Request) ([]Message, Usage, error)
}

// ServerCompactorFor returns c's server-side compactor, looking through any
// wrapper layers.
func ServerCompactorFor(c Client) (ServerCompactor, bool) { return clientAs[ServerCompactor](c) }

// codexCompactRequest is the /compact body. It is deliberately NOT the full
// codexRequest: the endpoint takes no tools and does not stream, and
// prompt_cache_options / prompt_cache_retention are rejected outright by this
// backend (measured — see TestCodexSendsNoPromptCacheLifetimeParameters).
type codexCompactRequest struct {
	Model          string `json:"model"`
	Instructions   string `json:"instructions,omitempty"`
	Input          []any  `json:"input"`
	PromptCacheKey string `json:"prompt_cache_key,omitempty"`
}

// codexCompactResponse is the answer: a replacement transcript to send as the
// next request's input.
type codexCompactResponse struct {
	Object string              `json:"object"`
	Output []codexCompactItem  `json:"output"`
	Usage  *codexCompactUsage  `json:"usage,omitempty"`
	Detail json.RawMessage     `json:"detail,omitempty"`
	Error  *codexCompactErrObj `json:"error,omitempty"`
}

type codexCompactUsage struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	InputTokensDetails struct {
		CachedTokens     int `json:"cached_tokens"`
		CacheWriteTokens int `json:"cache_write_tokens"`
	} `json:"input_tokens_details"`
}

type codexCompactErrObj struct {
	Message string `json:"message"`
}

type codexCompactItem struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Role string `json:"role"`
	// EncryptedContent is set on the compaction_summary item.
	EncryptedContent string `json:"encrypted_content"`
	Content          []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// CompactServerSide runs POST <base>/compact.
//
// The base URL already ends in /responses (codexDefaultBaseURL), so the
// endpoint is that path plus /compact.
func (c *codexClient) CompactServerSide(ctx context.Context, req Request) ([]Message, Usage, error) {
	var usage Usage
	token, err := c.cred(ctx)
	if err != nil {
		return nil, usage, err
	}
	// Reuse the ordinary request builder for the input array so the compacted
	// transcript is serialized by exactly the code that serializes a normal
	// turn. A second serializer here could drift, and the whole point of
	// compaction is that what comes back is a prefix of later requests.
	full, err := c.buildRequest(req)
	if err != nil {
		return nil, usage, err
	}
	body, err := json.Marshal(codexCompactRequest{
		Model:          full.Model,
		Instructions:   full.Instructions,
		Input:          full.Input,
		PromptCacheKey: full.PromptCacheKey,
	})
	if err != nil {
		return nil, usage, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/compact", bytes.NewReader(body))
	if err != nil {
		return nil, usage, err
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("authorization", "Bearer "+token)
	httpReq.Header.Set("chatgpt-account-id", c.accountID)
	httpReq.Header.Set("openai-beta", "responses=experimental")
	httpReq.Header.Set("originator", "terva")
	httpReq.Header.Set("user-agent", codexUserAgent())
	// Same derivation as the streaming path, so a conversation presents one
	// session identity across both endpoints rather than two.
	if sid := codexSessionID(full.PromptCacheKey); sid != "" {
		httpReq.Header.Set("session-id", sid)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, usage, fmt.Errorf("openai-codex compact: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, usage, NewHTTPError("openai-codex", resp.StatusCode,
			resp.Header.Get("Retry-After"), errorBodySnippet(resp.Body))
	}

	var out codexCompactResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, usage, fmt.Errorf("openai-codex compact: decode: %w", err)
	}
	usage = c.compactUsage(req.Model, out.Usage)

	msgs := compactItemsToMessages(out.Output)
	// A compaction that returns nothing would silently erase the conversation
	// if the caller replaced the transcript with it. Refuse instead: the
	// caller's fallback (summarize client-side) is strictly better than an
	// empty history.
	if len(msgs) == 0 {
		return nil, usage, fmt.Errorf("openai-codex compact: backend returned no usable items (object %q)", out.Object)
	}
	// And a reply with no compaction_summary in it is the DANGEROUS empty: the
	// endpoint's whole contract is that user turns come back verbatim while the
	// assistant turns are replaced by one encrypted blob, so a result carrying
	// only the user turns is a transcript with its entire assistant half deleted
	// and nothing standing in for it. It would look like a successful, very
	// effective compaction — smaller, well-formed, no error — right up until the
	// model answered from a conversation it has no memory of having had.
	if !hasCompactionBlock(msgs) {
		return nil, usage, fmt.Errorf(
			"openai-codex compact: reply has %d items but no compaction_summary — "+
				"replacing the transcript with it would drop the assistant turns outright", len(msgs))
	}
	return msgs, usage, nil
}

// compactUsage prices the compaction call. It goes through ApplyCost like every
// other decoder, because only here is the model still in scope (see
// TestEveryProviderPricesThroughApplyCost).
func (c *codexClient) compactUsage(reqModel string, u *codexCompactUsage) Usage {
	if u == nil {
		return Usage{}
	}
	usage := Usage{
		InputTokens:      u.InputTokens,
		OutputTokens:     u.OutputTokens,
		CacheReadTokens:  u.InputTokensDetails.CachedTokens,
		CacheWriteTokens: u.InputTokensDetails.CacheWriteTokens,
	}
	// The three prompt fields are DISJOINT and priced separately, so every
	// detail broken out on the wire comes off the total — the same arithmetic,
	// and the same non-negative guard, as the streaming decoder.
	if n := usage.InputTokens - usage.CacheReadTokens; n >= 0 {
		usage.InputTokens = n
	}
	if n := usage.InputTokens - usage.CacheWriteTokens; n >= 0 {
		usage.InputTokens = n
	}
	model, _ := FindModel("openai-codex", reqModel)
	if model.ID == "" {
		model, _ = FindModel("openai", reqModel)
	}
	ApplyCost(model, &usage)
	return usage
}

func hasCompactionBlock(msgs []Message) bool {
	for _, m := range msgs {
		for _, c := range m.Content {
			if _, ok := c.(CompactionBlock); ok {
				return true
			}
		}
	}
	return false
}

// compactItemsToMessages turns the endpoint's output array into a transcript.
//
// Observed shape, from a live probe rather than the docs: user turns come back
// verbatim as type "message" with role "user" and input_text content, the
// assistant turns are DROPPED, and one trailing "compaction_summary" carries
// the encrypted blob standing in for everything removed.
func compactItemsToMessages(items []codexCompactItem) []Message {
	var msgs []Message
	for _, it := range items {
		switch it.Type {
		case "compaction_summary":
			if it.EncryptedContent == "" {
				continue
			}
			msgs = append(msgs, Message{
				Role: RoleAssistant,
				Content: []Content{CompactionBlock{
					ID: it.ID, Encrypted: it.EncryptedContent, Provider: "openai-codex",
				}},
			})
		case "message":
			var blocks []Content
			for _, ct := range it.Content {
				if ct.Text != "" {
					blocks = append(blocks, TextBlock{Text: ct.Text})
				}
			}
			if len(blocks) == 0 {
				continue
			}
			role := RoleUser
			if it.Role == string(RoleAssistant) {
				role = RoleAssistant
			}
			msgs = append(msgs, Message{Role: role, Content: blocks})
		}
	}
	return msgs
}
