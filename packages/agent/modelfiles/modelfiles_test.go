package modelfiles

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// A missing file is an empty result, not an error, for every reader: a fresh
// TERVA_HOME has neither file.
func TestMissingFilesReadAsEmpty(t *testing.T) {
	dir := testsupport.TempDir(t)
	if c, err := LoadCache(filepath.Join(dir, "cache.json")); err != nil || len(c.Models) != 0 {
		t.Errorf("LoadCache = %+v, %v", c, err)
	}
	if o, w := LoadUserModelsWithWarnings(filepath.Join(dir, "models.json")); o != nil || w != nil {
		t.Errorf("LoadUserModelsWithWarnings = %v, %v", o, w)
	}
	f, err := ReadUserModelsFile(filepath.Join(dir, "models.json"))
	if err != nil || f.Providers == nil {
		t.Errorf("ReadUserModelsFile = %+v, %v; want a usable empty file", f, err)
	}
	if _, ok, err := FindUserModel(filepath.Join(dir, "models.json"), "openai", "x"); ok || err != nil {
		t.Errorf("FindUserModel = %v, %v", ok, err)
	}
}

// A malformed models.json is refused with its path in the error, so an edit
// never overwrites what it could not read, and the operator knows which file.
func TestAMalformedModelsFileIsRefusedByPath(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "models.json")
	if err := os.WriteFile(path, []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadUserModelsFile(path); err == nil || !strings.Contains(err.Error(), "parse "+path+": ") {
		t.Errorf("ReadUserModelsFile error = %v, want it to name %s", err, path)
	}
	if err := UpsertUserModel(path, "openai", provider.UserModel{ID: "m"}); err == nil {
		t.Error("UpsertUserModel rewrote a file it could not parse")
	}
	if b, _ := os.ReadFile(path); string(b) != "{nope" {
		t.Errorf("the malformed file changed to %q", b)
	}
}

// Upsert, find, and remove round-trip through the file. The writes leave no
// temporary file behind, create the directory, and a remove that finds
// nothing writes nothing.
func TestUserModelEditsRoundTripThroughTheFile(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "nested", "models.json")
	if err := UpsertUserModel(path, "openai", provider.UserModel{ID: "m", Name: "Mine"}); err != nil {
		t.Fatal(err)
	}
	if um, ok, err := FindUserModel(path, "openai", "m"); err != nil || !ok || um.Name != "Mine" {
		t.Fatalf("FindUserModel = %+v, %v, %v", um, ok, err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("the temporary file was left behind: %v", err)
	}
	if b, _ := os.ReadFile(path); !strings.HasSuffix(string(b), "}\n") {
		t.Errorf("models.json does not end in a newline: %q", b)
	}

	// Compact JSON, which any rewrite would re-indent, so unchanged bytes
	// prove nothing was written.
	compact := `{"providers":{"openai":{"models":[{"id":"m","name":"Mine"}]}}}`
	if err := os.WriteFile(path, []byte(compact), 0o600); err != nil {
		t.Fatal(err)
	}
	if removed, err := RemoveUserModel(path, "openai", "absent"); removed || err != nil {
		t.Fatalf("RemoveUserModel(absent) = %v, %v", removed, err)
	}
	if b, _ := os.ReadFile(path); string(b) != compact {
		t.Errorf("a remove that found nothing rewrote the file to %s", b)
	}

	if removed, err := RemoveUserModel(path, "openai", "m"); !removed || err != nil {
		t.Fatalf("RemoveUserModel = %v, %v", removed, err)
	}
	if b, _ := os.ReadFile(path); strings.Contains(string(b), "openai") {
		t.Errorf("the emptied provider block was not pruned: %s", b)
	}
}

// The cache round-trips, and a model read back without a source is marked as
// coming from the cache.
func TestTheCacheRoundTrips(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "cache", "models.json")
	in := provider.ModelCache{Version: provider.ModelCacheVersion, Models: []provider.Model{{Provider: "openai", ID: "m"}}}
	if err := SaveCache(path, in); err != nil {
		t.Fatal(err)
	}
	out, err := LoadCache(path)
	if err != nil || len(out.Models) != 1 || out.Models[0].ID != "m" || out.Models[0].Source != "cache" {
		t.Fatalf("LoadCache = %+v, %v", out, err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("the temporary file was left behind: %v", err)
	}
}
