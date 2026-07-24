package card

import (
	"reflect"
	"strings"
)

// Comparing two revisions of the same character.
//
// This exists so a revision list can say WHICH fields a restore would change
// rather than only when it was saved — "personality, first_mes" is actionable
// where a bare timestamp is not.
//
// Field names are read off the struct tags rather than listed by hand, so a
// field added to Card is compared from the moment it exists. A hand-written
// list is the failure this package has already seen once: a V3 `data` object
// was unmarshalled into a struct that had no such fields and every one of them
// was silently dropped.

// ChangedFields names the fields whose values differ between a and b, in the
// order Card declares them, using the CCv2 JSON names a client already speaks.
// SpecVersion is excluded — it is tagged `json:"-"` because it describes the
// document, not the character.
func ChangedFields(a, b Card) []string {
	var out []string
	t := reflect.TypeOf(a)
	va, vb := reflect.ValueOf(a), reflect.ValueOf(b)
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		if !sameValue(va.Field(i), vb.Field(i)) {
			out = append(out, name)
		}
	}
	return out
}

// sameValue compares two field values, treating an absent collection and an
// empty one as the same thing. A card that round-trips through JSON can come
// back with `tags: []` where it went in as nil, and reporting that as a change
// the user made would put noise in every revision list.
func sameValue(a, b reflect.Value) bool {
	switch a.Kind() {
	case reflect.Slice, reflect.Map:
		if a.Len() == 0 && b.Len() == 0 {
			return true
		}
	case reflect.Ptr:
		// A nil pointer and a pointer to the zero value are NOT merged: a card
		// with no character_book and a card with an empty one differ in what
		// they declare, and the lorebook is worth being told about either way.
		if a.IsNil() != b.IsNil() {
			return false
		}
		if a.IsNil() {
			return true
		}
		return reflect.DeepEqual(a.Elem().Interface(), b.Elem().Interface())
	}
	return reflect.DeepEqual(a.Interface(), b.Interface())
}
