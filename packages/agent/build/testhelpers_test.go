package build

import (
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// withTempHome points TERVA_HOME at a temp dir for config isolation.
//
// It lived in permissions_test.go until that file left for package permissions,
// which is how five unrelated build tests turned out to be leaning on it. It has
// its own file now rather than sitting inside whichever test needed it first.
func withTempHome(t *testing.T) string {
	t.Helper()
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	return home
}
