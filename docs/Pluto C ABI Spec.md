# Pluto C ABI & Name Mangling Specification

**Version:** 2.0 | **Status:** Draft | **Target:** C11 / C++17

## 1. Overview

All symbols start with `Pt_`, use single `_` as separator (no `__`), and are bijective (lossless).

**Symbol templates:**

| Kind | Template |
|------|----------|
| Function (no relpath) | `Pt_[ModPath]_p_[Name]_f[N]_[Types...]` |
| Function (with relpath) | `Pt_[ModPath]_p_[RelPath]_r_[Name]_f[N]_[Types...]` |
| Method (no relpath) | `Pt_[ModPath]_p_[Type]_m_[Name]_f[N]_[Types...]` |
| Method (with relpath) | `Pt_[ModPath]_p_[RelPath]_r_[Type]_m_[Name]_f[N]_[Types...]` |
| Operator (no relpath) | `Pt_[ModPath]_p_[Type]_m_op_[Code]_[Fixity]_[Types...]` |
| Operator (with relpath) | `Pt_[ModPath]_p_[RelPath]_r_[Type]_m_op_[Code]_[Fixity]_[Types...]` |
| Constant (no relpath) | `Pt_[ModPath]_p_[Name]` |
| Constant (with relpath) | `Pt_[ModPath]_p_[RelPath]_r_[Name]` |
| Script (no relpath) | `Pt_[ModPath]_p_[ScriptName]_e` |
| Script (with relpath) | `Pt_[ModPath]_p_[RelPath]_r_[ScriptName]_e` |

**Path structure:**
* `[ModPath]` = module path from `pt.mod` (e.g., `github.com/user/math`)
* `[RelPath]` = relative subdirectory path (may be empty)
* `[ScriptName]` = script basename without `.spt`, encoded as the final path component
* **All symbols:** Always include `_p_` after module path (marks end of module path)
* **Symbols with relpath:** Always include `_r_` after relpath (marks end of relpath; `d` is reserved for `.` separator)
* **Script roots:** Always include `_e` after the script name

**Arity rules:**
* N = number of Pluto arguments (methods include self, SRET excluded)
* Return type is NOT mangled; overloads differing only by return type are disallowed

### 1.1 Path Validation

Module paths, module-relative directory paths, and script basenames must satisfy
these rules before mangling. A script basename is one path component and cannot
contain `/`; its `.spt` suffix is not part of the name.

| Rule | Description |
|------|-------------|
| Module spelling | Module paths use lowercase ASCII |
| Source-path spelling | Relative directories and script names also allow uppercase ASCII and valid Unicode |
| Segment separator | Only `/` separates segments (maps to directories) |
| Valid ASCII characters | Letters, digits, `_`, `.`, `-` |
| No boundary underscores | Double underscores and `_` immediately before `.`, `-`, `/`, or the end are forbidden |
| No trailing `.` | Segments cannot end with dot (Windows compatibility) |
| No terminal `-` | The complete path or script name cannot end with `-`; the encoder would omit it |
| No empty segments | No `//` or leading/trailing `/` |
| No Windows reserved | Base name before first `.` cannot be `CON`, `PRN`, `AUX`, `NUL`, `COM1-9`, `LPT1-9` |

**Note on Windows reserved names:** Windows treats `con.txt` as `CON`, so we check the base name before the first dot. This means `con.txt.zip` is invalid, but `foo.con` and `.con` are valid (base is `foo` and empty respectively).

**Valid module examples:** `math`, `github.com/user/pkg`, `my-lib/v2`, `foo_bar`, `foo-con`, `v1.2.3`, `.hidden`, `.con`, `-flag`

**Valid relative-path and script examples:** `Reports/日本`, `Daily`, `1.2-report`, `.hidden`

**Invalid examples:** module `MyPkg` (uppercase), `my__pkg` (double underscore), `foo_.bar` (boundary underscore), `pkg_` (trailing underscore), `foo.` (trailing dot), `foo-` (terminal hyphen), `foo//bar` (empty segment), `con` (reserved), `con.txt` (base=con)

**Note on segment separator vs mangling:** Validation uses only `/` to split segments, but mangling encodes all of `.`, `/`, `-` as separator codes for C-safe symbol names. These are separate concerns: validation determines what paths are legal, mangling determines how they're encoded.

---

## 2. Components

### 2.1 Identifiers

Identifiers support both ASCII and Unicode characters. The mangled form uses length-prefixed ASCII segments with inline Unicode escapes.

**ASCII rules:**

* Start with letter or `_`, end with letter/digit
* No `__`, no trailing `_`, single `_` reserved
* No leading zeros in length prefix

Pluto reserves no otherwise valid source identifiers. Compiler-generated scope
bindings begin with `$`, which is outside the source identifier grammar; those
names are LLVM-local and never enter the external ABI.

**Structure:** Encoded names alternate ASCII/digit runs and Unicode runs. The exact grammar is defined in §6.

* ASCII segment: `<length><chars>` where length is char count
* Unicode segment: `u<count>_` followed by `count` consecutive 6-digit hex values (uppercase)
* Identifiers can start with either ASCII or Unicode segment directly
* After Unicode, if ASCII starts with digit: use `n<digits>` followed by:
  * Nothing if end of identifier
  * `_` if more Unicode follows
  * `_<len><ASCII>` if ASCII follows

| Identifier | Mangled |
|------------|---------|
| `foo` | `3foo` |
| `_private` | `8_private` |
| `foo_bar` | `7foo_bar` |
| `π` | `u1_0003C0` |
| `foo_π` | `4foo_u1_0003C0` |
| `aπb` | `1au1_0003C01b` |
| `⊕` | `u1_002295` |
| `😀` | `u1_01F600` |
| `foo😀bar` | `3foou1_01F6003bar` |
| `α2` | `u1_0003B1n2` |
| `α2y` | `u1_0003B1n2_1y` |
| `x1α2βz3` | `2x1u1_0003B1n2_u1_0003B22z3` |

**Parsing (ASCII-only):** Read the length prefix, then consume exactly that many characters as payload. Underscores inside payload are NOT separators (e.g., `7foo_bar` is one identifier, not `foo` + `bar`).

**Parsing (with Unicode):**

1. If starts with `u` + digit: parse Unicode segment, go to step 2
   Otherwise: read length N, read N ASCII chars
2. If next is `u` followed by digit and `_`, parse Unicode segment:
   * Read count M after `u`
   * Skip the `_`, then read M×6 consecutive hex digits (M codepoints)
3. If next is digit `[0-9]`, repeat from step 1 (more ASCII)
3a. If next is `n` followed by digit, parse numeric segment:
   * Read digits after `n`
   * If followed by `_` + digit: parse ASCII segment (`<len><alpha>`)
   * If followed by `_` + `u` + digit: consume `_`, continue to step 2
   * Otherwise: numeric segment complete
4. Otherwise, identifier is complete

**Note:** The `n` prefix for digits after Unicode avoids ambiguity: without it, `α2` would mangle to `u1_0003B112` where `12` is ambiguous (length=1,char='2' or length=12). With `n` prefix, it becomes `u1_0003B1n2` which is unambiguous.

### 2.2 Separators

| Code | Meaning |
|------|---------|
| `d` | `.` |
| `s` | `/` |
| `h` | `-` |

Consecutive separators MUST combine into single component:

| Path | Mangled | NOT |
|------|---------|-----|
| `..` | `dd` | `d_d` ❌ |
| `...` | `ddd` | `d_d_d` ❌ |
| `.-` | `dh` | `d_h` ❌ |
| `/-` | `sh` | `s_h` ❌ |

### 2.3 Numeric Segments

Version numbers and numeric path segments use `n` prefix.

| Type | Format | Example |
|------|--------|---------|
| Pure numeric | `n<digits>` | `123` → `n123` |
| Mixed ASCII | `n<digits>_<len><ascii>` | `45abc` → `n45_3abc` |
| Mixed Unicode | `n<digits>_` followed by `UnicodeSeg` | `2日本` → `n2_u2_0065E500672C` |

**Rules:**
* Digits are preserved, including leading zeros: `02` → `n02`
* Mixed segments: leading digits go after `n`; an ASCII continuation is length-prefixed, while Unicode starts with `UnicodeSeg`
* Disambiguation: After `n<digits>`, `_` introduces either the length-prefixed ASCII continuation or the following Unicode run

**Examples:**
| Path | Mangled |
|------|---------|
| `01` | `n01` |
| `2日本` | `n2_u2_0065E500672C` |
| `v1.2.3` | `2v1_d_n2_d_n3` |
| `v1.2.34abc` | `2v1_d_n2_d_n34_3abc` |
| `v2.45-abhijk` | `2v2_d_n45_h_6abhijk` |

### 2.4 Structure Markers

| Marker | Meaning | Entity Location |
|--------|---------|-----------------|
| `fN` | Function, N args | Name before marker |
| `e` | Script entry/root | Script name before marker |
| `m` | Method separator | Type before, method after |
| `p` | End of module path | RelPath or symbol name follows |
| `r` | End of relative path | Symbol or script name follows |
| `tN` | Generic, N type params | Type before marker |
| `op` | Operator prefix | Opcode follows |
| `in` | Infix (2 operands) | `a + b` |
| `pre` | Prefix (1 operand) | `-x` |
| `suf` | Suffix (1 operand) | `x!` |
| `cirN` | Circumfix (N operands) | `\|x\|`, `\|a,b\|` |

### 2.5 Operator Codes (fixed set, not length-prefixed)

```
add sub neg mul div mod eq neq lt gt le ge
```

**Notes:**

* `in`/`pre`/`suf` never include a number; only `cirN` has arity
* Indexing (`a[i]`) is built-in, not overloadable
* Callable objects (`f()`) are not supported

---

## 3. Types

### 3.1 Primitives

`I1` `I8` `I16` `I32` `I64` `U8` `U16` `U32` `U64` `F32` `F64` `Str`

### 3.2 Qualified Types

Full package path + type name. Type is identifier NOT followed by separator.

```
github.com/user/math.Vector → 6github_d_3com_s_4user_s_4math_6Vector
```

### 3.3 Generics

`[Type]_t[N]_[Args...]` — e.g., `Map<Str, I64>` → `3Map_t2_Str_I64`

### 3.4 Compound Types

Built-in compound types use the `_tN_` pattern:

| Type | Pattern | Example |
|------|---------|---------|
| Pointer | `Ptr_t1_[Elem]` | `Ptr_t1_I64` |
| Range | `Range_t1_[Iter]` | `Range_t1_I64` |
| Array | `Array_t1_[Elem]` | `Array_t1_I64` |
| Rank-N Array | repeated `Array_t1_` | `Array_t1_Array_t1_F64` |
| Internal ArrayRange | `ArrayRange_t2_[Array]_[Range]` | `ArrayRange_t2_Array_t1_I64_Range_t1_I64` |
| Table | `Table_t2N_[EncodedName Elem]...` | `Table_t4_5nName_StrH_6nScore_I64` |
| Function | `Func_tN_[ParamTypes...]` | `Func_t2_I64_F64` |

**Note:** Function types only mangle parameter types; return types are NOT included (per §2.4 Arity rules).

Table schemas include column names because named-column access participates in
type identity. Each column contributes two components. A named column encodes
`n` plus its source name; an unnamed column encodes `u`.

Rank-1 arrays lower as the existing opaque runtime-vector pointer. Higher-rank
arrays lower as `{ data, dim0, ..., dim(N-1) }`, where `data` is one flat row-major
runtime vector. Dimension lengths are runtime values and do not participate in
type mangling.

An `ArrayRange` is an internal, call-scoped descriptor used when an immediate
bare `array[range]` argument is specialized for callee-side iteration. Its two
type arguments are the full array type and the full range type:
`ArrayRange_t2_<full Array type>_<full Range type>`. Both are part of the
mangle, so array rank, array element type, and range iterator type cannot
collide. For example, a rank-2 I64 array indexed by an I64 range mangles as:

```
ArrayRange_t2_Array_t1_Array_t1_I64_Range_t1_I64
```

`ArrayRange` is not a source-level type. It cannot be bound to a variable,
stored, returned, printed, or otherwise escape the call that created it. The
function body observes one yielded element or owned subarray per iteration,
not the descriptor itself.

At the native boundary the descriptor is passed indirectly as a pointer to
`{ <full lowered Array>, <full lowered Range> }`. For example, the following
illustrative C declarations show the rank-1 and rank-2 I64 layouts (the
emitted LLVM structs are structural rather than named with these C names):

```c
typedef struct {
    int64_t start;
    int64_t stop;
    int64_t step;
} PtRangeI64;

typedef struct {
    PtArrayI64 *array;
    PtRangeI64 range;
} PtArrayRangeI64Rank1;

typedef struct {
    PtArrayI64 *data;
    int64_t dim0;
    int64_t dim1;
} PtArrayI64Rank2;

typedef struct {
    PtArrayI64Rank2 array;
    PtRangeI64 range;
} PtArrayRangeI64Rank2;
```

The descriptor occupies the ordinary source-parameter position. An indirect
result carrier, when present, comes first; all source parameters follow in
source order; hidden alias selectors follow them; and a hidden direct-return
seed is last.

---

## 4. Examples

### 4.1 Module Root (no relative path)

| Source | Mangled |
|--------|---------|
| `math.Square(I64)` | `Pt_6github_d_3com_s_4user_s_4math_p_6Square_f1_I64` |
| `math.pi` (constant) | `Pt_6github_d_3com_s_4user_s_4math_p_2pi` |
| `report.spt` (script) | `Pt_6github_d_3com_s_4user_s_4math_p_6report_e` |

### 4.2 With Relative Subdirectory

Module: `github.com/user/math`, RelPath: `stats`

| Source | Mangled |
|--------|---------|
| `stats.Mean(I64)` | `Pt_6github_d_3com_s_4user_s_4math_p_5stats_r_4Mean_f1_I64` |
| `stats.pi` (constant) | `Pt_6github_d_3com_s_4user_s_4math_p_5stats_r_2pi` |
| `stats/1.2-report.spt` (script) | `Pt_6github_d_3com_s_4user_s_4math_p_5stats_r_n1_d_n2_h_6report_e` |

Module: `github.com/user/math`, RelPath: `stats/integral`

| Source | Mangled |
|--------|---------|
| `integral.Quad(F64)` | `Pt_6github_d_3com_s_4user_s_4math_p_5stats_s_8integral_r_4Quad_f1_F64` |

### 4.3 Other Examples

| Source | Mangled |
|--------|---------|
| `Player.Move(I64, I64)` | `Pt_..._6Player_m_4Move_f3_..._6Player_I64_I64` |
| `Vector + Vector` | `Pt_..._6Vector_m_op_add_in_..._6Vector_..._6Vector` |
| `/v1.2.3` (version) | `..._s_2v1_d_n2_d_n3_...` |
| `1.spt` in module `example.com/math/v1.2.3` | `Pt_7example_d_3com_s_4math_s_2v1_d_n2_d_n3_p_n1_e` |

---

## 5. Calling Convention

The native calling convention is selected from the solved parameter and output
types:

- `I64` and `F64` parameters are passed directly. Ranges, internal
  `ArrayRange` descriptors, and other values are passed indirectly.
- A function with exactly one `I64` or `F64` output returns that scalar
  directly and receives one hidden seed value. The seed preserves the caller's
  staged value when the callee does not write its output, including a failed
  conditional assignment or an empty `Range`/internal `ArrayRange`.
- All other output lists use an indirect `void` return. Argument zero points
  to a carrier whose first `N` fields are output pointers and whose next `N`
  fields are pointers to `i1` write markers.
- The caller initializes every write marker to false. A callee sets the marker
  only when that output is actually written. This lets an empty range or
  skipped conditional preserve an existing caller destination.
- Output expressions are staged independently at the call site, so one output
  cannot mutate a destination before a sibling right-hand side reads its
  statement-start value.
- When a compatible caller destination has a different ownership or shape
  representation from the declared output, the ABI slot starts at the declared
  type's zero value. The caller commits it only if its write marker is set.

The direct-return seed is always present, even when the function body
unconditionally overwrites its output. Schematically, with mangled names
abbreviated:

```c
int64_t Pt_Square_I64(int64_t x, int64_t seed);
int64_t Pt_ConditionalSquare_I64(int64_t x, int64_t seed);
int64_t Pt_Acc_I64_Range(
    int64_t a,
    const PtRangeI64 *range,
    int32_t a_output_alias,
    int64_t seed
);
```

A C caller passes the destination's current value to request Pluto's keep-old
semantics, or the output type's zero value for a fresh destination. The seed is
`noundef` and must still be supplied to a function such as `Square` that does
not inspect it. Whether the body must write or may skip a write never changes
the public signature or mangled name. In particular, adding a conditional
assignment—or changing a reachable output-producing callee from must-write to
may-write—cannot alter the prototype of an existing symbol. A future seedless
internal fast path must therefore use a distinct private symbol behind this
stable boundary.

Conceptually, a two-output indirect call uses:

```c
struct Results {
    T0 *out0;
    T1 *out1;
    bool *wrote0;
    bool *wrote1;
};

void Pt_example(Results *results, I64 direct_arg, Other *indirect_arg);
```

Range-bearing variants may also receive hidden alias selectors for direct
scalar parameters that refer to an output destination. These preserve
loop-carried accumulation without changing the source signature or mangled
specialization identity. They do change the native C signature.
For compatible indirect parameters, the caller instead passes the matching
staged output pointer itself, so the callee observes the same loop-carried
value without another hidden parameter.
Hidden ABI fields and parameters are not part of name mangling.

An eligible immediate bare `array[range]` call argument may therefore select
an `ArrayRange` specialization and run its loop inside the callee. This
placement does not change source semantics. Calls that reuse one driver in
multiple argument positions, such as `F(i, i)`, must preserve one shared
iteration domain; the current lowering handles that case caller-side. Distinct
drivers, such as `F(i, j)`, retain their cartesian domain.

### 5.1 Reserved Collector Specialization Suffix

The following suffix is reserved for a possible future specialization that
writes each yielded result directly into one or more collectors:

```
_cN_<item types...>
```

`N` is the number of item types that follow. Examples are `_c1_I64`,
`_c1_Array_t1_I64`, and `_c2_I64_F64`. The suffix would follow the ordinary
function specialization mangle and includes the full type of every collected
item.

This suffix is reserved only. The current compiler does not emit collector
specializations, and no collector ABI is implemented. If that ABI is added, a
collector for an item type `T` will be passed as `PtArrayT *` in the final
native parameter position; this statement reserves the position but does not
make it part of the current calling convention.

---

## 6. Grammar

```ebnf
FunctionSym := 'Pt' ModPath '_p_' Ident '_f' Arity Types
            |  'Pt' ModPath '_p_' RelPath '_r_' Ident '_f' Arity Types
MethodSym   := 'Pt' ModPath '_p_' Ident '_m_' Ident '_f' Arity Types
            |  'Pt' ModPath '_p_' RelPath '_r_' Ident '_m_' Ident '_f' Arity Types
OperatorSym := 'Pt' ModPath '_p_' Ident '_m_op_' Opcode '_' Fixity Types
            |  'Pt' ModPath '_p_' RelPath '_r_' Ident '_m_op_' Opcode '_' Fixity Types
ConstantSym := 'Pt' ModPath '_p_' Ident
            |  'Pt' ModPath '_p_' RelPath '_r_' Ident
ScriptSym   := 'Pt' ModPath '_p_' ScriptName '_e'
            |  'Pt' ModPath '_p_' RelPath '_r_' ScriptName '_e'

ModPath    := '_' Path
RelPath    := Path
ScriptName := PathComponent
Path       := (ComponentSep '_')? PathPart ('_' Separator '_' PathPart)*
PathComponent := (ComponentSep '_')? PathPart ('_' ComponentSep '_' PathPart)*
PathPart   := Ident
Separator  := [dsh]+
ComponentSep := [dh]+
Digits     := [0-9]+
Num        := '0' | [1-9][0-9]*                    (* no leading zeros *)
PosNum     := [1-9][0-9]*

(* Encoded names alternate text and Unicode runs - see §2.1 *)
Ident         := TextSeg | BeforeUnicode UnicodeTail | UnicodeTail
UnicodeTail   := UnicodeSeg (BeforeUnicode UnicodeSeg)* TextSeg?
TextSeg       := ASCIISeg | DigitSeg
BeforeUnicode := ASCIISeg | DigitASCII | DigitOnly '_'
DigitSeg      := DigitOnly | DigitASCII
DigitOnly     := 'n' Digits
DigitASCII    := DigitOnly '_' ASCIISeg
ASCIISeg   := PosNum ASCIIChars                    (* length-prefixed ASCII *)
ASCIIChars := [A-Za-z0-9_]*                        (* 0 to N ASCII chars *)
UnicodeSeg := 'u' [1-9][0-9]* '_' HexCode+         (* u<count>_<hex6><hex6>... *)
HexCode    := [0-9A-F]{6}                          (* 6 uppercase hex digits *)

Arity  := Num

Opcode := 'add' | 'sub' | 'neg' | 'mul' | 'div' | 'mod'
        | 'eq' | 'neq' | 'lt' | 'gt' | 'le' | 'ge'
Fixity := 'in' | 'pre' | 'suf' | 'cir' Num

Types      := ('_' Type)*
Type       := Primitive | Qualified | Generic
Primitive  := 'I1' | 'I8' | ... | 'Str'
Qualified  := Ident ('_' Separator '_' Ident)* '_' Ident
Generic    := (Qualified | Ident) '_t' Num Types
```

**Notes:**

* Encoded names alternate text and Unicode runs. A pure digit run adds `_` only when Unicode follows; an ASCII continuation is length-prefixed
* Paths may start with a dot/hyphen separator or numeric segment, but cannot end with a separator
* Empty path only valid for builtins; user symbols always have a package path
* `Demangle` displays functions and constants as `path.entity` and scripts as the extensionless path `path/name`. The display intentionally omits the module/relative-path boundary and is not a unique key; use `DemangleParsed` when structured `ModPath`, `RelPath`, or `Kind` data is required
* Numeric path segments preserve source digits; `Num` keeps arities, counts, and length prefixes canonical
* Operators: Fixity implies arity (in=2, pre/suf=1, cirN=N); Types listed left-to-right
* Generics (`_tN`) only in type arguments, not as top-level linkable symbols
* All symbols always have `_p_` after ModPath; symbols with relpath use `_r_`, and script roots end with `_e`
