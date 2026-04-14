# Pluto Range Semantics

## Core Model

Expressions that mention ranges produce ordered per-iteration values.
Those values are not arrays by default.

There are two explicit closing steps:

1. `[]` closes a value stream into an array.
2. The root expression of a scalar assignment closes any remaining outer
   iteration by taking the final yielded value in iteration order.

This keeps array materialization and scalar finalization separate.

## Ranges And Drivers

A range or array-range used in an expression contributes an iteration driver.
Multiple distinct drivers form a nested iteration domain in source order.
Repeated use of the same driver name refers to the same loop, not a nested copy.

Example:

```pluto
i = 0:5
x = i + 1
```

This iterates `i` over `0, 1, 2, 3, 4` and the root assignment keeps the final
value, so `x = 5`.

## Calls, Infix, And Prefix

Calls, infix operators, and prefix operators all follow the same rule:
they transform the current per-iteration values of their range drivers.
They do not choose a special "base function" that owns the loop.

Examples:

```pluto
i = 0:5
x = Square(i)
```

This evaluates `Square` for each yielded `i` value, then the root assignment
keeps the final result, so `x = 16`.

```pluto
i = 0:5
x = i + 1
```

This yields `1, 2, 3, 4, 5` across the `i` stream and the root assignment keeps
the final value, so `x = 5`.

```pluto
i = 0:5
x = √(i + 1)
```

The infix expression first yields `1, 2, 3, 4, 5`, the prefix `√` is applied to
each yielded value, and the root assignment keeps the final result.

## Comparisons, Skip, And Fallback

Comparisons in value position are filters, not booleans.

```pluto
i > 2
```

This yields `i` when true and yields nothing when false.

`or` is a fallback on skip:

```pluto
i > 2 or 0
```

This yields `i` when the comparison succeeds, otherwise `0`.

## Array Literals

`[]` always materializes an array at the point where it appears.

The collector owns its own iteration domain:

- It iterates every range used inside the literal.
- It does not leak those ranges upward into the parent expression.
- If an outer loop currently shadows the same name with a scalar iterator,
  the collector reopens the original range driver instead of treating the
  scalar shadow as a singleton.

Example:

```pluto
i = 0:5
res = i + 1 + [i + 1]
```

`[i + 1]` first materializes `[1 2 3 4 5]`.
The outer expression then iterates `i` and broadcasts over that array, giving
`[6 7 8 9 10]` as the final value.

Likewise:

```pluto
i = 0:5
arr = [Square(i)]
```

collects the per-iteration results of `Square(i)` into `[0 1 4 9 16]`.

## Zero-Fill Inside `[]`

Array literals preserve shape.
If a cell yields nothing, the collector inserts the zero value of the element
type at that position.

That applies to:

- failed comparison cells
- out-of-bounds array access inside a cell

Examples:

```pluto
i = 0:10
[i > 2 < 8]
```

produces:

```pluto
[0 0 0 3 4 5 6 7 0 0 0]
```

and

```pluto
[i > 2 < 8 or 2]
```

produces:

```pluto
[2 2 2 3 4 5 6 7 2 2 2]
```

`or` is resolved before the collector sees the final cell result, so explicit
fallback values win over zero-fill.

## Gated Collection

Statement conditions outside `[]` gate the active iteration domain.
They do not preserve shape.

Example:

```pluto
i = 0:10
arr = i > 2, i < 8 [i]
```

produces:

```pluto
[3 4 5 6 7]
```

The conditions select which outer iterations execute the collector at all.

By contrast:

```pluto
arr = [i > 2 < 8]
```

keeps the full array shape and zero-fills failed positions.

## Statement Conditions And Tuples

Statement conditions are shared across the whole assignment.
They determine the admitted outer iteration domain for every output in the
statement.

Sibling RHS expressions do not share their local value drivers with each
other.
Each RHS adds only the extra drivers mentioned inside that expression.

Examples:

```pluto
i = 0:3
j = 0:2
x, y = i < 2 [1], j
```

The statement condition `i < 2` is shared.
`x` collects once for each admitted `i`, producing `[1 1]`.
`y` uses its own local `j` driver inside that shared gate and ends with the
final `j` value `1`.

Likewise:

```pluto
i = 0:10
j = 0:5
x, y = i < 8, j > 2 i + 1, (i + j) < 10
```

The outer gate is `i < 8, j > 2`, so both outputs run only on admitted
iterations.
Inside that shared gate:

- `x` uses only `i + 1`, so it ends with `8`
- `y` applies its own value-position comparison and ends with `9`

If a statement condition and an RHS expression mention the same driver name,
the statement condition opens that outer loop first.
Inside the RHS, the same name refers to the current scalar iterator value, not
to a fresh nested loop.

For non-collector tuple outputs, one admitted statement iteration is still one
shared scalar update step.
If one non-collector RHS hits an out-of-bounds failure on that iteration,
sibling non-collector outputs keep their previous values for that same
iteration.
Top-level `[]` collectors still use their own local zero-fill rules for cells.

## Nested Collectors

Nested collectors materialize before the surrounding expression continues.

Example:

```pluto
i = 0:5
j = 0:3
Square([i][j])
```

`[i]` first materializes `[0 1 2 3 4]`.
Then `[j]` indexes or slices that materialized array in the surrounding
expression context.

This is a semantic materialization boundary.
The compiler may later hoist or fuse loops as an optimization, but that does
not change the language meaning.

## Scalar Contexts

Outside `[]`, ranged expressions remain per-iteration values until the root
assignment or statement consumes them.

Examples:

```pluto
i = 0:5
x = i + 1
```

`x` becomes `5`.

```pluto
arr = [i + 1]
```

`arr` becomes `[1 2 3 4 5]`.

## Singleton Arrays

If no range drivers are open inside `[]`, the literal evaluates once and
produces a singleton array.

Example:

```pluto
x = 7
[x]
```

produces `[7]`.

This is not a special array-literal mode.
It is the same collector rule applied to an expression with no active drivers.
