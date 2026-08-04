package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

func askArgsFrom(t *testing.T, raw string) askArgs {
	t.Helper()
	var a askArgs
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	return a
}

// The tri-state is the whole point, and it is the one thing a later change
// cannot silently take away: with a plain bool the "did not say" case and the
// "said no" case are the same value, and defaulting an unstated field to open
// would also override a model that deliberately closed the set.
//
// Asserted on the STORED POINTER rather than on the resolved flag, because
// once the default is open an assertion of the effective value passes whether
// or not the model was consulted at all.
func TestAllowCustomIsStoredAsATriState(t *testing.T) {
	for _, tc := range []struct {
		name   string
		raw    string
		stated bool
		want   bool // meaningful only when stated
	}{
		{"unstated", `{"question":"Which?","options":["a","b"]}`, false, false},
		{"closed", `{"question":"Which?","options":["a","b"],"allow_custom":false}`, true, false},
		{"opened", `{"question":"Which?","options":["a","b"],"allow_custom":true}`, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := askArgsFrom(t, tc.raw)
			if stated := a.AllowCustom != nil; stated != tc.stated {
				t.Fatalf("stated = %v, want %v (a bool cannot tell these apart)", stated, tc.stated)
			}
			if tc.stated && *a.AllowCustom != tc.want {
				t.Errorf("stored %v, want %v", *a.AllowCustom, tc.want)
			}
		})
	}
}

// Tested THROUGH questions(), the only production caller: the resolution is
// worth nothing if it happens somewhere the tool does not go.
func TestAnUnstatedAllowCustomLeavesTheQuestionOpen(t *testing.T) {
	for _, tc := range []struct {
		shape string
		raw   string
	}{
		{"singular", `{"question":"Which?","options":["a","b"]}`},
		{"set", `{"questions":[{"question":"Which?","options":["a","b"]}]}`},
	} {
		t.Run(tc.shape, func(t *testing.T) {
			qs, err := askArgsFrom(t, tc.raw).questions()
			if err != nil {
				t.Fatalf("questions: %v", err)
			}
			if !qs[0].AllowCustom {
				t.Error("a model that said nothing about allow_custom closed the set anyway")
			}
		})
	}
}

// The flip must not cost the model the ability to say no — a closed set is a
// real answer ("which of these three branches?"), just the rarer one.
func TestTheModelCanStillCloseTheSet(t *testing.T) {
	for _, tc := range []struct {
		shape string
		raw   string
	}{
		{"singular", `{"question":"Which?","options":["a","b"],"allow_custom":false}`},
		{"set", `{"questions":[{"question":"Which?","options":["a","b"],"allow_custom":false}]}`},
	} {
		t.Run(tc.shape, func(t *testing.T) {
			qs, err := askArgsFrom(t, tc.raw).questions()
			if err != nil {
				t.Fatalf("questions: %v", err)
			}
			if qs[0].AllowCustom {
				t.Error("allow_custom:false was overridden by the default")
			}
		})
	}
}

// Each question carries its own answer. A set that closed one of them must not
// close the rest — the failure would be invisible, since every question in the
// set renders the same way apart from the missing input.
func TestClosingOneQuestionLeavesTheOthersOpen(t *testing.T) {
	qs, err := askArgsFrom(t, `{"questions":[
		{"question":"Which branch?","options":["main","next"],"allow_custom":false},
		{"question":"Which approach?","options":["rewrite","patch"]},
		{"question":"Ship when?","options":["now","after the cut"],"allow_custom":true}
	]}`).questions()
	if err != nil {
		t.Fatalf("questions: %v", err)
	}
	want := []bool{false, true, true}
	for i, w := range want {
		if qs[i].AllowCustom != w {
			t.Errorf("question %d allow_custom = %v, want %v", i+1, qs[i].AllowCustom, w)
		}
	}
}

// The description is the load-bearing half of this change — the resolution
// only decides what an unstated field means, and the schema is the only place
// the model is told there is a decision to make.
func TestTheSchemaSaysWhatAnUnstatedAllowCustomMeans(t *testing.T) {
	for _, want := range []string{"The default is true", "false only when"} {
		if !strings.Contains(allowCustomDesc, want) {
			t.Errorf("allow_custom description never says %q:\n%s", want, allowCustomDesc)
		}
	}
}

// Both shapes are hand-written into one JSON literal, which is why the shared
// text lives in consts — but nothing stopped the next field from being pasted
// twice and then edited once. This enrols every shared property by scanning
// the schema rather than listing them, so a field added tomorrow is covered
// without anyone remembering to add it here.
//
// The rule is PREFIX, not equality, because one field legitimately says more
// inside the array: slug matters most in a set, where the tab strip is one
// chip per question, so the set's copy is slugDesc plus that sentence. Shared
// text first, anything the set adds after it — which a pasted-and-edited
// description still fails.
//
// "question" is the single exemption. The singular one has to point at the
// plural shape ("use 'questions' to ask several at once"), and inside the
// array that sentence would be advice to do what it is already doing.
func TestBothShapesDescribeTheirSharedFieldsFromTheSameText(t *testing.T) {
	var doc struct {
		Properties map[string]struct {
			Description string `json:"description"`
			Items       struct {
				Properties map[string]struct {
					Description string `json:"description"`
				} `json:"properties"`
			} `json:"items"`
		} `json:"properties"`
	}
	if err := json.Unmarshal([]byte(askSchema), &doc); err != nil {
		t.Fatalf("the tool advertises a schema that is not JSON: %v", err)
	}
	inner := doc.Properties["questions"].Items.Properties
	if len(inner) == 0 {
		t.Fatal("questions[] has no item properties — the walk found nothing to check")
	}
	shared := 0
	for name, item := range inner {
		outer, ok := doc.Properties[name]
		if !ok || name == "question" {
			continue
		}
		shared++
		if item.Description == "" || outer.Description == "" {
			t.Errorf("%q is missing a description in one of the two shapes", name)
			continue
		}
		if !strings.HasPrefix(item.Description, outer.Description) {
			t.Errorf("%q does not start from the singular shape's text; put the shared part in a const and append after it\n singular: %s\n      set: %s", name, outer.Description, item.Description)
		}
	}
	if shared < 3 {
		t.Fatalf("only %d shared properties found — the walk is not seeing the schema", shared)
	}
}
