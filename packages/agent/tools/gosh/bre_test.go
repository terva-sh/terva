//go:build terva_gosh

package gosh

import (
	"regexp"
	"strings"
	"testing"
)

// The expectations are GNU grep 3.11's own answers, run as
// `/usr/bin/grep -e PATTERN` over breInput on 2026-10-01. They are kept as
// data so the test needs no host grep, which neither Windows nor iOS has.
var breInput = []string{
	"foo", "bar", "baz", "a+b", "aab", "x{2}", "xx", "Get(x)", "ababc", "c",
	"*star", "star", "a^b", "a$b", "the end", "end$x", "a word here",
	"swordfish", "(x", "|x", "ax", "a", "aaa", "color", "colour",
	"func (t *BashTool) Name()", "  func x", "]b", "ab", "bz", "x TODO y", "FIXME",
}

func TestBREToEREMatchesGNU(t *testing.T) {
	cases := []struct{ pattern, gnu string }{
		{`foo\|bar`, "foo|bar"},
		{`a+b`, "a+b"},
		{`x{2}`, "x{2}"},
		{`x\{2\}`, "xx"},
		{`Get(`, "Get(x)"},
		{`\(ab\)*c`, "ababc|c|color|colour|func (t *BashTool) Name()|  func x"},
		{`^*star`, "*star"},
		{`a^b`, "a^b"},
		{`a$b`, "a$b"},
		{`end$`, "the end"},
		{`\<word\>`, "a word here"},
		{`[(|)]x`, "Get(x)|(x||x"},
		{`a\+`, "bar|baz|a+b|aab|ababc|*star|star|a^b|a$b|a word here|ax|a|aaa|func (t *BashTool) Name()|ab"},
		{`colou\?r`, "color|colour"},
		{`func (t \*BashTool)`, "func (t *BashTool) Name()"},
		{`^\s*func`, "func (t *BashTool) Name()|  func x"},
		{`[]a]b`, "aab|ababc|]b|ab"},
		{`[^]a]z`, "bz"},
		{`.*TODO\|FIXME`, "x TODO y|FIXME"},
	}
	for _, tc := range cases {
		ere := breToERE(tc.pattern)
		re, err := regexp.Compile(ere)
		if err != nil {
			t.Errorf("%q -> %q does not compile: %v", tc.pattern, ere, err)
			continue
		}
		var got []string
		for _, line := range breInput {
			if re.MatchString(line) {
				got = append(got, line)
			}
		}
		if g := strings.Join(got, "|"); g != tc.gnu {
			t.Errorf("%q -> %q\n got: %s\nwant: %s", tc.pattern, ere, g, tc.gnu)
		}
	}
}

// A back-reference has no RE2 form. It must fail loudly, never match the
// wrong lines.
func TestBREBackReferenceIsRefused(t *testing.T) {
	if _, err := regexp.Compile(breToERE(`\(a\)\1`)); err == nil {
		t.Fatal("a back-reference compiled; it should be refused")
	}
}
