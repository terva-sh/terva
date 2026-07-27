package modes

import (
	"reflect"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/modes/dialogs"
)

// carrierExtConfigFields is the wire→dialog converter that replaced the TUI's
// local-disk config path: the daemon builds the whole form
// (ctrlproto.ExtensionConfigField) and this hand-copies it into
// dialogs.ConfigField. A hand-copy drifts one field at a time — an eleventh
// wire field compiles fine, renders nowhere, and the TUI quietly shows less
// form than the browser. These two tests make the chain self-enrolling:
//
//   - the shape test holds the two structs to the same field names and types,
//     both directions, so neither side can grow alone;
//   - the round-trip test populates EVERY wire field non-zero by reflection
//     and requires every dialog field non-zero on the way out, so a field
//     present in both structs but missing from the copy loop still fails.

func TestConfigFieldMirrorsTheWireShape(t *testing.T) {
	wire := reflect.TypeOf(ctrlproto.ExtensionConfigField{})
	dlg := reflect.TypeOf(dialogs.ConfigField{})
	for i := 0; i < wire.NumField(); i++ {
		wf := wire.Field(i)
		df, ok := dlg.FieldByName(wf.Name)
		if !ok {
			t.Errorf("dialogs.ConfigField lacks %s — the TUI form will render less than the daemon sent", wf.Name)
			continue
		}
		if df.Type != wf.Type {
			t.Errorf("ConfigField.%s is %s on the wire but %s in the dialog", wf.Name, wf.Type, df.Type)
		}
	}
	for i := 0; i < dlg.NumField(); i++ {
		if _, ok := wire.FieldByName(dlg.Field(i).Name); !ok {
			t.Errorf("dialogs.ConfigField.%s has no wire source — a phantom the form can never fill", dlg.Field(i).Name)
		}
	}
}

func TestCarrierExtConfigFieldsCarryTheWholeForm(t *testing.T) {
	// Every wire field non-zero, by reflection: a new field enrolls itself.
	var wireField ctrlproto.ExtensionConfigField
	v := reflect.ValueOf(&wireField).Elem()
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		switch f.Kind() {
		case reflect.String:
			f.SetString("x-" + v.Type().Field(i).Name)
		case reflect.Bool:
			f.SetBool(true)
		case reflect.Slice:
			f.Set(reflect.Append(f, reflect.New(f.Type().Elem()).Elem()))
			f.Index(0).SetString("opt")
		case reflect.Int, reflect.Int64:
			f.SetInt(7)
		default:
			t.Fatalf("ExtensionConfigField.%s has kind %s this test doesn't know how to populate — teach it", v.Type().Field(i).Name, f.Kind())
		}
	}

	fake := newFakeCarrier()
	fake.extView = &ctrlproto.ExtensionsView{Extensions: []ctrlproto.ExtensionInfo{
		{Name: "mailer", Status: "running", Enabled: true, Config: []ctrlproto.ExtensionConfigField{wireField}},
	}}
	i := &Interactive{cfg: InteractiveConfig{Carrier: fake, CarrierSession: "s1"}}

	got := i.carrierExtConfigFields("mailer")
	if len(got) != 1 {
		t.Fatalf("carrierExtConfigFields returned %d fields, want 1", len(got))
	}
	gv := reflect.ValueOf(got[0])
	for j := 0; j < gv.NumField(); j++ {
		if gv.Field(j).IsZero() {
			t.Errorf("dialog field %s came through zero — the copy in carrierExtConfigFields dropped it", gv.Type().Field(j).Name)
		}
	}

	if extra := i.carrierExtConfigFields("nobody"); extra != nil {
		t.Errorf("unknown extension returned a form: %v", extra)
	}
}
