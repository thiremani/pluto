# Pluto String and Formatting Semantics

This document defines Pluto string literals, interpolation markers, format
specifiers, and quoted output.

## String values

`Str` contains UTF-8 text and does not contain NUL characters. Scalar strings
print without quotes by default. String elements inside arrays print quoted so
element boundaries remain unambiguous.

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

If the main identifier is undefined, the complete marker-like text remains
literal. Any trailing pseudo-specifier is not parsed.

## Literal percent and format probing

A `%` outside a resolved marker is ordinary text. A `%` immediately after a
resolved marker starts a format-specifier probe.

The probe commits to format syntax when the next raw character can start:

- a supported conversion;
- a flag (`-`, `+`, `#`, or `0`);
- a width (`1` through `9` or `(-name)`);
- a precision (`.`);
- a length modifier; or
- the explicitly rejected direct `*` form.

If no such character follows, `%` remains literal and the marker uses its
default format.

```pluto
n = 95
"Progress: -n%"          # Progress: 95%
"Progress: -n% complete" # Progress: 95% complete
"Value: -n%v"            # Value: 95%v; v cannot start a Pluto specifier
```

Once the probe commits, the complete specifier must be valid. Malformed syntax
is a compile error and is never passed to `printf`.

Use `\%` to force a literal percent when the following character could start a
specifier:

```pluto
"Literal: -n\%d"         # Literal: 95%d
```

`%%` remains supported immediately after a marker as a printf-familiar spelling
for the value followed by one literal percent. Outside a marker, both percent
characters are literal.

## Format grammar

The supported grammar is:

```text
specifier  = "%" ("%" | flags width? precision? length? conversion)
flags      = { "-" | "+" | "#" | "0" }
width      = digits | dynamic
precision  = "." [digits | dynamic]
dynamic    = "(-" identifier ")"
length     = "l" ["l"]
```

Dynamic width and precision identifiers must exist in scope and have type
`I64`. A direct `*` is rejected; use `(-identifier)` instead.

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
| `d`, `i` | `-`, `+`, `0` |
| `u` | `-`, `0` |
| `o`, `x`, `X` | `-`, `#`, `0` |
| floating-point conversions | `-`, `+`, `#`, `0` |
| `c`, `s`, `q` | `-` |
| `p`, `n`, `%` | none |

Width is not supported for `p`, `n`, or `%`. Precision is not supported for
`c`, `p`, `n`, `%`, or `q`. Precision on `%q` is rejected because truncating
the quoted representation could remove its closing quote.

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
