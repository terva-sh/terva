package lore

import "testing"

func TestParseEntry_Valid(t *testing.T) {
	raw := `---
name: Auth
keys: [auth, login]
secondary_keys: [jwt]
logic: and_all
order: 50
position: before
case_sensitive: true
---
The auth flow issues a JWT.`
	e, ok, err := ParseEntry(raw, "auth.md")
	if err != nil || !ok {
		t.Fatalf("expected ok entry, got ok=%v err=%v", ok, err)
	}
	if e.Name != "Auth" {
		t.Errorf("name = %q", e.Name)
	}
	if len(e.Keys) != 2 || e.Keys[0] != "auth" || e.Keys[1] != "login" {
		t.Errorf("keys = %v", e.Keys)
	}
	if len(e.SecondaryKeys) != 1 || e.SecondaryKeys[0] != "jwt" {
		t.Errorf("secondary = %v", e.SecondaryKeys)
	}
	if e.Logic != LogicAndAll {
		t.Errorf("logic = %v", e.Logic)
	}
	if e.Order != 50 {
		t.Errorf("order = %d", e.Order)
	}
	if e.Position != PositionBefore {
		t.Errorf("position = %v", e.Position)
	}
	if !e.CaseSensitive {
		t.Errorf("case_sensitive not set")
	}
	if e.Content != "The auth flow issues a JWT." {
		t.Errorf("content = %q", e.Content)
	}
	if e.Source != "auth.md" {
		t.Errorf("source = %q", e.Source)
	}
}

func TestParseEntry_Defaults(t *testing.T) {
	raw := `---
name: Plain
keys: [x]
---
body`
	e, ok, err := ParseEntry(raw, "p.md")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if e.Order != DefaultOrder {
		t.Errorf("default order = %d, want %d", e.Order, DefaultOrder)
	}
	if e.Position != PositionAfter {
		t.Errorf("default position = %v, want after", e.Position)
	}
	if e.Logic != LogicAndAny {
		t.Errorf("default logic = %v, want and_any", e.Logic)
	}
}

func TestParseEntry_ConstantNeedsNoKeys(t *testing.T) {
	raw := `---
name: Always
constant: true
---
always present`
	_, ok, err := ParseEntry(raw, "c.md")
	if err != nil || !ok {
		t.Fatalf("constant entry should parse; ok=%v err=%v", ok, err)
	}
}

func TestParseEntry_Errors(t *testing.T) {
	cases := map[string]string{
		"no keys, not constant": `---
name: Bad
---
body`,
		"empty content": `---
name: Bad
keys: [x]
---
`,
		"bad logic": `---
name: Bad
keys: [x]
logic: xor
---
body`,
		"bad position": `---
name: Bad
keys: [x]
position: sideways
---
body`,
	}
	for name, raw := range cases {
		if _, ok, err := ParseEntry(raw, name); err == nil || ok {
			t.Errorf("%s: expected error, got ok=%v err=%v", name, ok, err)
		}
	}
}

func TestParseEntry_Disabled(t *testing.T) {
	raw := `---
name: Off
keys: [x]
enabled: false
---
body`
	e, ok, err := ParseEntry(raw, "off.md")
	if err != nil {
		t.Fatalf("disabled entry should not error: %v", err)
	}
	if ok {
		t.Fatalf("disabled entry should report ok=false, got %+v", e)
	}
}

func TestParseEntry_TrimsKeys(t *testing.T) {
	raw := `---
name: T
keys: ["  auth  ", "", "login"]
---
body`
	e, ok, err := ParseEntry(raw, "t.md")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if len(e.Keys) != 2 || e.Keys[0] != "auth" || e.Keys[1] != "login" {
		t.Errorf("keys not trimmed/compacted: %v", e.Keys)
	}
}

func TestParseEntry_LogicAliases(t *testing.T) {
	for in, want := range map[string]Logic{
		"":        LogicAndAny,
		"and_any": LogicAndAny,
		"not_all": LogicNotAll,
		"not_any": LogicNotAny,
		"and_all": LogicAndAll,
	} {
		got, err := parseLogic(in)
		if err != nil || got != want {
			t.Errorf("parseLogic(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
}
