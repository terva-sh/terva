package session

import (
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/core/transcripttest"
	"terva.sh/terva/packages/testsupport"
)

// The JSONL session passes the same behavior suite as the in-memory store, read
// back the way a host resumes: a lock-free replay of the file while the writer
// still holds it open.
func TestSessionStoreBehaves(t *testing.T) {
	transcripttest.Run(t, func(t *testing.T) transcripttest.Store {
		path := filepath.Join(testsupport.TempDir(t), "session.jsonl")
		sess, err := NewSessionAtPath(path, "/work", "scripted", "scripted", "test")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = sess.Close() })
		return transcripttest.Store{
			TranscriptStore: NewStore(sess),
			Reload:          func() (core.Transcript, error) { return ReadSessionTranscript(path) },
		}
	})
}
