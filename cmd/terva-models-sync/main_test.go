package main

import (
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// A sync that cannot read back what it wrote churns the block on every run,
// and one that drops a field it does not parse strips it from every row
// models.dev stops listing. Rendering the parsed block must reproduce it
// byte for byte, tiers included.
func TestRenderRoundTripsTheBakedBlock(t *testing.T) {
	src, err := os.ReadFile("../../" + catalogPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, tg := range targets {
		lines := strings.Split(string(src), "\n")
		start, end, ok := locateBlock(lines, tg.provider)
		if !ok {
			t.Fatalf("no %s block", tg.provider)
		}
		baked := parseBaked(string(src), tg.provider)
		es := make([]entry, 0, len(baked))
		for _, e := range baked {
			es = append(es, e)
		}
		sort.Slice(es, func(i, j int) bool { return es[i].ID < es[j].ID })

		got := strings.Split(strings.TrimSuffix(render(tg, es), "\n"), "\n")
		want := lines[start:end]
		if !reflect.DeepEqual(got, want) {
			for i := range want {
				if i >= len(got) || got[i] != want[i] {
					t.Fatalf("%s line %d does not round-trip:\n got: %s\nwant: %s", tg.provider, i, at(got, i), want[i])
				}
			}
			t.Fatalf("%s rendered %d lines, want %d", tg.provider, len(got), len(want))
		}
		tiered := 0
		for _, e := range es {
			if len(e.Tiers) > 0 {
				tiered++
			}
		}
		if tiered == 0 {
			t.Errorf("%s: no baked row carries tiers, so the round trip proves nothing about them", tg.provider)
		}
	}
}

func at(s []string, i int) string {
	if i < len(s) {
		return s[i]
	}
	return "(missing)"
}

// models.dev writes the same threshold as 200000 on one row and 200001 on
// the next. Both mean the step starts past 200,000, and a non-context tier
// is not a prompt-length step at all. A step that folds to zero would apply
// to every prompt, so it is dropped.
func TestTiersFromNormalizesThresholds(t *testing.T) {
	mk := func(typ string, size int, in float64) mdTier {
		var m mdTier
		m.Tier.Type, m.Tier.Size, m.Input = typ, size, in
		return m
	}
	got := tiersFrom([]mdTier{
		mk("context", 128001, 3),
		mk("context", 32000, 1.8),
		mk("batch", 1, 9),
		mk("context", 262145, 6),
		mk("context", 1, 99),
		mk("context", 0, 99),
	})
	want := []tier{{Above: 32000, In: 1.8}, {Above: 128000, In: 3}, {Above: 262144, In: 6}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tiersFrom = %+v, want %+v", got, want)
	}
}
