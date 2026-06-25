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

Multiple conditions separated by commas are ANDed; all must hold.

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

A filter is different: `arr > k` drops elements that fail rather than zeroing
them. See [Pluto Range Semantics](Pluto%20Range%20Semantics.md).

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
  collector zero-fill; array filters; the parenthesized `(cond value)`
  expression (local resolution to zero, with `|| fallback` and array-cell
  zero-fill), including per-cell use inside array literals.
- **Transitional inconsistency:** `(cond value)` resolves **locally** to zero,
  but a bare value-position comparison (`a > 2`) still **propagates** (keep-old).
  So `(a > 2 10) + 1` is `1` when `a <= 2` (local zero), while `(a > 2) + 1`
  keeps the old value. They agree for new variables and inside array cells; they
  differ only for existing variables and nested arithmetic.
- **Planned change:** migrate bare value-position comparisons from propagation to
  the same local resolution, removing the inconsistency above.
- **Ranges:** a `(cond value)` may be range-driven. In a collector it iterates and
  yields the per-iteration value or zero (`[(i > 2 i)]` → `[0 0 0 3 4]`,
  `[(i > 2 i*10)]` → `[0 0 0 30 40]`); at an assignment root it keeps the final
  iteration's value (`r = (i > 2 i)` → `4`).
