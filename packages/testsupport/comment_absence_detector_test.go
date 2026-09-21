package testsupport

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"testing"
)

// The tree gate passes vacuously if the detector never fires, so these cases
// pin both edges: the shapes it must catch and the true sentences it must
// leave alone. Each case is a package of one or two files, parsed from source.
func TestAbsenceDetector(t *testing.T) {
	const ctrlproto = `package ctrlproto
type Method string
const (
	MethodAuthLoginStart  Method = "auth.login.start"
	MethodAuthLoginSubmit Method = "auth.login.submit"
	MethodAuthLoginCancel Method = "auth.login.cancel"
)
func CanLogin() bool { return true }
type Opts struct{ Pair string }
func (o *Opts) Reset() {}
`
	cases := []struct {
		name  string
		files map[string]string // filename -> source, all in one package dir
		other map[string]string // filename -> source, a second package named by its own clause
		want  []string          // names expected to fire, sorted
	}{
		{
			name: "a wire verb named by value",
			files: map[string]string{
				"methods.go": ctrlproto,
				"pane.go": "package ctrlproto\n" +
					"// The verbs that change a credential, `auth.login.start` and friends,\n" +
					"// are a separate group. `auth.login.start` does not exist yet.\n" +
					"var x = 1\n",
			},
			want: []string{"auth.login.start"},
		},
		{
			name: "a list of verbs",
			files: map[string]string{
				"methods.go": ctrlproto,
				"pane.go": "package ctrlproto\n" +
					"// `auth.login.start`, `auth.login.submit` and `auth.login.cancel` do not exist yet.\n" +
					"var x = 1\n",
			},
			want: []string{"auth.login.cancel", "auth.login.start", "auth.login.submit"},
		},
		{
			name: "a function named bare, in a non-doc comment",
			files: map[string]string{
				"methods.go": ctrlproto,
				"pane.go": "package ctrlproto\n" +
					"func f() {\n\t// Until CanLogin is not implemented we show nothing.\n\t// CanLogin is not implemented yet, so the pane is read-only.\n\t_ = 1\n}\n",
			},
			want: []string{"CanLogin"},
		},
		{
			name: "there is no, and no X yet",
			files: map[string]string{
				"methods.go": ctrlproto,
				"pane.go": "package ctrlproto\n" +
					"// There is no `auth.login.submit`, so the form has one step.\n" +
					"// And no `auth.login.cancel` yet either.\n" +
					"var x = 1\n",
			},
			want: []string{"auth.login.cancel", "auth.login.submit"},
		},
		{
			name: "a member and an unimplemented method",
			files: map[string]string{
				"methods.go": ctrlproto,
				"pane.go": "package ctrlproto\n" +
					"// Opts.Pair is not defined; Opts.Reset() is unimplemented.\n" +
					"var x = 1\n",
			},
			want: []string{"Opts.Pair", "Opts.Reset"},
		},
		{
			name: "a qualified name from another package",
			files: map[string]string{
				"web.go": "package web\n" +
					"// ctrlproto.MethodAuthLoginStart does not exist yet, so the pane is read-only.\n" +
					"var x = 1\n",
			},
			other: map[string]string{"methods.go": ctrlproto},
			want:  []string{"ctrlproto.MethodAuthLoginStart"},
		},
		{
			name: "a stub's own doc comment is about itself",
			files: map[string]string{
				"clipboard_other.go": "package tui\n" +
					"// ReadClipboardImagePNG is not implemented on this platform yet.\n" +
					"func ReadClipboardImagePNG() {}\n",
			},
		},
		{
			name: "the subject is the paths, not the function",
			files: map[string]string{
				"roots.go": "package build\n" +
					"func AddSecretRoot() {}\n" +
					"func f() {\n\t// AddSecretRoot skips paths that do not exist yet.\n\t_ = 1\n}\n",
			},
		},
		{
			name: "a name nothing declares",
			files: map[string]string{
				"a.go": "package p\n" +
					"// `Nonexistent` does not exist yet; neither does other.Thing.\n" +
					"var x = 1\n",
			},
		},
		{
			name: "a bare name declared only in another package is not a hit",
			files: map[string]string{
				"web.go": "package web\n// CanLogin does not exist yet.\nvar x = 1\n",
			},
			other: map[string]string{"methods.go": ctrlproto},
		},
		{
			name: "a lack is not an absence",
			files: map[string]string{
				"methods.go": ctrlproto,
				"pane.go": "package ctrlproto\n" +
					"// Opts does not have a timeout, and CanLogin does not support a hint.\n" +
					"// There is no Opts timeout, and Opts is not available on Windows.\n" +
					"var x = 1\n",
			},
		},
		{
			name: "a claim inside a string literal is data",
			files: map[string]string{
				"methods.go": ctrlproto,
				"pane.go":    "package ctrlproto\nvar msg = \"CanLogin does not exist yet\"\n",
			},
		},
		{
			name: "a sentence-initial capital is not an identifier",
			files: map[string]string{
				"a.go": "package p\n" +
					"func Until() {}\n" +
					"// Until is not the point. Until\n// is not implemented as a verb.\n" +
					"var x = 1\n",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fset := token.NewFileSet()
			idx := newPkgIndex()
			var parsedFiles []*ast.File
			for name, src := range tc.files {
				f, err := parser.ParseFile(fset, name, src, parser.ParseComments)
				if err != nil {
					t.Fatalf("parse %s: %v", name, err)
				}
				idx.add(f)
				parsedFiles = append(parsedFiles, f)
			}
			byPkgName := map[string][]*pkgIndex{idx.name: {idx}}
			if tc.other != nil {
				o := newPkgIndex()
				for name, src := range tc.other {
					f, err := parser.ParseFile(fset, name, src, parser.ParseComments)
					if err != nil {
						t.Fatalf("parse %s: %v", name, err)
					}
					o.add(f)
				}
				byPkgName[o.name] = append(byPkgName[o.name], o)
			}
			var got []string
			for _, f := range parsedFiles {
				for _, c := range absenceClaims(fset, f, idx, byPkgName) {
					got = append(got, c.name)
				}
			}
			sort.Strings(got)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("fired on %v, want %v", got, tc.want)
			}
		})
	}
}

// The external-test package name must not displace the package it tests, or
// a claim in foo_test would resolve against an empty index.
func TestPkgIndexPrefersNonTestName(t *testing.T) {
	fset := token.NewFileSet()
	idx := newPkgIndex()
	for _, src := range []string{
		"package ctrlproto_test\nvar a = 1\n",
		"package ctrlproto\nfunc CanLogin() {}\n",
		"package ctrlproto_test\nvar b = 1\n",
	} {
		f, err := parser.ParseFile(fset, "x.go", src, 0)
		if err != nil {
			t.Fatal(err)
		}
		idx.add(f)
	}
	if idx.name != "ctrlproto" {
		t.Errorf("index named %q, want ctrlproto", idx.name)
	}
	if !idx.names["CanLogin"] {
		t.Error("CanLogin not indexed")
	}
}
