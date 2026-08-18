package ctrlproto

import (
	"encoding/json"
	"reflect"
	"testing"
)

// Every persona verb identifies by `ref`, and every one of them still honours
// the `name` it used to take. Both halves matter: the first is the point of the
// change, the second is what keeps a client that predates it working.
//
// Decoded from JSON rather than built as structs, because the compatibility
// claim is about bytes on the wire — a struct literal would test the field
// names in this file and nothing a peer actually sends.
func TestPersonaParamsPreferRefAndStillHonourName(t *testing.T) {
	cases := []struct {
		what string
		raw  string
		want string
	}{
		{"ref alone", `{"ref":"review-crew:vartija"}`, "review-crew:vartija"},
		{"name alone — the pre-ref client", `{"name":"Vartija"}`, "Vartija"},
		{"both — ref wins", `{"ref":"review-crew:vartija","name":"Vartija"}`, "review-crew:vartija"},
		{"blank ref falls through to name", `{"ref":"  ","name":"Vartija"}`, "Vartija"},
		{"neither", `{}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.what, func(t *testing.T) {
			var g PersonaGetParams
			if err := json.Unmarshal([]byte(tc.raw), &g); err != nil {
				t.Fatal(err)
			}
			if got := g.Query(); got != tc.want {
				t.Errorf("PersonaGetParams(%s).Query() = %q, want %q", tc.raw, got, tc.want)
			}
			var d PersonaDeleteParams
			if err := json.Unmarshal([]byte(tc.raw), &d); err != nil {
				t.Fatal(err)
			}
			if got := d.Query(); got != tc.want {
				t.Errorf("PersonaDeleteParams(%s).Query() = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

// On an EDIT the two are not alternatives — ref identifies, name is content —
// so an edit carries both independently, and the embed keeps them one flat
// object on the wire rather than a nested one.
func TestAnEditCarriesTheRefAndTheNameSeparately(t *testing.T) {
	var p PersonaEditParams
	raw := `{"ref":"review-crew:vartija","name":"Watchman","charter":"x"}`
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	if p.Ref != "review-crew:vartija" || p.Target() != "review-crew:vartija" {
		t.Errorf("Ref = %q, Target = %q; the edit would target whatever the new name resolves to", p.Ref, p.Target())
	}
	if p.Name != "Watchman" {
		t.Errorf("Name = %q, want the new name — a rename must still reach the file", p.Name)
	}
	if p.Charter != "x" {
		t.Errorf("Charter = %q — the embedded write params did not decode flat", p.Charter)
	}

	// Flat on the way out too. A nested {"write_params":{…}} would be a silent
	// break for every client that sends what it always sent.
	out, err := json.Marshal(PersonaEditParams{Ref: "r", PersonaWriteParams: PersonaWriteParams{Name: "N"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out); got != `{"ref":"r","name":"N"}` {
		t.Errorf("an edit marshals to %s, want a flat object", got)
	}
}

// 🔑 A create cannot be handed a ref to obey, because its params have nowhere
// to put one. Held by the type, and asserted here so the split is not undone by
// someone folding Ref back onto the write params for symmetry — which is
// exactly how it was written the first time.
func TestACreateHasNowhereToPutATarget(t *testing.T) {
	if f, ok := reflect.TypeOf(PersonaWriteParams{}).FieldByName("Ref"); ok {
		t.Errorf("PersonaWriteParams grew a %s field. Every field here is CONTENT that must survive the "+
			"file round trip (TestPersonaWriteChainPreservesEveryField enrolls them); a target is not, "+
			"and putting one here lets a create carry one.", f.Name)
	}
	var p PersonaWriteParams
	if err := json.Unmarshal([]byte(`{"ref":"review-crew:vartija","name":"Fresh"}`), &p); err != nil {
		t.Fatal(err)
	}
	if p.Name != "Fresh" {
		t.Fatalf("Name = %q", p.Name)
	}
}
