package secretstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	"terva.sh/terva/packages/secrets"
	"terva.sh/terva/packages/testsupport"
)

// The consequence, on the bytes that reach the disk.
//
// secrets.Codec.Enabled answering (false, nil) for an opening-only codec meant
// saveLocked skipped encryption entirely and privfs.WriteFile published the
// credential document in the clear — no error, nothing in any log. Asserting on
// Enabled alone would not have caught a save path that ignored it, which is the
// trap this review has now hit twice.
func TestASaveThroughAnOpeningOnlyCodecNeverLandsInPlaintext(t *testing.T) {
	dir := testsupport.TempDir(t)
	path := filepath.Join(dir, "secrets.json")

	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}

	s := New(path, secrets.NewOpeningCodec(id))
	err = s.Set("core:openai", "api_key", "sk-live-DO-NOT-LEAK")

	if err == nil {
		t.Error("Set succeeded through a codec that cannot seal")
	}

	b, rerr := os.ReadFile(path)
	if rerr != nil {
		return // nothing written at all is the best outcome
	}
	if !secrets.IsAgeFile(b) {
		t.Errorf("%s is on disk unencrypted after a save through an opening-only codec", path)
	}
	if strings.Contains(string(b), "sk-live-DO-NOT-LEAK") {
		t.Error("the credential is readable in the file on disk")
	}
}
