package persona

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// A persona write REPLACES the whole file, so every field must survive
// Persona → Marshal → Parse or an edit from any client silently
// deletes it — the charter-inheritance bug shipped exactly that way, and the
// existing round-trip test could not have caught it: its fixture is a
// hand-written literal, so a field absent from the fixture is a field the
// test never sees.
//
// This one populates EVERY field with a unique sentinel by reflection — a new
// field enrolls itself the day it is added, and fails until it reaches both
// frontmatter and Marshal's hand-written list (or claims a
// reasoned exemption below).
var notRoundTripped = map[string]string{
	"Namespace":       "derived from the file's location at lookup, never from content",
	"Inherited":       "resolution artifact: Marshal deliberately writes Extends, not the resolved charter — writing it back would fork the base",
	"InheritedSource": "resolution artifact, same reason as Inherited",
}

func TestEveryPersonaFieldSurvivesTheWriteReadRoundTrip(t *testing.T) {
	var p Persona
	v := reflect.ValueOf(&p).Elem()
	for i := 0; i < v.NumField(); i++ {
		name := v.Type().Field(i).Name
		f := v.Field(i)
		switch f.Kind() {
		case reflect.String:
			f.SetString("x-" + name)
		case reflect.Bool:
			f.SetBool(true)
		case reflect.Slice:
			f.Set(reflect.Append(f, reflect.ValueOf("x-"+name)))
		default:
			t.Fatalf("Persona.%s has kind %s this test doesn't know how to populate — teach it", name, f.Kind())
		}
	}
	// Name doubles as the file identity, so it has to be a plausible one.
	p.Name = "roundtrip-sentinel"

	raw, err := Marshal(p)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	// Source is Parse's own argument, so it round-trips by construction.
	got, err := Parse(string(raw), p.Source)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	gv := reflect.ValueOf(got)
	for i := 0; i < v.NumField(); i++ {
		name := v.Type().Field(i).Name
		if _, exempt := notRoundTripped[name]; exempt {
			if !gv.Field(i).IsZero() {
				t.Errorf("Persona.%s is on the exemption list but came back non-zero — the exemption is stale, delete it", name)
			}
			continue
		}
		want, gotf := fmt.Sprint(v.Field(i).Interface()), fmt.Sprint(gv.Field(i).Interface())
		if want != gotf {
			t.Errorf("Persona.%s did not survive the write→read round-trip: wrote %q, read back %q — a client edit would silently delete it (thread it through frontmatter AND Marshal, or add a reasoned exemption)", name, want, gotf)
		}
	}
	if !strings.Contains(string(raw), "x-Charter") {
		t.Fatalf("the marshalled file carries no charter body; the sentinel scheme is broken:\n%s", raw)
	}
}
