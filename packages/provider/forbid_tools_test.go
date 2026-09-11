package provider

import (
	"encoding/json"
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
	c := &openaiClient{name: "openai"}
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

// The three clients that send no tool-choice field cannot enforce the ban, so
// they drop the array and give up the saving. That is the contract on
// Request.ForbidTools, and it keeps the safety property on every provider
// rather than on the two that happen to be wired.
func TestForbidToolsUnwiredClientsDropTheArray(t *testing.T) {
	cases := []struct {
		name  string
		model string
		count func(t *testing.T, req Request) int
	}{
		{"anthropic", "claude-sonnet-4-5", func(t *testing.T, req Request) int {
			out, err := (&anthropicClient{}).buildRequest(req)
			if err != nil {
				t.Fatal(err)
			}
			return len(out.Tools)
		}},
		{"gemini", "gemini-2.5-flash", func(t *testing.T, req Request) int {
			out, _, err := (&geminiClient{}).buildRequest(req)
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
			out, err := (&bedrockClient{region: "us-east-1"}).buildRequest(req)
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
	out, err := (&bedrockClient{region: "us-east-1"}).buildRequest(req)
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
