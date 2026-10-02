//go:build terva_gosh

package gosh

import "strings"

// breToERE rewrites a POSIX basic regular expression, with the GNU
// extensions, into the extended syntax that Go's RE2 reads.
//
// 🚨 go-bash compiles grep's basic mode straight into RE2. RE2 reads `\|`
// as a literal bar and `(` as a group, so `grep 'foo\|bar'` matched nothing
// and exited 1 with no error. In the lampi lake, about 15% of agent scripts
// use that form. A model that gets "no match" acts on it, so this is a
// wrong answer, not a missing feature.
//
// The rules, from GNU grep's BRE:
//   - `\( \) \{ \} \| \+ \?` are the operators, and the bare characters
//     `( ) { } | + ?` are literals.
//   - `*` is a literal at the start of the expression, after `\(`, after
//     `\|`, and after an anchoring `^`.
//   - `^` anchors only at the start, after `\(`, or after `\|`. `$` anchors
//     only at the end, before `\)`, or before `\|`. Elsewhere both are
//     literals.
//   - `\<` and `\>` are word boundaries. RE2 has only `\b`, which matches
//     both.
//   - A bracket expression passes through unchanged.
//
// A back-reference such as `\1` passes through, and RE2 then refuses the
// pattern with an error. That failure is loud, which is the point.
func breToERE(p string) string {
	var b strings.Builder
	// atStart is true where `*` is literal and `^` anchors.
	atStart := true
	n := len(p)
	for i := 0; i < n; i++ {
		c := p[i]
		switch c {
		case '[':
			end := bracketEnd(p, i)
			b.WriteString(p[i:end])
			i = end - 1
			atStart = false
			continue
		case '\\':
			if i+1 >= n {
				b.WriteString(`\\`)
				continue
			}
			i++
			d := p[i]
			switch d {
			case '(':
				b.WriteByte('(')
				atStart = true
				continue
			case ')':
				b.WriteByte(')')
			case '|':
				b.WriteByte('|')
				atStart = true
				continue
			case '{', '}', '+', '?':
				b.WriteByte(d)
			case '<', '>':
				b.WriteString(`\b`)
			default:
				b.WriteByte('\\')
				b.WriteByte(d)
			}
			atStart = false
			continue
		case '(', ')', '{', '}', '|', '+', '?':
			b.WriteByte('\\')
			b.WriteByte(c)
		case '*':
			if atStart {
				b.WriteString(`\*`)
			} else {
				b.WriteByte('*')
			}
		case '^':
			if atStart {
				b.WriteByte('^')
				// `^*` in BRE: the star is a literal.
				continue
			}
			b.WriteString(`\^`)
		case '$':
			if i == n-1 || strings.HasPrefix(p[i+1:], `\)`) || strings.HasPrefix(p[i+1:], `\|`) {
				b.WriteByte('$')
			} else {
				b.WriteString(`\$`)
			}
		default:
			b.WriteByte(c)
		}
		atStart = false
	}
	return b.String()
}

// bracketEnd returns the index just past the bracket expression that opens
// at p[i]. A `]` first in the list (after an optional `^`) is a member, and
// `[:class:]`, `[=x=]` and `[.x.]` nest. An unclosed bracket runs to the end,
// and RE2 reports it.
func bracketEnd(p string, i int) int {
	j := i + 1
	if j < len(p) && p[j] == '^' {
		j++
	}
	if j < len(p) && p[j] == ']' {
		j++
	}
	for j < len(p) {
		switch {
		case p[j] == '[' && j+1 < len(p) && (p[j+1] == ':' || p[j+1] == '=' || p[j+1] == '.'):
			closer := string([]byte{p[j+1], ']'})
			if k := strings.Index(p[j+2:], closer); k >= 0 {
				j += 2 + k + 2
				continue
			}
			j++
		case p[j] == ']':
			return j + 1
		default:
			j++
		}
	}
	return len(p)
}
