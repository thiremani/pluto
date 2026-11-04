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
cells use these same operators, one cell at a time.

## Dividing I64 by zero

`÷` and `%` have a defined result for every pair of I64s, on every machine, and
never trap. A literal operand follows the same rules.

| Expression | Result |
| --- | --- |
| `x ÷ 0` | 0 |
| `x % 0` | `x` |
| `MIN ÷ -1` | `MIN` |
| `MIN % -1` | 0 |

So `((x ÷ y) * y + (x % y)) == x` holds for every pair. These are ordinary
successful values, so `||` can't detect them, and an accidental zero divisor can
pass unnoticed.

A program that wants a zero divisor to count as failure filters it:

```pluto
q = a / (n != 0)             # F64; keeps q when n is 0
q = (a / (n != 0)) || -1.0   # F64 fallback

r = a ÷ (n != 0)             # I64; keeps r when n is 0
r = (a ÷ (n != 0)) || -1     # I64 fallback
```

The parentheses around the comparison are optional, but they show it checks the
divisor, not the quotient. The ones around the whole division are required:
`a ÷ (n != 0) || -1` divides by -1 when `n` is 0. A function's output must always
be written, so use `||` there or initialize the output first.

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
