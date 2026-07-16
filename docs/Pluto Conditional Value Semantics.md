# Pluto Conditional Value Semantics

How conditions behave depending on where they appear: as a statement gate, or as
a value inside an expression. This is the general model under
[Pluto Range Semantics](Pluto%20Range%20Semantics.md).

## Two positions, two meanings

A condition means different things in two places:

1. **Statement gate** — left of the value in `lhs = cond value`. Gates the whole
   assignment.
2. **Value position** — inside the value expression, e.g. `x * (y > 2 && 3)`.
   Produces a value.

The rule, in one line: **conditions left of the value gate the statement and keep
the old value on failure; conditions inside the value propagate their failure to
the nearest resolver — `=` keeps the old value, a collector cell zero-fills, and
`||` supplies the fallback.**

Only the first form is a **statement gate**. It admits or rejects a shared
iteration point for every RHS expression and output in the statement. An `&&`
inside a value is a local value operator: it evaluates its right side lazily
when the left yields and propagates failure only through that containing value.
It does not gate the statement's iteration domain or its sibling RHS
expressions.

## Statement gate: keep-old

A condition before the value gates the assignment. If it fails, no assignment
happens: an existing variable keeps its value, a new variable is zero.

```pluto
old = 99
old = b > 2  x * 3    # b <= 2 -> old stays 99
                      # b >  2 -> old = x * 3

new = b > 2  x * 3    # b <= 2 -> new = 0
```

A statement condition is one expression — comma means positional lists only,
everywhere. Conjunctions are spelled with the operator: a condition's
top-level `&&` chain is the statement's condition list, each conjunct
validated separately and short-circuited left to right, so
`x = a > 2 && b > 3  7` gates on both,
and bare range drivers conjoin into iteration domains — `grid = i && j [...]`
walks the cartesian product, `i < 8 && j > 2  v` a filtered one. A multi-cell
comparison (`Pair > Pair`, including string pairs) likewise gates on the
conjunction of its cells — every cell must hold.

```pluto
g = 99
g = Pair(1, 2) > Pair(0, 1)  7   # 1 > 0 and 2 > 1 -> gate on,  g = 7
g = Pair(1, 2) > Pair(1, 5)  8   # 1 > 1 fails     -> gate off, g stays 7
```

The multi-cell AND is not a conjunction operator smuggled into the gate — it is
the only single-bit reading of a per-slot expression in a position that asks one
question: *did it yield?* A multi-value expression yields when it produces its
complete value, i.e. every slot yields. The same reading gives a `||` gate its
OR semantics (its yield flag — `a > 2 || b > 3` yields when either side does),
and is why an **array cell is rejected** in a gate: a mask always yields, so a
gate over one would look meaningful while testing nothing.

### Conditions are value positions

A condition is itself a value position: a comparison yields its LHS and chains,
and the gate asks "did that value-position expression yield?" (the conjunction of
its comparisons). So a chained comparison gates a range directly — the same
`i > 2 < 8` that filters in value position:

```pluto
arr = i > 2 < 8  [i * i]      # gate on 2 < i < 8        -> [9 16 25 36 49]
arr = [i > 2 < 8 && i * i]    # same gate, per cell      -> [0 0 0 9 16 25 36 49 0 0]
```

This holds in every condition slot: a statement gate, an `&&` condition, and a
scalar gate. The same rule applies everywhere, so there is no separate
"condition comparison" form to learn.

Because the chain binds to the **leftmost** operand (the value-position rule),
`i > 2 < 8` means `i > 2 AND i < 8`, but the math-natural `2 < i < 8` means just
`2 < i` — the `< 8` chains onto the constant `2` (`2 < 8`, always true), not onto
`i`. **Put the variable first** (`i > 2 < 8`), or use `&&` (`i > 2 && i < 8`),
for a two-sided bound. A `||` in a condition is an OR gate (`a > 2 || b > 2`);
its operands must have matching types and it must be able to fail (a `|| value`
fallback that always yields cannot gate).

## Value position: propagate to the nearest resolver

A conditional inside the value does not resolve on the spot. It yields when its
condition holds; when it does not, the **failure propagates** outward through
the expression until something resolves it:

- `=` — the commit is skipped: an existing variable keeps its value, a fresh
  one keeps its zero seed.
- a collector cell — the cell zero-fills (shape is preserved).
- `||` — the fallback resolves it locally.

```pluto
y > 2          # yields y when y > 2, else fails    (the value is the left operand)
y > 2 && 3     # yields 3 when y > 2, else fails
```

`cond && value` is the value-position conditional AND: it yields its **right** side
when the left yields. Chains are last-wins (`a > 2 && b > 3 && 10` is `10` only
when both hold), and the right side is **lazy** — evaluated at most once, only
when the left yielded. Propagation composes through arithmetic:

```pluto
z = 99
z = x * (y > 2 && 3)   # y >  2 -> z = x * 3
                       # y <= 2 -> the failure rides through the *; z stays 99
```

Naming the temporary moves the resolver: `tmp = y > 2 && 3` resolves at `tmp`'s
own `=` (a fresh `tmp` keeps `0`), and `z = x * tmp` then always assigns. Inline
and named forms differ exactly by where the nearest resolver sits.

## Fallback `||` resolves the failure

`||` supplies a chosen value when its left side fails to yield. Its left
operand must be able to fail; it is left-biased and chains:

```pluto
a > 2 || 7           # a when a > 2, else 7
a > 2 || b > 2 || 9  # a if a>2, else b if b>2, else 9
```

`&&` binds tighter than `||`, so `c && v || w` reads as `(c && v) || w` — a
per-slot if-else: `v` when `c` yields, else `w`.

```pluto
a > 2 && 3 || 7      # 3 when a > 2, else 7
```

The statement-gate spelling `x = a > 2  3 || 7` is instead an error — `a > 2`
gates the statement, leaving `3 || 7` as a value-position `||` whose left
operand cannot fail. And `cond && v || 0` reproduces an unconditional
zero-on-failure write where one is wanted.

`||` keys off the condition, not the resolved value, so a true condition whose
value happens to be zero is kept:

```pluto
a = 0
a < 2 || 7           # a < 2 is true -> yields a (0), not 7
```

The left operand need not be a bare conditional — it may **wrap** its condition
in arithmetic. Because value-position conditions propagate (the whole value gates
on them, the same conjunction that drives zero-fill), `||` attaches wherever a
value can fail and swaps its fallback in for the failure:

```pluto
(i > 2 < 8) ^ 2 || -1.0   # i^2 when 2 < i < 8, else -1.0 (incl. i >= 8)
(a > 2) + 100 || -1       # a + 100 when a > 2, else -1
(a > 2) + (b > 3) || -1   # a + b when both hold (conditions AND), else -1
```

Only a left operand with **no** condition anywhere in its tree is rejected
(`5 + 3 || -1`, `a || -1`) — there is nothing that can fail for `||` to catch.

## Parentheses are pure grouping

Parentheses only group — there is no parenthesized `(cond value)` expression
(`(a > 2 10)` is a parse error), and there are no comma conjunctions anywhere
(a statement condition is one expression). Write conjunctions with `&&`
(`c1 && c2 && v`), and use parentheses to choose what a gate or `||` attaches
to:

```pluto
x = a > 3  b < 5 || 10     # gate a > 3 (keep-old); || is the fallback for b < 5
x = a > 3 && b < 5 || 10   # one expression; either failure falls to || -> 10
```

In the first, `a > 3` gates the statement (keep-old) and `|| 10` catches only
`b < 5`. In the second, the `&&` chain yields `b` when both comparisons hold,
and the `||` resolves a failure of **either** link.

At an assignment root the two spellings agree: `y = a > 2 && 10` and the
statement gate `y = a > 2  10` both keep `y`'s old value when `a <= 2` — one
keep-old rule, reached by propagation or by the gate. The removed bracket form
`y = (a > 2 10)` was the odd one out (it always assigned, zero on failure);
spell that `y = a > 2 && 10 || 0`.

## Arrays

A collector cell is a value position, so the same rule applies: a failed cell is
zero, and `||` overrides.

```pluto
[i > 2]          # failed cells are 0   (zero-fill)
[i > 2 || -1]    # failed cells are -1
```

Each cell resolves locally and independently — a failed cell zero-fills (or takes
its `||` fallback) without gating the rest of the literal or the enclosing
assignment. This holds whether the cells are scalar or range-driven: `[a > 99]`
is `[0]` (not `[]`), and `[a > 2  a > 99]` is `[5 0]` (not empty or kept-old).
An `&&` cell propagates into the same resolution: `[b > 2 && 5]` zero-fills when
the condition fails, and `[b > 2 && 5 || -1]` takes the fallback instead.

For planned nested range construction after PIR migration, the same rule
extends recursively to array-valued cells. A failed child contributes a
zero-filled array of its expected shape; an explicit array fallback overrides
it:

```pluto
[i > 0 && [F(i, j)]]                  # failed i positions get a zero row
[i > 0 && [F(i, j)] || [j && -1]]    # failed i positions get a -1 row
```

The expected child shape must be known or derivable from bound range domains.
When it is not, the source must supply a shape-bearing fallback such as
`|| [j && 0]` for a zero row or `|| [j && -1]` for a different fill value.
Pluto never invents an empty, padded, or flattened child. This is value-position
resolution and preserves the outer domain; a statement gate instead rejects
the complete iteration point.

Spacing separates cells, and operators glue their operands into one cell:

```pluto
[a > 3  b < 5 || 10]     # two cells
[a > 3 && b < 5 || 10]   # one cell
```

A direct array comparison follows the same rule element-wise: `arr > k` is a
**mask** — each cell keeps its left value where the comparison holds, else 0, *in
place* (length- and position-preserving). It is the scalar "yield the left
operand, else 0" applied per element, so it stays consistent with the collector
cell above and with array arithmetic (`+`, `*`): `arr1 > arr2` zips to the
shorter length, `arr > scalar` spans the array and broadcasts the scalar.

```pluto
[1 3 5 7] > [0 4 4 8]    # [1 0 5 0]   (failed cells masked to 0, in place)
[4 8 1] > 3              # [4 8 0]      (array length, scalar broadcasts)
[1 3 5 7] > [0 4]        # [1 0]        (zip to min length; surplus dropped)
```

A failed comparison means "this element did not pass" (0 in place); a missing
element from a length mismatch is dropped, never zero-filled — so no fabricated 0
ever reaches a value. Strings stay lexicographic. (A range-driven comparison over
a *stream* still skips rather than masks — see
[Pluto Range Semantics](Pluto%20Range%20Semantics.md).)

## Multi-return comparisons

A comparison over multi-valued operands (`Pair(...) > Pair(...)`,
`Mix(...) > Mix(...)`) resolves **per slot** in value position — each slot behaves
exactly as the single-value comparison would, independent of its siblings, with no
all-or-nothing. An array slot is an element-wise mask (it overwrites its target). A
scalar slot yields its left operand where the comparison holds, and otherwise keeps
the target's prior value — `0` for a fresh target, the existing value for an
existing one — the same keep-old-on-false a bare comparison gives.

```pluto
a, b  = Pair(1, 5) > Pair(2, 4)   # 0 5      (fresh: 1 > 2 keeps 0; 5 > 4 -> 5)
r, n  = Mix(2) > Mix(1)           # [2 3] 2  (array slot masks; scalar slot yields)
n2 = 99
r2, n2 = Mix(1) > Mix(2)          # [0 0] 99 (array overwrites; scalar 1 > 2 keeps 99)
```

Per-slot resolution follows the value through its expression. A slot's conditions
merge along dataflow: chaining a multi-return comparison chains each slot
(`Pair > Pair < Pair` — the leftmost operand binds per slot, its conditions AND
slot-wise), and arithmetic over one stays per slot (`(Pair(5, 7) > Pair(1, 8)) +
Pair(0, 0)` → `5 0` fresh — the failing slot keeps old and its combine never
runs). A `||` falls back **per slot**: slot *i* takes the right side only when
slot *i* of the left failed to yield, a fallback beats keep-old, and a slot where
both sides fail keeps its old value (`Pair(5, 7) > Pair(1, 8) || Pair(0, 0)` →
`5 0`). A `||` inside an operand resolves to its value before the compare
(`Pair(a > 2 || 7, b) > Pair(1, 1)`). Array slots always yield (a mask is a
value), so `||` only ever affects scalar slots.

Slots merge where an expression produces its outputs together: a **call's**
outputs all share every argument's condition (ANDed), so `Sum2(Pair(5, 7) >
Pair(1, 8))` is gated whole — a failing argument comparison skips the call and
keeps old, the same hoisting rule a scalar call argument gets
(`q = Sum2(k > 9, 3)` keeps `q` when `k <= 9`).

The cell-wise **AND** (every cell must hold) applies only where a multi-cell
comparison is a **gate/condition** (`Pair(1, 2) > Pair(0, 1)  value`, including
chained gates `Pair > Pair < Pair  value`) — never to the value itself. A ranged
statement condition gates iterations without changing the value's per-slot
semantics.

A value-position `&&` zips slots like `||` does: slot *i* of the right side
commits exactly when slot *i* of the left yields, so a multi-cell condition
gates a multi-value right per slot, not as a unit
(`Pair(1, 2) > Pair(0, 5) && Pair(7, 7)` → `7 0`). The left contributes only
conditions, so its arity folds or broadcasts onto the value's: one condition
gating a multi-value right broadcasts (`x, y = a > 2 && Pair(10, 20)` gates
both slots together, like the statement gate), and a multi-slot condition
gating one value folds — every slot must yield
(`Pair(5, 7) > Pair(1, 8) && 9` keeps old, because slot 1 misses). Any other
arity mismatch is rejected.

Comparison operands are evaluated eagerly (once); only the yielding combine and
the commit are gated per slot. The one lazy operand is an `&&` right side,
evaluated at most once when some left slot yielded — so
`c > 0 && (a > 2 || 7)` composes: the inner `||` resolves inside the gated arm.

An **out-of-bounds read is a failed condition on the lanes it feeds**, merged
by the same rules: a plain read no-ops its assignment (`x = arr[oob]` keeps
old), sibling expressions in one statement commit independently
(`a, b = arr[oob], 5` keeps `a`, sets `b`), a call merges its lanes (an OOB
argument keeps that call's outputs old as a unit), comparisons and `||`
fallbacks fed by an OOB read keep old rather than judging a fabricated zero,
and an unevaluated `||` right side cannot fail anything. The one place OOB
still reads as `0` is a **collector cell over an explicit range** — the
documented full-control opt-in.

## Why this model

- **One propagation rule:** every failed condition — a comparison, an `&&`, an
  out-of-bounds read — travels the same dataflow lanes, and the resolvers are
  few and explicit: `=` keeps old, a collector cell zero-fills, `||` supplies
  the fallback. There is no second, locally-resolving conditional form.
- **The two spellings agree:** the statement gate and value-position
  propagation share the keep-old rule at `=`, so `y = a > 2  10` and
  `y = a > 2 && 10` mean the same thing at a root.
- **No fabricated zeros:** a failed slot skips its commit instead of
  materializing a sentinel `0` that flows onward; zeros appear only where a
  resolver deliberately introduces them (a fresh seed, a cell's zero-fill, an
  explicit `|| 0`).

## Status

- **Implemented:** statement gates (keep-old); gated prints — print position
  is value position, the target-less case of propagation: a comparison prints
  its yielded LHS, a failure prints nothing, a ranged comparison filters to
  admitted elements, and the explicit boolean spelling is `cond && 1 || 0`;
  `||` fallback in value and
  condition position (per slot over multi-return values); value-position
  comparisons (yield the left operand), resolved per slot through chains,
  arithmetic, and `||`/`&&` by one extraction pass; the value-position
  conditional `&&` (yields its right side per slot, lazy right, last-wins chains, binds
  tighter than `||`, so `c && v || w` is a per-slot if-else); conditions are
  value positions, so chained comparisons gate directly (`i > 2 < 8`,
  leftmost-binding, including chained multi-return gates) in every condition
  slot; collector zero-fill; array masks (element-wise, length-preserving).
- **Conjunction conditions:** short-circuit `&&`/`||` over conditions; a
  multi-cell comparison in a condition slot gates on the cell-wise AND —
  every cell must hold. Heap operands (e.g. string cells via `⊕`) are freed
  after gating.
- **Removed:** the parenthesized `(cond value)` expression and comma
  conjunctions everywhere — a statement condition is one expression, comma
  means positional lists only. Parentheses are pure grouping; write
  `c1 && c2 && v` for the conjunction, and `cond && v || 0` where the old local
  zero-on-failure resolution is wanted. Propagation now applies everywhere —
  `(a > 2 && 10) + 1` keeps the old value when `a <= 2`, exactly like
  `(a > 2) + 1`.
- **Ranges:** an `&&` may be range-driven. In a collector it iterates and
  zero-fills failed cells (`[i > 2 && i]` → `[0 0 0 3 4]`,
  `[i > 2 && i * 10]` → `[0 0 0 30 40]`); at an assignment root failing
  iterations keep the destination, so the last yielded value wins
  (`r = i > 2 && i` → `4`, and `r = i < 2 && i` → `1` — the failing tail keeps
  the yield from `i = 1`).
