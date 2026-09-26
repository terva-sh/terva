package core_test

import (
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/core/transcripttest"
)

func TestMemoryTranscriptStoreBehaves(t *testing.T) {
	transcripttest.Run(t, func(*testing.T) transcripttest.Store {
		s := core.NewMemoryTranscriptStore()
		return transcripttest.Store{
			TranscriptStore: s,
			Reload:          func() (core.Transcript, error) { return s.Transcript(), nil },
		}
	})
}
