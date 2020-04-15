package main

import "strings"

func sprintf(format string, args ...interface{}) string {
	if len(args) == 0 {
		return format
	}

	var b strings.Builder
	b.Grow(len(format))
	arg := 0
	i := 0
	for i < len(format) {
		if i+1 < len(format) && format[i] == '%' {
			switch format[i+1] {
			case 'c':
				b.WriteRune(formatC(args[arg]))
			case 's':
				b.WriteString(formatS(args[arg]))
			default:
				panic("sprintf: only %c and %s are supported")
			}
			arg++
			i += 2
		} else {
			b.WriteByte(format[i])
			i++
		}
	}

	return b.String()
}

func formatC(arg interface{}) rune {
	switch arg := arg.(type) {
	case rune:
		return arg
	case byte:
		return rune(arg)
	default:
		panic("sprintf: invalid argument in %c")
	}
}

func formatS(arg interface{}) string {
	switch arg := arg.(type) {
	case string:
		return arg
	case rune:
		return string(arg)
	case byte:
		return string(arg)
	default:
		panic("sprintf: invalid argument in %s")
	}
}
