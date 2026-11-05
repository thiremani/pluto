# Pluto Numeric Semantics

This document states what the I64 and F64 operators do at their limits. `MIN`
is -2^63, spelled `-9223372036854775807 - 1` because `-9223372036854775808`
doesn't parse, and `MAX` is 9223372036854775807.

## I64

`+`, `-`, `*` and unary `-` wrap in two's complement. There are no overflow
checks.

| Expression | Result |
| --- | --- |
| `MAX + 1` | `MIN` |
| `MIN - 1` | `MAX` |
| `MAX * 2` | -2 |
| `-MIN` | `MIN` |

`÷` truncates toward zero, and `%` takes the dividend's sign.

| Expression | Result |
| --- | --- |
| `-7 ÷ 2` | -3 |
| `7 ÷ -2` | -3 |
| `-7 % 3` | -1 |
| `7 % -3` | 1 |

`&`, `|`, `⊻`, `~`, `!` and comparisons have a result for every input. Array
cells use these same operators, one cell at a time. An array operation can still
abort, on a shape mismatch or when `⊕` would overflow a dimension.

## Dividing I64 by zero

`x ÷ 0` and `x % 0` stop the program on every machine with the message
`pluto: division by zero`. Output printed so far is kept, and a program can't
catch the stop. A literal operand follows the same rule: `7 ÷ 0` compiles and
stops when it runs. An array divides one cell at a time, so one zero divisor cell
stops it.

`MIN ÷ -1` and `MIN % -1` wrap instead, so a `-1` used to replace a zero divisor
can't stop the program:

| Expression | Result |
| --- | --- |
| `MIN ÷ -1` | `MIN` |
| `MIN % -1` | 0 |

So `((x ÷ y) * y + (x % y)) == x` holds for every nonzero divisor. To keep going
past a zero divisor, check it first, on the spot or on the call:

```pluto
q = a / n                         # F64: ±Inf or NaN, and it keeps going

r = a ÷ (n != 0)                  # keeps r when n is 0
r = (a ÷ (n != 0)) || -1          # fallback
x = n != 0 Div(a, n)              # skips the call; keeps x

y = [8 ÷ (d[i] != 0)]             # per cell; a failed cell is 0
z = [(v[i] ÷ (d[i] != 0)) || -1]  # per cell fallback
d = [d[i] != 0 || 1]              # replaces zeros before dividing
```

The parentheses around the comparison are optional, but they show it checks the
divisor, not the quotient. The ones around the whole division are required:
`a ÷ (n != 0) || -1` divides by -1 when `n` is 0. A function's output must always
be written, so use `||` there or initialize the output first.

A check protects only the statement it gates, because there's no early return.
`y = n != 0 1` on its own line doesn't protect the next line, and it can keep an
old 1. Save a flag with `ok = n != 0 && 1 || 0`, then gate on it:
`x = ok == 1 Div(a, n)`.

An out-of-bounds divisor counts as a failed check: `x = a ÷ v[9]` keeps `x`. An
array comparison doesn't: `d != 0` leaves zero cells in place, so
`[8 8] ÷ (d != 0)` stops, and `([8 8] ÷ (d != 0)) || -1` is a compile error
because nothing in it can fail. A scalar check can fail, so
`([6 8 10] ÷ (n != 0)) || [3 4 5]` gives `[3 4 5]`.

A failed check protects only the work in its own scope and slot. A failure that a
`||` or a cell resolves protects nothing after it, so
`[((v[9] > 0) || 2) + 7 ÷ n]` stops. In
`p, q = (Pair(a, a) ÷ (Pair(n, n) != Pair(0, 5))) > Pair(0, 0)` the first slot's
check fails and the second's passes, so the second slot's divide stops.

Three spellings stop despite a check, because today the divide or call runs
before the check does: a read after the divide, `x = (7 ÷ n + v[9]) > 0`; a
fallback's right side before a later check, `x = ((a > 9) || Div(7, n)) + (n != 0)`;
and a function that divides, called with a read past the end in an array-valued
cell, `x = [Row(7, d[9])]`. PIR's checks-first work (Steps 6-8) is planned to fix
these.

## Shift counts

`<<`, `>>` and `>>>` use the count's low 6 bits, `k = n & 63`, for every count on
every machine. So a count of 64 means 0, and -1 means 63. A literal follows the
same rule: `1 << 64` is 1.

| Expression | Result |
| --- | --- |
| `x << n` | `x * 2^k`, wrapped |
| `x >> n` | `floor(x / 2^k)` |
| `x >>> n` | `k` zeros shifted in from the top |

`>>` rounds down, unlike `÷`: `-7 >> 1` is -4, while `-7 ÷ 2` is -3.

A program can ask for other behaviour by filtering the count:
`(x << (n < 64)) || 0` gives 0 at `n = 64`.

## From I64 to F64

`/` and `^` on two I64s give F64, and so do `√`, `∛` and `∜`: `7 / 2` is 3.5,
`2 ^ 10` is 1024 and `2 ^ -1` is 0.5.

An I64 mixed with an F64 converts to F64. Every integer through ±2^53 converts
exactly, and beyond that some round: `2^53 + 1` becomes `2^53`, while `2^53 + 2`
stays exact.

## F64

F64 follows IEEE 754, with no fast-math flags. Arithmetic adds no failure for a
zero divisor or a nonfinite result. Comparisons and operand failures still
propagate as usual.

| Expression | Result |
| --- | --- |
| `7 / 0`, `7.5 ÷ 0.0` | +Inf |
| `-7 / 0`, `-7.5 ÷ 0.0` | -Inf |
| `0 / 0`, `0.0 ÷ 0.0` | NaN |
| `0.0 * -1.0` | -0 |
| `√-1`, `∜-16` | NaN |
| `∛-8` | -2 |
| `0 ^ 0`, `0 ^ -1` | 1, +Inf |

`÷` is `x / y` truncated toward zero, so `-7.5 ÷ 2.0` is -3.

`%` is fmod-style, not the IEEE remainder: it takes the dividend's sign, and
`-7.5 % 2.0` is -1.5 where the IEEE remainder would be 0.5. `x % 0.0` is NaN, and
`7.5 % +Inf` is 7.5.

`^` and `∛` call the platform's math library, so their last digit can differ
between machines.

`==`, `<`, `<=`, `>` and `>=` fail when either side is NaN, as failed conditions.
`!=` succeeds, as IEEE says: `nan != nan` holds, so `x != x` is the usual NaN
test, and `(n != 0.0) || -1.0` gives NaN, not -1.0.
