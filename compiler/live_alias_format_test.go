package compiler

import (
	"testing"

	"github.com/stretchr/testify/require"
	"tinygo.org/x/go-llvm"
)

func TestFormatCountRejectsInputParameter(t *testing.T) {
	tests := []struct {
		name   string
		script string
	}{
		{name: "plain", script: "value = 10\nvalue = Count(value)\nvalue"},
		{name: "range", script: "value = Count(1:3)\nvalue"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := llvm.NewContext()
			defer ctx.Dispose()

			code := mustParseCode(t, `out = Count(current)
    "count-current%n"
    out = current`)
			cc := NewCodeCompiler(ctx, "format_input_parameter", "", code)
			require.Empty(t, cc.Compile())

			sc := NewScriptCompiler(ctx, t.Name(), mustParseScript(t, tt.script), cc)
			linkCodeModuleForTest(t, ctx, sc.Compiler.Module, cc.Compiler.Module)
			errs := sc.Compile()

			require.Len(t, errs, 1)
			require.Equal(t, `cannot write to input parameter "current"`, errs[0].Msg)
		})
	}
}
