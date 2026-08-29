package provider

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func wireReq(msgs ...Message) Request {
	return Request{
		Model:    "gpt-5.6-terra",
		System:   "you are a test",
		Messages: msgs,
		Tools:    []Tool{{Name: "read", Description: "read a file", Schema: json.RawMessage(`{"type":"object"}`)}},
	}
}

func userMsg(text string) Message {
	return Message{Role: RoleUser, Content: []Content{TextBlock{Text: text}}}
}

func assistantMsg(text string) Message {
	return Message{Role: RoleAssistant, Content: []Content{TextBlock{Text: text}}}
}

// The header carries everything EXCEPT the input array, and every remaining
// line is exactly one input item. Without the split there is nothing to diff:
// the whole request is one line and "which item first differs" is back to
// reading a few hundred kilobytes by eye.
func TestDumpRequestJSONLSplitsHeaderFromItems(t *testing.T) {
	out, err := DumpRequestJSONL("openai-codex", "", wireReq(userMsg("one"), assistantMsg("two"), userMsg("three")))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) < 4 {
		t.Fatalf("want a header plus one line per item, got %d lines:\n%s", len(lines), out)
	}

	var head struct {
		Dump     string                     `json:"_dump"`
		Provider string                     `json:"_provider"`
		Field    string                     `json:"_field"`
		Request  map[string]json.RawMessage `json:"request"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &head); err != nil {
		t.Fatalf("header is not JSON: %v\n%s", err, lines[0])
	}
	if head.Dump != "header" || head.Provider != "openai-codex" || head.Field != "input" {
		t.Errorf("header mislabeled: %+v", head)
	}
	// The input array must be LIFTED OUT, not duplicated -- otherwise every
	// item appears twice and the header alone defeats the diff.
	if _, dup := head.Request["input"]; dup {
		t.Error("header still carries the input array; it must be lifted out")
	}
	if _, ok := head.Request["instructions"]; !ok {
		t.Error("header lost the system instructions")
	}
	if _, ok := head.Request["tools"]; !ok {
		t.Error("header lost the tool definitions")
	}
	for i, ln := range lines[1:] {
		if !json.Valid([]byte(ln)) {
			t.Errorf("item line %d is not valid JSON: %s", i, ln)
		}
	}
}

// THE property the mode exists for. A provider prompt cache matches on an exact
// byte prefix, so appending a turn must leave every earlier line untouched --
// otherwise a diff of two dumps reports churn that is the dumper's own and an
// investigation chases it. This is the assertion that would fail if anything in
// the request build ever became order- or time-dependent.
func TestDumpRequestJSONLIsAppendOnlyWhenAMessageIsAppended(t *testing.T) {
	base := []Message{userMsg("one"), assistantMsg("two")}
	before, err := DumpRequestJSONL("openai-codex", "", wireReq(base...))
	if err != nil {
		t.Fatal(err)
	}
	after, err := DumpRequestJSONL("openai-codex", "", wireReq(append(append([]Message{}, base...), userMsg("three"))...))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(after, before) {
		t.Fatalf("appending a message rewrote earlier lines.\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if len(after) <= len(before) {
		t.Errorf("appending a message did not add lines: %d -> %d bytes", len(before), len(after))
	}
}

// The dump must be a pure function of the request. Go randomizes map iteration,
// so a body that grew a map-valued field, or a builder that reached for a clock
// or a connection, would make two dumps of one request differ -- and a diff
// against a dump taken a minute earlier would be unreadable. This is the test
// the wireBody comment promises when it says the builders stay pure.
func TestDumpRequestJSONLIsDeterministic(t *testing.T) {
	req := wireReq(userMsg("one"), assistantMsg("two"))
	first, err := DumpRequestJSONL("openai-codex", "", req)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		again, err := DumpRequestJSONL("openai-codex", "", req)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(first, again) {
			t.Fatalf("dump %d differs from the first for an identical request:\n%s\n---\n%s", i, first, again)
		}
	}
}

// A provider with no arm must SAY so. Returning an empty dump would read as
// "this request carries nothing", which is the worst possible answer from a
// tool whose whole job is showing what goes on the wire.
//
// 🪤 The subject used to be a DELIBERATELY fictional id, because this test had
// named anthropic and then google, and each time that provider gained a dumper
// the test stopped testing a refusal. A fictional id is no longer the stable
// choice — it is no longer refused at all. wireBody now picks its arm by the
// WIRE the provider speaks, and reasoningWireFamily answers with the
// OpenAI-compatible default for any id it does not know. That default is the
// whole reason a NAMED ENDPOINT works here, since an endpoint's id is whatever
// the operator typed at /login and no static table can hold it. An unknown id
// and a runtime endpoint are the same string to this package.
//
// So the stable subject is a real provider whose WIRE has no arm. amazon-bedrock
// is the only one: it maps to reasoningWireNone. That is also the seam in
// routing a body dump through a reasoning classifier — "none" means it takes no
// reasoning knob, not that it has no body — so if bedrock is ever given its own
// arm, this test SHOULD fail and be re-pointed, exactly as its predecessors were.
//
// The CLI cannot reach the unrefused case: Resolve rejects a provider that is
// not in the registry long before promptDumpWire runs.
func TestDumpRequestJSONLRefusesProviderWithNoArm(t *testing.T) {
	out, err := DumpRequestJSONL("amazon-bedrock", "", wireReq(userMsg("one")))
	if err == nil {
		t.Fatalf("want an error for a provider with no arm, got a dump:\n%s", out)
	}
	if !strings.Contains(err.Error(), "amazon-bedrock") {
		t.Errorf("error should name the provider asked for, got: %v", err)
	}
}

// Non-vacuity for the refusal above: the refusal has to be specific to the
// unsupported wire, not a dump that is broken for everyone. Without this, a
// wireBody that failed on every input would leave the refusal test passing.
//
// One provider per supported wire, so a wire losing its arm fails HERE with a
// name rather than as a mystery elsewhere.
func TestDumpRequestJSONLRefusalIsSpecificToTheUnsupportedWire(t *testing.T) {
	for _, p := range []string{"openai-codex", "anthropic", "google", "openai"} {
		if _, err := DumpRequestJSONL(p, "", wireReq(userMsg("one"))); err != nil {
			t.Errorf("%s is a supported wire but does not dump: %v", p, err)
		}
	}
}

// Anthropic support, and the reason it needed a mode argument.
//
// The dump's contract is "exactly as providerName would put it on the wire".
// For every other provider the body is a function of the request alone. Anthropic
// has two bodies: a subscription request leads with the Claude Code identity
// system block and renames tools to Anthropic's canonical casing, and an api-key
// request does neither. Dumping one shape for the other misreports the two
// things a dump is most often opened to check — the cached prefix and the tools.
func TestDumpRequestJSONLAnthropicShowsTheModeItWouldSend(t *testing.T) {
	req := wireReq(userMsg("one"))

	apikey, err := DumpRequestJSONL("anthropic", "apikey", req)
	if err != nil {
		t.Fatalf("api-key dump: %v", err)
	}
	oauth, err := DumpRequestJSONL("anthropic", "oauth", req)
	if err != nil {
		t.Fatalf("oauth dump: %v", err)
	}

	if bytes.Equal(apikey, oauth) {
		t.Fatal("the two auth modes dumped identical bodies; the mode argument is doing nothing")
	}
	// The identity line is the concrete difference, and it must appear in the
	// subscription dump only. Asserting on it rather than on inequality alone
	// keeps this from passing on any incidental difference.
	if !bytes.Contains(oauth, []byte(claudeCodeIdentity)) {
		t.Error("the oauth dump is missing the Claude Code identity system block")
	}
	if bytes.Contains(apikey, []byte(claudeCodeIdentity)) {
		t.Error("the api-key dump carries the Claude Code identity block, which that mode never sends")
	}
	// Tool renaming is the other half: "read" goes out as "Read" under OAuth.
	if !bytes.Contains(oauth, []byte(`"name":"Read"`)) {
		t.Errorf("oauth dump did not rename the tool to Anthropic's casing:\n%s", oauth)
	}
	if !bytes.Contains(apikey, []byte(`"name":"read"`)) {
		t.Errorf("api-key dump should keep terva's own tool casing:\n%s", apikey)
	}
}

// The header/item split has to hold for Anthropic too — that is the whole
// reason the mode exists, and "messages" is a different input field name from
// the Codex "input" the split was first built against.
func TestDumpRequestJSONLAnthropicSplitsMessagesOut(t *testing.T) {
	out, err := DumpRequestJSONL("anthropic", "apikey", wireReq(userMsg("one"), assistantMsg("two"), userMsg("three")))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) < 4 {
		t.Fatalf("want a header plus one line per message, got %d lines:\n%s", len(lines), out)
	}
	var head struct {
		Provider string                     `json:"_provider"`
		Field    string                     `json:"_field"`
		Request  map[string]json.RawMessage `json:"request"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &head); err != nil {
		t.Fatalf("header is not JSON: %v\n%s", err, lines[0])
	}
	if head.Provider != "anthropic" || head.Field != "messages" {
		t.Errorf("header mislabeled: %+v", head)
	}
	if _, dup := head.Request["messages"]; dup {
		t.Error("header still carries the messages array; it must be lifted out")
	}
	if _, ok := head.Request["system"]; !ok {
		t.Error("header lost the system prompt")
	}
	for i, ln := range lines[1:] {
		if !json.Valid([]byte(ln)) {
			t.Errorf("item line %d is not valid JSON: %s", i, ln)
		}
	}
}

// 🪤 Anthropic is NOT append-only, and that is the provider's behavior rather
// than the dumper's. terva marks the cache breakpoint on the LAST user message
// (tagLastUserCache), so appending a turn moves the mark and rewrites the line
// that used to carry it. The Codex dump's "a pure append leaves every earlier
// line byte-identical" does not transfer.
//
// Asserting the weaker true property instead of the stronger false one: the
// breakpoint is the ONLY thing that moves. Anyone diffing two Anthropic dumps
// sees exactly one line of expected churn, and this is what says so — if a
// second kind of rewrite ever appeared, the stripped comparison below catches
// it while a plain HasPrefix would just keep failing for the reason it already
// fails.
func TestDumpRequestJSONLAnthropicRewritesOnlyTheCacheBreakpoint(t *testing.T) {
	base := []Message{userMsg("one"), assistantMsg("two")}
	before, err := DumpRequestJSONL("anthropic", "oauth", wireReq(base...))
	if err != nil {
		t.Fatal(err)
	}
	after, err := DumpRequestJSONL("anthropic", "oauth", wireReq(append(append([]Message{}, base...), userMsg("three"))...))
	if err != nil {
		t.Fatal(err)
	}

	// The documented-but-provider-specific expectation, stated so a reader knows
	// the next assertion is deliberate and not a weakened workaround.
	if bytes.HasPrefix(after, before) {
		t.Fatal("Anthropic dumps became append-only; the cache breakpoint no longer moves, " +
			"which means tagLastUserCache changed and this test's premise is stale")
	}

	strip := func(b []byte) string {
		return strings.ReplaceAll(string(b), `,"cache_control":{"type":"ephemeral"}`, "")
	}
	sb, sa := strip(before), strip(after)
	if !strings.HasPrefix(sa, sb) {
		t.Fatalf("appending rewrote earlier lines for a reason OTHER than the cache breakpoint.\nbefore:\n%s\nafter:\n%s", sb, sa)
	}
	if len(sa) <= len(sb) {
		t.Errorf("appending a message did not add lines: %d -> %d bytes", len(sb), len(sa))
	}
}

// Gemini's body names its input array `contents`, not `messages` or `input` —
// a third field name, which is the part of the header/item split most likely to
// be got wrong by copying an existing arm.
func TestDumpRequestJSONLGoogleSplitsContentsOut(t *testing.T) {
	out, err := DumpRequestJSONL("google", "", wireReq(userMsg("one"), assistantMsg("two"), userMsg("three")))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) < 4 {
		t.Fatalf("want a header plus one line per content, got %d lines:\n%s", len(lines), out)
	}
	var head struct {
		Provider string                     `json:"_provider"`
		Field    string                     `json:"_field"`
		Request  map[string]json.RawMessage `json:"request"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &head); err != nil {
		t.Fatalf("header is not JSON: %v\n%s", err, lines[0])
	}
	if head.Provider != "google" || head.Field != "contents" {
		t.Errorf("header mislabeled: %+v", head)
	}
	if _, dup := head.Request["contents"]; dup {
		t.Error("header still carries the contents array; it must be lifted out")
	}
	// Gemini carries the system prompt as systemInstruction, not a system role
	// inside contents — so losing it is invisible in the item lines.
	if _, ok := head.Request["systemInstruction"]; !ok {
		t.Errorf("header lost the systemInstruction:\n%s", lines[0])
	}
	for i, ln := range lines[1:] {
		if !json.Valid([]byte(ln)) {
			t.Errorf("item line %d is not valid JSON: %s", i, ln)
		}
	}
}
