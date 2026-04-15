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
	if info == nil {
		return nil
	}
	infoCopy := *info
	infoCopy.Rewrite = rewrite
	return &infoCopy
}

func (c *Compiler) compileTreeFor(expr ast.Expression) ast.Expression {
	info := c.ExprCache[key(c.FuncNameMangled, expr)]
	if info == nil || info.Rewrite == nil {
		return expr
	}
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

func mergeMaterializedCollectors(groups ...[]materializedCollector) []materializedCollector {
	total := 0
	for _, group := range groups {
		total += len(group)
	}
	if total == 0 {
		return nil
	}
	out := make([]materializedCollector, 0, total)
	for _, group := range groups {
		out = append(out, group...)
	}
	return out
}

func (c *Compiler) prepareCollectorExpr(expr ast.Expression, activeRanges []*RangeInfo, condExprs []ast.Expression) (ast.Expression, []materializedCollector) {
	switch e := expr.(type) {
	case *ast.ArrayLiteral:
		return c.materializeCollectorLiteral(e, activeRanges, condExprs)
	case *ast.InfixExpression:
		left, leftTemps := c.prepareCollectorExpr(e.Left, activeRanges, condExprs)
		right, rightTemps := c.prepareCollectorExpr(e.Right, activeRanges, condExprs)
		if left == e.Left && right == e.Right {
			return expr, mergeMaterializedCollectors(leftTemps, rightTemps)
		}
		prepared := &ast.InfixExpression{
			Token:    e.Token,
			Left:     left,
			Operator: e.Operator,
			Right:    right,
		}
		c.registerPreparedExpr(expr, prepared)
		return prepared, mergeMaterializedCollectors(leftTemps, rightTemps)
	case *ast.PrefixExpression:
		right, temps := c.prepareCollectorExpr(e.Right, activeRanges, condExprs)
		if right == e.Right {
			return expr, temps
		}
		prepared := &ast.PrefixExpression{
			Token:    e.Token,
			Operator: e.Operator,
			Right:    right,
		}
		c.registerPreparedExpr(expr, prepared)
		return prepared, temps
	case *ast.CallExpression:
		argsChanged := false
		preparedArgs := make([]ast.Expression, len(e.Arguments))
		allTemps := []materializedCollector{}
		for i, arg := range e.Arguments {
			preparedArg, temps := c.prepareCollectorExpr(arg, activeRanges, condExprs)
			preparedArgs[i] = preparedArg
			if preparedArg != arg {
				argsChanged = true
			}
			allTemps = append(allTemps, temps...)
		}
		if !argsChanged {
			return expr, allTemps
		}
		prepared := &ast.CallExpression{
			Token:     e.Token,
			Function:  e.Function,
			Arguments: preparedArgs,
		}
		c.registerPreparedExpr(expr, prepared)
		return prepared, allTemps
	case *ast.ArrayRangeExpression:
		arrayExpr, arrayTemps := c.prepareCollectorExpr(e.Array, activeRanges, condExprs)
		rangeExpr, rangeTemps := c.prepareCollectorExpr(e.Range, activeRanges, condExprs)
		if arrayExpr == e.Array && rangeExpr == e.Range {
			return expr, mergeMaterializedCollectors(arrayTemps, rangeTemps)
		}
		prepared := &ast.ArrayRangeExpression{
			Token: e.Token,
			Array: arrayExpr,
			Range: rangeExpr,
		}
		c.registerPreparedExpr(expr, prepared)
		return prepared, mergeMaterializedCollectors(arrayTemps, rangeTemps)
	case *ast.DotExpression:
		left, temps := c.prepareCollectorExpr(e.Left, activeRanges, condExprs)
		if left == e.Left {
			return expr, temps
		}
		prepared := &ast.DotExpression{
			Token: e.Token,
			Left:  left,
			Field: e.Field,
		}
		c.registerPreparedExpr(expr, prepared)
		return prepared, temps
	case *ast.RangeLiteral:
		start, startTemps := c.prepareCollectorExpr(e.Start, activeRanges, condExprs)
		stop, stopTemps := c.prepareCollectorExpr(e.Stop, activeRanges, condExprs)
		step := e.Step
		stepTemps := []materializedCollector(nil)
		if e.Step != nil {
			step, stepTemps = c.prepareCollectorExpr(e.Step, activeRanges, condExprs)
		}
		if start == e.Start && stop == e.Stop && step == e.Step {
			return expr, mergeMaterializedCollectors(startTemps, stopTemps, stepTemps)
		}
		prepared := &ast.RangeLiteral{
			Token: e.Token,
			Start: start,
			Stop:  stop,
			Step:  step,
		}
		c.registerPreparedExpr(expr, prepared)
		return prepared, mergeMaterializedCollectors(startTemps, stopTemps, stepTemps)
	default:
		return expr, nil
	}
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
