# Pluto String and Formatting Semantics

This document defines Pluto string literals, interpolation markers, format
specifiers, and quoted output.

## String values

`Str` is a sequence of non-NUL bytes stored with a trailing NUL terminator.
Source string literals are Unicode text encoded as UTF-8, but byte-oriented
operations such as `printf` precision can produce values that are not valid
UTF-8. Scalar strings print without quotes by default. String elements inside
arrays print quoted so element boundaries remain unambiguous.

```pluto
word = "hello world"
word                    # hello world
[word ""]               # ["hello world" ""]
```

## String escapes

Pluto supports these simple escapes:

```text
\\  \"  \-  \%  \n  \t  \r  \b  \f
```

It also supports fixed-width Unicode code-point escapes:

```text
\xNN  \uNNNN  \UNNNNNNNN
```

Each `N` is a hexadecimal digit. These escapes denote Unicode code points,
not raw bytes, and are encoded as UTF-8. For example, `\xff` is `ÿ` (UTF-8
bytes `c3 bf`), while `\xc3\xbf` is `Ã¿`.

NUL is rejected in every form. Surrogate code points, values above U+10FFFF,
octal escapes, malformed escapes, and unrecognized escapes are compile errors.

Escape-produced characters are always literal data. They never become marker
or format syntax. Thus `\x2dname` is literal `-name`, even when `name` exists.

## Interpolation markers

A raw `-` followed by an identifier is an interpolation marker only when that
identifier is defined in the current scope.

```pluto
name = "Pluto"
"Hello -name"           # Hello Pluto
"Send -mail"            # Send -mail, when mail is not defined
"Literal: \-name"       # Literal: -name
```

If an identifier is undefined, that marker alone remains literal. Scanning then
continues, so later defined markers are still interpolated:

```pluto
width = 5
"-missing%(-width)d" # -missing%(5)d
```

## Literal percent and strict formatting

A `%` outside a resolved marker is ordinary text. A `%` immediately after a
resolved marker always starts a format specifier. The complete specifier must
match the grammar below; incomplete or malformed specifiers are compile errors.

```pluto
n = 95
"Progress: -n%%"          # Progress: 95%
"Progress: -n\% complete" # Progress: 95% complete
"Padded: -n%05d"          # Padded: 00095
"Signed: -n% d"           # Signed:  95
```

Use `\%` to make the percent literal data, or `%%` as a printf-familiar
spelling for the value followed by one literal percent:

```pluto
"Literal: -n\%d"         # Literal: 95%d
"Progress: -n%% complete" # Progress: 95% complete
```

Outside a marker, both percent characters in `%%` are literal.

## Format grammar

The supported grammar is:

```text
specifier  = "%" ("%" | flags width? precision? length? conversion)
flags      = { "-" | "+" | " " | "#" | "0" }
width      = digits | dynamic
precision  = "." [digits | dynamic]
dynamic    = "(-" identifier ")"
length     = "l" ["l"]
```

Dynamic width and precision identifiers must be defined in the current scope
and have type `I64`. Undefined identifiers and non-`I64` values are compile
errors. A direct `*` is rejected; use `(-identifier)` instead.

Supported conversions are:

| Conversion | Value |
| --- | --- |
| `d`, `i`, `u`, `o`, `x`, `X` | `I64` |
| `f`, `F`, `e`, `E`, `g`, `G`, `a`, `A` | `F64` |
| `c` | `I64` character value |
| `s` | string-compatible output |
| `q` | quoted `Str` |
| `p` | pointer |
| `n` | writable `I64` byte-count target |
| `%` | default-formatted value followed by `%` |

Only `l` and `ll` length modifiers are accepted, and only for integer
conversions. Pluto normalizes integer formatting to its `I64` calling
convention. Other C length modifiers are rejected.

Flags are conversion-specific:

| Conversion | Flags |
| --- | --- |
| `d`, `i` | `-`, `+`, space, `0` |
| `u` | `-`, `0` |
| `o`, `x`, `X` | `-`, `#`, `0` |
| floating-point conversions | `-`, `+`, space, `#`, `0` |
| `c`, `s`, `q`, `p` | `-` |
| `n`, `%` | none |

Width is not supported for `n` or `%`. Pointer output uses lowercase
alternate-form hexadecimal, including an `0x` prefix for nonzero addresses.
Precision is not supported for `c`, `p`, `n`, or `%`. Following `printf`,
precision on `%s` and `%q` limits the original string in UTF-8 bytes, not Unicode
code points, and can split a multi-byte character. For example, `πx` has bytes
`cf 80 78`, so `%.1s` emits only `cf`, while `%.2s` emits `π`. `%q` applies
the same byte limit before escaping and quoting the selected prefix. Thus `%.2q`
applied to `hello` produces `"he"`, with a complete closing quote. Width and
alignment are then applied to that completed representation.

## Validation order

Formatting validation proceeds in this order:

1. Resolve the main interpolation marker. If it is undefined, keep the text
   literal and stop.
2. Parse and validate specifier syntax.
3. Resolve dynamic width and precision identifiers.
4. Validate dynamic identifier and conversion value types.
5. Lower the compiler-controlled format string and matching arguments.

This order ensures malformed or type-incompatible formats never reach the C
runtime.
