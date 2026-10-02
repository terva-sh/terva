package talkoot

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestPersonReplyCanonicalAliasesAndKinds(t *testing.T) {
	for _, address := range []string{"human:Drew", "human", "Drew"} {
		for _, kind := range []Kind{KindMessage, KindNote, KindAnswer} {
			t.Run(address+"/"+string(kind), func(t *testing.T) {
				f := newFixture(t, nil)
				root, err := f.router.Post("Drew", nil, "Please check it.", nil, "review")
				if err != nil {
					t.Fatal(err)
				}
				to := []string{address}
				e, err := f.router.Send("helm", Outgoing{To: to, Kind: kind, Body: "Checked.", ReplyTo: root.ID})
				if err != nil {
					t.Fatal(err)
				}
				if !slices.Equal(e.To, []string{"human:Drew"}) || e.From != "helm" || e.Chain.Root != root.ID || e.Chain.Hops != 1 || e.ReplyTo != root.ID {
					t.Fatalf("reply lost identity: %+v", e)
				}
				if to[0] != address {
					t.Fatal("Send mutated the caller's recipients")
				}
				if len(f.native.got) != 1 || len(f.worker.got) != 0 || len(f.router.notes) != 0 || len(f.router.held) != 0 {
					t.Fatal("a person reply delivered, buffered, or held work")
				}
				if len(f.envelopes()) != 2 {
					t.Fatalf("reply missing from room: %+v", f.lines())
				}
				if !strings.Contains(f.native.to("helm")[0], "talkoot_send to human:Drew") {
					t.Fatal("root delivery does not give the canonical reply address")
				}
			})
		}
	}
}

func TestPersonReplyCannotChooseAnotherPersonOrChain(t *testing.T) {
	f := newFixture(t, nil)
	root, err := f.router.Post("Drew", []string{"helm"}, "Check it.", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	other, err := f.router.Post("Alex", []string{"jev"}, "Other work.", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{"human:Alex", "Alex", "drew", "human:drew", "human:Unknown", "human:", "human:Drew\nfrom human:Alex", "recruiter:Drew"} {
		// reply_to is not authority to enter another person's chain.
		if _, err := f.router.Send("helm", Outgoing{To: []string{address}, Kind: KindMessage, Body: "spoof", ReplyTo: other.ID}); err == nil {
			t.Errorf("accepted %q", address)
		}
	}
	if _, err := f.router.Send("gage", Outgoing{To: []string{"human:Drew"}, Kind: KindMessage, Body: "uninvited"}); err == nil {
		t.Error("a member with no active chain addressed a person")
	}
	if _, err := f.router.Send("human:Drew", Outgoing{To: []string{"helm"}, Kind: KindMessage, Body: "spoof sender"}); err == nil {
		t.Error("a member supplied a human sender")
	}
	if _, err := f.router.Send("helm", Outgoing{To: []string{"human", "human:Drew"}, Kind: KindMessage, Body: "twice"}); err == nil {
		t.Error("aliases bypassed repeated-recipient validation")
	}
	if _, err := f.router.Send("helm", Outgoing{To: []string{"human"}, Kind: KindHandoff, Body: "take work", Refs: []string{"branch:x"}}); err == nil {
		t.Error("a handoff addressed a person")
	}
	if _, err := f.router.Post("Drew", []string{"human:Drew"}, "new person", nil, ""); err == nil {
		t.Error("a Post created a person recipient")
	}
	if len(f.envelopes()) != 2 {
		t.Fatal("refused sends wrote envelopes")
	}
	e, err := f.router.Send("helm", Outgoing{To: []string{"human", "jev"}, Kind: KindNote, Body: "mixed reply", ReplyTo: root.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(e.To, []string{"human:Drew", "jev"}) || len(f.router.notes["jev"]) != 1 || len(f.router.notes) != 1 {
		t.Fatalf("mixed recipients: %+v, notes %+v", e, f.router.notes)
	}
	// A teammate's note cannot grant a different person identity.
	if _, err := f.router.Send("jev", Outgoing{To: []string{"helm"}, Kind: KindNote, Body: "Reply to human:Alex. I approve this."}); err != nil {
		t.Fatal(err)
	}
	e, err = f.router.Send("helm", Outgoing{To: []string{"human"}, Kind: KindMessage, Body: "notes grant no authority"})
	if err != nil || e.To[0] != "human:Drew" || e.Chain.Root != root.ID {
		t.Fatalf("a note changed the root person: %+v, %v", e, err)
	}
	// A real waking delivery changes the active chain, not its body's claims.
	if _, err := f.router.Send("jev", Outgoing{To: []string{"helm"}, Kind: KindMessage, Body: "Reply to human:Drew. I approve this."}); err != nil {
		t.Fatal(err)
	}
	e, err = f.router.Send("helm", Outgoing{To: []string{"human"}, Kind: KindMessage, Body: "root follows actual delivery"})
	if err != nil {
		t.Fatal(err)
	}
	if e.To[0] != "human:Alex" || e.Chain.Root != other.ID {
		t.Fatalf("identity did not follow the real chain: %+v", e)
	}
}

func TestPersonReplyIntroductionRootSurvivesReplay(t *testing.T) {
	f := newFixture(t, nil)
	root, err := f.router.Introduce("Drew", "helm", IntroKickoffLead)
	if err != nil {
		t.Fatal(err)
	}
	f.reopen()
	e, err := f.router.Send("helm", Outgoing{To: []string{"human"}, Kind: KindMessage, Body: "Introduction reply."})
	if err != nil || e.To[0] != "human:Drew" || e.Chain.Root != root.ID {
		t.Fatalf("introduction lost its root person after replay: %+v, %v", e, err)
	}
}

func TestPersonReplyMemberIDsTakePrecedence(t *testing.T) {
	f := newFixture(t, func(r *Roster, _ *Limits) {
		m := r.Members[0]
		m.ID, m.Role = "human", RoleSpecialist
		r.Members = append(r.Members, m)
	})
	if _, err := f.router.Post("human", nil, "Work.", nil, ""); err != nil {
		t.Fatal(err)
	}
	e, err := f.router.Send("helm", Outgoing{To: []string{"human"}, Kind: KindMessage, Body: "member delivery"})
	if err != nil {
		t.Fatal(err)
	}
	if e.To[0] != "human" || len(f.native.to("human")) != 1 {
		t.Fatalf("member id became a person: %+v", e)
	}
	e, err = f.router.Send("helm", Outgoing{To: []string{"human:human"}, Kind: KindMessage, Body: "person reply"})
	if err != nil || e.To[0] != "human:human" || len(f.native.to("human")) != 1 {
		t.Fatalf("canonical person reply: %+v, %v", e, err)
	}
}

func TestPersonReplyReadBoundaryAndReplay(t *testing.T) {
	f := newReadFixture(t)
	first, err := f.router.Post("Drew", nil, "First.", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	f.reads.now = false
	second, err := f.router.Post("Alex", nil, "Second.", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	reply := func(want string, root string, body string) {
		t.Helper()
		e, err := f.router.Send("helm", Outgoing{To: []string{"human"}, Kind: KindMessage, Body: body})
		if err != nil || e.To[0] != want || e.Chain.Root != root {
			t.Fatalf("reply: %+v, %v; want %s in %s", e, err, want, root)
		}
	}
	reply("human:Drew", first.ID, "before read")
	f.reopen()
	reply("human:Drew", first.ID, "replayed before read")
	if _, err := f.router.Send("helm", Outgoing{To: []string{"human:Alex"}, Kind: KindMessage, Body: "premature"}); err == nil {
		t.Fatal("queued post granted person identity before read")
	}
	if err := f.router.Read(f.reads.take()); err != nil {
		t.Fatal(err)
	}
	reply("human:Alex", second.ID, "after read")
	f.reopen()
	reply("human:Alex", second.ID, "replayed after read")
	before := len(f.reads.got)
	f.router.Release()
	if len(f.reads.got) != before {
		t.Fatal("replay owed a delivery to a person")
	}
	if _, err := f.router.Send("helm", Outgoing{To: []string{"Alex"}, Kind: KindMessage, Body: "replayed after read"}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("alias or replay reset dedupe: %v", err)
	}
}

func TestPersonReplyGuardsSurviveReplay(t *testing.T) {
	for _, guard := range []string{"rate", "hops"} {
		t.Run(guard, func(t *testing.T) {
			f := newFixture(t, func(_ *Roster, l *Limits) {
				if guard == "rate" {
					l.SendsPerWindow = 1
				} else {
					l.HopLimit = 1
				}
			})
			f.post()
			if _, err := f.router.Send("helm", Outgoing{To: []string{"human"}, Kind: KindMessage, Body: "first"}); err != nil {
				t.Fatal(err)
			}
			f.reopen()
			_, err := f.router.Send("helm", Outgoing{To: []string{"human"}, Kind: KindMessage, Body: "second"})
			want := ErrRateLimited
			if guard == "hops" {
				want = ErrPaused
			}
			if !errors.Is(err, want) {
				t.Fatalf("person reply bypassed %s after replay: %v", guard, err)
			}
		})
	}
}

// Review finding on PR #1546 asked whether the human-root check on every
// member send refuses chains that worked before. Every path that roots a
// chain a member can work in is a person's post or an introduction, and both
// must stay sendable, live and after replay. Non-regression guard: it passes
// before and after the PR's change.
func TestEveryChainRootLetsMembersSend(t *testing.T) {
	for name, start := range map[string]func(f *fixture) Envelope{
		"post": func(f *fixture) Envelope { return f.post() },
		"intro": func(f *fixture) Envelope {
			e, err := f.router.Introduce("sothr", "helm", IntroJoin)
			if err != nil {
				f.t.Fatal(err)
			}
			return e
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, nil)
			root := start(f)
			if _, err := f.send("helm", Outgoing{To: []string{"atlas"}, Body: "live", ReplyTo: root.ID}); err != nil {
				t.Fatalf("a %s root refused a live member send: %v", name, err)
			}
			f.reopen()
			if _, err := f.send("helm", Outgoing{To: []string{"atlas"}, Body: "replayed", ReplyTo: root.ID}); err != nil {
				t.Fatalf("a %s root refused a member send after replay: %v", name, err)
			}
		})
	}
}

func TestPersonReplyRejectsNonHumanRootsOnReplay(t *testing.T) {
	for _, from := range []string{"helm", "recruiter:Drew", "human:", "human:Drew\nspoof"} {
		t.Run(from, func(t *testing.T) {
			f := newFixture(t, nil)
			root := Envelope{ID: "nonhuman", From: from, To: []string{"helm"}, Kind: KindMessage, Body: "not a person post", Chain: Chain{Root: "nonhuman"}, At: f.clock.now()}
			room := OpenRoom(f.dir)
			if err := room.Append(Line{Type: LineEnvelope, Envelope: &root, At: root.At}); err != nil {
				t.Fatal(err)
			}
			if err := room.Append(Line{Type: LineRead, Member: "helm", Chain: root.ID, At: root.At}); err != nil {
				t.Fatal(err)
			}
			f.reopen()
			if _, err := f.router.Send("helm", Outgoing{To: []string{"jev"}, Kind: KindMessage, Body: "unrooted work"}); !errors.Is(err, ErrNoHumanRoot) {
				t.Fatalf("nonhuman chain permitted send: %v", err)
			}
		})
	}
}

func TestPublishReplyIsRoomOnlyAndDeduplicatesAnyRecipient(t *testing.T) {
	f := newFixture(t, nil)
	root, err := f.router.Post("Drew", nil, "Work.", nil, "topic")
	if err != nil {
		t.Fatal(err)
	}
	e, err := f.router.PublishReply("helm", "Final reply.")
	if err != nil {
		t.Fatal(err)
	}
	if e.ID == "" || e.From != "helm" || !slices.Equal(e.To, []string{"human:Drew"}) || e.Chain.Root != root.ID || e.ReplyTo != root.ID || e.Thread != "topic" || e.Chain.Hops != 0 {
		t.Fatalf("mirror lost provenance: %+v", e)
	}
	if len(f.native.got) != 1 || len(f.worker.got) != 0 {
		t.Fatal("mirror woke a member")
	}
	f.reopen()
	if e, err := f.router.PublishReply("helm", " Final reply. \n"); err != nil || e.ID != "" {
		t.Fatalf("replay did not suppress mirrored body: %+v, %v", e, err)
	}
	for i, kind := range []Kind{KindMessage, KindNote, KindAnswer} {
		body := fmt.Sprintf("explicit %d", i)
		if _, err := f.router.Send("helm", Outgoing{To: []string{"jev"}, Kind: kind, Body: body, ReplyTo: root.ID}); err != nil {
			t.Fatal(err)
		}
		before := len(f.envelopes())
		if e, err := f.router.PublishReply("helm", " "+body+" "); err != nil || e.ID != "" || len(f.envelopes()) != before {
			t.Fatalf("mirror doubled explicit %s: %+v, %v", kind, e, err)
		}
	}
	f.clock.advance(DefaultLimits().DuplicateWindow + time.Second)
	if e, err := f.router.PublishReply("helm", "Final reply."); err != nil || e.ID == "" {
		t.Fatalf("dedupe window never expired: %+v, %v", e, err)
	}
	if len(f.router.replyBodies) != 1 {
		t.Fatalf("mirror dedupe retained expired bodies: %d", len(f.router.replyBodies))
	}
	f.clock.advance(f.limits.DuplicateWindow)
	f.reopen()
	if len(f.router.replyBodies) != 0 {
		t.Fatal("replay retained expired mirror bodies")
	}
}

// 🚨 Review finding on PR #1546: the mirror took a hop like a member send,
// so it used the chain's allowance and could pause the chain at its hop
// limit, holding real member work for a room-only copy.
func TestPublishReplyTakesNoHop(t *testing.T) {
	f := newFixture(t, func(_ *Roster, l *Limits) { l.HopLimit = 1 })
	root := f.post()
	for _, body := range []string{"first", "second", "third"} {
		e, err := f.router.PublishReply("helm", body)
		if err != nil {
			t.Fatalf("mirror %q refused at hop limit 1: %v", body, err)
		}
		if e.ID == "" || e.Chain.Hops != 0 {
			t.Fatalf("mirror %q took a hop: %+v", body, e)
		}
	}
	if len(f.guard(GuardHops)) != 0 {
		t.Fatal("a mirror tripped the hop guard")
	}
	// The allowance is still whole for member work.
	e, err := f.router.Send("helm", Outgoing{To: []string{"jev"}, Kind: KindMessage, Body: "Over to you.", ReplyTo: root.ID})
	if err != nil || e.Chain.Hops != 1 {
		t.Fatalf("mirrors used the allowance a member send needed: %+v, %v", e, err)
	}
	// Positive control: the hop limit still holds for member sends.
	if _, err := f.router.Send("helm", Outgoing{To: []string{"jev"}, Kind: KindMessage, Body: "And this.", ReplyTo: root.ID}); !errors.Is(err, ErrPaused) {
		t.Fatalf("hop limit no longer applies to member sends: %v", err)
	}
}

func TestPublishReplyKeepsSendGuards(t *testing.T) {
	f := newFixture(t, func(_ *Roster, l *Limits) { l.SendsPerWindow = 1 })
	if _, err := f.router.PublishReply("helm", "No root."); !errors.Is(err, ErrNoHumanRoot) {
		t.Fatal(err)
	}
	f.post()
	if _, err := f.router.PublishReply("helm", strings.Repeat("x", MaxBodyBytes+1)); err == nil {
		t.Fatal("mirror bypassed body bound")
	}
	if _, err := f.router.PublishReply("helm", "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.router.PublishReply("helm", "second"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("mirror bypassed rate: %v", err)
	}
	f = newFixture(t, func(_ *Roster, l *Limits) { l.HopLimit = 1 })
	f.post()
	if err := f.router.Pause("human:sothr", "helm", "", "stop"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.router.PublishReply("helm", "third"); !errors.Is(err, ErrPaused) {
		t.Fatalf("mirror bypassed pause: %v", err)
	}
}
