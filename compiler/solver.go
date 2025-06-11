package compiler

import (
	"fmt"
)

func (sc *ScriptCompiler) SolveTypes() error {
	c := sc.Compiler
	// get types, run checks etc.
	for i := 0; i < 100; i++ {
		for _, stmt := range sc.Program.Statements {
			c.doStatement(stmt, false)
		}
	}

	err := fmt.Errorf("max iterations exceeded. Could not infer function output types for script")
	return err
}
