package ext

import (
	"reflect"
	"testing"

	"terva.sh/terva/packages/agent/extproto"
)

// The other tool options are one-line boolean setters and carry no test.
// WithDisplay converts between two structs on its way to the wire, which is
// the kind of code that silently drops a field when one of them grows.
func TestWithDisplayReachesTheWire(t *testing.T) {
	var td toolDef
	WithDisplay(ToolDisplay{
		Subject: "{city}",
		Body:    BodyTable,
		Redact:  []string{"api_key"},
	})(&td)

	got := wireDisplay(td.display)
	want := &extproto.ToolDisplay{Subject: "{city}", Body: "table", Redact: []string{"api_key"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("wireDisplay = %+v, want %+v", got, want)
	}
}

// A tool that declares nothing must send nothing: a non-nil empty hint would
// put "display":{} on every registration and mean the same as its absence.
func TestWithoutDisplayNothingGoesOnTheWire(t *testing.T) {
	var td toolDef
	if wireDisplay(td.display) != nil {
		t.Error("a tool with no WithDisplay option produced a display frame")
	}
}

// The Body* constants are the SDK's copy of the host's closed set. They are
// hand-mirrored (like the Authority* constants), so nothing but a test stops
// the two spellings drifting apart.
func TestBodyConstantsMatchTheHostSet(t *testing.T) {
	sdk := []string{BodyText, BodyJSON, BodyDiff, BodyTable}
	host := extproto.ToolDisplayBodies()
	if !reflect.DeepEqual(sdk, host) {
		t.Errorf("SDK Body* constants %v do not match extproto.ToolDisplayBodies() %v", sdk, host)
	}
	for _, b := range sdk {
		if !extproto.ValidToolDisplayBody(b) {
			t.Errorf("the host rejects the SDK constant %q", b)
		}
	}
}
