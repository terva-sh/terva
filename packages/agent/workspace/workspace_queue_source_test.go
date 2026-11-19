package workspace

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// userSources lists each user message of a snapshot as text and source, in
// the order of the transcript. It reads the wire form, which is what every
// front end draws from.
func userSources(snap ctrlproto.Snapshot) [][2]string {
	var out [][2]string
	for _, m := range snap.Messages {
		if m.Role != string(provider.RoleUser) {
			continue
		}
		text := ""
		for _, b := range m.Content {
			text += b.Text
		}
		out = append(out, [2]string{text, m.Source})
	}
	return out
}

// A text that terva queued into an idle session starts a turn through Prompt,
// and not through the loop's drain. The message it becomes must still name
// its writer, in the stored transcript and in the snapshot a client reads.
func TestAQueuedSourceSurvivesTheStartOfATurn(t *testing.T) {
	s := newTurnTestSession(t, &twoStepClient{calls: 1})

	s.queueFrom(ctrlproto.QueueSourceSwarmRecap, "[auto-swarm update] 1 sub-agent(s) finished")
	waitIdle(t, s)

	want := [][2]string{{"[auto-swarm update] 1 sub-agent(s) finished", ctrlproto.QueueSourceSwarmRecap}}
	if got := userSources(s.snapshot()); !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot user messages = %v, want %v", got, want)
	}
	for _, m := range s.agent.Messages() {
		if m.Role == provider.RoleUser && m.Meta[core.MetaSource] != ctrlproto.QueueSourceSwarmRecap {
			t.Fatalf("the stored message has the source %q", m.Meta[core.MetaSource])
		}
	}

	// The positive control: a person's text through the same path has none.
	s.queue("and a question from me")
	waitIdle(t, s)
	want = append(want, [2]string{"and a question from me", ""})
	if got := userSources(s.snapshot()); !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot user messages = %v, want %v", got, want)
	}
}

// While a turn runs, the queue view names the writer of each waiting text,
// in the broadcast and in the snapshot. The loop then takes both texts, and
// each message keeps what the queue said about it.
func TestTheQueueViewAndTheDrainCarryTheSource(t *testing.T) {
	tool := gatedStepTool{entered: make(chan struct{}, 1), release: make(chan struct{})}
	s := newTurnTestSession(t, &twoStepClient{})
	s.agent.SetTools(core.Registry{"step": tool})
	s.agent.AddQueueDrainedObserver(func([]core.QueuedMessage) { s.broadcastQueue() })

	type view struct{ texts, sources []string }
	views := make(chan view, 64)
	sub := s.hub.add(nil, false)
	go func() {
		for ev := range sub {
			if ev.Type == ctrlproto.EventQueueUpdated {
				views <- view{ev.Queued, ev.QueuedSources}
			}
		}
	}()
	await := func(what string, ok func(view) bool) {
		t.Helper()
		deadline := time.After(3 * time.Second)
		for {
			select {
			case v := <-views:
				if ok(v) {
					return
				}
			case <-deadline:
				t.Fatalf("no queue_updated with %s", what)
			}
		}
	}

	if err := s.prompt("run the tool", nil, core.UserMessageExtras{}); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	<-tool.entered

	// A queue of typed texts alone has no list of sources, as before.
	s.queue("typed")
	await("one typed text and no sources", func(v view) bool {
		return reflect.DeepEqual(v.texts, []string{"typed"}) && v.sources == nil
	})
	s.queueFrom(ctrlproto.QueueSourceSwarmRecap, "the recap")
	await("both texts and the recap's source", func(v view) bool {
		return reflect.DeepEqual(v.texts, []string{"typed", "the recap"}) &&
			reflect.DeepEqual(v.sources, []string{"", ctrlproto.QueueSourceSwarmRecap})
	})
	snap := s.snapshot()
	if !reflect.DeepEqual(snap.Queued, []string{"typed", "the recap"}) ||
		!reflect.DeepEqual(snap.QueuedSources, []string{"", ctrlproto.QueueSourceSwarmRecap}) {
		t.Fatalf("snapshot queue = %v / %v", snap.Queued, snap.QueuedSources)
	}

	close(tool.release)
	waitIdle(t, s)
	want := [][2]string{
		{"run the tool", ""},
		{"typed", ""},
		{"the recap", ctrlproto.QueueSourceSwarmRecap},
	}
	if got := userSources(s.snapshot()); !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot user messages = %v, want %v", got, want)
	}
}

// queue.set from a client that names the sources keeps the source with its
// entry when two queued texts are equal. From a client that names none, the
// session matches by text, as it did for every client before.
func TestSetQueueKeepsTheSourceWithItsEntry(t *testing.T) {
	tool := gatedStepTool{entered: make(chan struct{}, 1), release: make(chan struct{})}
	s := newTurnTestSession(t, &twoStepClient{})
	s.agent.SetTools(core.Registry{"step": tool})
	w := &Workspace{sessions: map[string]*wsSession{s.id: s}}
	defer func() {
		close(tool.release)
		waitIdle(t, s)
	}()
	if err := s.prompt("run the tool", nil, core.UserMessageExtras{}); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	<-tool.entered
	fill := func() {
		s.agent.SetQueuedEntries(nil)
		s.queue("X")
		s.queueFrom(ctrlproto.QueueSourceSwarmRecap, "X")
	}
	recap := core.QueuedMessage{Text: "X", Source: ctrlproto.QueueSourceSwarmRecap}

	// The person removes the typed text. The client says what stays.
	fill()
	err := w.SetQueue(context.Background(), s.id, ctrlproto.QueueSetParams{
		Texts: []string{"X"}, Sources: []string{ctrlproto.QueueSourceSwarmRecap},
	})
	if err != nil {
		t.Fatalf("SetQueue: %v", err)
	}
	if got := s.agent.PendingQueuedMessages(); !reflect.DeepEqual(got, []core.QueuedMessage{recap}) {
		t.Fatalf("queue = %+v, want the recap with its source", got)
	}

	// An older client sends texts alone, and the first match gives the source.
	fill()
	if err := w.SetQueue(context.Background(), s.id, ctrlproto.QueueSetParams{Texts: []string{"X"}}); err != nil {
		t.Fatalf("SetQueue: %v", err)
	}
	if got := s.agent.PendingQueuedMessages(); !reflect.DeepEqual(got, []core.QueuedMessage{{Text: "X"}}) {
		t.Fatalf("queue = %+v, want the first entry's source, which is none", got)
	}

	// A list that does not line up changes nothing.
	fill()
	err = w.SetQueue(context.Background(), s.id, ctrlproto.QueueSetParams{Texts: []string{"X"}, Sources: []string{"", ""}})
	var ce *ctrlproto.Error
	if !errors.As(err, &ce) || ce.Code != ctrlproto.CodeBadRequest {
		t.Fatalf("err = %v, want a bad request", err)
	}
	if got := s.agent.PendingQueuedMessages(); len(got) != 2 {
		t.Fatalf("a refused call changed the queue to %+v", got)
	}
}

// 🚨 A client names the source, and every client then prints it. The
// workspace refuses a value that is not one short word, before it looks for
// the session.
func TestQueueRefusesASourceThatIsNotAName(t *testing.T) {
	w := &Workspace{sessions: map[string]*wsSession{}}
	for _, source := range []string{"Swarm:Recap", "swarm recap", "swarm:recap\n", "a/b", string(make([]byte, 65))} {
		err := w.Queue(context.Background(), "none", ctrlproto.QueueParams{Text: "x", Source: source})
		var ce *ctrlproto.Error
		if !errors.As(err, &ce) || ce.Code != ctrlproto.CodeBadRequest {
			t.Errorf("source %q: err = %v, want a bad request", source, err)
		}
	}
	for _, source := range []string{"", ctrlproto.QueueSourceSwarmRecap, ctrlproto.QueueSourceTalkootDelivery, "routine:nightly-2"} {
		if err := w.Queue(context.Background(), "none", ctrlproto.QueueParams{Text: "x", Source: source}); err != nil {
			t.Errorf("source %q: err = %v, want none", source, err)
		}
	}
}
