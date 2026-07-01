# Pluto Conditional Value Semantics

How conditions behave depending on where they appear: as a statement gate, or as
a value inside an expression. This is the general model under
[Pluto Range Semantics](Pluto%20Range%20Semantics.md).

## Two positions, two meanings

A condition means different things in two places:

1. **Statement gate** — left of the value in `lhs = cond value`. Gates the whole
   assignment.
2. **Value position** — inside the value expression, e.g. `x * (y > 2 3)`.
   Produces a value.

The rule, in one line: **conditions left of the value gate the statement and keep
the old value on failure; conditions inside the value are temporaries that
resolve to zero (or a `||` fallback) on failure.**

## Statement gate: keep-old

A condition before the value gates the assignment. If it fails, no assignment
happens: an existing variable keeps its value, a new variable is zero.

```pluto
old = 99
old = b > 2  x * 3    # b <= 2 -> old stays 99
                      # b >  2 -> old = x * 3

new = b > 2  x * 3    # b <= 2 -> new = 0
```

Multiple conditions separated by commas are ANDed; all must hold. A multi-cell
comparison (`Pair > Pair`, including string pairs) likewise gates on the
conjunction of its cells — every cell must hold. Both forms work in any condition
slot (statement gate, `(cond value)`, array cell).

### Conditions are value positions

A condition is itself a value position: a comparison yields its LHS and chains,
and the gate asks "did that value-position expression yield?" (the conjunction of
its comparisons). So a chained comparison gates a range directly — the same
`i > 2 < 8` that filters in value position:

```pluto
arr = i > 2 < 8  [i * i]      # gate on 2 < i < 8        -> [9 16 25 36 49]
arr = [(i > 2 < 8  i * i)]    # same gate, per cell      -> [0 0 0 9 16 25 36 49 0 0]
```

This holds in every condition slot: a statement gate, a `(cond value)` condition,
and a scalar gate. The same rule applies everywhere, so there is no separate
"condition comparison" form to learn.

Because the chain binds to the **leftmost** operand (the value-position rule),
`i > 2 < 8` means `i > 2 AND i < 8`, but the math-natural `2 < i < 8` means just
`2 < i` — the `< 8` chains onto the constant `2` (`2 < 8`, always true), not onto
`i`. **Put the variable first** (`i > 2 < 8`), or use comma-AND (`i > 2, i < 8`),
for a two-sided bound. A `||` in a condition is an OR gate (`a > 2 || b > 2`);
its operands must have matching types and it must be able to fail (a `|| value`
fallback that always yields cannot gate).

## Value position: a temporary, zero on false

A conditional inside the value is a computed temporary. It yields its value when
the condition holds, and the zero value when it does not — exactly like a fresh
variable.

```pluto
y > 2 3      # 3 when y > 2, else 0
y > 2        # y when y > 2, else 0   (the value is the left operand)
```

So it composes like any value:

```pluto
z = x * (y > 2 3)     # y >  2 -> z = x * 3
                      # y <= 2 -> z = x * 0 = 0
```

This is identical to naming the temporary first:

```pluto
tmp = y > 2 3         # tmp = 3 or 0
z   = x * tmp
```

Storing a conditional and inlining it behave the same. (Referential
transparency.)

## Fallback `||` overrides the zero

`||` supplies a chosen value when the condition fails, instead of zero. Its left
operand must be a conditional (able to fail); it is left-biased and chains:

```pluto
a > 2 || 7           # a when a > 2, else 7
a > 2 || b > 2 || 9  # a if a>2, else b if b>2, else 9
```

To fall back from an explicit value, parenthesize the conditional: `(a > 2 3) || 7`
yields `3` when `a > 2`, else `7`. Unparenthesized, `x = a > 2 3 || 7` is an error —
`a > 2` gates the statement, leaving `3 || 7` as a value-position `||` whose left
operand cannot fail.

`||` keys off the condition, not the resolved value, so a true condition whose
value happens to be zero is kept:

```pluto
a = 0
a < 2 || 7           # a < 2 is true -> yields a (0), not 7
```

The left operand need not be a bare conditional — it may **wrap** its condition
in arithmetic. Because value-position conditions propagate (the whole value gates
on them, the same conjunction that drives zero-fill), `||` attaches wherever a
value can fail and swaps its fallback in for the zero:

```pluto
(i > 2 < 8) ^ 2 || -1.0   # i^2 when 2 < i < 8, else -1.0 (incl. i >= 8)
(a > 2) + 100 || -1       # a + 100 when a > 2, else -1
(a > 2) + (b > 3) || -1   # a + b when both hold (conditions AND), else -1
```

Only a left operand with **no** condition anywhere in its tree is rejected
(`5 + 3 || -1`, `a || -1`) — there is nothing that can fail for `||` to catch.

## Parentheses group the condition

At the top of a statement the `=` separates condition from value, so no
parentheses are needed (`lhs = cond value`). Inside a larger expression,
parentheses mark where a conditional ends, choosing what the gate and `||`
attach to:

```pluto
x = a > 3  b < 5 || 10     # gate a > 3 (keep-old); || is the fallback for b < 5
x = (a > 3 b < 5) || 10    # one conditional; || is the fallback for a > 3
```

In the first, `a > 3` gates the statement and `|| 10` catches `b < 5`. In the
second, `(a > 3 b < 5)` is a single conditional, so `|| 10` fires when `a > 3`
fails, while a failing `b < 5` resolves locally to zero. A conditional resolves
locally and never reaches back out to an enclosing operator.

Wrapping the whole right-hand side keeps the bracket in value position, so
`y = (a > 2 10)` is a plain value assignment — it **always** assigns (local
resolution: `10` when `a > 2`, else `0`, overwriting `y`), and is treated as an
unconditional write by the dead-store check. The unbracketed `y = a > 2 10` is
instead a statement gate: it keeps `y`'s old value when `a <= 2`. Same-looking,
deliberately different — the brackets choose value-position (local) over the
gate (keep-old).

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
This matches the `(cond value)` cell form exactly; a bare comparison and
`(cond value)` are interchangeable as cells.

Spacing separates cells, so parentheses also control cell count:

```pluto
[a > 3  b < 5 || 10]     # two cells
[(a > 3 b < 5) || 10]    # one cell
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

The cell-wise **AND** (every cell must hold) applies only where a multi-cell
comparison is a **gate/condition** (`Pair(1, 2) > Pair(0, 1)  value`) or the test
behind a value-position `||` fallback — never to the value itself.

Two forms are **rejected at compile time** for now, rather than silently falling
back to all-or-nothing (which would contradict the per-slot rule above):

- *Chaining* a multi-return comparison (`Pair > Pair < Pair`) — per-slot chaining
  needs the leftmost-binding chain extraction the per-slot lowering can't yet reach.
- A **bare value-position `||` inside an operand** (`Pair(a > 2 || 7, b) > Pair(1, 1)`)
  — the per-slot lowering evaluates operands inline and can't set up the branching a
  `||` needs. (A `||` behind a `(cond value)` or array literal self-resolves and is
  fine; a *top-level* `||` fallback — `Pair > Pair || Pair` — is fine too.)

Single-value chained comparisons (`a > 2 < 8`) are unaffected. Per-slot support for
both is planned for the value-extraction unification.

## Why this model

- **Referential transparency:** a value-position conditional equals a named
  temporary, so storing and inlining never differ.
- **Keep-old stays explicit:** it is provided by the statement gate, not hidden
  inside value-position conditionals.
- **Simple lowering:** a value-position conditional is a `select` between the
  value and zero (or the `||` fallback) — no expression-wide branching.

## Status

- **Implemented:** statement gates (keep-old); `||` fallback in value and
  condition position; value-position comparisons (yield the left operand);
  conditions are value positions, so chained comparisons gate directly
  (`i > 2 < 8`, leftmost-binding) in every condition slot; collector zero-fill;
  array masks (element-wise, length-preserving); the parenthesized `(cond value)` expression (local resolution to
  zero, with `|| fallback` and array-cell zero-fill), including per-cell use
  inside array literals.
- **Conjunction conditions:** every condition slot accepts a comma-AND list
  (`a > 2, b > 3`) and a multi-cell comparison (`Pair(1, 2) > Pair(0, 1)`, gating
  on the cell-wise AND — every cell must hold). Heap operands (e.g. string cells
  via `⊕`) are freed after gating.
- **Transitional inconsistency:** `(cond value)`, array cells, and array masks
  resolve **locally** to zero, but a bare value-position comparison (`a > 2`) and a
  multi-return comparison's scalar slots still **propagate** (keep-old on false).
  So `(a > 2 10) + 1` is `1` when `a <= 2` (local zero), while `(a > 2) + 1`
  keeps the old value. They agree for new variables and inside array cells; they
  differ only for existing variables and nested arithmetic.
- **Planned change:** migrate bare value-position comparisons from propagation to
  the same local resolution, removing the inconsistency above.
- **Ranges:** a `(cond value)` may be range-driven. In a collector it iterates and
  yields the per-iteration value or zero (`[(i > 2 i)]` → `[0 0 0 3 4]`,
  `[(i > 2 i*10)]` → `[0 0 0 30 40]`); at an assignment root it keeps the final
  iteration's value (`r = (i > 2 i)` → `4`).
