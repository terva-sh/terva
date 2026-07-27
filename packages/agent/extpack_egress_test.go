package agent

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A pack URL is an arbitrary string handed to the CLI, so the real fetcher
// (fetchPackURL, not the test-injectable fetchPackWith) must refuse
// non-public addresses — loopback here standing in for the whole class.
// fetchPackWith stays client-injectable for the happy-path tests; this
// pins that the production client is the guarded one.
func TestFetchPackURLRefusesPrivateAddresses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"schema":"terva-extension-pack/v1"}`)
	}))
	defer srv.Close()

	_, err := fetchPackURL(srv.URL)
	if err == nil {
		t.Fatal("a loopback pack URL must be refused by the egress guard")
	}
	if !strings.Contains(err.Error(), "egress blocked") {
		t.Fatalf("want an egress-blocked error, got: %v", err)
	}
}
