package parser

import (
	"pluto/ast"
	"pluto/lexer"
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
