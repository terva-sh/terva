package lore

import "testing"

func activated(r Result) []string {
	var out []string
	for _, e := range r.All() {
		out = append(out, e.Name)
	}
	return out
}

func has(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

func TestSelect_ConstantAlwaysActivates(t *testing.T) {
	r := Select([]Entry{{Name: "c", Constant: true, Content: "x", Order: 100}}, Config{}, nil, ApproxTokens)
	if got := activated(r); len(got) != 1 || got[0] != "c" {
		t.Fatalf("constant should activate with empty scan; got %v", got)
	}
}

func TestSelect_KeywordMatch(t *testing.T) {
	entries := []Entry{
		{Name: "auth", Keys: []string{"auth"}, Content: "a", Order: 100},
		{Name: "db", Keys: []string{"database"}, Content: "b", Order: 100},
	}
	r := Select(entries, Config{}, []string{"how does auth work"}, ApproxTokens)
	got := activated(r)
	if !has(got, "auth") || has(got, "db") {
		t.Fatalf("only auth should fire; got %v", got)
	}
}

func TestSelect_CaseSensitivity(t *testing.T) {
	insensitive := []Entry{{Name: "k", Keys: []string{"Auth"}, Content: "x", Order: 1}}
	if got := activated(Select(insensitive, Config{}, []string{"the auth flow"}, ApproxTokens)); !has(got, "k") {
		t.Errorf("case-insensitive default should match lowercased scan; got %v", got)
	}
	sensitive := []Entry{{Name: "k", Keys: []string{"Auth"}, CaseSensitive: true, Content: "x", Order: 1}}
	if got := activated(Select(sensitive, Config{}, []string{"the auth flow"}, ApproxTokens)); has(got, "k") {
		t.Errorf("case-sensitive key should not match lowercased scan; got %v", got)
	}

	// A collection-level case_sensitive:true applies to entries that omit their
	// own setting (regression: this default used to be parsed but never applied).
	deflt := []Entry{{Name: "k", Keys: []string{"Auth"}, Content: "x", Order: 1}}
	if got := activated(Select(deflt, Config{CaseSensitive: true}, []string{"the auth flow"}, ApproxTokens)); has(got, "k") {
		t.Errorf("collection case_sensitive should make an omitting entry case-sensitive (no match on lowercase); got %v", got)
	}
	if got := activated(Select(deflt, Config{CaseSensitive: true}, []string{"the Auth flow"}, ApproxTokens)); !has(got, "k") {
		t.Errorf("collection case_sensitive should still match the exact case; got %v", got)
	}
}

func TestSelect_WholeWords(t *testing.T) {
	entries := []Entry{{Name: "k", Keys: []string{"class"}, Content: "x", Order: 1}}
	scan := []string{"a classic pattern"}
	if got := activated(Select(entries, Config{}, scan, ApproxTokens)); !has(got, "k") {
		t.Errorf("substring default should match 'class' in 'classic'; got %v", got)
	}
	if got := activated(Select(entries, Config{MatchWholeWords: true}, scan, ApproxTokens)); has(got, "k") {
		t.Errorf("whole-word should not match 'class' inside 'classic'; got %v", got)
	}
}

func TestSelect_MessageBoundary(t *testing.T) {
	// Newest-first; the phrase "world foo" only exists if messages are
	// concatenated without a boundary. The \x01 separator must block it.
	entries := []Entry{
		{Name: "phrase", Keys: []string{"world foo"}, Content: "x", Order: 1},
		{Name: "single", Keys: []string{"world"}, Content: "x", Order: 1},
	}
	r := Select(entries, Config{ScanDepth: 2}, []string{"world", "foo"}, ApproxTokens)
	got := activated(r)
	if has(got, "phrase") {
		t.Errorf("phrase must not match across a message boundary; got %v", got)
	}
	if !has(got, "single") {
		t.Errorf("single-word key should still match; got %v", got)
	}
}

func TestSelect_ScanDepth(t *testing.T) {
	entries := []Entry{{Name: "k", Keys: []string{"older"}, Content: "x", Order: 1}}
	scan := []string{"recent message", "an older one"} // index 0 newest
	if got := activated(Select(entries, Config{ScanDepth: 1}, scan, ApproxTokens)); has(got, "k") {
		t.Errorf("depth 1 should not reach the older message; got %v", got)
	}
	if got := activated(Select(entries, Config{ScanDepth: 2}, scan, ApproxTokens)); !has(got, "k") {
		t.Errorf("depth 2 should reach the older message; got %v", got)
	}
}

func TestSelect_SecondaryLogic(t *testing.T) {
	scan := []string{"primary alpha present, beta present"}
	mk := func(logic Logic, sec ...string) []Entry {
		return []Entry{{Name: "k", Keys: []string{"primary"}, SecondaryKeys: sec, Logic: logic, Content: "x", Order: 1}}
	}
	cases := []struct {
		name  string
		logic Logic
		sec   []string
		want  bool
	}{
		{"and_any hit", LogicAndAny, []string{"alpha", "zzz"}, true},
		{"and_any miss", LogicAndAny, []string{"zzz"}, false},
		{"and_all hit", LogicAndAll, []string{"alpha", "beta"}, true},
		{"and_all miss", LogicAndAll, []string{"alpha", "zzz"}, false},
		{"not_any hit", LogicNotAny, []string{"zzz"}, true},
		{"not_any miss", LogicNotAny, []string{"alpha"}, false},
		{"not_all hit", LogicNotAll, []string{"alpha", "zzz"}, true},
		{"not_all miss", LogicNotAll, []string{"alpha", "beta"}, false},
	}
	for _, c := range cases {
		got := has(activated(Select(mk(c.logic, c.sec...), Config{}, scan, ApproxTokens)), "k")
		if got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestSelect_Budget(t *testing.T) {
	// Each content is 8 bytes => 2 tokens under ApproxTokens. Budget 5
	// tokens fits two entries; the third (lowest Order) is dropped.
	body := "aaaaaaaa"
	entries := []Entry{
		{Name: "hi", Constant: true, Content: body, Order: 300},
		{Name: "mid", Constant: true, Content: body, Order: 200},
		{Name: "lo", Constant: true, Content: body, Order: 100},
	}
	r := Select(entries, Config{TokenBudget: 5}, nil, ApproxTokens)
	got := activated(r)
	if !has(got, "hi") || !has(got, "mid") || has(got, "lo") {
		t.Fatalf("budget should keep hi+mid, drop lo; got %v", got)
	}
	if len(r.Dropped) != 1 || r.Dropped[0].Name != "lo" {
		t.Fatalf("dropped should list lo; got %v", r.Dropped)
	}
}

func TestSelect_Placement(t *testing.T) {
	entries := []Entry{
		{Name: "aBefore", Constant: true, Position: PositionBefore, Content: "x", Order: 200},
		{Name: "aAfter100", Constant: true, Position: PositionAfter, Content: "x", Order: 100},
		{Name: "aAfter300", Constant: true, Position: PositionAfter, Content: "x", Order: 300},
	}
	r := Select(entries, Config{}, nil, ApproxTokens)
	if len(r.Before) != 1 || r.Before[0].Name != "aBefore" {
		t.Errorf("before bucket = %v", r.Before)
	}
	// After bucket ascending by Order: 100 then 300.
	if len(r.After) != 2 || r.After[0].Name != "aAfter100" || r.After[1].Name != "aAfter300" {
		t.Errorf("after bucket order = %v", r.After)
	}
}

func TestSelect_Recursion(t *testing.T) {
	// A fires on "trigger"; its content mentions "xyzzy"; B is keyed on
	// "xyzzy", which appears only in A's content.
	mk := func(preventA, excludeB bool) []Entry {
		return []Entry{
			{Name: "A", Keys: []string{"trigger"}, Content: "mentions xyzzy here", Order: 10, PreventRecursion: preventA},
			{Name: "B", Keys: []string{"xyzzy"}, Content: "b", Order: 10, ExcludeRecursion: excludeB},
		}
	}
	scan := []string{"please trigger it"}

	if got := activated(Select(mk(false, false), Config{}, scan, ApproxTokens)); has(got, "B") {
		t.Errorf("without recursive scanning, B must not fire; got %v", got)
	}
	if got := activated(Select(mk(false, false), Config{RecursiveScanning: true}, scan, ApproxTokens)); !has(got, "B") {
		t.Errorf("with recursion, B should fire off A's content; got %v", got)
	}
	if got := activated(Select(mk(true, false), Config{RecursiveScanning: true}, scan, ApproxTokens)); has(got, "B") {
		t.Errorf("prevent_recursion on A should keep B from firing; got %v", got)
	}
	if got := activated(Select(mk(false, true), Config{RecursiveScanning: true}, scan, ApproxTokens)); has(got, "B") {
		t.Errorf("exclude_recursion on B should keep it from firing via recursion; got %v", got)
	}
}

func TestSelect_Empty(t *testing.T) {
	if r := Select(nil, Config{}, []string{"anything"}, ApproxTokens); len(r.All()) != 0 {
		t.Fatalf("no entries => no activations; got %v", activated(r))
	}
}

func TestContainsKey_UnicodeBoundary(t *testing.T) {
	// Ensure whole-word matching handles multibyte runes without panicking
	// and treats letters as word characters.
	if !containsKey("café society", "café", true) {
		t.Errorf("expected café to match on a boundary")
	}
	if containsKey("caféteria", "café", true) {
		t.Errorf("café should not whole-word-match inside caféteria")
	}
}
