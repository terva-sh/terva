package core

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
)

func TestCompactionRetentionPartition(t *testing.T) {
	model, err := provider.FindModel("", "claude-haiku-4-5")
	if err != nil {
		t.Fatal(err)
	}
	budget := int(float64(model.EffectiveContextWindow()) * KeepTailMaxFraction)
	big := strings.Repeat("x", 4*budget+100)
	user := func(text string) provider.Message {
		return provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: text}}}
	}
	old, recent := user("original task"), user("continue from here")
	write := call("w", "write", `{"path":"already-written.txt"}`)
	mixed := result("w", "write succeeded", false)
	mixed.Content = append(mixed.Content, provider.TextBlock{Text: "preserve this constraint too"})
	batch := call("w", "write", `{"path":"already-written.txt"}`)
	batch.Content = append(batch.Content, call("pending", "write", `{"path":"pending.txt"}`).Content...)
	cases := []struct {
		name string
		msgs []provider.Message
		keep int
		cut  int
	}{
		{"oversized user", []provider.Message{old, user("final constraint: " + big)}, 1, 2},
		{"oversized result", []provider.Message{old, write, result("w", big, false), recent}, 3, 3},
		{"call outside count", []provider.Message{old, write, result("w", "write succeeded", false), recent}, 2, 3},
		{"mixed orphan message", []provider.Message{old, write, mixed, recent}, 2, 3},
		{"boundary removes another call", []provider.Message{old, write, call("b", "write", `{"path":"second.txt"}`), result("w", "written", false), result("b", "also written", false), recent}, 4, 5},
		{"failed result outside count", []provider.Message{old, write, result("w", "permission denied", true), recent}, 2, 3},
		{"partial batch retains unknown outcome", []provider.Message{old, batch, result("w", "written", false), recent}, 1, 3},
		{"call outside budget", []provider.Message{old, call("w", "write", fmt.Sprintf(`{"path":%q}`, big)), result("w", "write succeeded", false), recent}, 3, 3},
		{"pair fits", []provider.Message{old, write, result("w", "write succeeded", false), recent}, 3, 1},
		{"zero tail", []provider.Message{old, write, result("w", "write succeeded", false), recent}, 0, 4},
		{"negative tail", []provider.Message{old, recent}, -1, 2},
	}
	for _, mode := range []string{"cold", "warm", "fallback", "midturn"} {
		for _, tc := range cases {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				client := &scriptedClient{name: "scripted", script: func(n int, req provider.Request) ([]provider.Event, error) {
					if mode == "fallback" && req.EphemeralContext != "" {
						return calledATool(100), nil
					}
					return saidText("checkpoint", 100), nil
				}}
				a := NewAgent(client, model.ID, "system", Registry{})
				a.ReadOnly = NewReadOnlySet("read")
				warmMode := mode == "warm" || mode == "fallback"
				a.SetCacheAwareCompaction(warmMode)
				if warmMode {
					if err := a.Prompt(context.Background(), "warm the prefix", nil, nil); err != nil {
						t.Fatal(err)
					}
				}
				a.SetMessages(tc.msgs)
				before := serializeTranscript(a.Messages())
				var observed []provider.Message
				var observedResult CompactResult
				a.AddTranscriptCompactedObserver(func(msgs []provider.Message, res CompactResult) {
					observed, observedResult = msgs, res
				})
				var res CompactResult
				var err error
				if mode == "midturn" {
					release, ok := a.acquire()
					if !ok {
						t.Fatal("could not acquire the turn slot")
					}
					res, err = a.compactMidTurn(context.Background(), tc.keep)
					release()
				} else {
					res, err = a.Compact(context.Background(), tc.keep, nil)
				}
				if err != nil {
					t.Fatal(err)
				}
				summarized := tc.msgs[:tc.cut]
				tail := tc.msgs[tc.cut:]
				next := a.Messages()
				if len(next) != 1+len(tail) || !reflect.DeepEqual(next[1:], tail) {
					t.Errorf("retained %d messages; want the unchanged suffix of %d messages at index %d", len(next)-1, len(tail), tc.cut)
				}
				if res.SupersededMessages != tc.cut {
					t.Errorf("superseded %d messages; want %d", res.SupersededMessages, tc.cut)
				}
				if want := len(serializeTranscript(summarized)) / 4; res.TokensBefore != want {
					t.Errorf("TokensBefore = %d; want %d for everything removed", res.TokensBefore, want)
				}
				ledger := executedActionsLedger(summarized, a.ReadOnly, a.CWD)
				body := "## Context Summary (compacted)\n\ncheckpoint"
				if ledger != "" {
					body += "\n\n" + ledger
				}
				if got := next[0].Content[0].(provider.TextBlock).Text; got != body {
					t.Errorf("checkpoint ledger does not account for the removed calls and results:\n got: %s\nwant: %s", got, body)
				}
				if !reflect.DeepEqual(observed, next) || observedResult != res {
					t.Error("persistence observer received a different checkpoint or accounting")
				}
				if serializeTranscript(tc.msgs) != before {
					t.Error("compaction mutated the source transcript")
				}
				calls := client.calls()
				if warmMode {
					calls = calls[1:]
					warm := calls[0]
					wireMessages := append([]provider.Message(nil), tc.msgs...)
					if !reflect.DeepEqual(warm.Messages, repairToolUseResultPairs(wireMessages)) {
						t.Error("warm summary request changed the full transcript")
					}
					if warm.EphemeralContext != warmCompactInstruction(len(tail), false) {
						t.Errorf("warm summary instruction does not describe the actual %d retained messages", len(tail))
					}
				}
				wantStrategy := CompactCold
				if mode == "warm" {
					wantStrategy = CompactWarm
				} else {
					cold := calls[len(calls)-1]
					want := coldCompactRequest(promptPrefix{model: model.ID}, serializeTranscript(summarized), mode == "midturn")
					if len(cold.Messages) != 1 || !reflect.DeepEqual(cold.Messages[0].Content, want.Messages[0].Content) {
						t.Error("cold summary input omits content removed from the tail")
					}
					if mode == "fallback" {
						wantStrategy = CompactWarmFellBack
					}
				}
				if res.Strategy != wantStrategy {
					t.Errorf("strategy = %s; want %s", res.Strategy, wantStrategy)
				}
			})
		}
	}
}

func TestCompactionRetentionNoOp(t *testing.T) {
	for _, n := range []int{0, 2, 4} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			a := NewAgent(nil, "unknown-model", "system", Registry{})
			msgs := make([]provider.Message, n)
			for i := range msgs {
				msgs[i] = provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "keep me"}}}
			}
			a.SetMessages(msgs)
			before := a.Messages()
			if _, err := a.Compact(context.Background(), 4, nil); !errors.Is(err, ErrNothingToCompact) {
				t.Fatalf("Compact returned %v; want ErrNothingToCompact", err)
			}
			if !reflect.DeepEqual(a.Messages(), before) {
				t.Error("no-op compaction changed the transcript")
			}
		})
	}
}
