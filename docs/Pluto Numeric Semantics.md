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
