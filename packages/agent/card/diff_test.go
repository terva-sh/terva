package card

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestChangedFieldsNamesOnlyWhatMoved(t *testing.T) {
	a := Card{Name: "Kobeni", Personality: "anxious", FirstMes: "hi", Tags: []string{"x"}}

	if got := ChangedFields(a, a); len(got) != 0 {
		t.Errorf("a card differs from itself in nothing, got %v", got)
	}

	b := a
	b.Personality = "steadier"
	b.FirstMes = "hello"
	got := ChangedFields(a, b)
	if !reflect.DeepEqual(got, []string{"personality", "first_mes"}) {
		t.Errorf("changed fields = %v, want [personality first_mes]", got)
	}
	// Declaration order, not alphabetical: a list reads in the order the editor
	// lays the fields out.
	if got[0] != "personality" {
		t.Errorf("fields must come back in Card's declaration order, got %v", got)
	}
}

// A card that has been through JSON can come back with `tags: []` where it went
// in as nil. Reporting that as a change would put noise in every revision list.
func TestChangedFieldsIgnoresAbsentVersusEmpty(t *testing.T) {
	a := Card{Name: "Kobeni", Tags: nil, AlternateGreetings: nil}
	b := Card{Name: "Kobeni", Tags: []string{}, AlternateGreetings: []string{}}
	if got := ChangedFields(a, b); len(got) != 0 {
		t.Errorf("absent and empty are the same card, got %v", got)
	}
	// …but gaining a real entry IS a change.
	b.Tags = []string{"cursed"}
	if got := ChangedFields(a, b); !reflect.DeepEqual(got, []string{"tags"}) {
		t.Errorf("changed = %v, want [tags]", got)
	}
}

func TestChangedFieldsSeesTheLorebook(t *testing.T) {
	a := Card{Name: "Kobeni"}
	b := Card{Name: "Kobeni", CharacterBook: &CharacterBook{Entries: []BookEntry{{Keys: []string{"k"}, Content: "c"}}}}
	if got := ChangedFields(a, b); !reflect.DeepEqual(got, []string{"character_book"}) {
		t.Errorf("gaining a lorebook is a change, got %v", got)
	}
	// A lorebook whose contents differ is a change; an identical one is not.
	c := b
	book := *b.CharacterBook
	book.Entries = []BookEntry{{Keys: []string{"k"}, Content: "c"}}
	c.CharacterBook = &book
	if got := ChangedFields(b, c); len(got) != 0 {
		t.Errorf("an identical lorebook is not a change, got %v", got)
	}
	book2 := *b.CharacterBook
	book2.Entries = []BookEntry{{Keys: []string{"k"}, Content: "different"}}
	c.CharacterBook = &book2
	if got := ChangedFields(b, c); !reflect.DeepEqual(got, []string{"character_book"}) {
		t.Errorf("an edited lorebook is a change, got %v", got)
	}
}

// The point of reading the names off the struct tags: a field added to Card is
// compared from the moment it exists, with no hand-maintained list to update.
// This asserts the coverage rather than the mechanism — it fails if someone
// swaps the reflection for an explicit list and forgets a field.
func TestChangedFieldsCoversEveryTaggedCardField(t *testing.T) {
	t.Parallel()
	tp := reflect.TypeOf(Card{})
	for i := 0; i < tp.NumField(); i++ {
		f := tp.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		a, b := Card{}, Card{}
		if !setDistinct(reflect.ValueOf(&b).Elem().Field(i)) {
			t.Fatalf("field %s (%s) has no distinct value in this test — extend setDistinct", f.Name, name)
		}
		got := ChangedFields(a, b)
		if !reflect.DeepEqual(got, []string{name}) {
			t.Errorf("changing %s reported %v, want [%s]", f.Name, got, name)
		}
	}
}

// setDistinct puts a non-zero value into v, reporting false for a kind this
// test does not know how to vary (which is a signal to extend it, not to skip).
func setDistinct(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.String:
		v.SetString("different")
		return true
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.String {
			v.Set(reflect.ValueOf([]string{"different"}).Convert(v.Type()))
			return true
		}
		if v.Type() == reflect.TypeOf(json.RawMessage(nil)) {
			v.Set(reflect.ValueOf(json.RawMessage(`{"a":1}`)))
			return true
		}
		if v.Type() == reflect.TypeOf([]Asset(nil)) {
			v.Set(reflect.ValueOf([]Asset{{Type: "icon", URI: "ccdefault:"}}))
			return true
		}
		return false
	case reflect.Map:
		if v.Type() == reflect.TypeOf(map[string]string(nil)) {
			v.Set(reflect.ValueOf(map[string]string{"fi": "eri"}))
			return true
		}
		return false
	case reflect.Ptr:
		v.Set(reflect.New(v.Type().Elem()))
		if v.Type() == reflect.TypeOf((*CharacterBook)(nil)) {
			v.Elem().Set(reflect.ValueOf(CharacterBook{Entries: []BookEntry{{Content: "c"}}}))
		}
		if v.Type() == reflect.TypeOf((*int64)(nil)) {
			v.Elem().SetInt(7)
		}
		return true
	}
	return false
}

// SpecVersion is tagged json:"-" because it describes the DOCUMENT, not the
// character: a card re-saved under a different spec has not been edited by
// anyone. Skipping it is what keeps a field literally named "-" out of a
// revision list.
func TestChangedFieldsIgnoresTheSpecVersion(t *testing.T) {
	a := Card{Name: "Kobeni", SpecVersion: "2.0"}
	b := Card{Name: "Kobeni", SpecVersion: "3.0"}
	if got := ChangedFields(a, b); len(got) != 0 {
		t.Errorf("spec_version is not a character edit, got %v", got)
	}
	for _, f := range ChangedFields(Card{}, Card{SpecVersion: "3.0"}) {
		if f == "-" || f == "" {
			t.Errorf("an untagged field leaked into the list as %q", f)
		}
	}
}
