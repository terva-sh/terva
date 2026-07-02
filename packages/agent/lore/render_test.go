package lore

import (
	"strings"
	"testing"
)

func TestPartition(t *testing.T) {
	c, tr := Partition([]Entry{
		{Name: "c1", Constant: true},
		{Name: "t1", Keys: []string{"x"}},
		{Name: "c2", Constant: true},
	})
	if len(c) != 2 || len(tr) != 1 {
		t.Fatalf("partition: constant=%d triggered=%d", len(c), len(tr))
	}
}

func TestRender(t *testing.T) {
	if Render(nil) != "" {
		t.Errorf("empty render should be blank")
	}
	got := Render([]Entry{{Content: "one"}, {Content: "two"}})
	if !strings.HasPrefix(got, "<lore>") || !strings.HasSuffix(got, "</lore>") {
		t.Errorf("missing wrapper: %q", got)
	}
	if !strings.Contains(got, "one") || !strings.Contains(got, "two") {
		t.Errorf("missing content: %q", got)
	}
}

func TestPlaceConstant(t *testing.T) {
	if PlaceConstant(nil) != "" {
		t.Errorf("no constant => blank")
	}
	got := PlaceConstant([]Entry{
		{Name: "after", Constant: true, Position: PositionAfter, Content: "AFTERTEXT", Order: 100},
		{Name: "before", Constant: true, Position: PositionBefore, Content: "BEFORETEXT", Order: 100},
	})
	bi := strings.Index(got, "BEFORETEXT")
	ai := strings.Index(got, "AFTERTEXT")
	if bi < 0 || ai < 0 || bi > ai {
		t.Errorf("before-position should precede after-position: %q", got)
	}
}
