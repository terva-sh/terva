package config

import (
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// A Store serializes its writers with a mutex it OWNS, so handing every caller
// a freshly-minted one — which this used to do — gave each of them a private
// lock that ordered nothing against the others.
func TestAuthStoreForIsSharedPerPath(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	a, b := AuthStoreFor(), AuthStoreFor()
	if a != b {
		t.Error("AuthStoreFor handed out two Stores for one path; their mutexes order nothing between them")
	}
}

// Keyed by path, because TERVA_HOME is redirectable: two homes are two files
// and must not share a lock, and a test pointing TERVA_HOME somewhere new must
// not inherit the previous home's Store.
func TestAuthStoreForSeparatesHomes(t *testing.T) {
	homeA := testsupport.TempDir(t)
	homeB := testsupport.TempDir(t)

	t.Setenv("TERVA_HOME", homeA)
	a := AuthStoreFor()
	if err := a.SetAPIKey("kimi", "key-a"); err != nil {
		t.Fatal(err)
	}

	t.Setenv("TERVA_HOME", homeB)
	b := AuthStoreFor()
	if a == b {
		t.Fatal("two homes shared one Store")
	}
	if err := b.SetAPIKey("kimi", "key-b"); err != nil {
		t.Fatal(err)
	}

	// Each home kept its own credential.
	for _, tc := range []struct{ home, want string }{{homeA, "key-a"}, {homeB, "key-b"}} {
		t.Setenv("TERVA_HOME", tc.home)
		c, err := AuthStoreFor().Load()
		if err != nil {
			t.Fatal(err)
		}
		if c.Kimi.APIKey != tc.want {
			t.Errorf("%s: kimi key = %q, want %q", filepath.Base(tc.home), c.Kimi.APIKey, tc.want)
		}
	}
}
