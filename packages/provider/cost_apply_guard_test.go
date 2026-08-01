package provider

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Pricing a response is a two-field job — CostUSD and CacheSavedUSD — and only
// the decoder can do it, because only the decoder still knows which model
// answered. Read the usage row back later and that is gone: it records tokens
// and dollars, never a model, and a session that switched models mid-way has no
// single price sheet to recover it from.
//
// So the failure mode is a new decoder (or a new branch of an old one) that
// assigns CostUSD the way all five did before ApplyCost existed. It compiles, it
// reports the right cost, and the cache savings it never set stay zero — a
// session that reads as having saved nothing, which is indistinguishable from a
// session where the cache genuinely did nothing. Nobody would file that.
//
// This guard is written the way host_census_test.go is written: it discovers its
// subjects by what they ASSIGN rather than from a list of provider files. A list
// cannot fail when a provider is added, and "a provider was added" is the case.
var costAssign = regexp.MustCompile(`\.CostUSD\s*=[^=]`)

// applyCostHome is the one file allowed to assign CostUSD: ApplyCost itself
// lives there, and is the door everything else goes through.
const applyCostHome = "models.go"

func TestEveryProviderPricesThroughApplyCost(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read provider package: %v", err)
	}
	var offenders []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if name == applyCostHome {
			continue
		}
		src, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			if costAssign.MatchString(line) {
				offenders = append(offenders, fmt.Sprintf("%s:%d: %s", name, i+1, strings.TrimSpace(line)))
			}
		}
	}
	if len(offenders) > 0 {
		t.Errorf("these price a response without ApplyCost, so CacheSavedUSD stays zero:\n  %s\n\n"+
			"Call ApplyCost(model, &usage) instead — it sets both money fields from the\n"+
			"model in scope, which is the only place CacheSavedUSD can be computed.",
			strings.Join(offenders, "\n  "))
	}
}

// The guard above is worth exactly as much as its ability to see a violation, and
// a regexp that matched nothing would pass on a package that had regressed
// entirely. Point it at a known-bad line and require a hit.
func TestTheApplyCostGuardRecognisesADirectAssignment(t *testing.T) {
	bad := []string{
		"\t\tusage.CostUSD = ComputeCost(model, usage)",
		"u.CostUSD = 0",
		"\tout.CostUSD=ComputeCost(m, out)",
	}
	for _, line := range bad {
		if !costAssign.MatchString(line) {
			t.Errorf("guard missed a direct assignment: %q", line)
		}
	}
	good := []string{
		"\tif u.CostUSD == 0 {",
		"\t\tCostUSD:          u.CostUSD + v.CostUSD,",
		"// CostUSD is stamped by ApplyCost.",
	}
	for _, line := range good {
		if costAssign.MatchString(line) {
			t.Errorf("guard fired on a read, not an assignment: %q", line)
		}
	}
}
