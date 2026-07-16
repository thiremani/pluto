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

Distinct drivers nest in source order, so collecting over two ranges walks
their cartesian product:

```pluto
a = 0:2
b = 0:3
[a + b]
```

produces:

```pluto
[0 1 2 1 2 3]
```

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

Comparisons in value position over a range *stream* are filters (yield-or-skip),
not booleans. (A comparison on a materialized array is instead an element-wise
*mask* that keeps each element or zeros it — see
[Pluto Conditional Value Semantics](Pluto%20Conditional%20Value%20Semantics.md).)

```pluto
i > 2
```

This yields `i` when true and yields nothing when false.

`||` is a fallback on skip:

```pluto
i > 2 || 0
```

This yields `i` when the comparison succeeds, otherwise `0`.

A value-position `&&` conditionally sequences one per-iteration value into the
next: `i > 2 && i * 10` yields `i * 10` on the iterations where the comparison
yields and skips the rest. It is local to the containing value expression; it
is not a statement gate and does not reject sibling RHS expressions. At an
assignment root a skipped iteration keeps the destination, so the last
**yielded** value wins (`x = i > 2 && i * 2` over `0:5` ends as `8`).
`i > 2 && v || w` resolves per iteration as an if-else.

## One-Dimensional Array Literals

A headerless bracket literal with one scalar row materializes a rank-1 array at
the point where it appears. Rectangular literals with multiple scalar rows and
literals containing array-valued cells infer higher-rank arrays; they do not
use the collector behavior in this section. Heterogeneous rectangular literals
may infer tables instead.

The collector materializes over:

- statement gate ranges that admit the current RHS, and
- ranges mentioned inside the literal itself.

Sibling ranges from the surrounding expression do not expand the collector.
The collector also does not leak its own ranges upward into the parent
expression.

Once the literal has materialized, the result is just an ordinary array value.
Binding always produces an array value. Later statements treat it as an
ordinary array, the same as any other named binding.

### Collectors And Binding

Binding a collector to a variable freezes the array produced at that binding
site. Later statements treat it as an ordinary array, the same as any other
named binding.

For example:

```pluto
i = 0:5
res = i + [0]
```

produces:

```pluto
[4]
```

Here `[0]` has no internal ranges and no statement gate, so it materializes as
the singleton `[0]`. The sibling `i` range belongs to the surrounding infix
expression and finalizes to `4`.

To collect one `0` for each `i`, make `i` the statement gate:

```pluto
i = 0:5
y = i [0]
res = i + y
```

This produces:

```pluto
[4 4 4 4 4]
```

because `y` is collected as `[0 0 0 0 0]` under the admitted `i` domain.

By contrast:

```pluto
i = 0:5
y = [0]
res = i + y
```

also produces `[4]`.

Example:

```pluto
i = 0:5
res = i + 1 + [i + 1]
```

`[i + 1]` first materializes `[1 2 3 4 5]` because `i` is mentioned inside
the literal. The outer expression then continues with that frozen array value,
giving `[6 7 8 9 10]` as the final value.

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
- failed `&&` cells (`[i > 2 && i * 10]` → `[0 0 0 30 40]`)
- out-of-bounds array access inside a cell

Examples:

```pluto
i = 0:10
[i > 2 < 8]
```

produces:

```pluto
[0 0 0 3 4 5 6 7 0 0]
```

and

```pluto
[i > 2 < 8 || 2]
```

produces:

```pluto
[2 2 2 3 4 5 6 7 2 2]
```

`||` is resolved before the collector sees the final cell result, so explicit
fallback values win over zero-fill.

## Gated Collection

Statement conditions outside `[]` gate the active iteration domain.
They do not preserve shape. The gate is shared by the whole statement: for a
rejected domain point, none of its RHS expressions, collector appends, carried
updates, or output commits execute. RHS-local ranges are nested inside each
admitted point.

Example:

```pluto
i = 0:10
arr = i > 2 && i < 8 [i]
```

produces:

```pluto
[3 4 5 6 7]
```

The conditions select which outer iterations execute the collector at all.

This is distinct from value-position `&&`, which controls only the value that
contains it and leaves sibling RHS expressions in the same statement alone.

The same admitted domain applies to nested collectors in a non-collector RHS:

```pluto
i = 0:5
arr = i < 3 1 + [0]
```

produces:

```pluto
[1 1 1]
```

By contrast:

```pluto
arr = [i > 2 < 8]
```

keeps the full array shape and zero-fills failed positions.

## Deferred Nested Range Construction

After PIR owns range and collector scopes, value-position `&&` may also bind a
bare range for a local nested construction:

```pluto
i = 0:3
j = 0:3
result = [i && [matrix[i][j]]]
```

The outer collector would iterate `i`; the right side of the value-position
`&&` would run once per `i`; the inner collector would own `j`; and the outer
collector would stack the resulting rows. This is not statement gating. A
statement gate would instead sit before the statement's RHS and would admit or
reject the shared iteration point for every RHS expression.

The value-position `&&` establishes the local range domain but never collects
its right side. An explicit collector must surround the scalar yields that
form one array value:

```pluto
[j && -1]       # one row of length len(j)
j && [-1]       # one singleton-array yield per j
[j && [-1]]     # stacks those arrays into a len(j) x 1 value
```

This same placement rule applies to fallbacks. A row fallback is
`[j && -1]`, not `j && [-1]`; the former matches the shape of a row collected
over `j`, while the latter still yields multiple array values.

This range-left extension is deliberately not part of the current semantics.
It should be implemented only after PIR can state which collector owns each
range and validate that ownership before LLVM lowering.

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
x, y = i < 8 && j > 2 i + 1, (i + j) < 10
```

The outer gate is `i < 8 && j > 2`, so both outputs run only on admitted
iterations.
Inside that shared gate:

- `x` uses only `i + 1`, so it ends with `8`
- `y` applies its own value-position comparison and ends with `9`

If a statement condition and an RHS expression mention the same driver name,
the statement condition opens that outer loop first.
Inside the RHS, the same name refers to the current scalar iterator value, not
to a fresh nested loop.

For non-collector tuple outputs, one admitted statement iteration is still one
shared scalar update step, but each RHS has its own local yield outcome. If one
RHS hits an out-of-bounds failure, only that RHS keeps its previous value;
yielding siblings still update. Only rejection by the shared statement gate
suppresses every sibling. Top-level `[]` collectors use their own local
zero-fill rules for cells.

## Nested Collectors

Nested collectors materialize before the surrounding expression continues.

Example:

```pluto
i = 0:6
res = i > 2 i + [i]
```

The statement condition admits `i = 3 4 5`.
`[i]` first materializes `[3 4 5]` over that admitted stream.
The outer expression then continues with the frozen array value, so the final
result is `[8 9 10]`.

Sibling expression ranges still do not cross into nested collectors:

```pluto
i = 0:5
res = i + [([0] + 1)[0]]
```

`[0]` is a singleton because it has no internal range and no statement gate.
`[([0] + 1)[0]]` is also a singleton, and the outer `i` finalizes to `4`, so
the result is `[5]`.

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

## Self-Reference: Fold

When the destination also appears on the right-hand side of a ranged
assignment, each iteration reads the value the previous iteration wrote — the
statement folds over the iteration domain instead of keeping only the last
independent value.

```pluto
i = 1:5
res = 0
res = res + i
```

steps through `1, 3, 6, 10`, so `res` ends as `10`. The same rule accumulates
arrays through concatenation:

```pluto
m = 0:3
acc = []
acc = acc ⊕ [m]
```

grows `acc` one element per iteration, ending as `[0 1 2]`. Indexed reads fold
the same way: `x = x + arr[k]` over `k = 0:5` sums the array into `x`.

## Collection Commutes With Element-Wise Operations

Applying an element-wise operation to a collected array gives the same result
as collecting the per-iteration values:

```pluto
n = 0:5
√[n]    # collect n, then element-wise √ over the array
[√n]    # √ per iteration, then collect
```

Both produce `[0 1 1.41421 1.73205 2]`. Streams and materialized arrays agree
wherever both readings exist; the difference is only *when* the array comes
into being.

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
