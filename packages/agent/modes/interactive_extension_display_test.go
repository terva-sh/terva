package modes

import (
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/extproto"
)

func TestBackgroundExtensionDisplayRequestsRepaint(t *testing.T) {
	i := newNoteHost()
	i.dirty = make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		i.applyExtensionCommandResponse("example", extproto.CommandResponseFromExt{Action: "display", Display: "command completed"})
		close(done)
	}()
	select {
	case <-i.dirty:
	case <-time.After(time.Second):
		t.Fatal("background display did not request repaint")
	}
	<-done
	i.mu.Lock()
	defer i.mu.Unlock()
	if !strings.Contains(i.plainNotes(), "[example] command completed") {
		t.Fatalf("display note is missing: %q", i.plainNotes())
	}
}
