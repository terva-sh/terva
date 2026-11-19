package provider

import (
	"fmt"
	"strings"
	"testing"
)

func responsesArgumentsFrame(kind string, index int, args string) string {
	if kind == "response.output_item.done" {
		return fmt.Sprintf(`{"type":%q,"output_index":%d,"item":{"type":"function_call","arguments":%q}}`, kind, index, args)
	}
	return fmt.Sprintf(`{"type":%q,"output_index":%d,"arguments":%q}`, kind, index, args)
}

func TestResponsesFinalArguments(t *testing.T) {
	const full = `{"path":"a.txt"}`
	const other = `{"path":"b.txt"}`
	for _, tc := range []struct {
		name       string
		delta      string
		finals     []string
		want       string
		wantRaw    string
		wantStream string
	}{
		{"arguments done only", "", []string{responsesArgumentsFrame("response.function_call_arguments.done", 0, full)}, full, "", full},
		{"item done only", "", []string{responsesArgumentsFrame("response.output_item.done", 0, full)}, full, "", full},
		{"partial deltas", `{"path":`, []string{responsesArgumentsFrame("response.function_call_arguments.done", 0, full), responsesArgumentsFrame("response.output_item.done", 0, full)}, full, "", full},
		{"complete deltas", full, []string{responsesArgumentsFrame("response.function_call_arguments.done", 0, full), responsesArgumentsFrame("response.output_item.done", 0, full)}, full, "", full},
		{"deltas without snapshots", full, nil, full, "", full},
		{"missing arguments field", full, []string{`{"type":"response.function_call_arguments.done","output_index":0}`, `{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call"}}`}, full, "", full},
		{"item snapshot wins", full, []string{responsesArgumentsFrame("response.function_call_arguments.done", 0, full), responsesArgumentsFrame("response.output_item.done", 0, other)}, other, "", full},
		{"conflicting partial snapshot then extension", `{"path":"a`, []string{responsesArgumentsFrame("response.function_call_arguments.done", 0, `{"path":"b`), responsesArgumentsFrame("response.output_item.done", 0, other)}, other, "", `{"path":"a`},
		{"conflicting complete snapshot then extension", full, []string{responsesArgumentsFrame("response.function_call_arguments.done", 0, other), responsesArgumentsFrame("response.output_item.done", 0, other+" ")}, other + " ", "", full},
		{"shorter snapshot then extension", full, []string{responsesArgumentsFrame("response.function_call_arguments.done", 0, `{"path":`), responsesArgumentsFrame("response.output_item.done", 0, full)}, full, "", full},
		{"late arguments snapshot", "", []string{responsesArgumentsFrame("response.output_item.done", 0, full), responsesArgumentsFrame("response.function_call_arguments.done", 0, other)}, full, "", full},
		{"invalid final snapshot", full, []string{responsesArgumentsFrame("response.output_item.done", 0, `{"path":`)}, "{}", `{"path":`, full},
		{"invalid arguments snapshot", "", []string{responsesArgumentsFrame("response.function_call_arguments.done", 0, `{"path":`)}, "{}", `{"path":`, `{"path":`},
		{"empty final snapshot", full, []string{responsesArgumentsFrame("response.output_item.done", 0, "")}, "{}", "", full},
		{"duplicate item completion", "", []string{responsesArgumentsFrame("response.output_item.done", 0, full), responsesArgumentsFrame("response.output_item.done", 0, other)}, full, "", full},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frames := []string{cutCallFrames[0]}
			if tc.delta != "" {
				frames = append(frames, fmt.Sprintf(`{"type":"response.function_call_arguments.delta","output_index":0,"delta":%q}`, tc.delta))
			}
			frames = append(frames, tc.finals...)
			frames = append(frames, `{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call"}}`, `{"type":"response.completed","response":{"status":"completed"}}`)
			for route, evs := range streamResponsesEvents(t, frames...) {
				t.Run(route, func(t *testing.T) {
					done := doneOf(t, evs)
					if done.Err != nil || done.Stop != StopToolUse || len(done.Message.Content) != 1 {
						t.Fatalf("done = %+v", done)
					}
					call, ok := done.Message.Content[0].(ToolCallBlock)
					if !ok || string(call.Arguments) != tc.want || call.RawArguments != tc.wantRaw {
						t.Fatalf("call = %+v, want arguments %s, raw %q", call, tc.want, tc.wantRaw)
					}
					var args strings.Builder
					starts, ends := 0, 0
					for _, ev := range evs {
						switch e := ev.(type) {
						case EventToolStart:
							starts++
						case EventToolArgs:
							if ends != 0 || e.ID != call.ID {
								t.Fatalf("argument event after end or for wrong call: %+v", e)
							}
							args.WriteString(e.Delta)
						case EventToolEnd:
							ends++
						}
					}
					if starts != 1 || ends != 1 || args.String() != tc.wantStream {
						t.Fatalf("starts=%d ends=%d arguments=%q, want one each and %q", starts, ends, args.String(), tc.wantStream)
					}
				})
			}
		})
	}
}

func TestResponsesFinalArgumentsInterleaved(t *testing.T) {
	frames := []string{
		`{"type":"response.output_item.added","output_index":3,"item":{"type":"function_call","call_id":"first","name":"read"}}`,
		`{"type":"response.output_item.added","output_index":7,"item":{"type":"function_call","call_id":"second","name":"read"}}`,
		`{"type":"response.function_call_arguments.delta","output_index":7,"delta":"{\"path\":"}`,
		responsesArgumentsFrame("response.function_call_arguments.done", 3, `{"path":"a.txt"}`),
		responsesArgumentsFrame("response.output_item.done", 7, `{"path":"b.txt"}`),
		responsesArgumentsFrame("response.output_item.done", 3, `{"path":"a.txt"}`),
		`{"type":"response.function_call_arguments.delta","output_index":7,"delta":"ignored"}`,
		`{"type":"response.completed","response":{"status":"completed"}}`,
	}
	for route, evs := range streamResponsesEvents(t, frames...) {
		t.Run(route, func(t *testing.T) {
			done := doneOf(t, evs)
			if done.Err != nil || len(done.Message.Content) != 2 {
				t.Fatalf("done = %+v", done)
			}
			args, ends := map[string]string{}, map[string]int{}
			for _, ev := range evs {
				switch e := ev.(type) {
				case EventToolArgs:
					args[e.ID] += e.Delta
				case EventToolEnd:
					ends[e.ID]++
				}
			}
			for j, want := range []struct{ id, args string }{{"first", `{"path":"a.txt"}`}, {"second", `{"path":"b.txt"}`}} {
				call, ok := done.Message.Content[j].(ToolCallBlock)
				if !ok || call.ID != want.id || string(call.Arguments) != want.args || args[want.id] != want.args || ends[want.id] != 1 {
					t.Fatalf("call=%+v streamed=%q ends=%d, want %+v", call, args[want.id], ends[want.id], want)
				}
			}
		})
	}
}
