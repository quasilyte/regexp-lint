package main

import (
	"syscall/js"

	"github.com/quasilyte/regex/syntax"
)

type lintResult struct {
	Messages []string
	Err      string
}

var vet = &regexpVet{
	parser: syntax.NewParser(&syntax.ParserOptions{
		NoLiterals: false,
	}),
}

func stringSliceToInterfaceSlice(prefix string, s []string) []interface{} {
	if len(s) == 0 {
		return []interface{}{}
	}
	out := make([]interface{}, len(s))
	for i := range s {
		out[i] = prefix + s[i]
	}
	return out
}

func regexpLint(this js.Value, args []js.Value) interface{} {
	// lang := args[0].String()
	pattern := args[1].String()
	warnings, err := vet.CheckPattern(pattern)
	if err != nil {
		return map[string]interface{}{
			"Err": err.Error(),
		}
	}
	return map[string]interface{}{
		"Messages": stringSliceToInterfaceSlice("warning: ", warnings),
	}
}

func main() {
	js.Global().Set("regexpLint", js.FuncOf(regexpLint))

	c := make(chan struct{})
	<-c
}
