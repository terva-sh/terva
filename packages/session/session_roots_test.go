package session

import (
	"encoding/json"
	"os"
	"slices"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

func TestSessionRootsResumeAndPortability(t *testing.T) {
	root := testsupport.TempDir(t)
	s, err := NewSession(root, root, "fixture", "model", "test")
	if err != nil {
		t.Fatal(err)
	}
	peer := testsupport.TempDir(t)
	if err := s.SetAdditionalRoots([]string{peer}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendMessage(provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "fixture"}}}); err != nil {
		t.Fatal(err)
	}
	path := s.Path
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, _, err := OpenSession(path)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(reopened.Meta.AdditionalRoots, []string{peer}) {
		t.Fatal("root metadata did not round-trip")
	}
	_ = reopened.Close()
	rows := DescribeSessions(root, root)
	if len(rows) != 1 || !slices.Equal(rows[0].AdditionalRoots, []string{peer}) {
		t.Fatal("session list lost peer roots")
	}
	exported, err := ExportSession(path, testsupport.TempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	_, meta, err := ReadSessionMeta(exported)
	if err != nil || len(meta.AdditionalRoots) != 0 {
		t.Fatalf("export retained machine grants: %+v, %v", meta.AdditionalRoots, err)
	}
	// Import must discard grants even when the source did not use our exporter.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exported, data, 0600); err != nil {
		t.Fatal(err)
	}
	imported, err := ImportSession(exported, root, root, "test")
	if err != nil {
		t.Fatal(err)
	}
	_, meta, err = ReadSessionMeta(imported)
	if err != nil || len(meta.AdditionalRoots) != 0 {
		t.Fatalf("import granted source roots: %+v, %v", meta.AdditionalRoots, err)
	}
	legacy, _ := json.Marshal(SessionMeta{CWD: root})
	var decoded SessionMeta
	if err := json.Unmarshal(legacy, &decoded); err != nil || len(decoded.AdditionalRoots) != 0 {
		t.Fatal("legacy metadata acquired roots")
	}
}
