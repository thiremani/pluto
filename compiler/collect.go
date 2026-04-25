package compiler

import (
	"fmt"

	"github.com/thiremani/pluto/ast"
)

func cloneExprInfoWithRewrite(info *ExprInfo, rewrite ast.Expression) *ExprInfo {
	// ExprInfo slice fields are solver-owned and treated as immutable after
	// solve; prepared/backfilled cache entries share them and only swap Rewrite.
	infoCopy := *info
	infoCopy.Rewrite = rewrite
	return &infoCopy
}

func (c *Compiler) compileTreeFor(expr ast.Expression) ast.Expression {
	info := c.ExprCache[key(c.FuncNameMangled, expr)]
	if info.Rewrite == nil {
		return expr
	}
	// Solver rewrites are keyed by the original expression node. Backfill an
	// entry for the rewritten tree on demand so later collector preparation can
	// register derived nodes against a stable ExprCache entry.
	if _, ok := c.ExprCache[key(c.FuncNameMangled, info.Rewrite)]; !ok {
		c.ExprCache[key(c.FuncNameMangled, info.Rewrite)] = cloneExprInfoWithRewrite(info, info.Rewrite)
	}
	return info.Rewrite
}

func (c *Compiler) registerPreparedExpr(orig ast.Expression, prepared ast.Expression) {
	if orig == prepared {
		return
	}
	info := c.ExprCache[key(c.FuncNameMangled, orig)]
	c.ExprCache[key(c.FuncNameMangled, prepared)] = cloneExprInfoWithRewrite(info, prepared)
}

func (c *Compiler) newMaterializedCollectorTemp(sym *Symbol) (*ast.Identifier, string) {
	name := fmt.Sprintf("collecttmp_%d", c.tmpCounter)
	c.tmpCounter++
	ident := &ast.Identifier{Value: name}
	scopeSym := GetCopy(sym)
	scopeSym.Borrowed = true
	scopeSym.ReadOnly = true
	Put(c.Scopes, name, scopeSym)
	return ident, name
}

func (c *Compiler) prepareCollectorTreeFor(expr ast.Expression, activeRanges []*RangeInfo, condExprs []ast.Expression) (ast.Expression, []string) {
	tree := c.compileTreeFor(expr)
	prepared, temps := c.prepareCollectorExpr(tree, activeRanges, condExprs)
	if prepared == tree {
		return expr, temps
	}

	// The prepared tree may have been derived from a solver rewrite whose local
	// range metadata is already scalarized. Register the final root against the
	// source expression so callers keep the source ranges and output metadata.
	c.registerPreparedExpr(expr, prepared)
	return prepared, temps
}

func (c *Compiler) materializeCollectorLiteral(lit *ast.ArrayLiteral, activeRanges []*RangeInfo, condExprs []ast.Expression) (ast.Expression, []string) {
	resolved, info := c.resolveArrayLiteralRewrite(lit)
	sym := c.compileArrayLiteralInDomain(resolved, info, activeRanges, condExprs)
	ident, temp := c.newMaterializedCollectorTemp(sym)
	return ident, []string{temp}
}

func (c *Compiler) prepareCollectorExpr(expr ast.Expression, activeRanges []*RangeInfo, condExprs []ast.Expression) (ast.Expression, []string) {
	if lit, ok := expr.(*ast.ArrayLiteral); ok {
		return c.materializeCollectorLiteral(lit, activeRanges, condExprs)
	}

	temps := []string{}
	prepared := ast.RewriteExpr(expr, func(child ast.Expression) ast.Expression {
		rewritten, childTemps := c.prepareCollectorExpr(child, activeRanges, condExprs)
		temps = append(temps, childTemps...)
		return rewritten
	})
	c.registerPreparedExpr(expr, prepared)
	return prepared, temps
}

func (c *Compiler) cleanupMaterializedCollectors(temps []string) {
	if len(temps) == 0 {
		return
	}
	for _, name := range temps {
		if sym, ok := Get(c.Scopes, name); ok {
			c.freeSymbolValue(sym, name+"_cleanup")
		}
	}
	DeleteBulk(c.Scopes, temps)
}

func withPreparedCollectorExpr[T ast.Expression](c *Compiler, expr T, activeRanges []*RangeInfo, condExprs []ast.Expression, body func(T)) {
	preparedExpr, collectorTemps := c.prepareCollectorTreeFor(expr, activeRanges, condExprs)
	defer c.cleanupMaterializedCollectors(collectorTemps)

	prepared, ok := preparedExpr.(T)
	if !ok {
		panic(fmt.Sprintf("internal: prepared collector expr has unexpected type %T", preparedExpr))
	}
	body(prepared)
}

func withCollectorPreparedLoopNest[T ast.Expression](c *Compiler, expr T, activeRanges []*RangeInfo, condExprs []ast.Expression, body func(T)) {
	withPreparedCollectorExpr(c, expr, activeRanges, condExprs, func(prepared T) {
		c.withLoopNestVersioned(activeRanges, []ast.Expression{prepared}, func() {
			body(prepared)
		})
	})
}
