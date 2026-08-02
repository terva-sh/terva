package tools

import "strings"

// modelLineages maps a keyword found in a model id to the weight family it
// belongs to. Two models of the same lineage are the SAME judgment wearing
// two hats — the thing a cross-provider panel exists to avoid — and they
// reach a panel through providers that look entirely different: Anthropic and
// GitHub Copilot both serve Claude, Bedrock serves it a third time under
// `anthropic.claude-…`, and a gateway serves whatever it likes.
//
// Deliberately COARSE. Merging two families that are really distinct costs a
// seat and, at worst, refuses to seat a panel — the operator sees why and
// names the providers themselves. Splitting two families that are really the
// same seats a correlated panel under a label that promises decorrelation,
// and nothing downstream can tell. Only one of those failures is safe, so
// when a keyword is arguable this table takes the merge.
var modelLineages = []struct{ keyword, lineage string }{
	{"claude", "claude"},
	{"gemini", "gemini"},
	{"gemma", "gemma"},
	{"deepseek", "deepseek"},
	{"kimi", "kimi"},
	{"k2", "kimi"},
	{"k3", "kimi"},
	{"moonshot", "kimi"},
	{"glm", "glm"},
	{"qwen", "qwen"},
	{"llama", "llama"},
	{"grok", "grok"},
	{"minimax", "minimax"},
	{"mistral", "mistral"},
	{"nova", "nova"},
	{"mimo", "mimo"},
	{"granite", "granite"},
	{"nemotron", "nemotron"},
	// Last: "gpt" is a substring of gpt-oss, which is not OpenAI's flagship
	// lineage — but see the doc above, a merge is the safe direction and
	// every earlier keyword gets first refusal.
	{"gpt", "gpt"},
	{"o1", "gpt"},
	{"o3", "gpt"},
	{"o4", "gpt"},
}

// ModelLineage names the weight family a model id belongs to, for callers
// that must not seat the same judgment twice. An id matching nothing is its
// own lineage — an unknown model is assumed independent, which is the
// permissive answer, but it is also the only honest one: terva knows nothing
// about it to say otherwise.
func ModelLineage(modelID string) string {
	id := strings.ToLower(strings.TrimSpace(modelID))
	if id == "" {
		return ""
	}
	for _, l := range modelLineages {
		if strings.Contains(id, l.keyword) {
			return l.lineage
		}
	}
	return id
}
