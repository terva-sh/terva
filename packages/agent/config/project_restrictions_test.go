package config

import (
	"reflect"
	"strings"
	"testing"
)

// notRestrictions are the ProjectConfig fields that do not only narrow what
// terva does, with why. A broken project file loses these too, but losing one
// never runs the agent with fewer checks than the file asked for.
var notRestrictions = map[string]string{
	"context_files":    "adds files to the prompt; trust-gated, and losing it only removes context",
	"adopt_extensions": "re-adopts global extensions into a scoped project; losing it starts fewer",
	"hooks":            "runs commands; trust-gated, and losing it runs fewer",
	"mcp":              "starts servers; trust-gated, and losing it starts fewer",
	"provider":         "picks the default provider; trust-gated, and losing it falls back to the user's",
	"model":            "picks the default model; trust-gated, and losing it falls back to the user's",
	"user_name":        "a display name; trust-gated",
}

// Every ProjectConfig field is classified: either a restriction, which the
// warning for a broken project file names, or listed in notRestrictions with
// the reason. A new field fails here until someone decides which it is, so a
// new restrict-only setting cannot be lost silently the way five were
// (TKT-01M372ESJB). A renamed or removed one fails too.
func TestEveryProjectConfigFieldIsClassified(t *testing.T) {
	fields := map[string]bool{}
	typ := reflect.TypeOf(ProjectConfig{})
	for i := 0; i < typ.NumField(); i++ {
		name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		fields[name] = true
	}
	restricts := map[string]bool{}
	for _, n := range ProjectRestrictions {
		restricts[n] = true
		if !fields[n] {
			t.Errorf("ProjectRestrictions names %q, which is not a ProjectConfig JSON field", n)
		}
		if _, both := notRestrictions[n]; both {
			t.Errorf("%q is listed both as a restriction and as not one", n)
		}
	}
	for n := range notRestrictions {
		if !fields[n] {
			t.Errorf("notRestrictions names %q, which is not a ProjectConfig JSON field", n)
		}
	}
	for n := range fields {
		if _, ok := notRestrictions[n]; !restricts[n] && !ok {
			t.Errorf("ProjectConfig field %q is unclassified: if it can only narrow what terva does, add it to ProjectRestrictions (and its case to permissions' test); otherwise add it to notRestrictions with the reason", n)
		}
	}
}
