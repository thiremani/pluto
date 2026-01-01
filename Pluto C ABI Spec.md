# Pluto C ABI & Name Mangling Specification

**Version:** 2.0 | **Status:** Draft | **Target:** C11 / C++17

## 1. Overview

All symbols start with `Pt_`, use single `_` as separator (no `__`), and are bijective (lossless).

**Symbol templates:**

| Kind | Template |
|------|----------|
| Function | `Pt_[Path]_[Name]_f[N]_[Types...]` |
| Method | `Pt_[Path]_[Type]_m_[Name]_f[N]_[Types...]` |
| Operator | `Pt_[Path]_[Type]_m_op_[Code]_[Fixity]_[Types...]` |
| Constant | `Pt_[Path]_p_[Name]` |

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

Version numbers and numeric path segments use `n` prefix: `2` → `n2`

No leading zeros: use `n2`, not `n02`.

### 2.4 Structure Markers

| Marker | Meaning | Entity Location |
|--------|---------|-----------------|
| `fN` | Function, N args | Name before marker |
| `m` | Method separator | Type before, method after |
| `p` | Constant | Name after marker |
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

---

## 4. Examples

| Source | Mangled |
|--------|---------|
| `math.Square(I64)` | `Pt_6github_d_3com_s_4user_s_4math_6Square_f1_I64` |
| `Player.Move(I64, I64)` | `Pt_..._6Player_m_4Move_f3_..._6Player_I64_I64` |
| `Vector + Vector` | `Pt_..._6Vector_m_op_add_in_..._6Vector_..._6Vector` |
| `math.pi` | `Pt_6github_d_3com_s_4user_s_4math_p_2pi` |
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
FunctionSym := 'Pt' Path '_' Ident '_f' Arity Types
MethodSym   := 'Pt' Path '_' Ident '_m_' Ident '_f' Arity Types
OperatorSym := 'Pt' Path '_' Ident '_m_op_' Opcode '_' Fixity Types
ConstantSym := 'Pt' Path '_p_' Ident

Path       := ('_' Ident ('_' Separator '_' PathTail)?)?
PathTail   := (Ident | NumericSeg) ('_' Separator '_' PathTail)?
Ident      := [1-9][0-9]* [A-Za-z_][A-Za-z0-9_]*   (* length + payload *)
Separator  := [dsh]+
NumericSeg := 'n' [0-9]+

Opcode := 'add' | 'sub' | 'neg' | 'mul' | 'div' | 'mod'
        | 'eq' | 'neq' | 'lt' | 'gt' | 'le' | 'ge'
Fixity := 'in' | 'pre' | 'suf' | 'cir' [0-9]+

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
* Operators: Fixity implies arity (in=2, pre/suf=1, cirN=N); Types listed left-to-right
* Generics (`_tN`) only in type arguments, not as top-level linkable symbols
