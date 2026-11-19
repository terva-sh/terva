package core

import (
	"context"
	"reflect"
	"testing"

	"terva.sh/terva/packages/provider"
)

// A text that the host queued becomes a user message that says so, in the
// stored message and in the event. A person's text beside it says nothing.
func TestAQueuedSourceReachesTheMessageAndTheEvent(t *testing.T) {
	a := newTestAgent(endTurnClient{}, "test", "", Registry{})
	a.QueueMessageFrom("swarm:recap", "[auto-swarm update] 1 sub-agent(s) finished")
	a.QueueMessage("and one from me")

	sources := map[string]string{}
	err := a.Prompt(context.Background(), "start", nil, func(ev AgentEvent) {
		if e, ok := ev.(EvUserMessage); ok {
			sources[extractText(e.Message)] = e.Source
		}
	})
	if err != nil {
		t.Fatalf("Prompt returned error: %v", err)
	}
	want := map[string]string{
		"start": "",
		"[auto-swarm update] 1 sub-agent(s) finished": "swarm:recap",
		"and one from me": "",
	}
	if !reflect.DeepEqual(sources, want) {
		t.Fatalf("event sources = %v, want %v", sources, want)
	}

	stored := map[string]string{}
	for _, m := range a.Messages() {
		if m.Role == provider.RoleUser {
			stored[extractText(m)] = m.Meta[MetaSource]
		}
	}
	if !reflect.DeepEqual(stored, want) {
		t.Fatalf("stored sources = %v, want %v", stored, want)
	}
	// A recap can start a turn, so it stays a turn. MetaSynthetic is the mark
	// that takes a message out of the turns, and a source is not that mark.
	for _, m := range a.Messages() {
		if m.Meta[MetaSource] == "swarm:recap" && !IsUserTurn(m) {
			t.Fatal("a message with a source must still read as a user turn")
		}
	}
}

// A host starts a turn from a queued entry with Prompt. The entry's Meta is
// what makes that message say the same as one the loop appended.
func TestAQueuedEntryThatStartsATurnKeepsItsSource(t *testing.T) {
	a := newTestAgent(endTurnClient{}, "test", "", Registry{})
	a.QueueMessageFrom("swarm:recap", "the recap")
	head, ok := a.ShiftQueuedMessage()
	if !ok || head != (QueuedMessage{Text: "the recap", Source: "swarm:recap"}) {
		t.Fatalf("ShiftQueuedMessage = %+v,%v", head, ok)
	}

	var got []string
	err := a.promptExtra(context.Background(), head.Text, nil, UserMessageExtras{Meta: head.Meta()}, func(ev AgentEvent) {
		if e, ok := ev.(EvUserMessage); ok {
			got = append(got, e.Source)
		}
	})
	if err != nil {
		t.Fatalf("Prompt returned error: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"swarm:recap"}) {
		t.Fatalf("event sources = %v, want [swarm:recap]", got)
	}
	if (QueuedMessage{Text: "typed"}).Meta() != nil {
		t.Fatal("a person's entry must have no metadata")
	}
}

// 🚨 A source is a label. A client names it, so a text with a source must
// meet the same guard as a text without one.
func TestTheGuardJudgesAQueuedTextWithASource(t *testing.T) {
	a := newTestAgent(endTurnClient{}, "test", "", Registry{})
	var judged []string
	a.beforeUserMessage = func(text string) (bool, string, string) {
		judged = append(judged, text)
		return text != "blocked", "no", ""
	}
	a.QueueMessageFrom("swarm:recap", "blocked")

	var rejected []string
	err := a.Prompt(context.Background(), "start", nil, func(ev AgentEvent) {
		if e, ok := ev.(EvUserMessageRejected); ok {
			rejected = append(rejected, e.Text)
		}
	})
	if err != nil {
		t.Fatalf("Prompt returned error: %v", err)
	}
	if !reflect.DeepEqual(judged, []string{"start", "blocked"}) {
		t.Fatalf("the guard judged %v, want [start blocked]", judged)
	}
	if !reflect.DeepEqual(rejected, []string{"blocked"}) {
		t.Fatalf("rejected = %v, want [blocked]", rejected)
	}
	for _, tx := range userTexts(a.Messages()) {
		if tx == "blocked" {
			t.Fatal("a refused text with a source reached the transcript")
		}
	}
}

// queue.set sends texts. A text that is still in the queue keeps its source,
// and a text that a person changed is the person's text.
func TestSetQueuedMessagesKeepsTheSourceOfAnUnchangedText(t *testing.T) {
	a := newTestAgent(nil, "fake", "", Registry{})
	a.QueueMessageFrom("swarm:recap", "recap")
	a.QueueMessage("typed")
	a.QueueMessageFrom("talkoot:delivery", "same")
	a.QueueMessage("same")

	// Drop "typed", keep the rest in order.
	a.SetQueuedMessages([]string{"recap", "same", "same"})
	want := []QueuedMessage{
		{Text: "recap", Source: "swarm:recap"},
		{Text: "same", Source: "talkoot:delivery"},
		{Text: "same"},
	}
	if got := a.PendingQueuedMessages(); !reflect.DeepEqual(got, want) {
		t.Fatalf("after a removal: %+v, want %+v", got, want)
	}

	// A changed recap has no match. Three equal texts find two entries, so
	// the third is new.
	a.SetQueuedMessages([]string{"recap, but shorter", "same", "same", "same"})
	want = []QueuedMessage{
		{Text: "recap, but shorter"},
		{Text: "same", Source: "talkoot:delivery"},
		{Text: "same"},
		{Text: "same"},
	}
	if got := a.PendingQueuedMessages(); !reflect.DeepEqual(got, want) {
		t.Fatalf("after an edit: %+v, want %+v", got, want)
	}
}

// The case that texts alone cannot settle: a person typed a text that equals
// a queued report. The caller names the entry it keeps, and the source stays
// with that entry, whichever of the two leaves.
func TestSetQueuedEntriesTellsTwoEqualTextsApart(t *testing.T) {
	queue := func() *Agent {
		a := newTestAgent(nil, "fake", "", Registry{})
		a.QueueMessage("X")
		a.QueueMessageFrom("swarm:recap", "X")
		return a
	}
	recap := QueuedMessage{Text: "X", Source: "swarm:recap"}
	typed := QueuedMessage{Text: "X"}

	a := queue()
	a.SetQueuedEntries([]QueuedMessage{recap})
	if got := a.PendingQueuedMessages(); !reflect.DeepEqual(got, []QueuedMessage{recap}) {
		t.Fatalf("the typed text left, and the queue holds %+v, want the recap", got)
	}
	a = queue()
	a.SetQueuedEntries([]QueuedMessage{typed})
	if got := a.PendingQueuedMessages(); !reflect.DeepEqual(got, []QueuedMessage{typed}) {
		t.Fatalf("the recap left, and the queue holds %+v, want the typed text", got)
	}

	// The positive control: texts alone give the first entry's source, which
	// is the wrong one when the typed text was the one that left.
	a = queue()
	a.SetQueuedMessages([]string{"X"})
	if got := a.PendingQueuedMessages(); !reflect.DeepEqual(got, []QueuedMessage{typed}) {
		t.Fatalf("SetQueuedMessages gave %+v", got)
	}
}

// 🚨 The caller confirms a source and cannot give one. A source that the
// queue does not hold for that text is dropped, and so is a source on a text
// that the caller changed.
func TestSetQueuedEntriesGivesNoNewSource(t *testing.T) {
	a := newTestAgent(nil, "fake", "", Registry{})
	a.QueueMessage("typed")
	a.QueueMessageFrom("swarm:recap", "recap")

	a.SetQueuedEntries([]QueuedMessage{
		{Text: "typed", Source: "swarm:recap"},
		{Text: "recap, but changed", Source: "swarm:recap"},
		{Text: " recap ", Source: "swarm:recap"},
		{Text: "recap", Source: "swarm:recap"},
		{Text: "  "},
	})
	want := []QueuedMessage{
		{Text: "typed"},
		{Text: "recap, but changed"},
		{Text: "recap", Source: "swarm:recap"},
		// The queue held one recap, and it answered for the entry above.
		{Text: "recap"},
	}
	if got := a.PendingQueuedMessages(); !reflect.DeepEqual(got, want) {
		t.Fatalf("queue = %+v, want %+v", got, want)
	}

	a.SetQueuedEntries(nil)
	if n := a.QueuedMessageCount(); n != 0 {
		t.Fatalf("an empty list left %d entries", n)
	}
}

func TestRequeueFrontKeepsTheSource(t *testing.T) {
	a := newTestAgent(nil, "fake", "", Registry{})
	a.QueueMessage("later")
	a.RequeueFront(QueuedMessage{Text: " recap ", Source: "swarm:recap"})
	want := []QueuedMessage{{Text: "recap", Source: "swarm:recap"}, {Text: "later"}}
	if got := a.PendingQueuedMessages(); !reflect.DeepEqual(got, want) {
		t.Fatalf("PendingQueuedMessages = %+v, want %+v", got, want)
	}
}

func TestTheQueueDrainedObserverGetsTheSources(t *testing.T) {
	a := newTestAgent(endTurnClient{}, "test", "", Registry{})
	var drained []QueuedMessage
	a.AddQueueDrainedObserver(func(q []QueuedMessage) { drained = append(drained, q...) })
	a.QueueMessageFrom("swarm:recap", "recap")
	if err := a.Prompt(context.Background(), "start", nil, nil); err != nil {
		t.Fatalf("Prompt returned error: %v", err)
	}
	want := []QueuedMessage{{Text: "recap", Source: "swarm:recap"}}
	if !reflect.DeepEqual(drained, want) {
		t.Fatalf("drained = %+v, want %+v", drained, want)
	}
}

// The wire carries the source of a user message both ways, because a client
// rebuilds its transcript from a snapshot. It carries none for an assistant
// message, whose sources have typed fields of their own.
func TestTheWireCarriesAUserMessageSource(t *testing.T) {
	recap := provider.Message{
		Role:    provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: "recap"}},
		Meta:    map[string]string{MetaSource: "swarm:recap"},
	}
	w := MessageToWire(recap)
	if w.Source != "swarm:recap" {
		t.Fatalf("WireMessage.Source = %q, want swarm:recap", w.Source)
	}
	if back := MessageFromWire(w); back.Meta[MetaSource] != "swarm:recap" {
		t.Fatalf("the source did not survive the round trip: %v", back.Meta)
	}

	plain := provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "hi"}}}
	if MessageToWire(plain).Source != "" {
		t.Error("a person's message must carry no source")
	}
	greeting := provider.Message{
		Role:    provider.RoleAssistant,
		Content: []provider.Content{provider.TextBlock{Text: "hello"}},
		Meta:    map[string]string{MetaSource: "card:greeting"},
	}
	if got := MessageToWire(greeting).Source; got != "" {
		t.Errorf("an assistant message carried the source %q", got)
	}
	directed := provider.Message{
		Role:    provider.RoleAssistant,
		Content: []provider.Content{provider.TextBlock{Text: "a line"}},
		Meta:    map[string]string{MetaSource: MetaDirected},
	}
	if w := MessageToWire(directed); !w.Directed || w.Source != "" {
		t.Errorf("a directed line: Directed=%v Source=%q", w.Directed, w.Source)
	}
}

// 🚨 A queue source and a Stage marker share one message key. On a user
// message the value is always the writer of a queued text. A marker there
// must not make the message a directed or routed line, which would hide the
// writer from every client, live and after a reload.
func TestAStageMarkerOnAUserMessageIsStillASource(t *testing.T) {
	for _, marker := range []string{MetaDirected, MetaRouted} {
		m := provider.Message{
			Role:    provider.RoleUser,
			Content: []provider.Content{provider.TextBlock{Text: "queued"}},
			Meta:    map[string]string{MetaSource: marker},
		}
		w := MessageToWire(m)
		if w.Source != marker || w.Directed || w.Routed {
			t.Errorf("%s on a user message: Source=%q Directed=%v Routed=%v", marker, w.Source, w.Directed, w.Routed)
		}
		back := MessageFromWire(w)
		if back.Meta[MetaSource] != marker {
			t.Errorf("%s did not survive the round trip: %v", marker, back.Meta)
		}
		if w2 := MessageToWire(back); w2.Source != marker || w2.Directed || w2.Routed {
			t.Errorf("%s after a round trip: Source=%q Directed=%v Routed=%v", marker, w2.Source, w2.Directed, w2.Routed)
		}
	}
	// The positive control: the same marker on an assistant message is the
	// authored line it was.
	routed := provider.Message{
		Role:    provider.RoleAssistant,
		Content: []provider.Content{provider.TextBlock{Text: "a line"}},
		Meta:    map[string]string{MetaSource: MetaRouted},
	}
	if w := MessageToWire(routed); !w.Routed || w.Source != "" {
		t.Errorf("a routed line: Routed=%v Source=%q", w.Routed, w.Source)
	}
}
