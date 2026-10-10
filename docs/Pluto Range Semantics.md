# Pluto Range Semantics

## Core Model

A `Range<T>` is a descriptor value, and a range literal is the only way to
construct one (#146):

```pluto
i = 0:5
k = (0:5)
```

`i` and `k` hold equal descriptor values with the same captured bounds;
parentheses are transparent. Each binding is a distinct driver identity:
consuming `i` and `k` together forms a cartesian domain, while repeated uses
of `i` share one loop.

Every use of a range name creates a ranged computation, a bare one included:
`j = i` iterates `i` and keeps its final yield, exactly as `j = i + 0` does.
`Ranged<T>` is a useful description of that expression effect, not a storable
source type. An operation produces ordered per-iteration values; an individual
yield may be a scalar or an owned subarray.

There are two explicit closing steps for a ranged computation:

1. `[]` closes it into an array.
2. The root expression of an assignment closes any remaining outer iteration
   by taking the final yielded value in iteration order.

A range literal assigned on its own is not a closing step and does not
iterate: it constructs the descriptor.

## Migration From Descriptor Copies

Before #146, assigning a bare named Range (`copy = i`) copied the descriptor,
and printing a range name or naming it in a main interpolation marker showed
the descriptor. A range name now iterates wherever it is used, which restores
the earlier bare-range finalization:

- `copy = i` keeps `i`'s final yield. For a second driver over the same
  domain, construct another literal (`j = 0:5`), keeping the bounds in names
  if they can change in between. Assigning a range name to a binding that
  holds a Range is a type-changing reassignment.
- Printing a range name prints once per yield, two distinct names print their
  cartesian product, and an empty range prints nothing. `[i]` lists the values
  on one line, and a range literal printed on its own shows as written.
- A main marker naming a range, as in `"v=-i"`, runs once per yield.

These changes are silent where a program still compiles. Range-indexed
expressions such as `last = data[i]` are unchanged because indexing is already
a ranged computation.

## Ranges And Drivers

A range identifier consumed by an operator, array index, collector, statement
condition, or function argument contributes an iteration driver. A
range-indexed array access is itself a ranged computation.
Multiple distinct drivers form a nested iteration domain in source order: the
first distinct driver is outermost and the last is innermost. Repeated use of
the same driver name refers to the same loop, not a nested copy.
Driver identity belongs to the binding name, not to descriptor equality.
Substituting one Range name for another can therefore change a shared loop into
a cartesian domain.

Range `start`/`stop`/`step` fields are not part of the language.
The bound values are captured when the range is constructed, so later changes
to the source variables do not mutate the existing range. Functions that need
those bounds as data should currently receive the scalar values explicitly.

A Range can be named but not copied, since assigning a range name iterates
it, and it is not yet a fully first-class container element. Arrays and
tables contain scalar/string elements rather than Range
descriptors, so `[i]` consumes `i` and collects its yields. Passing a Range to a
function likewise consumes it as a driver rather than passing inert metadata.
A function cannot return a Range either: an output assigned one is rejected
at the function's definition (#146). The function returns the bounds instead,
and the caller builds the range: `lo, hi = Bounds(n)`, then `r = lo:hi`. If
the range was assigned conditionally, the rebuild keeps the condition:
`r = n > 0 lo:hi`.

Range-indexed arrays follow the same rule:

```pluto
arr = [10 20 30 40]
i = 1:4
last = arr[i]
selected = [arr[i]]
```

`last` becomes `40`, while `selected` becomes `[20 30 40]`. A range-indexed
access is not a public slice or view value: it is either consumed by its
surrounding expression, finalized at an assignment root, or materialized by
`[]`. For a rank-N source, one yield is an owned rank-(N-1) subarray, so the
final value can itself be an array. This ownership is semantic: copy-on-write
is permitted, but an escaping view into the source is not.

Assigning an empty range literal still performs a normal write. Consuming
an empty Range produces no yields: a fresh ranged-computation destination
retains its type's zero value (an empty array for a subarray result), while an
existing destination is unchanged. Outside `[]`, an out-of-bounds selection
point yields nothing, so the last valid selected value wins. Inside `[]`,
failed cells are zero-filled to preserve collection shape, as described below.

Print is a sink: a bare range literal argument formats its descriptor as
`start:stop` (or `start:stop:step`), exactly as written. Any other argument
naming a range, bare or in a computation, drives the print loop, and so does a
marker naming one, whether it formats the value or uses it as a width or
precision; see
[Pluto String and Formatting Semantics](Pluto%20String%20and%20Formatting%20Semantics.md#interpolation-markers)
for the formatting rules and examples. For ordinary print arguments:

```pluto
i = 0:2
j = 2:4
0:2, 2:4        # one line: 0:2 2:4
i, j            # cartesian: 0 2 / 0 3 / 1 2 / 1 3
i + 0, j + 0    # the same
i, Square(i)    # one shared loop: 0 0 / 1 1
```

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
Whether the compiler places a call's loop around the call or in a specialized
callee is an implementation detail.

An immediate bare range or range-indexed argument may select such a
range-bearing specialization. In particular, `F(arr[i])` may pass an internal,
call-scoped `ArrayRange` descriptor so the callee loops over the selection.
That descriptor is not a language value: it cannot be stored, returned,
printed, or otherwise escape the call, and the parameter inside `F` observes
one yielded element or owned subarray at a time.

Driver identity takes priority over loop placement. When one driver occurs in
multiple call arguments, as in `F(i, i)`, both arguments must observe the same
iteration; the current lowering runs that loop caller-side. Distinct drivers,
as in `F(i, j)`, still form their normal cartesian domain. These choices do not
change the results visible to source code.

Function outputs are staged independently before the call. A body that runs
writes every output (#123), so a destination keeps its value only when no
invocation is admitted: the driver is empty, an argument fails at every
point, or the statement's gate is false. Zero iterations always mean no
assignment, wherever the loop is placed. At representation boundaries, such as
a static string result being assigned into an owned-string destination, the
callee receives its declared zero value and the caller commits the adapted
result once the callee writes it.

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

## Array Collectors

A one-row inline headerless bracket literal materializes an array at the point
where it appears. Fixed block literals do not use the collector behavior in
this section. Their layout and rank rules are defined in
[Pluto Array Semantics](Pluto%20Array%20Semantics.md#literal-inference).

The collector materializes over:

- statement gate ranges that admit the current RHS, and
- ranges mentioned inside the literal itself.

Sibling ranges from the surrounding expression do not expand the collector.
The collector also does not leak its own ranges upward into the parent
expression.

Once the literal has materialized, the result is just an ordinary array value.
Binding freezes that value, so later statements treat it like any other named
array. With no active drivers, a collector evaluates once and produces a
singleton array.

### Collectors And Binding

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

If no domain point is admitted, the statement performs no write and an existing
collector destination keeps its old value. This differs from an ungated
collector over an empty range: `[i]` still evaluates to `[]` when `i` itself has
an empty domain.

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

The binder is what distinguishes loop levels:

```pluto
[F(i, j)]                    # one flat collector over i x j
[i && [F(i, j)]]             # outer i collector, inner j collector
[j && [F(i, j)]]             # outer j collector, inner i collector
[i && [j && [F(i, j, k)]]]   # three explicitly nested domains
[i && 1]                     # one scalar 1 per i
```

The bare range is a domain, not a truth value; the right side also runs for an
iterator value of zero.

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

A bare range binder never fails, so `i && [matrix[i][j]] || [-1]` has no
reachable fallback: `i` yields every domain point and the inner collector
always resolves to an array. The eventual validator should diagnose that dead
fallback. When alternatives are reachable, every row must have the same shape;
Pluto does not pad, truncate, or flatten mismatched rows.

When a condition such as `i > 0 && [F(i, j)]` fails in value position, the
enclosing collector retains that `i` position and inserts a zero-filled child
with the expected `j` shape. `|| [j && -1]` replaces that default with an
explicit row. PIR must be able to derive the skipped child's shape; otherwise
the compiler requires an explicit shape-bearing fallback instead of guessing.
`|| [j && 0]` states the default zero row directly; `|| [j && -1]` selects a
different fill value. A statement gate remains the only form that rejects the
complete iteration point for every RHS expression.

This range-left extension is deliberately not part of the current semantics.
It should be implemented only after PIR can state which collector owns each
range and validate that ownership before LLVM lowering.

## Statement Conditions And Tuples

For tuple assignments, the statement-wide gate described above applies to
every output. Each RHS adds only the local drivers mentioned inside that
expression; sibling RHS expressions do not share those drivers.

Examples:

```pluto
i = 0:3
j = 0:2
x, y = i < 2 [1], j + 0
```

The statement condition `i < 2` is shared.
`x` collects once for each admitted `i`, producing `[1 1]`.
`y`'s operation uses its own local `j` driver inside that shared gate and ends
with the final result `1`. A bare `j` in this position iterates the same way
and also ends with `1`.

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

Consequently, `filtered = i > 2 i` keeps the final admitted scalar, and
`cross = outer > 2 i` iterates `i` inside each admitted `outer` point and
keeps the final yield. If the statement domain is empty, it performs no
write: an existing destination stays unchanged and a fresh one keeps its zero
value.

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

This is the same semantic materialization boundary described for all
collectors: sibling expression ranges do not cross it. The compiler may later
hoist or fuse loops as an optimization, but that does not change the language
meaning.

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
acc = []0
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
