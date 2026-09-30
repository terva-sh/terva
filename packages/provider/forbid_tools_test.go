package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ForbidTools lets a side computation advertise the conversation's own tools so
// its prompt matches the cached prefix, while banning the CALL rather than
// hiding the tools. Each client either enforces that ban with an explicit
// tool_choice and keeps the saving, or drops the array and gives it up. What no
// client may do is advertise a tool it cannot hold the model back from.
//
// Every case below asserts BOTH directions. A client that dropped its tools for
// some unrelated reason would satisfy a one-way "no tools under the flag" check
// while proving nothing about the flag.

func forbidTools() []Tool {
	return []Tool{{Name: "read", Description: "read a file", Schema: []byte(`{"type":"object"}`)}}
}

func forbidMessages() []Message {
	return []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}}
}

// The codex builder enforces the ban, so the array stays on the wire and
// tool_choice carries it. The array must also be BYTE-identical to the allowed
// one: a suggestion whose tools block differs from the main line's diverges from
// the cached prefix exactly where omitting it did, and the field buys nothing.
func TestForbidToolsCodexAdvertisesToolsAndBansTheCall(t *testing.T) {
	c := NewOpenAICodex("token", "acct", "").(*codexClient)
	req := Request{Model: "gpt-5.5", Tools: forbidTools(), Messages: forbidMessages()}

	allowed, err := c.buildRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(allowed.Tools) != 1 || allowed.ToolChoice != "auto" {
		t.Fatalf("without the flag: %d tools, tool_choice %q; want 1 and auto", len(allowed.Tools), allowed.ToolChoice)
	}

	req.ForbidTools = true
	forbidden, err := c.buildRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(forbidden.Tools) != 1 {
		t.Fatalf("%d tools under ForbidTools, want 1: dropping the array is the cache miss this field exists to remove", len(forbidden.Tools))
	}
	if forbidden.ToolChoice != "none" {
		t.Fatalf("tool_choice = %q, want none", forbidden.ToolChoice)
	}

	was, err := json.Marshal(allowed.Tools)
	if err != nil {
		t.Fatal(err)
	}
	now, err := json.Marshal(forbidden.Tools)
	if err != nil {
		t.Fatal(err)
	}
	if string(was) != string(now) {
		t.Fatalf("the tools block changed under the flag, so the prefix no longer matches the main line:\n allowed:   %s\n forbidden: %s", was, now)
	}
}

// tool_choice governs the function tools. Whether it also holds back a built-in
// tool is undocumented, and a caller sets ForbidTools because a call would be
// unsafe, so the one tool whose ban is unverified must not go on the wire.
func TestForbidToolsCodexDropsTheBuiltInImageTool(t *testing.T) {
	c := NewOpenAICodex("token", "acct", "").(*codexClient)
	wire, err := c.buildRequest(Request{
		Model:       "gpt-5.5",
		Tools:       forbidTools(),
		Messages:    forbidMessages(),
		ImageOutput: &ImageOutputConfig{Size: "1024x1024", Quality: "low"},
		ForbidTools: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	sawFunction := false
	for _, tl := range wire.Tools {
		switch v := tl.(type) {
		case codexImageTool:
			t.Fatal("image_generation advertised under ForbidTools: tool_choice covers the function tools, and its hold on a built-in tool is unverified")
		case codexTool:
			if v.Name == "read" {
				sawFunction = true
			}
		}
	}
	if !sawFunction {
		t.Fatal("the function tool went missing, so the prefix no longer matches the main line")
	}
}

// The chat-completions builder enforces the ban the same way.
func TestForbidToolsOpenAIAdvertisesToolsAndBansTheCall(t *testing.T) {
	c := &openaiClient{catalogRef: catalogRef{testReg}, name: "openai"}
	req := Request{Model: "gpt-4o", Tools: forbidTools(), Messages: forbidMessages()}

	allowed, err := c.buildRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(allowed.Tools) != 1 || allowed.ToolChoice != "auto" {
		t.Fatalf("without the flag: %d tools, tool_choice %q; want 1 and auto", len(allowed.Tools), allowed.ToolChoice)
	}

	req.ForbidTools = true
	forbidden, err := c.buildRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(forbidden.Tools) != 1 {
		t.Fatalf("%d tools under ForbidTools, want 1: the array is what matches the cached prefix", len(forbidden.Tools))
	}
	if forbidden.ToolChoice != "none" {
		t.Fatalf("tool_choice = %q, want none", forbidden.ToolChoice)
	}
}

// The two clients that neither send a tool choice nor withhold calls cannot
// enforce the ban, so they drop the array and give up the saving. That is the
// contract on Request.ForbidTools, and it keeps the safety property on every
// provider rather than on the ones that happen to be wired.
func TestForbidToolsUnwiredClientsDropTheArray(t *testing.T) {
	cases := []struct {
		name  string
		model string
		count func(t *testing.T, req Request) int
	}{
		{"gemini", "gemini-2.5-flash", func(t *testing.T, req Request) int {
			out, _, err := (&geminiClient{catalogRef: catalogRef{testReg}}).buildRequest(req)
			if err != nil {
				t.Fatal(err)
			}
			n := 0
			for _, tl := range out.Tools {
				n += len(tl.FunctionDeclarations)
			}
			return n
		}},
		{"bedrock", "anthropic.claude-sonnet-4-5-20250929-v1:0", func(t *testing.T, req Request) int {
			out, err := (&bedrockClient{catalogRef: catalogRef{testReg}, region: "us-east-1"}).buildRequest(req)
			if err != nil {
				t.Fatal(err)
			}
			if out.ToolConfig == nil {
				return 0
			}
			return len(out.ToolConfig.Tools)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := Request{Model: tc.model, Tools: forbidTools(), Messages: forbidMessages()}
			if n := tc.count(t, req); n != 1 {
				t.Fatalf("without the flag: %d tools, want 1. This client advertises no tools for a reason of its own, so the assertion below would pass without testing anything", n)
			}
			req.ForbidTools = true
			if n := tc.count(t, req); n != 0 {
				t.Fatalf("%d tools under ForbidTools, want 0: this client cannot stop a call, so it must not advertise a tool", n)
			}
		})
	}
}

// Anthropic keeps the tools under the ban and sends no tool_choice at all.
// Anthropic invalidates the cached messages when tool_choice changes, so a
// tool_choice of "none" would rewrite the whole transcript on every side
// request (TKT-01M2ZT3SK). The body under the flag must therefore be
// byte-identical to the body without it. The ban rides on the stream instead:
// see TestAnthropicWithholdsACallUnderForbidTools.
func TestForbidToolsAnthropicSendsTheSameBody(t *testing.T) {
	c := &anthropicClient{catalogRef: catalogRef{testReg}}
	req := Request{Model: "claude-sonnet-4-5", Tools: forbidTools(), Messages: forbidMessages()}
	allowed, err := c.buildRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(allowed.Tools) != 1 {
		t.Fatalf("without the flag: %d tools, want 1, so the comparison below would prove nothing", len(allowed.Tools))
	}
	req.ForbidTools = true
	forbidden, err := c.buildRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	a, err := json.Marshal(allowed)
	if err != nil {
		t.Fatal(err)
	}
	f, err := json.Marshal(forbidden)
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(f) {
		t.Fatalf("the body under ForbidTools differs, so the side request misses the conversation's cache:\n allowed:   %s\n forbidden: %s", a, f)
	}
	if strings.Contains(string(f), "tool_choice") {
		t.Fatalf("the body carries a tool_choice, which invalidates Anthropic's cached messages: %s", f)
	}
}

// The model can still answer a forbidden request with a call, because nothing
// on the wire stops it. No call may reach the caller: the tool events are
// dropped and the call leaves the assembled message. The text beside it
// arrives, and the stop reason still says the model tried.
//
// The same stream without the flag must deliver the call, so the fixture is
// shown to carry one.
func TestAnthropicWithholdsACallUnderForbidTools(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, f := range []string{
			`{"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":0}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"run the tests"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"read","input":{}}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"x\"}"}}`,
			`{"type":"content_block_stop","index":1}`,
			`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`,
			`{"type":"message_stop"}`,
		} {
			var typ struct{ Type string }
			_ = json.Unmarshal([]byte(f), &typ)
			_, _ = w.Write([]byte("event: " + typ.Type + "\ndata: " + f + "\n\n"))
		}
	}))
	defer srv.Close()

	run := func(forbid bool) (tools int, text string, done EventDone) {
		t.Helper()
		evs, err := NewAnthropic("k", srv.URL).Stream(context.Background(),
			Request{Model: "claude-opus-5", Tools: forbidTools(), Messages: forbidMessages(), ForbidTools: forbid})
		if err != nil {
			t.Fatal(err)
		}
		for ev := range evs {
			switch e := ev.(type) {
			case EventToolStart, EventToolArgs, EventToolEnd:
				tools++
			case EventTextDelta:
				text += e.Delta
			case EventDone:
				done = e
			}
		}
		if done.Err != nil {
			t.Fatalf("stream error: %v", done.Err)
		}
		return tools, text, done
	}
	calls := func(m Message) (n int) {
		for _, c := range m.Content {
			if _, ok := c.(ToolCallBlock); ok {
				n++
			}
		}
		return n
	}

	tools, _, done := run(false)
	if tools == 0 || calls(done.Message) != 1 {
		t.Fatalf("without the flag the fixture delivered %d tool events and %d calls; it must carry one call", tools, calls(done.Message))
	}

	tools, text, done := run(true)
	if tools != 0 {
		t.Fatalf("%d tool events reached the caller under ForbidTools", tools)
	}
	if n := calls(done.Message); n != 0 {
		t.Fatalf("%d calls in the assembled message under ForbidTools", n)
	}
	if text != "run the tests" {
		t.Fatalf("text = %q; the text beside a withheld call must still arrive", text)
	}
	if done.Stop != StopToolUse {
		t.Fatalf("stop = %v, want StopToolUse, which tells the caller the model tried a call", done.Stop)
	}
}

// Bedrock rejects a request whose history holds tool blocks unless toolConfig
// is present, so a forbidden request over such a transcript falls into the stub
// branch. That is exactly what a no-tools request already did, which is what
// makes the change byte-identical for every existing caller.
func TestForbidToolsBedrockKeepsTheStubOverToolBlocks(t *testing.T) {
	req := Request{
		Model: "anthropic.claude-sonnet-4-5-20250929-v1:0",
		Tools: forbidTools(),
		Messages: []Message{
			{Role: RoleUser, Content: []Content{TextBlock{Text: "read the file"}}},
			{Role: RoleAssistant, Content: []Content{ToolCallBlock{ID: "t1", Name: "read", Arguments: json.RawMessage(`{}`)}}},
			{Role: RoleUser, Content: []Content{ToolResultBlock{CallID: "t1", Content: []Content{TextBlock{Text: "ok"}}}}},
		},
		ForbidTools: true,
	}
	out, err := (&bedrockClient{catalogRef: catalogRef{testReg}, region: "us-east-1"}).buildRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if out.ToolConfig == nil {
		t.Fatal("no toolConfig over a transcript with tool blocks; Bedrock rejects that request")
	}
	for _, ts := range out.ToolConfig.Tools {
		if ts.ToolSpec.Name == "read" {
			t.Fatal("the real tool was advertised under ForbidTools; only the placeholder may ride here")
		}
	}
}

// WireTools is the one-line form of the contract, for a client with no way to
// enforce the ban.
func TestWireToolsDropsToolsOnlyUnderTheFlag(t *testing.T) {
	req := Request{Tools: forbidTools()}
	if got := req.WireTools(); len(got) != 1 {
		t.Fatalf("WireTools returned %d tools without the flag, want 1", len(got))
	}
	req.ForbidTools = true
	if got := req.WireTools(); got != nil {
		t.Fatalf("WireTools returned %v under the flag, want nil", got)
	}
}

// EnforcesToolBan is a claim about the wire, so it is checked against the wire.
// Each real client reports true exactly when its forbidden request still
// carries the tools. A client that flipped the capability without the wire
// behind it would let the next-step offer spend a full transcript read it
// believes is cached, and one that kept tools without declaring it would lose
// the offer for no reason.
func TestEnforcesToolBanMatchesWhatTheClientSends(t *testing.T) {
	cases := []struct {
		name   string
		client Client
		model  string
		kept   func(t *testing.T, req Request) bool
	}{
		{"openai", &openaiClient{catalogRef: catalogRef{testReg}, name: "openai"}, "gpt-4o", func(t *testing.T, req Request) bool {
			out, err := (&openaiClient{catalogRef: catalogRef{testReg}, name: "openai"}).buildRequest(req)
			if err != nil {
				t.Fatal(err)
			}
			return len(out.Tools) > 0
		}},
		{"codex", NewOpenAICodex("token", "acct", ""), "gpt-5.5", func(t *testing.T, req Request) bool {
			out, err := NewOpenAICodex("token", "acct", "").(*codexClient).buildRequest(req)
			if err != nil {
				t.Fatal(err)
			}
			return len(out.Tools) > 0
		}},
		{"anthropic", &anthropicClient{catalogRef: catalogRef{testReg}}, "claude-sonnet-4-5", func(t *testing.T, req Request) bool {
			out, err := (&anthropicClient{catalogRef: catalogRef{testReg}}).buildRequest(req)
			if err != nil {
				t.Fatal(err)
			}
			return len(out.Tools) > 0
		}},
		{"gemini", &geminiClient{catalogRef: catalogRef{testReg}}, "gemini-2.5-flash", func(t *testing.T, req Request) bool {
			out, _, err := (&geminiClient{catalogRef: catalogRef{testReg}}).buildRequest(req)
			if err != nil {
				t.Fatal(err)
			}
			return len(out.Tools) > 0
		}},
		{"bedrock", &bedrockClient{catalogRef: catalogRef{testReg}, region: "us-east-1"}, "anthropic.claude-sonnet-4-5-20250929-v1:0", func(t *testing.T, req Request) bool {
			out, err := (&bedrockClient{catalogRef: catalogRef{testReg}, region: "us-east-1"}).buildRequest(req)
			if err != nil {
				t.Fatal(err)
			}
			return out.ToolConfig != nil && len(out.ToolConfig.Tools) > 0
		}},
	}
	enforcing := 0
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := Request{Model: tc.model, Tools: forbidTools(), Messages: forbidMessages(), ForbidTools: true}
			if got, want := clientCaps(tc.client).EnforcesToolBan, tc.kept(t, req); got != want {
				t.Fatalf("EnforcesToolBan = %v, but the forbidden request keeps its tools: %v", got, want)
			}
			if clientCaps(tc.client).EnforcesToolBan {
				enforcing++
			}
		})
	}
	// A positive control: at least one client enforces, so a capability that
	// read false everywhere cannot pass by agreeing with a wire that also
	// dropped everything.
	if enforcing == 0 {
		t.Fatal("no client reports EnforcesToolBan; the comparison above proved nothing")
	}
	// Through a wrapper too, the way openai-responses ships.
	if !ToolBanKeepsCache(&renamedClient{inner: NewOpenAICodex("token", "acct", "")}, "gpt-6-sol") {
		t.Fatal("a wrapped codex client lost EnforcesToolBan")
	}
}

// A wire that keeps the tools is not enough when a gateway serves Claude on
// it. The gateway forwards tool_choice and the thinking settings, and either
// change invalidates Anthropic's cached messages, so the next-step offer would
// pay for the whole transcript again. The same wire serving another model
// still reads the cache, and that half keeps the check from refusing
// everything.
func TestToolBanKeepsCacheOnlyOffClaudeBehindAChoice(t *testing.T) {
	openRouter := NewOpenRouter("token", "")
	compat := NewOpenAI("key", "https://gateway.example/v1")
	for _, tc := range []struct {
		name   string
		client Client
		model  string
		want   bool
	}{
		{"openrouter serving gpt", openRouter, "openai/gpt-6-sol", true},
		{"openrouter serving claude", openRouter, "anthropic/claude-opus-4.8", false},
		{"a compatible gateway serving claude", compat, "claude-sonnet-4-5", false},
		{"a compatible gateway serving claude, any case", compat, "Claude-Opus-5-5", false},
		{"codex", NewOpenAICodex("token", "acct", ""), "gpt-6-sol", true},
		// Anthropic bans the call without a tool_choice, so Claude keeps its cache.
		{"anthropic", &anthropicClient{catalogRef: catalogRef{testReg}}, "claude-sonnet-4-5", true},
		{"gemini", &geminiClient{catalogRef: catalogRef{testReg}}, "gemini-2.5-flash", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ToolBanKeepsCache(tc.client, tc.model); got != tc.want {
				t.Fatalf("ToolBanKeepsCache(%s, %q) = %v, want %v", tc.client.Name(), tc.model, got, tc.want)
			}
		})
	}
}
