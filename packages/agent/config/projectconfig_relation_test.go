package config

import (
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// ProjectConfig's doc comment says "the type is the guard": it is the subset
// of Config a project may set. Nothing asserted the relation, so it held by
// review alone (04-wiring-and-cli.md records exactly that). These tests pin
// the three parts of the contract, self-enrolling — a new ProjectConfig key
// fails until its author answers all three questions a project key raises.

// projectOnly are the two keys that deliberately have NO Config counterpart.
var projectOnly = map[string]string{
	"project_scoped":   "read pre-trust and pre-Config: it decides whether the project layer loads at all",
	"adopt_extensions": "meaningless globally — it adopts the project's own extensions into user scope",
}

// Every project key is applied under exactly one merge discipline. A key's
// discipline is a security decision: trust-gated keys vanish for untrusted
// clones, restrict-only keys are honored even then (they can only narrow),
// and handled-elsewhere keys have their own trust gate at their own merge
// site, named here so the next reader can find it.
var (
	trustGated = map[string]bool{
		"context_files": true, "provider": true, "model": true, "user_name": true,
	}
	restrictOnly = map[string]bool{
		"disable_context_extensions": true, "disable_extensions": true, "disable_mcp": true,
	}
	handledElsewhere = map[string]string{
		"hooks":            "TrustedProjectHooks + MergeHookConfigs",
		"mcp":              "TrustedProjectMCP + MergeMCPConfigs",
		"permissions":      "build/permissions.go's project rule loader",
		"project_scoped":   "projectscope.go (pre-trust by design)",
		"adopt_extensions": "projectscope.go",
	}
)

func jsonTags(t *testing.T, typ reflect.Type) map[string]reflect.Type {
	t.Helper()
	out := map[string]reflect.Type{}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		tag := strings.Split(f.Tag.Get("json"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}
		out[tag] = f.Type
	}
	if len(out) < 5 {
		t.Fatalf("%s yielded only %d json tags; the reflection is not seeing them", typ.Name(), len(out))
	}
	return out
}

func TestProjectConfigIsASubsetOfConfig(t *testing.T) {
	full := jsonTags(t, reflect.TypeOf(Config{}))
	project := jsonTags(t, reflect.TypeOf(ProjectConfig{}))

	for tag, ptype := range project {
		ftype, shared := full[tag]
		if !shared {
			if _, excused := projectOnly[tag]; !excused {
				t.Errorf("ProjectConfig key %q has no Config counterpart and no entry on the project-only list — a project would set what no global layer can", tag)
			}
			continue
		}
		if ftype != ptype {
			t.Errorf("key %q is %s in Config but %s in ProjectConfig — the same json key with two shapes is a parse trap", tag, ftype, ptype)
		}
	}
	for tag := range projectOnly {
		if _, ok := project[tag]; !ok {
			t.Errorf("project-only entry %q names a key ProjectConfig no longer has — delete it", tag)
		}
		if _, ok := full[tag]; ok {
			t.Errorf("project-only entry %q now exists in Config too — the exemption is stale", tag)
		}
	}
}

func TestEveryProjectKeyDeclaresItsMergeDiscipline(t *testing.T) {
	project := jsonTags(t, reflect.TypeOf(ProjectConfig{}))
	for tag := range project {
		n := 0
		if trustGated[tag] {
			n++
		}
		if restrictOnly[tag] {
			n++
		}
		if _, ok := handledElsewhere[tag]; ok {
			n++
		}
		if n != 1 {
			t.Errorf("ProjectConfig key %q is claimed by %d merge disciplines, want exactly 1 — a new project key must say whether trust gates it, and where", tag, n)
		}
	}
	for _, m := range []map[string]bool{trustGated, restrictOnly} {
		for tag := range m {
			if _, ok := project[tag]; !ok {
				t.Errorf("discipline lists name %q, which ProjectConfig no longer has — delete it", tag)
			}
		}
	}
	for tag := range handledElsewhere {
		if _, ok := project[tag]; !ok {
			t.Errorf("handled-elsewhere names %q, which ProjectConfig no longer has — delete it", tag)
		}
	}
}

// The `terva project` CLI edits .terva/config.json through a generic map with
// raw string keys — zero compile-time link to the struct. A renamed json tag
// would orphan the CLI writer silently; this reads the writer's source and
// holds every key literal it passes to the four helpers against the live tags.
func TestProjectCmdKeyLiteralsAreLiveTags(t *testing.T) {
	src, err := os.ReadFile("../projectcmd.go")
	if err != nil {
		t.Fatalf("read projectcmd.go: %v", err)
	}
	re := regexp.MustCompile(`(?:runProjectScalar|readProjectList|setProjectList|setProjectField)\("([a-z_]+)"`)
	found := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		found[m[1]] = true
	}
	if len(found) < 4 {
		t.Fatalf("saw only %d key literals in projectcmd.go; the extraction is not seeing them", len(found))
	}
	project := jsonTags(t, reflect.TypeOf(ProjectConfig{}))
	for key := range found {
		if _, ok := project[key]; !ok {
			t.Errorf("projectcmd.go writes key %q, which is not a ProjectConfig json tag — the CLI writer is orphaned", key)
		}
	}
}
