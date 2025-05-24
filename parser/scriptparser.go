package parser

import (
	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/lexer"
)

type ScriptParser struct {
	p *StmtParser
}

func NewScriptParser(l *lexer.Lexer) *ScriptParser {
	return &ScriptParser{
		p: New(l),
	}
}

func (sp *ScriptParser) Errors() []string {
	return sp.p.Errors()
}

func (sp *ScriptParser) Parse() *ast.Program {
	return sp.p.ParseProgram()
}
