//go:build terva_scripting

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/internal/coretest"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

func runResultScript(t *testing.T, call hostCallFn, script string) core.ToolResult {
	t.Helper()
	tool := &CodeExecutionTool{HostCall: call, Catalog: NewDisclosureCatalog([]CatalogEntry{pluginEntry("mcp_fixture_lookup")})}
	args, _ := json.Marshal(codeExecArgs{Script: script})
	res, err := tool.Execute(context.Background(), args, nil)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestCodeExecutionStructuredResultEnvelope(t *testing.T) {
	calls := 0
	res := runResultScript(t, func(ctx context.Context, name string, args json.RawMessage) (core.ToolResult, error) {
		calls++
		if !isScriptCall(ctx) || name != "mcp_fixture_lookup" || !strings.Contains(string(args), `"q":"needle"`) {
			t.Error("structured result bypassed the normal dispatch")
		}
		return core.ToolResult{Content: []provider.Content{provider.TextBlock{Text: "human display"}, provider.ImageBlock{MimeType: "image/png", Data: []byte{1, 2, 3}}}, StructuredContent: json.RawMessage(`{"count":7,"items":[{"name":"found"}]}`), Details: map[string]any{"private": "do-not-expose"}}, nil
	}, `const r = callResult("mcp_fixture_lookup", {q:"needle"}); print(r.structuredContent.count, r.structuredContent.items[0].name, r.isError, r.attachments[0].bytes, r.attachments[0].omitted); print(JSON.stringify(r));`)
	text := ceResultText(res)
	if res.IsError || calls != 1 || !strings.Contains(text, "7 found false 3 true") || strings.Contains(text, "do-not-expose") || strings.Contains(text, `"data"`) {
		t.Fatalf("wrong envelope: %s", text)
	}
}

func TestScriptResultStableAttachmentsAndNumberRepresentation(t *testing.T) {
	res := runResultScript(t, func(context.Context, string, json.RawMessage) (core.ToolResult, error) {
		return core.ToolResult{StructuredContent: json.RawMessage(`{"safe":9007199254740991,"exact":"9007199254740993"}`), Content: []provider.Content{provider.ReasoningBlock{Summary: "private reasoning"}}}, nil
	}, `const r=callResult("mcp_fixture_lookup",{});print(r.structuredContent.safe, r.structuredContent.exact, r.attachments[0].type);`)
	if res.IsError || !strings.Contains(ceResultText(res), "9007199254740991 9007199254740993 other") || strings.Contains(ceResultText(res), "private reasoning") {
		t.Fatalf("number or attachment contract changed: %s", ceResultText(res))
	}
}

func TestCodeExecutionDescriptionMatchesDisclosureAvailability(t *testing.T) {
	tool := &CodeExecutionTool{}
	if strings.Contains(tool.Description(), "callResult") {
		t.Fatal("unavailable disclosure advertised")
	}
	tool.Catalog = NewDisclosureCatalog([]CatalogEntry{pluginEntry("mcp_fixture_lookup")})
	if !strings.HasPrefix(tool.Description(), "Run a short JavaScript program.") || !strings.Contains(tool.Description(), "callResult") {
		t.Fatal("available disclosure or tool purpose missing")
	}
}

func TestCodeExecutionStructuredResultTextAndErrors(t *testing.T) {
	for _, isError := range []bool{false, true} {
		t.Run(fmt.Sprint(isError), func(t *testing.T) {
			host := func(context.Context, string, json.RawMessage) (core.ToolResult, error) {
				return core.ToolResult{Content: []provider.Content{provider.TextBlock{Text: `{"count":99}`}}, IsError: isError}, nil
			}
			res := runResultScript(t, host, `const r=callResult("mcp_fixture_lookup",{}); print(r.text, r.structuredContent===null,r.isError,r.attachments.length);`)
			if res.IsError || !strings.Contains(ceResultText(res), fmt.Sprintf(`{"count":99} true %t 0`, isError)) {
				t.Fatalf("text parsed or error lost: %s", ceResultText(res))
			}
			legacy := runResultScript(t, host, `print(call("mcp_fixture_lookup",{}));`)
			if legacy.IsError != isError || !strings.Contains(ceResultText(legacy), `{"count":99}`) {
				t.Fatalf("legacy behavior changed: %s", ceResultText(legacy))
			}
		})
	}
}

func TestCodeExecutionStructuredResultRefusals(t *testing.T) {
	for _, script := range []string{`let name="mcp_fixture_lookup";callResult(name,{})`, `callResult("bash",{})`, `const f=callResult;f("mcp_fixture_lookup",{})`} {
		res := runResultScript(t, func(context.Context, string, json.RawMessage) (core.ToolResult, error) {
			t.Fatal("refused script reached host")
			return core.ToolResult{}, nil
		}, script)
		if !res.IsError {
			t.Fatalf("unsafe script accepted: %s", script)
		}
	}
	denied := runResultScript(t, func(context.Context, string, json.RawMessage) (core.ToolResult, error) {
		return core.ToolResult{}, fmt.Errorf("fixture gate denied")
	}, `callResult("mcp_fixture_lookup",{});`)
	if !denied.IsError || !strings.Contains(ceResultText(denied), "fixture gate denied") {
		t.Fatal("dispatch failure did not throw")
	}
}

func TestCodeExecutionStructuredResultImportLimits(t *testing.T) {
	for _, tc := range []struct{ raw, reason string }{{`{`, "invalid structured JSON"}, {`{"blob":"` + strings.Repeat("x", maxScriptResultBytes) + `"}`, "import limit"}} {
		res := runResultScript(t, func(context.Context, string, json.RawMessage) (core.ToolResult, error) {
			return core.ToolResult{StructuredContent: json.RawMessage(tc.raw)}, nil
		}, `callResult("mcp_fixture_lookup",{});`)
		if !res.IsError || !strings.Contains(ceResultText(res), tc.reason) {
			t.Fatal("invalid or oversized structured result entered VM")
		}
	}
	res := runResultScript(t, func(context.Context, string, json.RawMessage) (core.ToolResult, error) {
		return core.ToolResult{Content: []provider.Content{provider.TextBlock{Text: strings.Repeat("x", maxScriptResultBytes)}}}, nil
	}, `callResult("mcp_fixture_lookup",{});`)
	if !res.IsError || !strings.Contains(ceResultText(res), "import limit") {
		t.Fatal("envelope overhead bypassed import limit")
	}
}

func TestCodeExecutionOmittedStructuredResultKeepsLegacyText(t *testing.T) {
	host := func(context.Context, string, json.RawMessage) (core.ToolResult, error) {
		return core.ToolResult{Content: []provider.Content{provider.TextBlock{Text: "display"}}, StructuredContentOmitted: true}, nil
	}
	legacy := runResultScript(t, host, `print(call("mcp_fixture_lookup",{}));`)
	if legacy.IsError || !strings.Contains(ceResultText(legacy), "display") {
		t.Fatalf("structured omission broke legacy call: %s", ceResultText(legacy))
	}
	structured := runResultScript(t, host, `callResult("mcp_fixture_lookup",{});`)
	if !structured.IsError || !strings.Contains(ceResultText(structured), "import limit") {
		t.Fatalf("structured omission looked like absent output: %s", ceResultText(structured))
	}
}

func TestCodeExecutionStructuredCallsReachStallDetector(t *testing.T) {
	ce := &CodeExecutionTool{Catalog: NewDisclosureCatalog([]CatalogEntry{pluginEntry("mcp_fixture_lookup")})}
	ce.HostCall = func(context.Context, string, json.RawMessage) (core.ToolResult, error) {
		return core.ToolResult{Content: []provider.Content{provider.TextBlock{Text: "no such file"}}, IsError: true}, nil
	}
	client := &stallScriptedClient{script: func(n int) []provider.Event {
		if n >= 5 {
			return finishedTurn()
		}
		args, _ := json.Marshal(codeExecArgs{Script: fmt.Sprintf(`callResult("mcp_fixture_lookup",{});print("attempt %d");`, n)})
		return []provider.Event{provider.EventStart{Provider: "scripted"}, provider.EventDone{Stop: provider.StopToolUse, Message: provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{provider.ToolCallBlock{ID: fmt.Sprintf("call-%d", n), Name: "code_execution", Arguments: args}}}}}
	}}
	ag, _ := coretest.NewAgentWithStall(client, "m", "fixture", core.Registry{"code_execution": ce}, core.WithMaxSteps(20))
	if err := ag.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(nudges(client.calls()), "\n")
	if !strings.Contains(got, "mcp_fixture_lookup") {
		t.Fatalf("structured failures did not reach detector: %s", got)
	}
}
