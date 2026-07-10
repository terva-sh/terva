package dialogs

import (
	"testing"

	"terva.sh/terva/packages/tui"
)

func typeChars(d *ExtConfigDialog, s string) {
	for _, r := range s {
		d.HandleKey(rn(r))
	}
}

// A required field with no value/default blocks save; once filled, save
// emits the typed value.
func TestExtConfigDialogRequiredAndSave(t *testing.T) {
	d := NewExtConfigDialog()
	d.Open("weather", []ConfigField{{Key: "api_key", Type: "secret", Required: true}})

	if act := d.HandleKey(rn('s')); act.Save {
		t.Fatalf("save with an empty required field should be blocked, got %+v", act)
	}
	if d.status == "" {
		t.Error("expected a required-field status message")
	}

	// Activate the field, type a value, commit, then save.
	d.HandleKey(kind(tui.KeyEnter))
	typeChars(d, "sk-123")
	d.HandleKey(kind(tui.KeyEnter))
	act := d.HandleKey(rn('s'))
	if !act.Save || act.Name != "weather" || act.Values["api_key"] != "sk-123" {
		t.Fatalf("save = %+v, want api_key=sk-123", act)
	}
}

// A blank secret with an existing stored value passes the required check
// (the host keeps the old value) and saves as an empty working string.
func TestExtConfigDialogSecretKeptWhenSet(t *testing.T) {
	d := NewExtConfigDialog()
	d.Open("weather", []ConfigField{{Key: "api_key", Type: "secret", Required: true, HasSaved: true}})
	if got := d.fieldDisplay(d.fields[0]); got != "•••• (set)" {
		t.Errorf("secret display = %q, want masked (set)", got)
	}
	act := d.HandleKey(rn('s'))
	if !act.Save {
		t.Fatalf("blank secret with HasSaved should save, got %+v", act)
	}
	if act.Values["api_key"] != "" {
		t.Errorf("blank secret should emit empty (host keeps old), got %q", act.Values["api_key"])
	}
}

func TestExtConfigDialogBoolToggle(t *testing.T) {
	d := NewExtConfigDialog()
	d.Open("x", []ConfigField{{Key: "flag", Type: "bool", Default: "false"}})
	if d.fieldDisplay(d.fields[0]) != "off" {
		t.Fatal("bool should seed off from default")
	}
	d.HandleKey(kind(tui.KeyEnter)) // toggle
	if d.working["flag"] != "true" || d.fieldDisplay(d.fields[0]) != "on" {
		t.Errorf("toggle should turn the bool on, got %q", d.working["flag"])
	}
}

func TestExtConfigDialogSelectCycle(t *testing.T) {
	d := NewExtConfigDialog()
	d.Open("x", []ConfigField{{Key: "units", Type: "select", Options: []string{"celsius", "fahrenheit"}, Default: "celsius"}})
	if d.working["units"] != "celsius" {
		t.Fatalf("select should seed from default, got %q", d.working["units"])
	}
	d.HandleKey(kind(tui.KeyEnter)) // cycle
	if d.working["units"] != "fahrenheit" {
		t.Errorf("cycle should advance the option, got %q", d.working["units"])
	}
	d.HandleKey(kind(tui.KeyEnter)) // wrap
	if d.working["units"] != "celsius" {
		t.Errorf("cycle should wrap, got %q", d.working["units"])
	}
}

func TestExtConfigDialogEscCloses(t *testing.T) {
	d := NewExtConfigDialog()
	d.Open("x", []ConfigField{{Key: "a"}})
	if act := d.HandleKey(kind(tui.KeyEsc)); !act.Close || d.Active() {
		t.Errorf("esc should close, got %+v active=%v", act, d.Active())
	}
}
