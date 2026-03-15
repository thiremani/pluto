package compiler

import (
	"fmt"
	"github.com/thiremani/pluto/ast"
	"github.com/thiremani/pluto/token"
	"tinygo.org/x/go-llvm"
)

type CodeCompiler struct {
	Compiler *Compiler
	Code     *ast.Code
}

func NewCodeCompiler(ctx llvm.Context, modName, relPath string, code *ast.Code) *CodeCompiler {
	mangledPath := MangleDirPath(modName, relPath)
	cc := &CodeCompiler{
		Compiler: NewCompiler(ctx, mangledPath, nil),
		Code:     code,
	}
	return cc
}

// validateStructUsage checks that all headers in a usage reference fields from the definition.
func validateStructUsage(def *Struct, headers []token.Token) []*token.CompileError {
	var errs []*token.CompileError
	for _, header := range headers {
		if _, ok := def.FieldSet[header.Literal]; !ok {
			errs = append(errs, &token.CompileError{
				Token: header,
				Msg:   fmt.Sprintf("field %q not in struct type %s", header.Literal, def.Name),
			})
		}
	}
	return errs
}

// checkFieldTypes returns an error if a statement's value types conflict with the canonical definition.
// Int→float promotion is allowed to match the codegen widening in structFieldConstValue.
func checkFieldTypes(def *Struct, stmt *ast.StructStatement) *token.CompileError {
	for i, header := range stmt.Value.Headers {
		defIdx := def.FieldIndex(header.Literal)
		if defIdx < 0 {
			continue // validateStructUsage catches unknown fields
		}
		cellType, ok := structFieldTypeFromConstant(stmt.Value.Row[i])
		if !ok {
			continue // buildStructDef catches unsupported expressions
		}
		defType := def.Fields[defIdx].Type
		if structFieldTypeAssignable(cellType, defType) {
			continue
		}
		return &token.CompileError{
			Token: stmt.Value.Row[i].Tok(),
			Msg:   fmt.Sprintf("struct field %q expects %s, got %s", header.Literal, defType.String(), cellType.String()),
		}
	}
	return nil
}

// checkFieldOrder returns an error if an equal-length header list has fields in a different order.
func checkFieldOrder(def *Struct, headers []token.Token) *token.CompileError {
	for i, header := range headers {
		if header.Literal != def.Fields[i].Name {
			return &token.CompileError{
				Token: header,
				Msg:   fmt.Sprintf("struct %s fields reordered: expected %s at position %d, got %s (all fields must match definition order)", def.Name, def.Fields[i].Name, i, header.Literal),
			}
		}
	}
	return nil
}

// buildStructDef builds a Struct from a struct statement's headers and row values.
func buildStructDef(stmt *ast.StructStatement) (*Struct, []*token.CompileError) {
	def := &Struct{
		Name:     stmt.Value.Token.Literal,
		FieldSet: make(map[string]struct{}, len(stmt.Value.Headers)),
	}
	var errs []*token.CompileError
	for i, header := range stmt.Value.Headers {
		cellType, ok := structFieldTypeFromConstant(stmt.Value.Row[i])
		if !ok {
			errs = append(errs, &token.CompileError{
				Token: stmt.Value.Row[i].Tok(),
				Msg:   fmt.Sprintf("unsupported struct constant field expression %T", stmt.Value.Row[i]),
			})
			continue
		}
		def.Fields = append(def.Fields, StructField{Name: header.Literal, Type: cellType})
		def.FieldSet[header.Literal] = struct{}{}
	}
	return def, errs
}

// validateStructHeaders validates all statements against the canonical definition:
// equal-length statements must have the same fields in the same order,
// shorter statements must only reference fields from the definition.
func validateStructHeaders(defs map[string]*Struct, stmts []*ast.StructStatement) []*token.CompileError {
	var errs []*token.CompileError
	for _, stmt := range stmts {
		if len(stmt.Value.Headers) == 0 {
			continue
		}
		errs = append(errs, validateStructStmt(defs[stmt.Value.Token.Literal], stmt)...)
	}
	return errs
}

// validateStructStmt validates a single struct statement against its canonical definition.
func validateStructStmt(def *Struct, stmt *ast.StructStatement) []*token.CompileError {
	usageErrs := validateStructUsage(def, stmt.Value.Headers)
	if len(usageErrs) > 0 {
		if len(stmt.Value.Headers) == len(def.Fields) {
			return []*token.CompileError{{
				Token: stmt.Value.Token,
				Msg:   fmt.Sprintf("struct %s redefined with different fields", def.Name),
			}}
		}
		return usageErrs
	}
	if len(stmt.Value.Headers) == len(def.Fields) {
		if err := checkFieldOrder(def, stmt.Value.Headers); err != nil {
			return []*token.CompileError{err}
		}
	}
	if err := checkFieldTypes(def, stmt); err != nil {
		return []*token.CompileError{err}
	}
	return nil
}

// collectStructDefs finds the canonical definition (max-header statement) for each struct type
// and validates all other statements against it.
func collectStructDefs(stmts []*ast.StructStatement) (map[string]*Struct, []*token.CompileError) {
	defs := make(map[string]*Struct)
	var errs []*token.CompileError

	// Find the definition (max headers) for each struct type.
	for _, stmt := range stmts {
		typeName := stmt.Value.Token.Literal
		if len(stmt.Value.Headers) == 0 {
			continue
		}

		existing, exists := defs[typeName]
		if !exists || len(stmt.Value.Headers) > len(existing.Fields) {
			def, defErrs := buildStructDef(stmt)
			errs = append(errs, defErrs...)
			defs[typeName] = def
		}
	}
	if len(errs) > 0 {
		return defs, errs
	}

	errs = append(errs, validateStructHeaders(defs, stmts)...)
	return defs, errs
}

// validateStructDefs finds the single canonical definition for each struct type,
// populates StructCache, and reports errors for
// unknown fields, field order conflicts, and undefined types.
// Must be called after validateReservedNames.
func (cc *CodeCompiler) validateStructDefs() {
	c := cc.Compiler
	prior := len(c.Errors)

	defs, errs := collectStructDefs(cc.Code.Struct.Statements)
	if len(errs) > 0 {
		c.Errors = append(c.Errors, errs...)
		return
	}

	// Validate zero-header usages have a definition somewhere.
	for _, stmt := range cc.Code.Struct.Statements {
		typeName := stmt.Value.Token.Literal
		if len(stmt.Value.Headers) == 0 {
			if _, exists := defs[typeName]; !exists {
				c.Errors = append(c.Errors, &token.CompileError{
					Token: stmt.Value.Token,
					Msg:   fmt.Sprintf("struct type %s has not been defined", typeName),
				})
			}
		}
	}
	if len(c.Errors) > prior {
		return
	}

	// Populate StructCache from definitions.
	for typeName, def := range defs {
		c.StructCache[typeName] = def
	}
}

// validateReservedNames rejects constant, struct type, struct binding,
// and function names that collide with built-in type names.
func (cc *CodeCompiler) validateReservedNames() {
	for _, stmt := range cc.Code.Const.Statements {
		for _, id := range stmt.Name {
			cc.Compiler.rejectReservedName(id.Token, "constant")
		}
	}
	for _, stmt := range cc.Code.Struct.Statements {
		cc.Compiler.rejectReservedName(stmt.Value.Token, "struct type")
		cc.Compiler.rejectReservedName(stmt.Name.Token, "struct constant")
	}
	for _, stmt := range cc.Code.Func.Statements {
		cc.Compiler.rejectReservedName(stmt.Token, "function")
	}
}

// Compile compiles the constants in the AST and adds them to the compiler's symbol table.
func (cc *CodeCompiler) Compile() []*token.CompileError {
	cc.validateReservedNames()
	cc.validateStructDefs()
	if len(cc.Compiler.Errors) > 0 {
		return cc.Compiler.Errors
	}

	// Compile constants
	for _, stmt := range cc.Code.Const.Statements {
		cc.Compiler.compileConstStatement(stmt)
	}
	for _, stmt := range cc.Code.Struct.Statements {
		cc.Compiler.compileStructStatement(stmt)
	}

	cfg := NewCFG(nil, cc)
	cfg.AnalyzeFuncs()
	cc.Compiler.Errors = append(cc.Compiler.Errors, cfg.Errors...)

	return cc.Compiler.Errors
}
