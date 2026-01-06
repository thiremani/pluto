# Pluto C ABI & Name Mangling Specification

**Version:** 2.0 | **Status:** Draft | **Target:** C11 / C++17

## 1. Overview

All symbols start with `Pt_`, use single `_` as separator (no `__`), and are bijective (lossless).

**Symbol templates:**

| Kind | Template |
|------|----------|
| Function | `Pt_[ModPath]_p_[RelPath]_[Name]_f[N]_[Types...]` |
| Method | `Pt_[ModPath]_p_[RelPath]_[Type]_m_[Name]_f[N]_[Types...]` |
| Operator | `Pt_[ModPath]_p_[RelPath]_[Type]_m_op_[Code]_[Fixity]_[Types...]` |
| Constant | `Pt_[ModPath]_p_[RelPath]_p_[Name]` |

**Path structure:**
* `[ModPath]` = module path from `pt.mod` (e.g., `github.com/user/math`)
* `[RelPath]` = relative subdirectory path (may be empty)
* When `[RelPath]` is empty, omit `_p_[RelPath]` entirely
* When `[RelPath]` is present, format as `_p_[mangled_relpath]` (the `_p_` implies `/` separator)

**Arity rules:**
* N = number of Pluto arguments (methods include self, SRET excluded)
* Return type is NOT mangled; overloads differing only by return type are disallowed

---

## 2. Components

### 2.1 Identifiers

Length-prefixed, ASCII-only. Must satisfy:

* Start with letter or `_`, end with letter/digit
* No `__`, no trailing `_`, single `_` reserved
* No leading zeros in length prefix

| Identifier | Mangled |
|------------|---------|
| `foo` | `3foo` |
| `_private` | `8_private` |
| `foo_bar` | `7foo_bar` |

**Parsing:** Read the length prefix, then consume exactly that many characters as payload. Underscores inside payload are NOT separators (e.g., `7foo_bar` is one identifier, not `foo` + `bar`).

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
| Mixed (digit-prefix) | `n<digits>_<len><alpha>` | `45abc` → `n45_3abc` |

**Rules:**
* No leading zeros: use `n2`, not `n02`
* Mixed segments: leading digits go after `n`, alpha suffix is length-prefixed
* Disambiguation: After `n<digits>`, continuation `_<len><alpha>` only if payload starts with non-digit

**Examples:**
| Path | Mangled |
|------|---------|
| `v1.2.3` | `2v1_d_n2_d_n3` |
| `v1.2.34abc` | `2v1_d_n2_d_n34_3abc` |
| `v2.45-abhijk` | `2v2_d_n45_h_6abhijk` |

### 2.4 Structure Markers

| Marker | Meaning | Entity Location |
|--------|---------|-----------------|
| `fN` | Function, N args | Name before marker |
| `m` | Method separator | Type before, method after |
| `p` | Path/Constant separator | RelPath or Constant name after marker |
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
| Array | `Array_tN_[ColTypes...]` | `Array_t2_I64_F64` |
| ArrayRange | `ArrayRange_tN_[ColTypes...]` | `ArrayRange_t1_I64` |
| Function | `Func_tN_[ParamTypes...]` | `Func_t2_I64_F64` |

**Note:** Function types only mangle parameter types; return types are NOT included (per §2.4 Arity rules).

---

## 4. Examples

### 4.1 Module Root (no relative path)

| Source | Mangled |
|--------|---------|
| `math.Square(I64)` | `Pt_6github_d_3com_s_4user_s_4math_6Square_f1_I64` |
| `math.pi` (constant) | `Pt_6github_d_3com_s_4user_s_4math_p_2pi` |

### 4.2 With Relative Subdirectory

Module: `github.com/user/math`, RelPath: `stats`

| Source | Mangled |
|--------|---------|
| `stats.Mean(I64)` | `Pt_6github_d_3com_s_4user_s_4math_p_5stats_4Mean_f1_I64` |
| `stats.pi` (constant) | `Pt_6github_d_3com_s_4user_s_4math_p_5stats_p_2pi` |

Module: `github.com/user/math`, RelPath: `stats/integral`

| Source | Mangled |
|--------|---------|
| `integral.Quad(F64)` | `Pt_6github_d_3com_s_4user_s_4math_p_5stats_s_8integral_4Quad_f1_F64` |

### 4.3 Other Examples

| Source | Mangled |
|--------|---------|
| `Player.Move(I64, I64)` | `Pt_..._6Player_m_4Move_f3_..._6Player_I64_I64` |
| `Vector + Vector` | `Pt_..._6Vector_m_op_add_in_..._6Vector_..._6Vector` |
| `/v1.2.3` (version) | `..._s_2v1_d_n2_d_n3_...` |

---

## 5. Calling Convention

* **SRET:** All functions return `void`. Return via pointer at arg 0.
* **Pass by reference:** All arguments (including primitives) are pointers.
* **Methods:** `self` is arg 1 (after SRET).

```c
void Pt_..._6Person_m_5Clone_f2_..._6Person_I64(
    Person* ret,    // 0: SRET
    Person* self,   // 1: self
    I64* count      // 2: argument
);
```

---

## 6. Grammar

```ebnf
FunctionSym := 'Pt' FullPath '_' Ident '_f' Arity Types
MethodSym   := 'Pt' FullPath '_' Ident '_m_' Ident '_f' Arity Types
OperatorSym := 'Pt' FullPath '_' Ident '_m_op_' Opcode '_' Fixity Types
ConstantSym := 'Pt' FullPath '_p_' Ident

FullPath   := ModPath ('_p_' RelPath)?
ModPath    := '_' Path
RelPath    := Path
Path       := Ident ('_' Separator '_' PathTail)?
PathTail   := (Ident | NumericSeg) ('_' Separator '_' PathTail)?
Ident      := [1-9][0-9]* [A-Za-z_][A-Za-z0-9_]*   (* length + payload *)
Separator  := [dsh]+
NumericSeg := 'n' Num
Num        := '0' | [1-9][0-9]*                    (* no leading zeros *)

Arity  := Num

Opcode := 'add' | 'sub' | 'neg' | 'mul' | 'div' | 'mod'
        | 'eq' | 'neq' | 'lt' | 'gt' | 'le' | 'ge'
Fixity := 'in' | 'pre' | 'suf' | 'cir' Num

Types      := ('_' Type)*
Type       := Primitive | Qualified | Generic
Primitive  := 'I1' | 'I8' | ... | 'Str'
Qualified  := Ident ('_' Separator '_' (Ident | NumericSeg))* '_' Ident
Generic    := (Qualified | Ident) '_t' [0-9]+ Types
```

**Notes:**

* Identifier payload must satisfy §2.1 rules
* Path grammar enforces: starts with Ident, alternates Separator/(Ident|NumericSeg)
* Empty path only valid for builtins; user symbols always have a package path
* `Num` rule enforces no leading zeros for all numeric suffixes (fN, tN, nX, cirN)
* Operators: Fixity implies arity (in=2, pre/suf=1, cirN=N); Types listed left-to-right
* Generics (`_tN`) only in type arguments, not as top-level linkable symbols
