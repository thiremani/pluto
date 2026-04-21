package compiler

import (
	"fmt"

	"github.com/thiremani/pluto/ast"
)

// Collector preparation layers a second rewrite pass on top of the solver's
// Rewrite tree: nested [] nodes are materialized early into temporary array
// values before the surrounding ranged expression opens its outer loops.
type materializedCollector struct {
	name  string
	owner *Symbol
}

func cloneExprInfoWithRewrite(info *ExprInfo, rewrite ast.Expression) *ExprInfo {
	infoCopy := *info
	infoCopy.Rewrite = rewrite
	return &infoCopy
}

func (c *Compiler) compileTreeFor(expr ast.Expression) ast.Expression {
	info := c.ExprCache[key(c.FuncNameMangled, expr)]
	if info == nil || info.Rewrite == nil {
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
	if info == nil {
		return
	}
	c.ExprCache[key(c.FuncNameMangled, prepared)] = cloneExprInfoWithRewrite(info, prepared)
}

func (c *Compiler) newMaterializedCollectorTemp(sym *Symbol) (*ast.Identifier, materializedCollector) {
	name := fmt.Sprintf("collecttmp_%d", c.tmpCounter)
	c.tmpCounter++
	ident := &ast.Identifier{Value: name}
	scopeSym := GetCopy(sym)
	scopeSym.Borrowed = true
	scopeSym.ReadOnly = true
	Put(c.Scopes, name, scopeSym)
	return ident, materializedCollector{name: name, owner: sym}
}

func (c *Compiler) materializeCollectorLiteral(lit *ast.ArrayLiteral, activeRanges []*RangeInfo, condExprs []ast.Expression) (ast.Expression, []materializedCollector) {
	resolved, info := c.resolveArrayLiteralRewrite(lit)
	sym := c.compileArrayLiteralInDomain(resolved, info, activeRanges, condExprs)
	ident, temp := c.newMaterializedCollectorTemp(sym)
	return ident, []materializedCollector{temp}
}

func (c *Compiler) prepareCollectorExpr(expr ast.Expression, activeRanges []*RangeInfo, condExprs []ast.Expression) (ast.Expression, []materializedCollector) {
	if lit, ok := expr.(*ast.ArrayLiteral); ok {
		return c.materializeCollectorLiteral(lit, activeRanges, condExprs)
	}

	temps := []materializedCollector{}
	prepared := ast.RewriteExpr(expr, func(child ast.Expression) ast.Expression {
		rewritten, childTemps := c.prepareCollectorExpr(child, activeRanges, condExprs)
		temps = append(temps, childTemps...)
		return rewritten
	})
	if prepared != expr {
		c.registerPreparedExpr(expr, prepared)
	}
	return prepared, temps
}

func (c *Compiler) cleanupMaterializedCollectors(temps []materializedCollector) {
	if len(temps) == 0 {
		return
	}
	names := make([]string, 0, len(temps))
	for _, temp := range temps {
		c.freeSymbolValue(temp.owner, temp.name+"_cleanup")
		names = append(names, temp.name)
	}
	DeleteBulk(c.Scopes, names)
}

func withPreparedCollectorExpr[T ast.Expression](c *Compiler, expr T, activeRanges []*RangeInfo, condExprs []ast.Expression, body func(T)) {
	preparedExpr, collectorTemps := c.prepareCollectorExpr(expr, activeRanges, condExprs)
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
