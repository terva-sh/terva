package workspace

import (
	"fmt"
	"reflect"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/testsupport"
)

// The persona write path is a chain of hand-written field lists —
// PersonaWriteParams → paramsToPersona → MarshalPersona → disk →
// ParsePersona → personaView — and a field dropped by ANY link silently
// vanishes on the next edit from any client. build's round-trip test pins the
// marshal/parse link; this one drives the whole chain the personas.create
// verb uses, with every wire field carrying a unique sentinel, and requires
// each sentinel to come back out of both the store and the view a client
// would re-render. A new PersonaWriteParams field enrolls itself and fails
// until every link carries it.
func TestPersonaWriteChainPreservesEveryField(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))

	var params ctrlproto.PersonaWriteParams
	pv := reflect.ValueOf(&params).Elem()
	for i := 0; i < pv.NumField(); i++ {
		name := pv.Type().Field(i).Name
		f := pv.Field(i)
		switch f.Kind() {
		case reflect.String:
			f.SetString("x-" + name)
		case reflect.Bool:
			f.SetBool(true)
		case reflect.Slice:
			f.Set(reflect.Append(f, reflect.ValueOf("x-"+name)))
		default:
			t.Fatalf("PersonaWriteParams.%s has kind %s this test doesn't know how to populate — teach it", name, f.Kind())
		}
	}
	// The name is the file identity and the lookup key.
	params.Name = "chain-sentinel"

	if _, err := build.WritePersona(paramsToPersona(params)); err != nil {
		t.Fatalf("WritePersona: %v", err)
	}
	stored, ok := build.LookupPersona("chain-sentinel")
	if !ok {
		t.Fatal("the persona just written is not findable by name")
	}
	view := personaView(stored)

	// Flatten the view (PersonaSummary is embedded) so every wire field can be
	// found by its Go name on one value or the other.
	viewField := func(name string) (reflect.Value, bool) {
		vv := reflect.ValueOf(view)
		if f := vv.FieldByName(name); f.IsValid() {
			return f, true
		}
		return reflect.Value{}, false
	}
	storedV := reflect.ValueOf(stored)
	for i := 0; i < pv.NumField(); i++ {
		name := pv.Type().Field(i).Name
		want := fmt.Sprint(pv.Field(i).Interface())
		if sf := storedV.FieldByName(name); !sf.IsValid() {
			t.Errorf("build.Persona has no %s field for wire field of that name — paramsToPersona cannot be carrying it", name)
		} else if got := fmt.Sprint(sf.Interface()); got != want {
			t.Errorf("Persona.%s after write+lookup = %q, want %q — a link in the write chain dropped it", name, got, want)
		}
		vf, ok := viewField(name)
		if !ok {
			t.Errorf("PersonaView carries no %s — an editor that renders the view and saves it back would delete the field", name)
			continue
		}
		if got := fmt.Sprint(vf.Interface()); got != want {
			t.Errorf("PersonaView.%s = %q, want %q — the view drops what the store holds", name, got, want)
		}
	}
}
