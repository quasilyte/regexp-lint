package main

import (
	"errors"
	"strings"
	"syscall/js"

	gosyntax "regexp/syntax"

	"github.com/quasilyte/regex/syntax"
)

var vet = &regexpVet{
	parser: syntax.NewParser(&syntax.ParserOptions{
		NoLiterals: false,
	}),
}

var simplifier = &regexpSimplifier{
	parser: syntax.NewParser(&syntax.ParserOptions{
		NoLiterals: true,
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

func jsError(err error) interface{} {
	return map[string]interface{}{
		"Err": err.Error(),
	}
}

func regexpLint(this js.Value, args []js.Value) interface{} {
	pattern := args[0].String()
	lang := args[1].Get("lang").String()
	errorsEnables := args[1].Get("errors").Bool()
	warningsEnabled := args[1].Get("warnings").Bool()
	suggestionsEnables := args[1].Get("suggestions").Bool()

	if lang != "go" {
		return jsError(errors.New("unsupported language " + lang))
	}

	var out []interface{}
	if errorsEnables {
		_, err := gosyntax.Parse(pattern, gosyntax.Perl)
		if err != nil {
			errMessage := strings.TrimPrefix(err.Error(), "error parsing regexp: ")
			out = append(out, "error: "+errMessage)
		}
	}
	if warningsEnabled {
		warnings, err := vet.CheckPattern(pattern)
		if err != nil {
			return jsError(err)
		}
		out = append(out, stringSliceToInterfaceSlice("warning: ", warnings)...)
	}
	if suggestionsEnables {
		suggestions, err := simplifier.CheckPattern(pattern)
		if err != nil {
			return jsError(err)
		}
		out = append(out, stringSliceToInterfaceSlice("suggestion: ", suggestions)...)
	}

	return map[string]interface{}{
		"Messages": out,
	}
}

func main() {
	js.Global().Set("regexpLint", js.FuncOf(regexpLint))

	c := make(chan struct{})
	<-c
}
