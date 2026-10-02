//go:build !terva_scripting

package build

// taggedGroupLines is empty in a build without terva_scripting. See
// golden_tags_scripting_test.go.
var taggedGroupLines []string
