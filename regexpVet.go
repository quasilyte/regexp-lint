package main

import (
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/quasilyte/regex/syntax"
)

type regexpVet struct {
	parser *syntax.Parser

	warnings []string

	flagStates  []regexpFlagState
	goodAnchors []syntax.Position
}

type regexpFlagState [utf8.RuneSelf]bool

func (c *regexpVet) CheckPattern(pat string) ([]string, error) {
	re, err := c.parser.Parse(pat)
	if err != nil {
		return nil, err
	}

	c.flagStates = c.flagStates[:0]
	c.goodAnchors = c.goodAnchors[:0]
	c.warnings = c.warnings[:0]

	// In Go all flags (modifiers) are set to false by default,
	// so we start from the empty flag set.
	c.flagStates = append(c.flagStates, regexpFlagState{})

	c.markGoodCarets(re.Expr)
	c.walk(re.Expr)

	return c.warnings, nil
}

func (c *regexpVet) markGoodCarets(e syntax.Expr) {
	canSkip := func(e syntax.Expr) bool {
		switch e.Op {
		case syntax.OpFlagOnlyGroup:
			return true
		case syntax.OpGroup:
			x := e.Args[0]
			return x.Op == syntax.OpConcat && len(x.Args) == 0
		}
		return false
	}

	if e.Op == syntax.OpConcat && len(e.Args) > 1 {
		i := 0
		for i < len(e.Args) && canSkip(e.Args[i]) {
			i++
		}
		if i < len(e.Args) {
			c.markGoodCarets(e.Args[i])
		}
		return
	}
	if e.Op == syntax.OpCaret {
		c.addGoodAnchor(e.Pos)
	}
	for _, a := range e.Args {
		c.markGoodCarets(a)
	}
}

func (c *regexpVet) walk(e syntax.Expr) {
	switch e.Op {
	case syntax.OpAlt:
		c.checkAltAnchor(e)
		c.checkAltDups(e)
		for _, a := range e.Args {
			c.walk(a)
		}

	case syntax.OpCharClass, syntax.OpNegCharClass:
		if c.checkCharClassRanges(e) {
			c.checkCharClassDups(e)
		}

	case syntax.OpStar, syntax.OpPlus:
		c.checkNestedQuantifier(e)
		c.walk(e.Args[0])

	case syntax.OpFlagOnlyGroup:
		c.updateFlagState(c.currentFlagState(), e, e.Args[0].Value)
	case syntax.OpGroupWithFlags:
		// Creates a new context using the current context copy.
		// New flags are evaluated inside a new context.
		// After nested expressions are processed, previous context is restored.
		nflags := len(c.flagStates)
		c.flagStates = append(c.flagStates, *c.currentFlagState())
		c.updateFlagState(c.currentFlagState(), e, e.Args[1].Value)
		c.walk(e.Args[0])
		c.flagStates = c.flagStates[:nflags]
	case syntax.OpGroup, syntax.OpCapture, syntax.OpNamedCapture:
		// Like with OpGroupWithFlags, but doesn't evaluate any new flags.
		nflags := len(c.flagStates)
		c.flagStates = append(c.flagStates, *c.currentFlagState())
		c.walk(e.Args[0])
		c.flagStates = c.flagStates[:nflags]

	case syntax.OpCaret:
		if !c.isGoodAnchor(e) {
			c.warn("dangling or redundant ^, maybe \\^ is intended?")
		}

	case syntax.OpConcat:
		c.checkConcat(e)
		for _, a := range e.Args {
			c.walk(a)
		}

	default:
		for _, a := range e.Args {
			c.walk(a)
		}
	}
}

func (c *regexpVet) currentFlagState() *regexpFlagState {
	return &c.flagStates[len(c.flagStates)-1]
}

func (c *regexpVet) updateFlagState(state *regexpFlagState, e syntax.Expr, flagString string) {
	clearing := false
	for i := 0; i < len(flagString); i++ {
		ch := flagString[i]
		if ch == '-' {
			clearing = true
			continue
		}
		if int(ch) >= len(state) {
			continue // Should never happen in practice, but we don't want a panic
		}

		if clearing {
			if !state[ch] {
				c.warn("clearing unset flag %c in %s", ch, e.Value)
			}
		} else {
			if state[ch] {
				c.warn("redundant flag %c in %s", ch, e.Value)
			}
		}
		state[ch] = !clearing
	}
}

var domainPrefixes = []string{
	"com/",
	"net/",
	"org/",
	"edu/",
	"gov/",
	"ru/",
	"de/",
	"us/",
}

func (c *regexpVet) checkConcat(concat syntax.Expr) {
	if len(concat.Args) < 2 {
		return
	}
	for i := 1; i < len(concat.Args); i++ {
		curr := concat.Args[i]
		if curr.Op != syntax.OpLiteral {
			continue
		}
		confidence := 0
		length := 0
		switch curr.Value {
		case "com", "net", "org", "edu", "gov":
			confidence = 2
			length = 3
		case "ru", "de", "us":
			confidence = 1
			length = 2
		default:
			found := false
			for _, dp := range domainPrefixes {
				if strings.HasPrefix(curr.Value, dp) {
					found = true
					confidence = 2
					length = len(dp)
					break
				}
			}
			if !found {
				continue
			}
		}
		prev := concat.Args[i-1]
		if prev.Op != syntax.OpDot {
			continue
		}
		if i-2 >= 0 {
			prevprev := concat.Args[i-2]
			if c.isCharOrLit(prevprev) {
				confidence++
			}
		}
		if confidence > 1 {
			c.warn("'.%s' should be '\\.%s'",
				curr.Value[:length], curr.Value[:length])
		}
	}
}

func (c *regexpVet) checkNestedQuantifier(e syntax.Expr) {
	x := e.Args[0]
	switch x.Op {
	case syntax.OpGroup, syntax.OpCapture, syntax.OpGroupWithFlags:
		if len(e.Args) == 1 {
			x = x.Args[0]
		}
	}

	switch x.Op {
	case syntax.OpPlus, syntax.OpStar:
		c.warn("repeated greedy quantifier in %s", e.Value)
	}
}

func (c *regexpVet) checkAltDups(alt syntax.Expr) {
	// Seek duplicated alternation expressions.

	set := make(map[string]struct{}, len(alt.Args))
	for _, a := range alt.Args {
		if _, ok := set[a.Value]; ok {
			c.warn("`%s` is duplicated in %s", a.Value, alt.Value)
		}
		set[a.Value] = struct{}{}
	}
}

func (c *regexpVet) isCharOrLit(e syntax.Expr) bool {
	return e.Op == syntax.OpChar || e.Op == syntax.OpLiteral
}

func (c *regexpVet) checkAltAnchor(alt syntax.Expr) {
	// Seek suspicious anchors.

	// Case 1: an alternation of literals where 1st expr begins with ^ anchor.
	first := alt.Args[0]
	if first.Op == syntax.OpConcat && len(first.Args) == 2 && first.Args[0].Op == syntax.OpCaret && c.isCharOrLit(first.Args[1]) {
		matched := true
		for _, a := range alt.Args[1:] {
			if !c.isCharOrLit(a) {
				matched = false
				break
			}
		}
		if matched {
			c.warn("^ applied only to `%s` in %s", first.Value[len(`^`):], alt.Value)
		}
	}

	// Case 2: an alternation of literals where last expr ends with $ anchor.
	last := alt.Args[len(alt.Args)-1]
	if last.Op == syntax.OpConcat && len(last.Args) == 2 && last.Args[1].Op == syntax.OpDollar && c.isCharOrLit(last.Args[0]) {
		matched := true
		for _, a := range alt.Args[:len(alt.Args)-1] {
			if !c.isCharOrLit(a) {
				matched = false
				break
			}
		}
		if matched {
			c.warn("$ applied only to `%s` in %s", last.Value[:len(last.Value)-len(`$`)], alt.Value)
		}
	}
}

func (c *regexpVet) checkCharClassRanges(cc syntax.Expr) bool {
	// Seek for suspicious ranges like `!-_`.
	//
	// We permit numerical ranges (0-9, hex and octal literals)
	// and simple ascii letter ranges.

	for _, e := range cc.Args {
		if e.Op != syntax.OpCharRange {
			continue
		}
		switch e.Args[0].Op {
		case syntax.OpEscapeOctal, syntax.OpEscapeHex:
			continue
		}
		ch := c.charClassBoundRune(e.Args[0])
		if ch == 0 {
			return false
		}
		good := unicode.IsLetter(ch) || (ch >= '0' && ch <= '9')
		if !good {
			c.warnSloppyCharRange(e.Value, cc.Value)
		}
	}

	return true
}

func (c *regexpVet) checkCharClassDups(cc syntax.Expr) {
	// Seek for excessive elements inside a character class.
	// Report them as intersections.

	if len(cc.Args) == 1 {
		return // Can't had duplicates.
	}

	type charRange struct {
		low    rune
		high   rune
		source string
	}
	ranges := make([]charRange, 0, 8)
	addRange := func(source string, low, high rune) {
		ranges = append(ranges, charRange{source: source, low: low, high: high})
	}
	addRange1 := func(source string, ch rune) {
		addRange(source, ch, ch)
	}

	// 1. Collect ranges, O(n).
	for _, e := range cc.Args {
		switch e.Op {
		case syntax.OpEscapeOctal:
			addRange1(e.Value, c.octalToRune(e))
		case syntax.OpEscapeHex:
			addRange1(e.Value, c.hexToRune(e))
		case syntax.OpChar:
			addRange1(e.Value, c.stringToRune(e.Value))
		case syntax.OpCharRange:
			addRange(e.Value, c.charClassBoundRune(e.Args[0]), c.charClassBoundRune(e.Args[1]))
		case syntax.OpEscapeMeta:
			addRange1(e.Value, rune(e.Value[1]))
		case syntax.OpEscapeChar:
			ch := c.stringToRune(e.Value[len(`\`):])
			if unicode.IsPunct(ch) {
				addRange1(e.Value, ch)
				break
			}
			switch e.Value {
			case `\|`, `\<`, `\>`, `\+`, `\=`: // How to cover all symbols?
				addRange1(e.Value, c.stringToRune(e.Value[len(`\`):]))
			case `\t`:
				addRange1(e.Value, '\t')
			case `\n`:
				addRange1(e.Value, '\n')
			case `\r`:
				addRange1(e.Value, '\r')
			case `\v`:
				addRange1(e.Value, '\v')
			case `\d`:
				addRange(e.Value, '0', '9')
			case `\D`:
				addRange(e.Value, 0, '0'-1)
				addRange(e.Value, '9'+1, utf8.MaxRune)
			case `\s`:
				addRange(e.Value, '\t', '\n') // 9-10
				addRange(e.Value, '\f', '\r') // 12-13
				addRange1(e.Value, ' ')       // 32
			case `\S`:
				addRange(e.Value, 0, '\t'-1)
				addRange(e.Value, '\n'+1, '\f'-1)
				addRange(e.Value, '\r'+1, ' '-1)
				addRange(e.Value, ' '+1, utf8.MaxRune)
			case `\w`:
				addRange(e.Value, '0', '9') // 48-57
				addRange(e.Value, 'A', 'Z') // 65-90
				addRange1(e.Value, '_')     // 95
				addRange(e.Value, 'a', 'z') // 97-122
			case `\W`:
				addRange(e.Value, 0, '0'-1)
				addRange(e.Value, '9'+1, 'A'-1)
				addRange(e.Value, 'Z'+1, '_'-1)
				addRange(e.Value, '_'+1, 'a'-1)
				addRange(e.Value, 'z'+1, utf8.MaxRune)
			default:
				// Give up: unknown escape sequence.
				return
			}
		default:
			// Give up: unexpected operation inside char class.
			return
		}
	}

	// 2. Sort ranges, O(nlogn).
	sort.Slice(ranges, func(i, j int) bool {
		return ranges[i].low < ranges[j].low
	})

	// 3. Search for duplicates, O(n).
	for i := 0; i < len(ranges)-1; i++ {
		x := ranges[i+0]
		y := ranges[i+1]
		if x.high >= y.low {
			c.warnCharClassDup(x.source, y.source, cc.Value)
			break
		}
	}
}

func (c *regexpVet) charClassBoundRune(e syntax.Expr) rune {
	switch e.Op {
	case syntax.OpChar:
		return c.stringToRune(e.Value)
	case syntax.OpEscapeHex:
		return c.hexToRune(e)
	case syntax.OpEscapeOctal:
		return c.octalToRune(e)
	default:
		return 0
	}
}

func (c *regexpVet) octalToRune(e syntax.Expr) rune {
	v, _ := strconv.ParseInt(e.Value[len(`\`):], 8, 32)
	return rune(v)
}

func (c *regexpVet) hexToRune(e syntax.Expr) rune {
	var s string
	switch e.Form {
	case syntax.FormEscapeHexFull:
		s = e.Value[len(`\x{`) : len(e.Value)-len(`}`)]
	default:
		s = e.Value[len(`\x`):]
	}
	v, _ := strconv.ParseInt(s, 16, 32)
	return rune(v)
}

func (c *regexpVet) stringToRune(s string) rune {
	ch, _ := utf8.DecodeRuneInString(s)
	return ch
}

func (c *regexpVet) addGoodAnchor(pos syntax.Position) {
	c.goodAnchors = append(c.goodAnchors, pos)
}

func (c *regexpVet) isGoodAnchor(e syntax.Expr) bool {
	for _, pos := range c.goodAnchors {
		if e.Pos == pos {
			return true
		}
	}
	return false
}

func (c *regexpVet) warn(format string, args ...interface{}) {
	c.warnings = append(c.warnings, sprintf(format, args...))
}

func (c *regexpVet) warnSloppyCharRange(rng, charClass string) {
	c.warn("suspicious char range '%s' in %s", rng, charClass)
}

func (c *regexpVet) warnCharClassDup(x, y, charClass string) {
	if x == y {
		c.warn("'%s' is duplicated in %s", x, charClass)
	} else {
		c.warn("'%s' intersects with '%s' in %s", x, y, charClass)
	}
}
