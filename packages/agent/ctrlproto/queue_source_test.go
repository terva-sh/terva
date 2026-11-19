package ctrlproto

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
)

// A queue of typed texts has the wire form it had before sources existed. A
// client built before them reads `queued` alone and sees the same list.
func TestAQueueOfTypedTextsHasNoSourcesOnTheWire(t *testing.T) {
	ev := QueueUpdatedEvent([]core.QueuedMessage{{Text: "one"}, {Text: "two"}})
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(raw); strings.Contains(got, "queued_sources") {
		t.Fatalf("a typed queue carries sources: %s", got)
	}
	if !reflect.DeepEqual(ev.Queued, []string{"one", "two"}) {
		t.Fatalf("Queued = %v", ev.Queued)
	}

	if texts, sources := QueueView(nil); texts != nil || sources != nil {
		t.Fatalf("an empty queue = %v / %v, want nothing", texts, sources)
	}
}

// One entry with a source makes the list, and the list then has one value
// for each text, so a client can read the two lists by position.
func TestQueueSourcesLineUpWithTheTexts(t *testing.T) {
	ev := QueueUpdatedEvent([]core.QueuedMessage{
		{Text: "typed"},
		{Text: "recap", Source: QueueSourceSwarmRecap},
		{Text: "typed again"},
	})
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	var back Event
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back.Queued, []string{"typed", "recap", "typed again"}) {
		t.Fatalf("Queued = %v", back.Queued)
	}
	if !reflect.DeepEqual(back.QueuedSources, []string{"", QueueSourceSwarmRecap, ""}) {
		t.Fatalf("QueuedSources = %v", back.QueuedSources)
	}
}

// The queue call carries the source to the service, and a call from a client
// that sends none reads as a person's text.
func TestTheQueueCallCarriesTheSource(t *testing.T) {
	var with QueueParams
	if err := json.Unmarshal([]byte(`{"text":"recap","source":"swarm:recap"}`), &with); err != nil {
		t.Fatal(err)
	}
	if with != (QueueParams{Text: "recap", Source: QueueSourceSwarmRecap}) {
		t.Fatalf("params = %+v", with)
	}
	var without QueueParams
	if err := json.Unmarshal([]byte(`{"text":"typed"}`), &without); err != nil {
		t.Fatal(err)
	}
	if without.Source != "" {
		t.Fatalf("a call with no source read as %q", without.Source)
	}
	raw, err := json.Marshal(QueueParams{Text: "typed"})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"text":"typed"}` {
		t.Fatalf("a typed text marshals as %s", raw)
	}
}

// queue.set from a client that names the sources gives entries. A client that
// names none is an older one, and the session then matches by text.
func TestQueueSetEntries(t *testing.T) {
	entries, known, err := QueueSetParams{
		Texts:   []string{"typed", "recap"},
		Sources: []string{"", QueueSourceSwarmRecap},
	}.Entries()
	if err != nil || !known {
		t.Fatalf("Entries: known=%v err=%v", known, err)
	}
	want := []core.QueuedMessage{{Text: "typed"}, {Text: "recap", Source: QueueSourceSwarmRecap}}
	if !reflect.DeepEqual(entries, want) {
		t.Fatalf("entries = %+v, want %+v", entries, want)
	}

	entries, known, err = QueueSetParams{Texts: []string{"typed", "recap"}}.Entries()
	if err != nil || known {
		t.Fatalf("no sources: known=%v err=%v", known, err)
	}
	if !reflect.DeepEqual(entries, []core.QueuedMessage{{Text: "typed"}, {Text: "recap"}}) {
		t.Fatalf("entries = %+v", entries)
	}

	// A list of typed texts still names its sources, so that the session
	// does not fall back to texts: [""] is a list, and it must reach the wire.
	raw, err := json.Marshal(QueueSetParams{Texts: []string{"typed"}, Sources: []string{""}})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"texts":["typed"],"sources":[""]}` {
		t.Fatalf("a typed queue with its sources marshals as %s", raw)
	}

	for name, p := range map[string]QueueSetParams{
		"two sources for one text": {Texts: []string{"a"}, Sources: []string{"", ""}},
		"one source for two texts": {Texts: []string{"a", "b"}, Sources: []string{""}},
		"a source that is no name": {Texts: []string{"a"}, Sources: []string{"Not A Name"}},
		"a Stage marker":           {Texts: []string{"a"}, Sources: []string{core.MetaDirected}},
	} {
		_, _, err := p.Entries()
		var ce *Error
		if !errors.As(err, &ce) || ce.Code != CodeBadRequest {
			t.Errorf("%s: err = %v, want a bad request", name, err)
		}
	}
}

func TestValidQueueSource(t *testing.T) {
	for _, s := range []string{"", QueueSourceSwarmRecap, QueueSourceTalkootDelivery, "routine:nightly-2", "a.b_c", strings.Repeat("a", 64)} {
		if !ValidQueueSource(s) {
			t.Errorf("%q must be a valid source", s)
		}
	}
	// The last two are well-formed. They are refused because they mean an
	// authored line under the same message key.
	for _, s := range []string{"Swarm", "swarm recap", "swarm\n", "a/b", "é", "\x1b[2J", strings.Repeat("a", 65), core.MetaDirected, core.MetaRouted} {
		if ValidQueueSource(s) {
			t.Errorf("%q must not be a valid source", s)
		}
	}
}
