package auth

import (
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

func TestStoreAdditionalAPIKeyClear(t *testing.T) {
	store := NewStore(filepath.Join(testsupport.TempDir(t), "auth.json"))
	if err := store.SetAPIKey("groq", "gsk_test"); err != nil {
		t.Fatal(err)
	}
	creds, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := creds.Method("groq"); got != "apikey" {
		t.Fatalf("method before clear=%q", got)
	}
	if err := store.Clear("groq"); err != nil {
		t.Fatal(err)
	}
	creds, err = store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := creds.Method("groq"); got != "" {
		t.Fatalf("method after clear=%q", got)
	}
	if len(creds.AdditionalAPIKeyCreds) != 0 {
		t.Fatalf("additional creds not cleared: %+v", creds.AdditionalAPIKeyCreds)
	}
}
