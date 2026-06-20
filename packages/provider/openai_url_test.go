package provider

import "testing"

// TestChatCompletionsURL pins the endpoint built for OpenAI-compatible
// providers. The regression that motivated it: Z.AI's coding-plan base
// carries a "/v4" version segment, so blindly appending
// "/v1/chat/completions" produced ".../paas/v4/v1/chat/completions"
// and a 404. Any base that already ends in a version segment must get
// "/chat/completions" appended directly.
func TestChatCompletionsURL(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
		want    string
	}{
		{
			name:    "bare host gets conventional /v1 path",
			baseURL: "https://api.openai.com",
			want:    "https://api.openai.com/v1/chat/completions",
		},
		{
			name:    "v1 base is not doubled",
			baseURL: "https://api.moonshot.ai/v1",
			want:    "https://api.moonshot.ai/v1/chat/completions",
		},
		{
			name:    "zai coding plan v4 base is not given a spurious /v1",
			baseURL: "https://api.z.ai/api/coding/paas/v4",
			want:    "https://api.z.ai/api/coding/paas/v4/chat/completions",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := chatCompletionsURL(tc.baseURL); got != tc.want {
				t.Errorf("chatCompletionsURL(%q) = %q, want %q", tc.baseURL, got, tc.want)
			}
		})
	}
}
