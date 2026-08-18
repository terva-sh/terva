package acp

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// translateEvent switches on core.AgentEvent with no default arm. A type switch
// with no default is silent about what it does not handle: a core event added
// tomorrow produces no ACP notification, no error, and no compile failure — the
// editor on the other end simply never hears about it, and the first report is
// a user saying nothing happened.
//
// This census reads both sides out of source. core's event types come from
// packages/core/events.go, the handled set from translateEvent's case labels.
// Every type must be in one of two places: handled, or listed below with the
// reason it is deliberately not.
//
// Listing a type here is a decision, not a dismissal. The point is that adding
// an event to core forces someone to make that decision once, instead of the
// event quietly going nowhere.
var acpUntranslated = map[string]string{
	// Terminators and turn structure. The prompt handler resolves session/prompt
	// with a stopReason off EvDone informed by EvTurnEnd (§11), so translating
	// them here as well would double-report the end of a turn.
	"EvDone":      "the prompt handler treats it as the turn terminator (§11)",
	"EvTurnEnd":   "informs the prompt handler's stopReason; not a session update",
	"EvTurnStart": "turn framing is implicit in ACP's prompt request/response",

	// Composing-call deltas. ACP carries a completed tool call; streaming the
	// argument assembly is polish, and emitToolCall already reports the call.
	"EvToolUseStart": "composing-call delta; the completed call is emitted by EvToolCall",
	"EvToolUseArgs":  "composing-call delta; arguments arrive with EvToolCall",
	"EvToolUseEnd":   "composing-call delta; completion arrives with EvToolResult",

	// Echoes of input the ACP client already has: it sent the prompt, so
	// re-emitting it as an agent message would duplicate it in the editor.
	"EvUserMessage":          "the client sent this text; echoing it would duplicate the turn",
	"EvUserMessageWithdrawn": "withdrawal is resolved client-side; no ACP update exists for it",
	"EvAssistantStart":       "start-of-message framing; the first EvTextDelta is the observable start",
	"EvAssistantMessage":     "the assembled message; already streamed as EvTextDelta chunks",

	// Not yet translated, and tracked as such. Each is a real gap rather than a
	// deliberate omission — an ACP client currently sees nothing for any of
	// them. They are listed so this census can do its job (catch NEW events)
	// without silently blessing them; removing an entry by translating it is
	// the intended direction of travel.
	//
	// EvReasoningDelta and EvUsage came off this list by being translated —
	// which is the census working as designed: it named them until someone
	// closed them, and then refused to let the excuse outlive the gap.
	"EvRetry":        "NOT YET TRANSLATED: a transient retry is invisible to the client",
	"EvStall":        "NOT YET TRANSLATED: a stalled turn is invisible to the client",
	"EvEscalation":   "NOT YET TRANSLATED: escalation is invisible to the client",
	"EvContinuation": "NOT YET TRANSLATED: continuation is invisible to the client",
}

func coreEventTypes(t *testing.T) []string {
	t.Helper()
	path := filepath.Join("..", "..", "..", "packages", "core", "events.go")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read core events: %v", err)
	}
	re := regexp.MustCompile(`(?m)^type (Ev[A-Za-z]+) `)
	var out []string
	for _, m := range re.FindAllStringSubmatch(string(data), -1) {
		out = append(out, m[1])
	}
	sort.Strings(out)
	return out
}

func acpHandledEvents(t *testing.T) map[string]bool {
	t.Helper()
	data, err := os.ReadFile("translate.go")
	if err != nil {
		t.Fatalf("read translate.go: %v", err)
	}
	re := regexp.MustCompile(`case core\.(Ev[A-Za-z]+)`)
	handled := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(string(data), -1) {
		handled[m[1]] = true
	}
	return handled
}

func TestEveryCoreEventIsTranslatedOrDeclared(t *testing.T) {
	types := coreEventTypes(t)
	handled := acpHandledEvents(t)

	// Guard the guard: if either extractor matched nothing, every assertion
	// below passes vacuously.
	if len(types) < 15 {
		t.Fatalf("found %d core event types; the scan is broken and this census proves nothing", len(types))
	}
	if len(handled) < 5 {
		t.Fatalf("found %d handled events in translate.go; the scan is broken", len(handled))
	}

	for _, typ := range bothHandledAndDeclared(types, handled, acpUntranslated) {
		t.Errorf("%s is both handled by translateEvent and listed in acpUntranslated; remove the list entry", typ)
	}
	for _, typ := range undeclaredEvents(types, handled, acpUntranslated) {
		t.Errorf("core.%s is not handled by translateEvent and not declared in acpUntranslated.\n"+
			"  translateEvent has no default arm, so this event currently produces NOTHING for an ACP "+
			"client — no notification, no error, no compile failure.\n"+
			"  Either add a case, or add an acpUntranslated entry saying why it needs none.", typ)
	}

	// A stale entry hides a regression: if a listed type disappears from core,
	// the list keeps excusing a name that no longer exists.
	known := map[string]bool{}
	for _, typ := range types {
		known[typ] = true
	}
	var stale []string
	for name := range acpUntranslated {
		if !known[name] {
			stale = append(stale, name)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("acpUntranslated names event types core no longer defines: %s", strings.Join(stale, ", "))
	}
}

// undeclaredEvents is the census's actual rule: an event type that translateEvent
// does not handle and acpUntranslated does not excuse. Split out so the teeth
// test below can drive it with synthetic inputs rather than restating it.
func undeclaredEvents(types []string, handled map[string]bool, declared map[string]string) []string {
	var out []string
	for _, typ := range types {
		if handled[typ] {
			continue
		}
		if _, ok := declared[typ]; ok {
			continue
		}
		out = append(out, typ)
	}
	sort.Strings(out)
	return out
}

// bothHandledAndDeclared catches the opposite drift: a type translated in code
// while still carrying an excuse, so the list claims a gap that closed.
func bothHandledAndDeclared(types []string, handled map[string]bool, declared map[string]string) []string {
	var out []string
	for _, typ := range types {
		if _, ok := declared[typ]; ok && handled[typ] {
			out = append(out, typ)
		}
	}
	sort.Strings(out)
	return out
}

// TestACPEventCensusCatchesANewEvent is the teeth, driven through the same
// predicate the census uses rather than asserting facts about the live maps
// (which would be true by construction and prove nothing).
func TestACPEventCensusCatchesANewEvent(t *testing.T) {
	types := []string{"EvTextDelta", "EvDone", "EvBrandNewThing"}
	handled := map[string]bool{"EvTextDelta": true}
	declared := map[string]string{"EvDone": "terminator"}

	got := undeclaredEvents(types, handled, declared)
	if len(got) != 1 || got[0] != "EvBrandNewThing" {
		t.Fatalf("census predicate failed to flag a new undeclared event: got %v, want [EvBrandNewThing]", got)
	}

	// Handling it must silence the census, or the rule is unpassable.
	handled["EvBrandNewThing"] = true
	if got := undeclaredEvents(types, handled, declared); len(got) != 0 {
		t.Fatalf("census still flags an event after it is handled: %v", got)
	}

	// And so must declaring it instead of handling it.
	delete(handled, "EvBrandNewThing")
	declared["EvBrandNewThing"] = "deliberately ignored"
	if got := undeclaredEvents(types, handled, declared); len(got) != 0 {
		t.Fatalf("census still flags an event after it is declared: %v", got)
	}

	// Doing both is itself a drift signal.
	handled["EvBrandNewThing"] = true
	if got := bothHandledAndDeclared(types, handled, declared); len(got) != 1 {
		t.Fatalf("census failed to flag an event that is both handled and declared: %v", got)
	}
}
