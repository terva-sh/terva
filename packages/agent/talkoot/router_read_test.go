package talkoot

import "testing"

// reader is a ReadDriver. With now set, the member reads each delivery inside
// DeliverRead, as an idle member's turn starts. Otherwise it keeps the
// receipt, as a busy member's queue does, until the test reads it.
type reader struct {
	recorder
	f        *fixture
	now      bool
	receipts []Receipt
	// onRead runs after a read inside DeliverRead.
	onRead func(m Member)
}

func (d *reader) DeliverRead(talkoot string, m Member, text string, r Receipt) error {
	if err := d.Deliver(talkoot, m, text); err != nil {
		return err
	}
	if !d.now {
		d.mu.Lock()
		d.receipts = append(d.receipts, r)
		d.mu.Unlock()
		return nil
	}
	if err := d.f.router.Read(r); err != nil {
		return err
	}
	if d.onRead != nil {
		d.onRead(m)
	}
	return nil
}

func (d *reader) take() Receipt {
	d.mu.Lock()
	defer d.mu.Unlock()
	r := d.receipts[0]
	d.receipts = d.receipts[1:]
	return r
}

func newReadFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t, nil)
	f.reads = &reader{recorder: recorder{name: "native"}, f: f, now: true}
	f.reopen()
	return f
}

func (f *fixture) rootOfSend(from, to, body string) string {
	f.t.Helper()
	e, err := f.send(from, Outgoing{To: []string{to}, Body: body})
	if err != nil {
		f.t.Fatalf("%s sends: %v", from, err)
	}
	return e.Chain.Root
}

// An idle member reads the delivery as its turn starts, before the driver
// returns, and it sends in the new chain from its first step.
func TestAnIdleMemberTakesTheChainAsItsTurnStarts(t *testing.T) {
	f := newReadFixture(t)
	var root string
	f.reads.onRead = func(m Member) {
		if m.ID == "helm" && root == "" {
			root = f.rootOfSend("helm", "jev", "Build it.")
		}
	}
	e := f.post()
	if root != e.ID {
		t.Errorf("helm's first send landed in chain %q, want the post's %q", root, e.ID)
	}
}

// 🔑 A busy member reads a queued envelope at its turn's next safe boundary.
// Until then its sends count in the chain it works in, and a restart agrees.
func TestABusyMemberKeepsItsChainUntilItReads(t *testing.T) {
	f := newReadFixture(t)
	first := f.post()
	f.reads.now = false
	second, err := f.router.Post("sothr", nil, "Also check the index.", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := f.rootOfSend("helm", "jev", "one"); got != first.ID {
		t.Errorf("before the read, helm sent in %q, want the first post's %q", got, first.ID)
	}
	for _, l := range f.lines() {
		if l.Type == LineDelivery && l.Ref == second.ID && l.Chain != "" {
			t.Errorf("the queued delivery names chain %q, so replay would move helm before the read", l.Chain)
		}
	}
	f.reopen()
	if got := f.rootOfSend("helm", "jev", "two"); got != first.ID {
		t.Errorf("after a restart before the read, helm sent in %q, want %q", got, first.ID)
	}

	// The receipt came from the router before the restart. The router that
	// replaced it knows the chain from the room.
	if err := f.router.Read(f.reads.take()); err != nil {
		t.Fatal(err)
	}
	if got := f.rootOfSend("helm", "jev", "three"); got != second.ID {
		t.Errorf("after the read, helm sent in %q, want the second post's %q", got, second.ID)
	}
	f.reopen()
	if got := f.rootOfSend("helm", "jev", "four"); got != second.ID {
		t.Errorf("after a restart past the read, helm sent in %q, want %q", got, second.ID)
	}
	var reads []Line
	for _, l := range f.lines() {
		if l.Type == LineRead && l.Member == "helm" {
			reads = append(reads, l)
		}
	}
	if len(reads) != 2 || reads[1].Chain != second.ID || reads[1].Ref != second.ID {
		t.Errorf("want a read line for each post, the last in the second chain; got %+v", reads)
	}
}

// A member that never reads a delivery keeps its chain. The turn line names
// the chain the turn worked in.
func TestATurnThatNeverReadKeepsItsChain(t *testing.T) {
	f := newReadFixture(t)
	first := f.post()
	f.reads.now = false
	if _, err := f.router.Post("sothr", nil, "Also check the index.", nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := f.router.TurnEnded("helm", 0.1); err != nil {
		t.Fatal(err)
	}
	var turn Line
	for _, l := range f.lines() {
		if l.Type == LineTurn {
			turn = l
		}
	}
	if turn.Chain != first.ID {
		t.Errorf("the turn line names chain %q, want %q", turn.Chain, first.ID)
	}
}

// Only the router makes a receipt, but Read still refuses one that names no
// member or no chain it knows.
func TestReadRefusesAReceiptItCannotPlace(t *testing.T) {
	f := newReadFixture(t)
	e := f.post()
	for _, r := range []Receipt{
		{member: "ghost", ref: e.ID, chain: e.ID},
		{member: "helm", ref: e.ID, chain: "no-such-chain"},
		{},
	} {
		if err := f.router.Read(r); err == nil {
			t.Errorf("Read(%+v) passed", r)
		}
	}
	var reads []Line
	for _, l := range f.lines() {
		if l.Type == LineRead {
			reads = append(reads, l)
		}
	}
	if len(reads) != 1 || reads[0].Member != "helm" || reads[0].Ref != e.ID {
		t.Errorf("want only helm's read of the post, got %+v", reads)
	}
}

// Two deliveries queue behind a busy turn, and the drain reads both at once.
// The member works in the chain of the newer one.
func TestTwoQueuedReadsLeaveTheNewerChain(t *testing.T) {
	f := newReadFixture(t)
	f.post()
	f.reads.now = false
	var posts []Envelope
	for _, body := range []string{"a", "b"} {
		e, err := f.router.Post("sothr", nil, body, nil, "")
		if err != nil {
			t.Fatal(err)
		}
		posts = append(posts, e)
	}
	for range posts {
		if err := f.router.Read(f.reads.take()); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.rootOfSend("helm", "jev", "after"); got != posts[1].ID {
		t.Errorf("helm sent in %q, want the newer post's %q", got, posts[1].ID)
	}
}

// 🔑 A session tells deliveries apart by their text. Two identical posts must
// still render two texts, or a stale entry could read a later delivery.
func TestTwoIdenticalPostsRenderTwoTexts(t *testing.T) {
	f := newFixture(t, nil)
	f.post()
	f.post()
	got := f.native.to("helm")
	if len(got) != 2 || got[0] == got[1] {
		t.Fatalf("two identical posts delivered %q", got)
	}
}
