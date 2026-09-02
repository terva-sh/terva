package build

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/config"
)

// The price of the config.json read-lift.
//
// ConfigReadableByAgent hands the agent a file it has decided holds nothing
// secret. That decision is a claim about a schema that keeps changing: every
// new string field in config.json is a chance for a credential to land
// somewhere no rule classifies, and the lift would keep saying "clean" because
// nothing knows to look there. A comment asking the next contributor to
// remember is not a control.
//
// So every string-shaped leaf in config.Config must be CLASSIFIED here, and an
// unclassified one fails this test. Written empty on purpose — the first run
// was the audit, and each entry below is a decision someone made once, with the
// reason attached.
//
// Numbers and booleans are exempt by shape: a credential is a string.
//
// Adding a config field? This test will name it. Decide which it is:
//
//	sealed   — encrypted at rest; ScanConfigSecrets must find it too
//	denies   — not sealed, but its presence blocks the read-lift
//	public   — genuinely not a credential, and never will be
func TestEveryStringConfigFieldIsClassified(t *testing.T) {
	// Keyed by dotted path. `*` stands for a map key (an extension name, a
	// server name, a backend id).
	classified := map[string]string{
		// sealed — encrypted at rest, and ScanConfigSecrets reports them.
		"extensions.*.*":           "sealed when the extension's manifest marks the field `secret`; an extension with NO manifest blocks the lift instead (ConfigSecretsUnknown), because unknown is not clean",
		"image.backends.*.api_key": "sealed; the one credential the config schema itself declares",
		"secrets.recipient":        "sealed is meaningless here — it IS the public half of the key, and publishing it is the design (a keyless host can still seal a value)",

		// denies — not sealed, but their presence keeps config.json denied.
		"mcp.servers.*.env.*":     "denies unless it is a ${VAR} reference; MCP env is deliberately not sealed (the sanctioned form is indirection), so a literal keeps the file denied",
		"mcp.servers.*.headers.*": "denies unless it is a ${VAR} reference; same rule — an Authorization header written literally is a bearer token",
		"mcp.servers.*.url":       "denies when it carries userinfo (https://user:pass@host) — a URL is a credential carrier, and this is exact rather than a heuristic",

		// public — genuinely not credentials, with the reason each is safe.
		"endpoints.*.apiKeyEnv":         "the NAME of an env var, not its value — pointing at a secret is the sanctioned alternative to holding one",
		"mcp.servers.*.auth.bearer_env": "likewise a variable name, not a token",

		"always_on_skills[]":                "skill names; the bodies they pin are files terva already reads",
		"approval":                          "a policy word (ask/allow/deny)",
		"auto_compact":                      "a mode word",
		"auto_title_model":                  "a model id",
		"classifier.mode":                   "a policy word (off/screen/approve)",
		"classifier.model":                  "a model id",
		"classifier.provider":               "a provider id; the credential comes from the auth store, never from here",
		"context_files[]":                   "paths into the user's own tree",
		"default_persona":                   "a persona name",
		"disable_context_extensions[]":      "extension names",
		"disable_extensions[]":              "extension names",
		"disable_mcp[]":                     "server names",
		"endpoints.*.baseUrl":               "a base URL for a provider endpoint; credentials there would arrive via apiKeyEnv",
		"escalation.model":                  "a model id",
		"escalation.provider":               "a provider id",
		"favorite_models[]":                 "model ids",
		"hidden_models[]":                   "model-id visibility patterns; a picker preference, and the same shape as favorite_models",
		"image.backend":                     "a backend id",
		"image.backends.*.base_url":         "an endpoint URL; the credential beside it is api_key",
		"image.backends.*.model":            "a model id",
		"image.backends.*.protocol":         "a protocol name",
		"image.backends.*.sampler":          "a sampler name",
		"image.backends.*.size":             "a dimension string",
		"image.backends.*.workflow":         "a workflow name",
		"image.backends.*.workflow_file":    "a path into the user's tree",
		"language":                          "a BCP-47 tag",
		"last_changelog_shown":              "a version string",
		"lazy_tool_active[]":                "tool-group names",
		"mcp.servers.*.args[]":              "argv for a local server; see the note on command",
		"mcp.servers.*.command":             "a command line the user wrote",
		"mcp.servers.*.transport":           "a transport name",
		"model":                             "a model id",
		"native_output.quality":             "a quality word",
		"native_output.size":                "a dimension string",
		"permissions[].args":                "a matcher pattern for tool arguments",
		"permissions[].decision":            "allow/deny/ask",
		"permissions[].reason":              "human text the user wrote",
		"permissions[].tool":                "a tool name",
		"persona_name":                      "a persona name",
		"provider":                          "a provider id",
		"raati.auto_panel_providers[]":      "provider ids",
		"raati.level2[].model":              "a model id",
		"raati.level2[].provider":           "a provider id",
		"raati.profiles.*.class":            "a class name",
		"raati.profiles.*.description":      "human text",
		"raati.profiles.*.inquire":          "a mode word",
		"raati.profiles.*.seat_order":       "an ordering word",
		"raati.profiles.*.seats[].model":    "a model id",
		"raati.profiles.*.seats[].provider": "a provider id",
		"raati.seat_order":                  "an ordering word",
		"reasoning":                         "an effort word",
		"reasoning_summary":                 "a verbosity word",
		"status_line.rows[][]":              "status-line cell names",
		"swarm_tiers.*.cheap.model":         "a model id",
		"swarm_tiers.*.cheap.reasoning":     "an effort word",
		"swarm_tiers.*.medium.model":        "a model id",
		"swarm_tiers.*.medium.reasoning":    "an effort word",
		"swarm_tiers.*.strong.model":        "a model id",
		"swarm_tiers.*.strong.reasoning":    "an effort word",
		"swarm_tiers.*.weak.model":          "a model id",
		"swarm_tiers.*.weak.reasoning":      "an effort word",
		"theme":                             "a theme name",
		"user_name":                         "the user's display name — personal, but not a credential",

		// User-authored command lines. A token pasted inline into one is a real
		// leak and is NOT covered: detecting it means pattern-sniffing shell,
		// which fails in both directions — missing `--header "$(cat tok)"` while
		// blocking the lift on any string that looks like base64. The sanctioned
		// form here is the same as everywhere else, an env reference.
		"hooks.post_tool_use[].args[]":  "user-authored argv — see the note on inline tokens",
		"hooks.post_tool_use[].command": "user-authored command line — see the note on inline tokens",
		"hooks.post_tool_use[].tools":   "a tool matcher",
		"hooks.pre_tool_use[].args[]":   "user-authored argv — see the note on inline tokens",
		"hooks.pre_tool_use[].command":  "user-authored command line — see the note on inline tokens",
		"hooks.pre_tool_use[].tools":    "a tool matcher",
		"status_line.scripts.*.command": "user-authored command line — see the note on inline tokens",
	}

	var unclassified []string
	for _, path := range stringLeaves(reflect.TypeOf(config.Config{}), "") {
		if _, ok := classified[path]; !ok {
			unclassified = append(unclassified, path)
		}
	}
	sort.Strings(unclassified)
	if len(unclassified) > 0 {
		t.Fatalf("%d config field(s) hold a string and no rule says whether they can hold a credential.\n"+
			"Classify each in this test (sealed / denies / public), and if it is a secret, teach ScanConfigSecrets about it:\n  %s",
			len(unclassified), strings.Join(unclassified, "\n  "))
	}

	// The reverse direction: a path classified here that no longer exists is a
	// stale claim, and stale claims are how a list stops describing the code.
	live := map[string]bool{}
	for _, path := range stringLeaves(reflect.TypeOf(config.Config{}), "") {
		live[path] = true
	}
	var stale []string
	for path := range classified {
		if !live[path] {
			stale = append(stale, path)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("classified path(s) no longer exist in config.Config: %s", strings.Join(stale, ", "))
	}
}

// stringLeaves walks a config struct and returns the dotted json path of every
// leaf that can hold a string — including inside maps and slices, since
// `extensions.<name>.<key>` and `mcp.servers.<name>.env.<var>` are exactly
// where credentials have historically been put.
func stringLeaves(t reflect.Type, prefix string) []string {
	return appendStringLeaves(nil, t, prefix, map[reflect.Type]bool{})
}

func appendStringLeaves(out []string, t reflect.Type, prefix string, seen map[reflect.Type]bool) []string {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.String:
		return append(out, prefix)
	case reflect.Slice, reflect.Array:
		// json.RawMessage is a []byte, but it holds an arbitrary JSON value —
		// including a string, which is where every extension secret lives. A
		// naive walk drops it as "a slice of uint8" and the most secret-bearing
		// path in the file goes unclassified.
		if t.Elem().Kind() == reflect.Uint8 && t.Name() != "" {
			return append(out, prefix)
		}
		return appendStringLeaves(out, t.Elem(), prefix+"[]", seen)
	case reflect.Map:
		return appendStringLeaves(out, t.Elem(), prefix+".*", seen)
	case reflect.Interface:
		// An any-typed value can hold a string. Name it rather than skip it.
		return append(out, prefix)
	case reflect.Struct:
		// json.RawMessage and time.Time are leaves, not structures to descend
		// into; RawMessage in particular is where an extension's opaque values
		// live, and its path is what matters.
		if t.PkgPath() != "" && t.PkgPath() != "terva.sh/terva/packages/agent/config" &&
			!strings.HasPrefix(t.PkgPath(), "terva.sh/terva/packages/agent/") {
			return append(out, prefix)
		}
		if seen[t] { // a self-referential config type would otherwise loop
			return out
		}
		seen[t] = true
		defer delete(seen, t)
		for i := range t.NumField() {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			name := jsonName(f)
			if name == "-" {
				continue
			}
			next := name
			if prefix != "" {
				next = prefix + "." + name
			}
			out = appendStringLeaves(out, f.Type, next, seen)
		}
		return out
	default:
		return out
	}
}

// jsonName is the field's wire name, falling back to the Go name when it has
// no tag — a field with no tag still ships, so it still needs classifying.
func jsonName(f reflect.StructField) string {
	tag := f.Tag.Get("json")
	if tag == "" {
		return f.Name
	}
	name, _, _ := strings.Cut(tag, ",")
	if name == "" {
		return f.Name
	}
	return name
}

// Teeth for the census above. A guard that walks a struct is only as good as
// the shapes its walker understands: every shape it silently drops is a place a
// credential can be added without the census ever noticing, and the test would
// keep passing — the worst failure mode a guard can have. json.RawMessage is
// the live example (it is a []byte, and a naive walk calls it a slice of
// numbers and moves on), which is how extensions.*.* went missing on the first
// draft of this file.
func TestCensusWalkerSeesEveryShapeAFieldCanTake(t *testing.T) {
	type inner struct {
		Leaf string `json:"leaf"`
	}
	type sample struct {
		Plain     string                       `json:"plain"`
		Ptr       *string                      `json:"ptr"`
		List      []string                     `json:"list"`
		Nested    inner                        `json:"nested"`
		NestedPtr *inner                       `json:"nested_ptr"`
		Map       map[string]string            `json:"map"`
		MapStruct map[string]inner             `json:"map_struct"`
		Deep      map[string]map[string]string `json:"deep"`
		Raw       json.RawMessage              `json:"raw"`
		RawMap    map[string]json.RawMessage   `json:"raw_map"`
		Any       any                          `json:"any"`
		Ignored   string                       `json:"-"`
		Untagged  string
		Number    int  `json:"number"`
		Flag      bool `json:"flag"`
		unxported string
	}

	got := map[string]bool{}
	for _, p := range stringLeaves(reflect.TypeOf(sample{}), "") {
		got[p] = true
	}
	for _, want := range []string{
		"plain", "ptr", "list[]", "nested.leaf", "nested_ptr.leaf",
		"map.*", "map_struct.*.leaf", "deep.*.*", "raw", "raw_map.*", "any",
		"Untagged",
	} {
		if !got[want] {
			t.Errorf("walker missed %q — a credential could live there uncensused", want)
		}
	}
	for _, unwanted := range []string{"-", "number", "flag", "unxported"} {
		if got[unwanted] {
			t.Errorf("walker reported %q, which cannot hold a string", unwanted)
		}
	}
}
