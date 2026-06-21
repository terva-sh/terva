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

func TestWithHTTPClientNilIsNoop(t *testing.T) {
	c := NewOpenAI("key", "")
	if got := WithHTTPClient(c, nil); got != c {
		t.Fatal("WithHTTPClient(nil) should return the client unchanged")
	}
}
