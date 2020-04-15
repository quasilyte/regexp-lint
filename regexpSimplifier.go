package main

import (
	"strings"
	"unicode/utf8"

	"github.com/quasilyte/regex/syntax"
)

type regexpSimplifier struct {
	parser *syntax.Parser

	suggestions []string
}

func (c *regexpSimplifier) CheckPattern(pat string) ([]string, error) {
	re, err := c.parser.Parse(pat)
	if err != nil {
		return nil, err
	}

	c.suggestions = c.suggestions[:0]

	// TODO(quasilyte): suggest char ranges for things like [012345689]?
	// TODO(quasilyte): evaluate char range to suggest better replacements.
	// TODO(quasilyte): (?:ab|ac) -> a[bc]
	// TODO(quasilyte): suggest "s" and "." flag if things like [\w\W] are used.
	// TODO(quasilyte): x{n}x? -> x{n,n+1}

	c.walk(re.Expr)
	return c.suggestions, nil
}

func (c *regexpSimplifier) walk(e syntax.Expr) {
	switch e.Op {
	case syntax.OpConcat:
		c.walkConcat(e)

	case syntax.OpAlt:
		c.walkAlt(e)

	case syntax.OpCharRange:
		s := c.simplifyCharRange(e)
		if s != "" {
			c.suggestRewrite(e.Value, s)
		}

	case syntax.OpGroupWithFlags:
		c.walk(e.Args[0])
	case syntax.OpGroup:
		c.walkGroup(e)
	case syntax.OpCapture:
		c.walk(e.Args[0])
	case syntax.OpNamedCapture:
		c.walk(e.Args[0])

	case syntax.OpRepeat:
		// TODO(quasilyte): is it worth it to analyze repeat argument
		// more closely and handle `{n,n} -> {n}` cases?
		rep := e.Args[1].Value
		switch rep {
		case "{0,1}":
			c.suggestRewrite(e.Value, "%s?", e.Args[0].Value)
		case "{1,}":
			c.suggestRewrite(e.Value, "%s+", e.Args[0].Value)
		case "{0,}":
			c.suggestRewrite(e.Value, "%s*", e.Args[0].Value)
		case "{0}":
			c.suggestRewrite(e.Value, "")
		case "{1}":
			c.suggestRewrite(e.Value, e.Args[0].Value)
		default:
			c.walk(e.Args[0])
		}

	case syntax.OpNegCharClass:
		s := c.simplifyNegCharClass(e)
		if s != "" {
			c.suggestRewrite(e.Value, s)
		} else {
			for _, a := range e.Args {
				c.walk(a)
			}
		}

	case syntax.OpCharClass:
		s := c.simplifyCharClass(e)
		if s != "" {
			c.suggestRewrite(e.Value, s)
		} else {
			for _, a := range e.Args {
				c.walk(a)
			}
		}

	case syntax.OpEscapeChar:
		switch e.Value {
		case `\&`, `\#`, `\!`, `\@`, `\%`, `\<`, `\>`, `\:`, `\;`, `\/`, `\,`, `\=`, `\.`:
			c.suggestRewrite(e.Value, e.Value[len(`\`):])
		}

	case syntax.OpQuestion, syntax.OpNonGreedy:
		c.walk(e.Args[0])
	case syntax.OpStar:
		c.walk(e.Args[0])
	case syntax.OpPlus:
		c.walk(e.Args[0])

	default:
		for _, a := range e.Args {
			c.walk(a)
		}
	}
}

func (c *regexpSimplifier) walkGroup(g syntax.Expr) {
	switch g.Args[0].Op {
	case syntax.OpChar, syntax.OpEscapeChar, syntax.OpEscapeMeta, syntax.OpCharClass:
		c.suggestRewrite(g.Value, g.Args[0].Value)
	}

	c.walk(g.Args[0])
}

func (c *regexpSimplifier) simplifyNegCharClass(e syntax.Expr) string {
	switch e.Value {
	case `[^0-9]`:
		return `\D`
	case `[^\s]`:
		return `\S`
	case `[^\S]`:
		return `\s`
	case `[^\w]`:
		return `\W`
	case `[^\W]`:
		return `\w`
	case `[^\d]`:
		return `\D`
	case `[^\D]`:
		return `\d`
	case `[^[:^space:]]`:
		return `\s`
	case `[^[:space:]]`:
		return `\S`
	case `[^[:^word:]]`:
		return `\w`
	case `[^[:word:]]`:
		return `\W`
	case `[^[:^digit:]]`:
		return `\d`
	case `[^[:digit:]]`:
		return `\D`
	}

	return ""
}

func (c *regexpSimplifier) simplifyCharClass(e syntax.Expr) string {
	switch e.Value {
	case `[0-9]`:
		return `\d`
	case `[[:word:]]`:
		return `\w`
	case `[[:^word:]]`:
		return `\W`
	case `[[:digit:]]`:
		return `\d`
	case `[[:^digit:]]`:
		return `\D`
	case `[[:space:]]`:
		return `\s`
	case `[[:^space:]]`:
		return `\S`
	case `[][]`:
		return `\]\[`
	case `[]]`:
		return `\]`
	}

	if len(e.Args) == 1 {
		switch e.Args[0].Op {
		case syntax.OpChar:
			switch v := e.Args[0].Value; v {
			case "|", "*", "+", "?", ".", "[", "^", "$", "(", ")":
				// Can't take outside of the char group without escaping.
			default:
				return v
			}
		case syntax.OpEscapeChar:
			return e.Args[0].Value
		}
	}

	return ""
}

func (c *regexpSimplifier) canMerge(x, y syntax.Expr) bool {
	if x.Op != y.Op {
		return false
	}
	switch x.Op {
	case syntax.OpChar, syntax.OpCharClass, syntax.OpEscapeMeta, syntax.OpEscapeChar, syntax.OpNegCharClass, syntax.OpGroup:
		return x.Value == y.Value
	default:
		return false
	}
}

func (c *regexpSimplifier) canCombine(x, y syntax.Expr) (threshold int, ok bool) {
	if x.Op != y.Op {
		return 0, false
	}

	switch x.Op {
	case syntax.OpDot:
		return 3, true

	case syntax.OpChar:
		if x.Value != y.Value {
			return 0, false
		}
		if x.Value == " " {
			return 1, true
		}
		return 4, true

	case syntax.OpEscapeMeta, syntax.OpEscapeChar:
		if x.Value == y.Value {
			return 2, true
		}

	case syntax.OpCharClass, syntax.OpNegCharClass, syntax.OpGroup:
		if x.Value == y.Value {
			return 1, true
		}
	}

	return 0, false
}

func (c *regexpSimplifier) walkAlt(alt syntax.Expr) {
	// `x|y|z` -> `[xyz]`.
	if c.allChars(alt) {
		var b strings.Builder
		b.WriteString("[")
		for _, e := range alt.Args {
			b.WriteString(e.Value)
		}
		b.WriteString("]")
		c.suggestRewrite(alt.Value, b.String())
	}

	if c.factorPrefixSuffix(alt) {
		return
	}

	for _, a := range alt.Args {
		c.walk(a)
	}
}

func (c *regexpSimplifier) walkConcat(concat syntax.Expr) {
	i := 0
	for i < len(concat.Args) {
		x := concat.Args[i]
		c.walk(x)
		i++

		if i >= len(concat.Args) {
			break
		}

		// Try merging `xy*` into `x+` where x=y.
		if concat.Args[i].Op == syntax.OpStar {
			if c.canMerge(x, concat.Args[i].Args[0]) {
				c.suggestRewrite(x.Value+concat.Args[i].Value, "%s+", x.Value)
				i++
				continue
			}
		}

		// Try combining `xy` into `x{2}` where x=y.
		threshold, ok := c.canCombine(x, concat.Args[i])
		if !ok {
			continue
		}
		n := 1 // Can combine at least 1 pair.
		for j := i + 1; j < len(concat.Args); j++ {
			_, ok := c.canCombine(x, concat.Args[j])
			if !ok {
				break
			}
			n++
		}
		if n >= threshold {
			c.suggestRewrite(strings.Repeat(x.Value, n+1), "%s{%d}", x.Value, n+1)
			i += n
		}
	}
}

func (c *regexpSimplifier) simplifyCharRange(rng syntax.Expr) string {
	if rng.Args[0].Op != syntax.OpChar || rng.Args[1].Op != syntax.OpChar {
		return ""
	}

	lo := rng.Args[0].Value
	hi := rng.Args[1].Value
	if len(lo) == 1 && len(hi) == 1 {
		switch hi[0] - lo[0] {
		case 0:
			return lo
		case 1:
			return lo + hi
		case 2:
			return lo + string(lo[0]+1) + hi
		}
	}

	return ""
}

func (c *regexpSimplifier) factorPrefixSuffix(alt syntax.Expr) bool {
	// TODO: more forms of prefixes/suffixes?
	//
	// A more generalized algorithm could handle `fo|fo1|fo2` -> `fo[12]?`.
	// but it's an open question whether the latter form universally better.
	//
	// Right now it handles only the simplest cases:
	// `http|https` -> `https?`
	// `xfoo|foo` -> `x?foo`
	if len(alt.Args) != 2 {
		return false
	}
	x := c.concatLiteral(alt.Args[0])
	y := c.concatLiteral(alt.Args[1])
	if x == y {
		return false // Reject non-literals and identical strings early
	}

	// Let x be a shorter string.
	if len(x) > len(y) {
		x, y = y, x
	}
	// Do we have a common prefix?
	tail := strings.TrimPrefix(y, x)
	if len(tail) <= utf8.UTFMax && utf8.RuneCountInString(tail) == 1 {
		c.suggestRewrite(alt.Value, x+tail+"?")
		return true
	}
	// Do we have a common suffix?
	head := strings.TrimSuffix(y, x)
	if len(head) <= utf8.UTFMax && utf8.RuneCountInString(head) == 1 {
		c.suggestRewrite(alt.Value, head+"?"+x)
		return true
	}
	return false
}

func (c *regexpSimplifier) concatLiteral(e syntax.Expr) string {
	if e.Op == syntax.OpConcat && c.allChars(e) {
		return e.Value
	}
	return ""
}

func (c *regexpSimplifier) allChars(e syntax.Expr) bool {
	for _, a := range e.Args {
		if a.Op != syntax.OpChar {
			return false
		}
	}
	return true
}

func (c *regexpSimplifier) suggestRewrite(orig, format string, args ...interface{}) {
	suggestion := "`" + orig + "` -> `" + sprintf(format, args...) + "`"
	c.suggestions = append(c.suggestions, suggestion)
}
