# Pluto String and Formatting Semantics

This document defines Pluto string literals, interpolation markers, format
specifiers, and quoted output.

## String values

`Str` is a sequence of non-NUL bytes stored with a trailing NUL terminator.
Unescaped source text and Unicode escapes are encoded as UTF-8. Byte escapes
and byte-oriented operations such as `printf` precision can produce values that
are not valid UTF-8. Scalar strings print without quotes by default. String
elements inside arrays print quoted so element boundaries remain unambiguous.

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

It also supports fixed-width byte and Unicode escapes:

```text
\xNN  \uNNNN  \UNNNNNNNN
```

Each `N` is a hexadecimal digit. `\xNN` inserts exactly one raw byte. `\uNNNN`
and `\UNNNNNNNN` denote Unicode code points and encode them as UTF-8. For
example, `\xff` inserts the byte `ff`, while `\u00ff` and `\xc3\xbf` both
produce `ÿ` (UTF-8 bytes `c3 bf`).

NUL is rejected in every form. Unicode escapes also reject surrogate code
points and values above U+10FFFF. Octal escapes, malformed escapes, and
unrecognized escapes are compile errors.

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

A marker that reads a `Range` participates in normal range execution. At an
assignment root, formatting runs once per yield and the final owned string is
kept; in print position, one formatted line is emitted per yield. Range
identifiers used for dynamic width or precision are drivers too.

```pluto
i = 0:3
last = "item -i" # "item 2"
"item -i"        # prints item 0, item 1, item 2 on separate lines
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
| `d`, `i`, `u`, `o` | `I64` |
| `x`, `X` | hexadecimal `I64`, or hexadecimal bytes of `Str` |
| `f`, `F`, `e`, `E`, `g`, `G`, `a`, `A` | `F64` |
| `c` | `I64` character value |
| `s` | string-compatible output |
| `q` | quoted `Str` |
| `p` | pointer |
| `n` | writable `I64` byte-count target |
| `%` | default-formatted value followed by `%` |

## Array element formatting

A compatible scalar conversion attached to an array marker is applied to every
element. The brackets and single-space element separators remain part of the
array representation; width and precision apply independently to each element.

```pluto
ints = [1 20 255]
values = [1.5 2.25]
words = ["hello" "hello world"]

"-ints%4d"       # [   1   20  255]
"-ints%#x"       # [0x1 0x14 0xff]
"-values%7.2f"   # [   1.50    2.25]
"-words%s"       # [hello hello world]
```

The supported element conversions are:

| Array element type | Conversions |
| --- | --- |
| `I64` | `d`, `i`, `u`, `o`, `x`, `X`, `c` |
| `F64` | `f`, `F`, `e`, `E`, `g`, `G`, `a`, `A` |
| `Str` | `s` |

Static and dynamic width and precision use the normal grammar. For example,
`"-values%(-width).(-precision)f"` applies the same runtime width and precision
to every element. Without a custom conversion, array defaults are unchanged:
string elements remain quoted and escaped. Explicit `%s` on a string array is
intentionally unquoted and can therefore be ambiguous when elements contain
spaces or are empty.

Array `%c` follows C's conversion through `unsigned char`. Because Pluto strings
cannot contain NUL, a resulting NUL is rendered as the visible text `\x00`;
field width applies to that rendered representation.

Only `l` and `ll` length modifiers are accepted, and only for integer
conversions. Pluto normalizes integer formatting to its `I64` calling
convention. Other C length modifiers are rejected.

Flags are conversion-specific:

| Conversion | Flags |
| --- | --- |
| `d`, `i` | `-`, `+`, space, `0` |
| `u` | `-`, `0` |
| `o`, and `I64` `x`, `X` | `-`, `#`, `0` |
| `Str` `x`, `X` | `-`, `#`, space |
| floating-point conversions | `-`, `+`, space, `#`, `0` |
| `c`, `s`, `q`, `p` | `-` |
| `n`, `%` | none |

Width is not supported for `n` or `%`. Pointer output uses lowercase
alternate-form hexadecimal, including an `0x` prefix for nonzero addresses.
Precision is not supported for `c`, `p`, `n`, or `%`. Following `printf`,
precision on `%s`, `%q`, and string `%x`/`%X` limits the original string in
bytes, not Unicode code points, and can split a multi-byte character. For example,
`πx` has bytes `cf 80 78`, so `%.1s` emits only `cf`, while `%.2s` emits `π`.
`%q` applies the same byte limit before escaping and quoting the selected prefix;
`%.2q` applied to `hello` therefore produces `"he"`, with a complete closing
quote. Quoted output preserves well-formed UTF-8 and writes each invalid byte as
`\xNN`; `%.1q` applied to `πx` therefore produces `"\xcf"`. String `%.1x` and
`%.2x` applied to `πx` produce `cf` and `cf80` respectively. `%x` uses lowercase
digits and `%X` uppercase digits. With strings, the space flag separates encoded
bytes and combining it with `#` prefixes every byte, so `%# x` produces output
such as `0xcf 0x80`; without the space flag, `#` prefixes the complete encoding
once. Zero padding remains specific to numeric hexadecimal conversions. Width
and alignment are applied to the completed quoted or hexadecimal representation.

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
