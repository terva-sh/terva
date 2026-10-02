//go:build terva_scripting

package build

// taggedGroupLines are the [inactive tool groups] lines that only a
// terva_scripting build writes. The goldens hold the untagged build's note, so
// a tagged run removes these lines after it checks that they are there.
var taggedGroupLines = []string{
	"  - scripting: code_execution\n",
	"  - scripting_mutating: code_execution_mutating\n",
}
