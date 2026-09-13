package provider

import (
	"net/http"
	"testing"
)

func TestNewHTTPClientSecureByDefault(t *testing.T) {
	c := NewHTTPClient(false)
	if c.Transport != nil {
		t.Fatalf("secure client should use the default transport, got %T", c.Transport)
	}
}

func TestNewHTTPClientInsecureScopedNotGlobal(t *testing.T) {
	c := NewHTTPClient(true)
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("insecure client transport = %T, want *http.Transport", c.Transport)
	}
	if tr.TLSClientConfig == nil || !tr.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("insecure client did not set InsecureSkipVerify")
	}
	// The process-wide default transport must NOT be mutated — auth and
	// discovery keep verifying certificates.
	if dt, ok := http.DefaultTransport.(*http.Transport); ok {
		if dt.TLSClientConfig != nil && dt.TLSClientConfig.InsecureSkipVerify {
			t.Fatal("NewHTTPClient(true) leaked InsecureSkipVerify into http.DefaultTransport")
		}
	}
}

func TestWithHTTPClientScopesOpenAIClient(t *testing.T) {
	insecure := NewHTTPClient(true)
	c := NewOpenAI("key", "https://example.invalid")
	WithHTTPClient(c, insecure)
	oc, ok := c.(*openaiClient)
	if !ok {
		t.Fatalf("NewOpenAI returned %T, want *openaiClient", c)
	}
	if oc.http != insecure {
		t.Fatal("WithHTTPClient did not swap the openai client's http client")
	}
}

// 🪤 WithHTTPClient handled only the OpenAI client while --insecure was gated
// to openai-compatible/ollama, and the two facts had to stay in step. Extending
// the gate to anthropic-compatible without extending this would accept the flag,
// report nothing, and leave the self-signed endpoint failing its TLS handshake —
// a setting that looks applied and does nothing.
func TestWithHTTPClientScopesAnthropicClient(t *testing.T) {
	insecure := NewHTTPClient(true)
	c := NewAnthropicCompatible("key", "https://example.invalid", AnthropicCompatOptions{})
	WithHTTPClient(c, insecure)
	ac, ok := c.(*anthropicClient)
	if !ok {
		t.Fatalf("NewAnthropicCompatible returned %T, want *anthropicClient", c)
	}
	if ac.http != insecure {
		t.Fatal("WithHTTPClient did not swap the anthropic client's http client")
	}
}

// The swap must reach through a wrapper, which is how kimi (a pollingUsageClient
// around an anthropicClient) is shaped — and how a named Anthropic endpoint
// would be if it ever grew a usage poll.
func TestWithHTTPClientReachesAnthropicThroughAWrapper(t *testing.T) {
	insecure := NewHTTPClient(true)
	c := NewKimiCodingWithHeaders("key", "https://example.invalid", nil)
	WithHTTPClient(c, insecure)
	inner := innerAnthropic(c)
	if inner == nil {
		t.Fatal("innerAnthropic could not see through the usage wrapper")
	}
	if inner.http != insecure {
		t.Fatal("WithHTTPClient did not reach the wrapped anthropic client")
	}
}

func TestWithHTTPClientNilIsNoop(t *testing.T) {
	c := NewOpenAI("key", "")
	if got := WithHTTPClient(c, nil); got != c {
		t.Fatal("WithHTTPClient(nil) should return the client unchanged")
	}
}
