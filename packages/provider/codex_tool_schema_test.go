package provider

import (
	"encoding/json"
	"reflect"
	"testing"
)

// Responses may normalize an omitted strict flag into strict mode, making
// optional parameters required. For ticket_create, that forces a terminal
// status even though omitting status is the only way to create a draft.
func TestCodexFunctionToolsPreserveOptionalParameters(t *testing.T) {
	const schema = `{
		"type":"object",
		"properties":{
			"title":{"type":"string"},
			"status":{"type":"string","enum":["done","archived"]},
			"references":{"type":"array","items":{
				"type":"object",
				"properties":{"ref":{"type":"string"},"path":{"type":"string"}},
				"required":["ref"]
			}}
		},
		"required":["title"]
	}`
	client := NewOpenAICodex("token", "account", "").(*codexClient)
	request, err := client.buildRequest(Request{
		Model: "gpt-5.5",
		Tools: []Tool{
			{Name: "ticket_create", Schema: json.RawMessage(schema)},
			{Name: "no_parameters"},
		},
		ImageOutput: &ImageOutputConfig{Size: "1024x1024"},
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Tools []struct {
			Type       string          `json:"type"`
			Name       string          `json:"name"`
			Strict     *bool           `json:"strict"`
			Parameters json.RawMessage `json:"parameters"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatal(err)
	}
	if len(wire.Tools) != 3 {
		t.Fatalf("got %d tools, want two functions and image_generation", len(wire.Tools))
	}
	wantSchemas := map[string]string{
		"ticket_create": schema,
		"no_parameters": `{"type":"object","properties":{}}`,
	}
	for _, tool := range wire.Tools {
		if tool.Type == "image_generation" {
			if tool.Strict != nil {
				t.Error("strict belongs to function tools, not image_generation")
			}
			continue
		}
		if tool.Strict == nil {
			t.Errorf("%s: strict is absent; Responses may require optional parameters", tool.Name)
		} else if *tool.Strict {
			t.Errorf("%s: strict must be false to preserve optional parameters", tool.Name)
		}
		wantSchema, ok := wantSchemas[tool.Name]
		if !ok {
			t.Fatalf("unexpected function %q", tool.Name)
		}
		var got, want any
		if err := json.Unmarshal(tool.Parameters, &got); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(wantSchema), &want); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: parameter schema changed: %s", tool.Name, tool.Parameters)
		}
	}
}
