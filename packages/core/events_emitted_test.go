package core

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestEveryDeclaredEventIsEmitted pins the producer half of the event
// contract: every AgentEvent type declared in events.go must be constructed
// somewhere in this package's non-test code. EvError shipped without that —
// declared, wire-serialized, ACP-translated, and emitted nowhere for the
// fork's whole life — so its serializer branches were dead code that looked
// alive. (ctrlproto's TestServerHelloAdvertisesServeGatedFeatures pins the
// mirror-image failure for negotiated features: a consumer chain built to a
// producer that can never fire.)
//
// If this fails on a type you are ADDING: emit it, or don't declare it yet —
// vocabulary is not reserved in advance here. If it fails on a type you are
// REMOVING, mind the trap that nearly widened EvError's removal: the wire
// `type` namespace is SHARED with frames hosts synthesize directly as
// WireEvent literals (modes/json.go, sdk.go, and the workspace turn-failure
// broadcast all emit {type:"error"} with no core event behind it — that wire
// type and its TUI/web consumers are alive). Deleting a core struct retires
// only the in-process struct and its serializer branches, never the wire
// type itself; grep for `Type: "<name>"` literals before touching consumers.
func TestEveryDeclaredEventIsEmitted(t *testing.T) {
	src, err := os.ReadFile("events.go")
	if err != nil {
		t.Fatalf("read events.go: %v", err)
	}
	declRE := regexp.MustCompile(`func \((Ev\w+)\) Type\(\)`)
	var declared []string
	for _, m := range declRE.FindAllStringSubmatch(string(src), -1) {
		declared = append(declared, m[1])
	}
	// Guard the scan itself: if the Type() idiom changes, this test must not
	// pass vacuously on an empty declaration list.
	if len(declared) < 15 {
		t.Fatalf("declaration scan found only %d event types — declRE no longer matches events.go's Type() idiom", len(declared))
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	var sources []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		sources = append(sources, string(b))
	}

	// No exemptions today: every declared event has a real emission site.
	// If one ever needs deferring, list it here WITH the reason and a date —
	// an undated exemption is how the next EvError gets grandfathered in.
	exempt := map[string]string{}

	for _, name := range declared {
		if why, ok := exempt[name]; ok {
			t.Logf("exempt: %s (%s)", name, why)
			continue
		}
		// A construction is `EvX{`; a type-switch `case EvX:` and the
		// declaration `type EvX struct {` do not match, so serializer
		// branches cannot satisfy this.
		conRE := regexp.MustCompile(`\b` + name + `\{`)
		found := false
		for _, s := range sources {
			if conRE.MatchString(s) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s is declared in events.go but constructed nowhere in package core — a declared event nobody emits is dead wire vocabulary wearing live-looking consumers (see EvError, removed 2026-07-26)", name)
		}
	}
}
