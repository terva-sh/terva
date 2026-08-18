package provider

import (
	"encoding/base64"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// imageCanary is bytes that no decoder can read, so nothing on the way to the
// wire re-encodes them (anthShrinkImageBytesIfTooBig returns undecodable data
// untouched). Their base64 is therefore a literal substring of any request
// that carries the image, whichever wire built it.
var imageCanary = []byte("TERVA-IMAGE-INPUT-CANARY-NOT-A-REAL-PNG")

func canaryB64() string { return base64.StdEncoding.EncodeToString(imageCanary) }

// wireBuilder is one provider request builder and how to drive it.
//
// receiver names the client type whose buildRequest this covers; the census
// below refuses a builder that has no entry here, so a sixth provider cannot
// be added without either honoring the capability or failing this test.
type wireBuilder struct {
	receiver string
	// provider and model are what the builder's own FindModel call looks up.
	// They differ per builder on purpose — anthropic resolves under its client
	// name, gemini hardcodes "google", codex tries "openai-codex" then
	// "openai", bedrock strips the geo prefix first — and a synthetic catalog
	// entry has to land where that specific builder will actually find it.
	provider string
	model    string
	build    func(t *testing.T, req Request) any
}

var wireBuilders = []wireBuilder{
	{
		receiver: "anthropicClient", provider: "anthropic", model: "gate-anthropic",
		build: func(t *testing.T, req Request) any {
			t.Helper()
			out, err := (&anthropicClient{}).buildRequest(req)
			if err != nil {
				t.Fatalf("anthropic buildRequest: %v", err)
			}
			return out
		},
	},
	{
		receiver: "openaiClient", provider: "openai", model: "gate-openai",
		build: func(t *testing.T, req Request) any {
			t.Helper()
			out, err := (&openaiClient{}).buildRequest(req)
			if err != nil {
				t.Fatalf("openai buildRequest: %v", err)
			}
			return out
		},
	},
	{
		receiver: "codexClient", provider: "openai-codex", model: "gate-codex",
		build: func(t *testing.T, req Request) any {
			t.Helper()
			out, err := (&codexClient{}).buildRequest(req)
			if err != nil {
				t.Fatalf("codex buildRequest: %v", err)
			}
			return out
		},
	},
	{
		receiver: "geminiClient", provider: "google", model: "gate-gemini",
		build: func(t *testing.T, req Request) any {
			t.Helper()
			out, _, err := (&geminiClient{}).buildRequest(req)
			if err != nil {
				t.Fatalf("gemini buildRequest: %v", err)
			}
			return out
		},
	},
	{
		receiver: "bedrockClient", provider: "amazon-bedrock", model: "gate-bedrock",
		build: func(t *testing.T, req Request) any {
			t.Helper()
			out, err := (&bedrockClient{region: "us-east-1"}).buildRequest(req)
			if err != nil {
				t.Fatalf("bedrock buildRequest: %v", err)
			}
			return out
		},
	},
}

// Enrollment. Every buildRequest method in the package must have a case above.
//
// This is the half that can fail when a builder is ADDED. The bug it guards
// was not that someone wrote the check wrong — it was that four wires were
// written after the one that had it, and nothing noticed. A table of five
// names would have the same blind spot; the scan is what closes it.
func TestEveryWireBuilderIsCoveredByTheImageInputGate(t *testing.T) {
	covered := map[string]bool{}
	for _, b := range wireBuilders {
		covered[b.receiver] = true
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if perr != nil {
			t.Fatalf("parse %s: %v", name, perr)
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || fd.Name.Name != "buildRequest" {
				continue
			}
			recv := receiverTypeName(fd)
			found++
			if !covered[recv] {
				t.Errorf("%s: %s.buildRequest has no case in wireBuilders, so nothing checks that it "+
					"honors CapImageInput. Add one — for most of this package's life exactly one of "+
					"five builders consulted the capability while the UI promised all of them did.",
					name, recv)
			}
		}
	}
	if found < len(wireBuilders) {
		t.Fatalf("the scan found %d buildRequest methods but the table has %d; the walk is broken "+
			"and a pass here proves nothing", found, len(wireBuilders))
	}
}

func receiverTypeName(fd *ast.FuncDecl) string {
	if len(fd.Recv.List) == 0 {
		return ""
	}
	switch v := fd.Recv.List[0].Type.(type) {
	case *ast.StarExpr:
		if id, ok := v.X.(*ast.Ident); ok {
			return id.Name
		}
	case *ast.Ident:
		return v.Name
	}
	return ""
}

// Behavior. A model that does not accept image input must not receive one, on
// any wire, from either a user message or a tool result.
//
// Asserted against the MARSHALED request rather than the builder's return
// value: the contract is about bytes leaving the process, and each wire nests
// its image differently (anthropic source.data, gemini inlineData, bedrock
// image.source.bytes, codex a data: URL). The canary's base64 is a substring
// of all four shapes.
func TestNoWireBuilderSendsAnImageToAModelWithoutImageInput(t *testing.T) {
	for _, b := range wireBuilders {
		t.Run(b.receiver, func(t *testing.T) {
			installGateModel(t, b, false)
			wire := marshalWire(t, b.build(t, gateRequest(b.model)))

			if strings.Contains(wire, canaryB64()) {
				t.Errorf("%s put the image on the wire for a model with image-input:false. The user "+
					"was told \"%s can't see images — 1 attachment(s) will be dropped\" and is billed "+
					"for image tokens anyway; if the endpoint rejects it, the retry loop writes a "+
					"permanent exclude_image directive into their session.", b.receiver, b.model)
			}
			if !strings.Contains(wire, imageInputOmittedNote) {
				t.Errorf("%s dropped the image without a trace. The model is left answering a "+
					"question about a picture it was never told existed, and on the wires that "+
					"reject an empty content array the message is now malformed.", b.receiver)
			}
		})
	}
}

// The complement, and the half that keeps the one above from passing for the
// wrong reason: a builder that stripped every image unconditionally would
// satisfy it. image-input is true by default, so this is also the ordinary
// case for nearly every model in the catalog.
func TestEveryWireBuilderStillSendsImagesToAVisionModel(t *testing.T) {
	for _, b := range wireBuilders {
		t.Run(b.receiver, func(t *testing.T) {
			installGateModel(t, b, true)
			wire := marshalWire(t, b.build(t, gateRequest(b.model)))

			if !strings.Contains(wire, canaryB64()) {
				t.Errorf("%s did not send the image to a model that accepts image input", b.receiver)
			}
			if strings.Contains(wire, imageInputOmittedNote) {
				t.Errorf("%s replaced an image a vision model can read", b.receiver)
			}
		})
	}
}

// A tool result's nested image is the same capability question one level down,
// and it is the path a screenshot actually arrives by (the read tool, an MCP
// server, an extension). Kept separate so a builder that handles the top-level
// case and not the nested one is named for what it missed.
//
// Two of the five hold this for a second, independent reason: chat-completions
// and the Responses API cannot carry an image in a tool message at all, so
// openai and codex text-ify the result and rely on the mirror, and gemini's
// convertGemToolResultParts drops the block outright. Mutation confirms only
// anthropic and bedrock are held HERE by the capability — for the other three
// this asserts the wire shape they already had. Worth keeping either way: it
// is the property that matters, and a future builder that starts nesting tool
// images inherits the check instead of needing someone to remember it.
func TestNoWireBuilderSendsANestedToolImageToAModelWithoutImageInput(t *testing.T) {
	for _, b := range wireBuilders {
		t.Run(b.receiver, func(t *testing.T) {
			installGateModel(t, b, false)
			req := Request{Model: b.model, Messages: []Message{
				{Role: RoleUser, Content: []Content{TextBlock{Text: "look"}}},
				{Role: RoleAssistant, Content: []Content{
					ToolCallBlock{ID: "call-1", Name: "read", Arguments: json.RawMessage(`{}`)},
				}},
				{Role: RoleTool, Content: []Content{ToolResultBlock{
					CallID:  "call-1",
					Content: []Content{ImageBlock{MimeType: "image/png", Data: imageCanary}},
				}}},
			}}
			if wire := marshalWire(t, b.build(t, req)); strings.Contains(wire, canaryB64()) {
				t.Errorf("%s put a tool-result image on the wire for a model with image-input:false",
					b.receiver)
			}
		})
	}
}

// The transcript belongs to the caller — core.Agent hands over a shallow copy
// whose Content slices are the live session's. Replacing an image in place
// would edit the user's transcript to say their screenshot was never sent.
func TestEnforcingImageInputDoesNotMutateTheCallersTranscript(t *testing.T) {
	img := ImageBlock{MimeType: "image/png", Data: imageCanary}
	msgs := []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "look"}, img}}}

	out := enforceImageInput(Model{Caps: map[Capability]bool{CapImageInput: false}}, msgs)

	if _, ok := msgs[0].Content[1].(ImageBlock); !ok {
		t.Fatalf("the caller's message now holds %T — the live transcript was edited", msgs[0].Content[1])
	}
	if _, ok := out[0].Content[1].(TextBlock); !ok {
		t.Fatalf("the returned copy still holds %T, so nothing was enforced", out[0].Content[1])
	}
}

// Nothing to do must cost nothing: a transcript a vision model can have, and a
// transcript with no images at all, come back as the same slice.
func TestEnforcingImageInputIsAPassThroughWhenThereIsNothingToDo(t *testing.T) {
	msgs := []Message{{Role: RoleUser, Content: []Content{
		TextBlock{Text: "hi"}, ImageBlock{MimeType: "image/png", Data: imageCanary},
	}}}
	if got := enforceImageInput(Model{}, msgs); &got[0] != &msgs[0] {
		t.Error("a vision-capable model got a copied transcript")
	}

	textOnly := []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}}
	blind := Model{Caps: map[Capability]bool{CapImageInput: false}}
	if got := enforceImageInput(blind, textOnly); &got[0] != &textOnly[0] {
		t.Error("a transcript with no images got copied")
	}
}

// Assistant images are the image OUTPUT path: Codex replays the most recent
// assistant images to edit them, gated on CapImageOutput. CapImageInput names
// "ImageBlocks in user/tool content" and must not reach across into that.
func TestEnforcingImageInputLeavesAssistantImagesAlone(t *testing.T) {
	msgs := []Message{{Role: RoleAssistant, Content: []Content{
		ImageBlock{MimeType: "image/png", Data: imageCanary, ID: "ig_1"},
	}}}
	out := enforceImageInput(Model{Caps: map[Capability]bool{CapImageInput: false}}, msgs)
	if _, ok := out[0].Content[0].(ImageBlock); !ok {
		t.Fatalf("a generated image the model may be asked to edit became %T", out[0].Content[0])
	}
}

// gateRequest is one user turn carrying text and the canary image.
func gateRequest(model string) Request {
	return Request{Model: model, Messages: []Message{{
		Role:    RoleUser,
		Content: []Content{TextBlock{Text: "what is in this picture?"}, ImageBlock{MimeType: "image/png", Data: imageCanary}},
	}}}
}

// installGateModel puts a synthetic model in the catalog where b's builder
// will find it, asserting image-input explicitly either way.
func installGateModel(t *testing.T, b wireBuilder, imageInput bool) {
	t.Helper()
	withCatalogState(t)
	RegisterExtraModel(Model{
		Provider: b.provider, ID: b.model, ContextWindow: 128000, MaxOutput: 4096,
		Caps: map[Capability]bool{CapImageInput: imageInput},
	})
	m, err := FindModel(b.provider, b.model)
	if err != nil {
		t.Fatalf("the synthetic model did not land in the catalog: %v", err)
	}
	if m.Has(CapImageInput) != imageInput {
		t.Fatalf("registered image-input=%v but the catalog reports %v", imageInput, m.Has(CapImageInput))
	}
}

func marshalWire(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	return string(b)
}
