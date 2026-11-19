package config

import (
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/testsupport"
)

// 🚨 A default of a kind is authority that a routine's card does not show, so
// only the user layer sets it. A project file that names the key must change
// nothing, for a trusted project too.
func TestTalkootRoutinesIsAUserLayerKey(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	cwd := testsupport.TempDir(t)
	on := true
	if err := SaveConfig(Config{TalkootEnabled: &on}); err != nil {
		t.Fatal(err)
	}
	writeProjectConfig(t, cwd, `{"disable_extensions":["positive-control"],"talkoot_routines":{"max":50,"kinds":{"task":{"spend_usd_per_day":1000}}}}`)
	for _, trusted := range []bool{false, true} {
		eff := ResolveConfig(cwd, trusted)
		if len(eff.DisableExtensions) != 1 || eff.DisableExtensions[0] != "positive-control" {
			t.Fatal("the project fixture did not reach the resolver")
		}
		if eff.TalkootRoutines != nil || TalkootRoutines().On {
			t.Fatalf("a project config (trusted=%v) turned routines on", trusted)
		}
	}
	// The user layer does turn it on, with the built-in defaults under what
	// the person set.
	spend, catchUp := 12.5, false
	if err := SaveConfig(Config{TalkootEnabled: &on, TalkootRoutines: &TalkootRoutinesConfig{Max: 4, Kinds: map[string]TalkootRoutineKindConfig{
		"task": {SpendUSDPerDay: &spend, CatchUp: &catchUp},
	}}}); err != nil {
		t.Fatal(err)
	}
	got := TalkootRoutines()
	if !got.On || got.Max != 4 || got.Problem != "" || len(got.Kinds) != 3 {
		t.Fatalf("TalkootRoutines() = %+v, want the user layer's policy", got)
	}
	if task := got.Kinds["task"]; task.SpendUSDPerDay != 12.5 || task.CatchUp || task.ReviewsPerDay != 2 || task.FollowUps != 2 {
		t.Errorf("a kind keeps the built-in default of each field the person left out: %+v", task)
	}
	if brief := got.Kinds["brief"]; brief != builtinRoutineDefaults["brief"] || brief.CatchUp {
		t.Errorf("a kind the person left out takes the built-in defaults, and a brief does not catch up: %+v", brief)
	}
	if !got.Kinds["watcher"].CatchUp {
		t.Error("a watcher catches up by default")
	}
}

func TestTalkootRoutinesIsOffByDefault(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	on := true
	if err := SaveConfig(Config{TalkootEnabled: &on}); err != nil {
		t.Fatal(err)
	}
	if got := TalkootRoutines(); got.On || got.Problem != "" {
		t.Fatalf("with no key, routines are off and nothing is wrong: %+v", got)
	}
}

// 🚨 Every doubt reads as off. A bad default must not become a built-in one
// in silence, because the person believes that their own value holds.
func TestADoubtfulRoutinesKeyReadsAsOff(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	on, off := true, false
	neg, negSpend := -1, -0.01
	// 🚨 An approval stores a default for each limit that an ask left out.
	// A default above the top of an ask would store a routine that no
	// later read accepts.
	manyFollowUps, manyReviews, longEnd, bigSpend := TalkootRoutineMaxFollowUps+1, TalkootRoutineMaxReviewsPerDay+1, TalkootRoutineMaxEnd+1, TalkootRoutineMaxSpendUSDPerDay+0.5
	kinds := func(k string, c TalkootRoutineKindConfig) map[string]TalkootRoutineKindConfig {
		return map[string]TalkootRoutineKindConfig{k: c}
	}
	for _, tc := range []struct {
		name string
		cfg  Config
		want string
	}{
		{"talkoot off", Config{TalkootEnabled: &off, TalkootRoutines: &TalkootRoutinesConfig{Max: 2}}, "talkoot_enabled is off"},
		{"no count", Config{TalkootEnabled: &on, TalkootRoutines: &TalkootRoutinesConfig{}}, "max is 0"},
		{"a negative count", Config{TalkootEnabled: &on, TalkootRoutines: &TalkootRoutinesConfig{Max: -3}}, "max is -3"},
		{"an unknown kind", Config{TalkootEnabled: &on, TalkootRoutines: &TalkootRoutinesConfig{Max: 2, Kinds: kinds("cron", TalkootRoutineKindConfig{})}}, `names "cron"`},
		{"a negative allowance", Config{TalkootEnabled: &on, TalkootRoutines: &TalkootRoutinesConfig{Max: 2, Kinds: kinds("task", TalkootRoutineKindConfig{FollowUps: &neg})}}, "kinds.task.follow_ups is -1"},
		{"a negative review limit", Config{TalkootEnabled: &on, TalkootRoutines: &TalkootRoutinesConfig{Max: 2, Kinds: kinds("task", TalkootRoutineKindConfig{ReviewsPerDay: &neg})}}, "kinds.task.reviews_per_day is -1"},
		{"a negative end", Config{TalkootEnabled: &on, TalkootRoutines: &TalkootRoutinesConfig{Max: 2, Kinds: kinds("brief", TalkootRoutineKindConfig{EndAfterDays: &neg})}}, "kinds.brief.end_after_days is -1"},
		{"a negative spend", Config{TalkootEnabled: &on, TalkootRoutines: &TalkootRoutinesConfig{Max: 2, Kinds: kinds("watcher", TalkootRoutineKindConfig{SpendUSDPerDay: &negSpend})}}, "kinds.watcher.spend_usd_per_day is -0.01"},
		{"an allowance above the top", Config{TalkootEnabled: &on, TalkootRoutines: &TalkootRoutinesConfig{Max: 2, Kinds: kinds("task", TalkootRoutineKindConfig{FollowUps: &manyFollowUps})}}, "kinds.task.follow_ups is 101, and it must be from 0 to 100"},
		{"a review limit above the top", Config{TalkootEnabled: &on, TalkootRoutines: &TalkootRoutinesConfig{Max: 2, Kinds: kinds("task", TalkootRoutineKindConfig{ReviewsPerDay: &manyReviews})}}, "kinds.task.reviews_per_day is 1001"},
		{"an end above the top", Config{TalkootEnabled: &on, TalkootRoutines: &TalkootRoutinesConfig{Max: 2, Kinds: kinds("brief", TalkootRoutineKindConfig{EndAfterRuns: &longEnd})}}, "kinds.brief.end_after_runs is 100001"},
		{"a spend above the top", Config{TalkootEnabled: &on, TalkootRoutines: &TalkootRoutinesConfig{Max: 2, Kinds: kinds("watcher", TalkootRoutineKindConfig{SpendUSDPerDay: &bigSpend})}}, "kinds.watcher.spend_usd_per_day is 10000.5"},
		{"a wait that is no duration", Config{TalkootEnabled: &on, TalkootRoutines: &TalkootRoutinesConfig{Max: 2, QuestionWait: "soon"}}, `question_wait "soon" is not a duration`},
		{"a wait under a minute", Config{TalkootEnabled: &on, TalkootRoutines: &TalkootRoutinesConfig{Max: 2, QuestionWait: "30s"}}, `question_wait "30s" is not a duration from one minute to 7 days`},
		{"a wait of nothing", Config{TalkootEnabled: &on, TalkootRoutines: &TalkootRoutinesConfig{Max: 2, QuestionWait: "0"}}, `question_wait "0"`},
		{"a wait above the top", Config{TalkootEnabled: &on, TalkootRoutines: &TalkootRoutinesConfig{Max: 2, QuestionWait: "169h"}}, `question_wait "169h"`},
	} {
		if err := SaveConfig(tc.cfg); err != nil {
			t.Fatal(err)
		}
		got := TalkootRoutines()
		if got.On || !strings.Contains(got.Problem, tc.want) {
			t.Errorf("%s: got %+v, want off with a problem that holds %q", tc.name, got, tc.want)
		}
	}
}

// The wait for a person in a routine's chain: the key sets it, an empty key
// takes the default, and a policy that is off still has one.
func TestTheQuestionWaitOfARoutinesChain(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	on := true
	if err := SaveConfig(Config{TalkootEnabled: &on, TalkootRoutines: &TalkootRoutinesConfig{Max: 2, QuestionWait: "90m"}}); err != nil {
		t.Fatal(err)
	}
	if got := TalkootRoutines(); !got.On || got.Wait() != 90*time.Minute {
		t.Fatalf("the key sets the wait: %+v", got)
	}
	if err := SaveConfig(Config{TalkootEnabled: &on, TalkootRoutines: &TalkootRoutinesConfig{Max: 2}}); err != nil {
		t.Fatal(err)
	}
	if got := TalkootRoutines(); !got.On || got.Wait() != DefaultTalkootQuestionWait {
		t.Fatalf("an empty key takes the default: %+v", got)
	}
	// 🚨 A problem in another field keeps the wait that the key set. A
	// person who set a short wait and then mistyped a limit must not get
	// the default in silence.
	neg := -1
	for name, c := range map[string]TalkootRoutinesConfig{
		"a bad limit": {Max: 2, QuestionWait: "30m", Kinds: map[string]TalkootRoutineKindConfig{"task": {FollowUps: &neg}}},
		"no count":    {QuestionWait: "30m"},
	} {
		if err := SaveConfig(Config{TalkootEnabled: &on, TalkootRoutines: &c}); err != nil {
			t.Fatal(err)
		}
		if got := TalkootRoutines(); got.On || got.Problem == "" || got.Wait() != 30*time.Minute {
			t.Fatalf("%s: routines are off with a problem, and the wait is kept: %+v", name, got)
		}
	}
	// 🚨 A chain of a routine can still work after a person turned routines
	// off, and its question still needs a deadline.
	if err := SaveConfig(Config{TalkootEnabled: &on}); err != nil {
		t.Fatal(err)
	}
	if got := TalkootRoutines(); got.On || got.Wait() != DefaultTalkootQuestionWait {
		t.Fatalf("a policy that is off still has a wait: %+v", got)
	}
}

// The patterns that a person adds to the merge rule of a chain that no
// person began (decision 0029 rule 15).
func TestTheMergeRulesPatternsOfARoutinesChain(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	on := true
	save := func(c TalkootRoutinesConfig) TalkootRoutinePolicy {
		t.Helper()
		if err := SaveConfig(Config{TalkootEnabled: &on, TalkootRoutines: &c}); err != nil {
			t.Fatal(err)
		}
		return TalkootRoutines()
	}
	got := save(TalkootRoutinesConfig{Max: 2, MergeCommands: []string{`^just land\b`}, ProtectedBranches: []string{"release", "release/*"}})
	if !got.On || len(got.MergeCommands) != 1 || !got.MergeCommands[0].MatchString("just land 12") || len(got.ProtectedBranches) != 2 {
		t.Fatalf("the key sets the patterns: %+v", got)
	}
	// 🚨 A pattern that does not compile turns routines off. A pattern that
	// a person mistyped must not become a rule that matches nothing in
	// silence. Every pattern that does compile still binds a chain at work.
	for name, tc := range map[string]struct {
		c    TalkootRoutinesConfig
		want string
	}{
		"a merge pattern that does not compile": {TalkootRoutinesConfig{Max: 2, MergeCommands: []string{`^just land\b`, `(`}, ProtectedBranches: []string{"release"}}, `merge_commands holds "("`},
		"an empty merge pattern":                {TalkootRoutinesConfig{Max: 2, MergeCommands: []string{`^just land\b`, " "}, ProtectedBranches: []string{"release"}}, `merge_commands holds " "`},
		"a branch pattern that does not parse":  {TalkootRoutinesConfig{Max: 2, MergeCommands: []string{`^just land\b`}, ProtectedBranches: []string{"release", "[a"}}, `protected_branches holds "[a"`},
		"a problem in another field":            {TalkootRoutinesConfig{MergeCommands: []string{`^just land\b`}, ProtectedBranches: []string{"release"}}, "max is 0"},
		"a wait that is no duration":            {TalkootRoutinesConfig{Max: 2, QuestionWait: "soon", MergeCommands: []string{`^just land\b`}, ProtectedBranches: []string{"release"}}, "question_wait"},
	} {
		got := save(tc.c)
		if got.On || !strings.Contains(got.Problem, tc.want) {
			t.Errorf("%s: got %+v, want off with a problem that holds %q", name, got, tc.want)
		}
		if len(got.MergeCommands) != 1 || !got.MergeCommands[0].MatchString("just land 12") || len(got.ProtectedBranches) != 1 || got.ProtectedBranches[0] != "release" {
			t.Errorf("%s: the patterns that compile are kept: %+v", name, got)
		}
	}
}

// The patterns that name a review request (decision 0029 rule 17).
func TestThePatternsOfAReviewRequest(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	on := true
	save := func(c TalkootRoutinesConfig) TalkootRoutinePolicy {
		t.Helper()
		if err := SaveConfig(Config{TalkootEnabled: &on, TalkootRoutines: &c}); err != nil {
			t.Fatal(err)
		}
		return TalkootRoutines()
	}
	if got := save(TalkootRoutinesConfig{Max: 2}); !got.On || len(got.ReviewCommands) != 0 {
		t.Fatalf("a key with no pattern names no review request: %+v", got)
	}
	got := save(TalkootRoutinesConfig{Max: 2, ReviewCommands: []string{`^just pr-review\b`, `^tea pulls review\b`}})
	if !got.On || len(got.ReviewCommands) != 2 || !got.ReviewCommands[0].MatchString("just pr-review 12") || !got.ReviewCommands[1].MatchString("tea pulls review 12") {
		t.Fatalf("the key sets the patterns: %+v", got)
	}
	// 🚨 A pattern that does not compile turns routines off. A pattern that a
	// person mistyped must not become a count of nothing in silence. Every
	// pattern that does compile still counts for a chain at work, beside a
	// problem in any field.
	for name, tc := range map[string]struct {
		c    TalkootRoutinesConfig
		want string
	}{
		"a pattern that does not compile": {TalkootRoutinesConfig{Max: 2, ReviewCommands: []string{`^just pr-review\b`, `(`}}, `review_commands holds "("`},
		"an empty pattern":                {TalkootRoutinesConfig{Max: 2, ReviewCommands: []string{`^just pr-review\b`, " "}}, `review_commands holds " "`},
		"a problem in another field":      {TalkootRoutinesConfig{ReviewCommands: []string{`^just pr-review\b`}}, "max is 0"},
		"a wait that is no duration":      {TalkootRoutinesConfig{Max: 2, QuestionWait: "soon", ReviewCommands: []string{`^just pr-review\b`}}, "question_wait"},
	} {
		got := save(tc.c)
		if got.On || !strings.Contains(got.Problem, tc.want) {
			t.Errorf("%s: got %+v, want off with a problem that holds %q", name, got, tc.want)
		}
		if len(got.ReviewCommands) != 1 || !got.ReviewCommands[0].MatchString("just pr-review 12") {
			t.Errorf("%s: the patterns that compile are kept: %+v", name, got)
		}
	}
}
