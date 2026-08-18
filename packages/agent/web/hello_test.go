//go:build terva_web

package web

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
)

func hasFeature(h ctrlproto.Hello, f string) bool { return slices.Contains(h.Features, f) }
func hasGroup(h ctrlproto.Hello, g ctrlproto.Group) bool {
	for _, x := range h.Groups {
		if string(x) == string(g) {
			return true
		}
	}
	return false
}

// TestEveryPermissionGateReachesTheHello: each Allow* option turns on exactly
// one thing, and turns on nothing else.
//
// The whole browser client keys off this mapping, and until now the only
// assertion made on the server hello anywhere was that its protocol is 1. A gate
// wired to the wrong constant — or dropped in a refactor — does not fail
// anything; it silently removes a control from every client, which reads to a
// user as "that feature does not exist in this build".
func TestEveryPermissionGateReachesTheHello(t *testing.T) {
	base := buildHello(Options{}, 0, 0)

	for _, tc := range []struct {
		name    string
		opts    Options
		feature string
		group   ctrlproto.Group
	}{
		{name: "restart", opts: Options{AllowRestart: true}, feature: ctrlproto.FeatureRestart},
		{name: "stage", opts: Options{AllowStage: true}, feature: ctrlproto.FeatureStage},
		{name: "login", opts: Options{AllowLogin: true}, group: ctrlproto.GroupAuth},
		{name: "secrets", opts: Options{AllowSecrets: true}, group: ctrlproto.GroupSecrets},
	} {
		t.Run(tc.name, func(t *testing.T) {
			off, on := base, buildHello(tc.opts, 0, 0)

			if tc.feature != "" {
				if hasFeature(off, tc.feature) {
					t.Errorf("%q is advertised with the option OFF — the gate is not gating", tc.feature)
				}
				if !hasFeature(on, tc.feature) {
					t.Errorf("%q is NOT advertised with the option on — every client will hide the control", tc.feature)
				}
			}
			if tc.group != "" {
				if hasGroup(off, tc.group) {
					t.Errorf("group %q is advertised with the option OFF — the gate is not gating", tc.group)
				}
				if !hasGroup(on, tc.group) {
					t.Errorf("group %q is NOT advertised with the option on", tc.group)
				}
			}
			// And nothing ELSE moved: a gate wired to the wrong constant would
			// still pass the two checks above if it happened to add something.
			if len(on.Features)+len(on.Groups) != len(off.Features)+len(off.Groups)+1 {
				t.Errorf("one option changed %d advertised capabilities, want exactly 1 (features %v groups %v vs base %v/%v)",
					len(on.Features)+len(on.Groups)-len(off.Features)-len(off.Groups),
					on.Features, on.Groups, off.Features, off.Groups)
			}
		})
	}
}

// TestTheCarrierFeaturesAreUnconditional: attachments and shared files are
// properties of THIS carrier (it mounts POST /upload and GET /shared/), not
// permissions — a client that hides its drop target because a flag was off
// would be hiding a route that works.
func TestTheCarrierFeaturesAreUnconditional(t *testing.T) {
	h := buildHello(Options{}, 0, 0)
	for _, f := range []string{ctrlproto.FeatureAttachments, ctrlproto.FeatureSharedFiles} {
		if !hasFeature(h, f) {
			t.Errorf("%q is missing from a hello with every option off — it is a fact about the carrier", f)
		}
	}
}

// TestTheHelloCarriesTheWorkspaceFactsAndLimits: the scalar half. A dropped
// assignment here is invisible — the client renders an empty cwd, an unlocked
// padlock, or an unbounded upload — so each is pinned to a distinct value.
func TestTheHelloCarriesTheWorkspaceFactsAndLimits(t *testing.T) {
	h := buildHello(Options{
		Version: "9.9.9-test",
		Locale:  "fi-FI",
		CWD:     "/tmp/somewhere",
		Jailed:  true,
	}, 1234, 5678)

	if h.Locale != "fi-FI" || h.CWD != "/tmp/somewhere" || !h.Jailed {
		t.Errorf("hello = locale %q cwd %q jailed %v, want the options verbatim", h.Locale, h.CWD, h.Jailed)
	}
	if h.MaxUploadBytes != 1234 {
		t.Errorf("MaxUploadBytes = %d, want the carrier's ceiling (1234)", h.MaxUploadBytes)
	}
	if h.MaxAttachmentBytes != 5678 {
		t.Errorf("MaxAttachmentBytes = %d, want the attachment ceiling (5678) — these two are "+
			"different limits and were assigned from different sources", h.MaxAttachmentBytes)
	}
	if !strings.Contains(h.Version, "9.9.9-test") {
		t.Errorf("Version = %q, want the option's", h.Version)
	}
}

// TestEveryAllowOptionIsCoveredHere is a census, not a list: it reads the
// Options struct, so an Allow* field added tomorrow fails until somebody says
// what it advertises. That is the moment to remember the hello — the mapping
// lived eighteen lines inside serveWS precisely because nobody had to.
func TestEveryAllowOptionIsCoveredHere(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "web.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse web.go: %v", err)
	}
	var allows []string
	ast.Inspect(file, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != "Options" {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return true
		}
		for _, f := range st.Fields.List {
			for _, nm := range f.Names {
				if strings.HasPrefix(nm.Name, "Allow") {
					allows = append(allows, nm.Name)
				}
			}
		}
		return false
	})
	if len(allows) < 4 {
		t.Fatalf("found %d Allow* option(s) — the scan is not reading Options: %v", len(allows), allows)
	}

	// AllowInsecure gates the BIND, not the hello: it decides whether the server
	// will listen on a non-loopback address with no auth, and advertising it
	// would tell a client something about the socket it is already on.
	exempt := map[string]string{
		"AllowInsecure": "gates the bind, not anything a client is told",
	}
	src := readSelf(t)
	for _, name := range allows {
		if why, ok := exempt[name]; ok {
			if !strings.Contains(readWebGo(t), name) {
				t.Errorf("exemption for %s (%s) is stale — the field is gone", name, why)
			}
			continue
		}
		if !strings.Contains(src, name) {
			t.Errorf("Options.%s is a permission gate that no test here exercises — buildHello may "+
				"not advertise it at all, and no client would ever know", name)
		}
	}
}

func readSelf(t *testing.T) string {
	t.Helper()
	return readFileForTest(t, "hello_test.go")
}

func readWebGo(t *testing.T) string {
	t.Helper()
	return readFileForTest(t, "web.go")
}

func readFileForTest(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}
