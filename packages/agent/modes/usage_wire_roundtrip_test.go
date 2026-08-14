package modes

import (
	"reflect"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// usageFromWire is the inverse of core.UsageToWire, and the comment saying so
// is the only thing that ever checked it. It carried five of the nine fields.
//
// That gap is not cosmetic. ImageOutputTokens is a SUBSET of OutputTokens
// billed at its own rate — 10 to 20 times the text rate on the image models —
// so a surface that reconstructs a Usage here and re-prices it under-reports an
// image turn by that factor. Nothing re-prices from this seam today, which is
// exactly why a dropped field would have sat here unnoticed until something
// did.
//
// So the guard is written to enroll fields by ITSELF. It fills every field of
// provider.Usage by reflection rather than from a literal, which means a field
// added tomorrow is covered without anyone remembering this file exists: a
// hand-written fixture would keep passing while the new field went missing,
// and that is the failure this is here to make impossible.
func TestTheUsageWireRoundTripCarriesEveryField(t *testing.T) {
	want := fullyPopulatedUsage(t)

	got := usageFromWire(ptr(core.UsageToWire(want)))

	if !reflect.DeepEqual(got, want) {
		// Name the offenders rather than dumping two structs: the whole point
		// is to tell the next person which field they forgot.
		rv, rw := reflect.ValueOf(got), reflect.ValueOf(want)
		for i := 0; i < rv.NumField(); i++ {
			if !reflect.DeepEqual(rv.Field(i).Interface(), rw.Field(i).Interface()) {
				t.Errorf("%s: round-tripped to %v, sent %v — the wire pair drops it",
					rv.Type().Field(i).Name, rv.Field(i).Interface(), rw.Field(i).Interface())
			}
		}
		t.Fatal("provider.Usage does not survive UsageToWire -> usageFromWire intact")
	}
}

// fullyPopulatedUsage returns a Usage with every field set to a distinct
// non-zero value. Distinct so a pair of fields crossed in the mapping fails
// rather than cancelling out; non-zero so a field dropped to its zero value
// cannot pass as "carried".
func fullyPopulatedUsage(t *testing.T) provider.Usage {
	t.Helper()
	var u provider.Usage
	rv := reflect.ValueOf(&u).Elem()
	for i := 0; i < rv.NumField(); i++ {
		f := rv.Field(i)
		switch f.Kind() {
		case reflect.Int:
			f.SetInt(int64(i + 1))
		case reflect.Float64:
			f.SetFloat(float64(i) + 1.5)
		case reflect.Bool:
			f.SetBool(true)
		default:
			// A new kind means this helper can no longer claim to fill the
			// struct, and a guard that silently skips a field it cannot fill
			// is worse than no guard.
			t.Fatalf("provider.Usage.%s is a %s, which this guard does not know how to populate",
				rv.Type().Field(i).Name, f.Kind())
		}
	}
	return u
}

func ptr[T any](v T) *T { return &v }
