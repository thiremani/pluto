# Pluto Memory Model

This document describes Pluto's semantic model and compares it with other major languages.

## The Pluto Model (Summary)

1. **Materialized Assignment is Copy:** assigning a scalar, array, table, string,
   or struct creates an independent value.
2. **Arrays are Values:** `arr2 = arr1` copies data (COW).
3. **Range Selections are Streams:** `s = arr[i]` keeps the final selected
   value (an element or owned subarray); `s = [arr[i]]` materializes every
   selected value.
4. **Ranges are Loop Syntax:** `x = i` and `x = i + 1` generate loops, not lazy
   values.
5. **Empty-Domain Initialization:** A fresh destination keeps its type's zero
   value; an existing destination remains unchanged.
6. **Driver Identity Determines Looping:** Repeated use of one range shares a
   loop; distinct ranges form a cartesian domain.
7. **Function Arguments by Value:** Scalar parameters are passed by value;
   outputs write into caller destination slots.
8. **Function Locking:** Input arguments hold read locks, outputs hold write locks (automatic concurrency safety).
9. **Memory Management:** Automatic scope-based deallocation (no GC pauses).

---

## Comparison Table

| Feature | Pluto | Python | Rust | Go | Julia | Zig |
|---------|-------|--------|------|-----|-------|-----|
| **Assignment (`a=b`)** | **Copy** | Reference | Move / Copy | Copy | Reference | Copy |
| **Array Assign** | **Copy** (COW) | Reference | Move | Reference (Slice) | Reference | Copy |
| **Function Args** | **Value** (Scalars) | Reference | Move / Borrow | Copy (Slice Ref) | Reference | Copy |
| **Range selection (`a[range]`)** | **Value stream** (final value or explicit collection) | Copy (List) / View (NumPy) | View (Slice) | View (Slice) | Copy (default) / View (`@view`) | View (Slice) |
| **Range Usage** | **Loop Syntax** (Immediate) | Reference (Generator) | Reference (Iterator) | N/A | Reference (Iterator) | N/A |
| **Mutability** | **In-Place Only** | Mutable Objects | Mutable (if `mut`) | Mutable | Mutable | Mutable |
| **Memory Mgmt** | **Auto (Scope)** | Auto (GC) | Auto (Owner) | Auto (GC) | Auto (GC) | Manual |

---

## Detailed Breakdown

### 1. vs. Python

**Python:** "Everything is a Reference."

```python
a = [1]; b = a; a[0] = 2  # b sees 2
x = (i+1 for i in iter)   # Lazy generator
```

**Pluto:** "Values + Loop Syntax."

```python
a = [1]; b = a; a[0] = 2  # b sees 1 (independent copy)

i = 0:5
x = i                      # Loop executes, x = 4 (last yield)
x = i + 1                  # Loop executes, x = 5 (last value)
i = 0:10                   # Bind a new reusable Range domain
y = i + 1                  # Consuming statement runs the loop; y = 10
```

**Difference:** Pluto is safer and more predictable. A range literal binds a
reusable execution domain; a consuming statement runs it as a loop rather than
creating a lazy generator.

---

### 2. vs. Rust

**Rust:** "Ownership & Borrowing."

```rust
let a = vec![1]; let b = a;  // a is MOVED (invalidated)
let s = &a[..];              // Borrow checking prevents mutation
```

**Pluto:** "Values + Explicit Collection."

```python
a = [1]; b = a               # Both valid and independent
i = 0:3
x = arr[i]                   # Final selected element
s = [arr[i]]                 # Independent materialized array
```

**Difference:** Pluto does not expose a borrowed range-selection value, so the
selection cannot outlive its source. Materialized arrays use value semantics
and may use COW internally.

---

### 3. vs. Go

**Go:** "Arrays are Values, Slices are References."

```go
var a [3]int; b := a         // Copy
s := make([]int, 3); t := s  // Reference (same backing array)
arr := [5]int{1,2,3,4,5}
s := arr[0:2]
s[0] = 99                    // Mutates arr via s
```

**Pluto:** "Arrays are Values, Range Selections are Streams."

```python
arr = [1 2 3 4 5]
last = arr[0:2]              # 2
s = [arr[0:2]]               # [1 2], independent of arr
```

**Difference:** Pluto does not expose slice aliasing through range indexing.
Collected selections are ordinary array values.

---

### 4. vs. Julia

**Julia:** "Pass-by-Sharing."

```julia
b = a                        # Aliases (like Python)
a[1:5]                       # Copy by default
@view a[1:5]                 # View (explicit)
```

**Pluto:** A range-valued `arr[i]` is a value stream. At an assignment root it
keeps the final valid element or owned subarray; `[arr[i]]` materializes the
selected values.

**Difference:** Pluto separates final-value selection from explicit collection;
it does not expose a persistent range-selection view.

---

### 5. vs. Zig

**Zig:** "Explicit Memory."

```zig
// Arrays are values. Slices []T are pointer+len structs.
// No hidden allocations.
```

**Pluto:** Range-indexed access is consumed as a value stream and does not
expose a slice value.

**Difference:** Pluto manages memory automatically (scope-based), Zig is manual.

---

## Range Semantics Summary

### Statement-Level Loop Generation

Ranges generate loops at statement boundaries. Operations consume one yielded
value at a time; a rank-N selection can yield an owned subarray:

```python
i = 0:5
x = i          # Loop at statement: x = 4 (last yielded iterator)
x = i + 1      # Loop at statement: x = 5 (last scalar value)
y = i * 2      # Loop at statement: y = 8 (last scalar value)
z = (i + 1) / (i + 2)  # Single loop: z = 5/6 (last value)
```

Bare ranged expressions and range-indexed array accesses execute as loop
drivers rather than becoming lazy values. An assignment root keeps the last
yield; `[]` collects every yield.

### Driver Identity Determines Loop Structure

```python
i = 0:5
j = 0:5

# Repeated use of one driver → one shared loop
ratio = (i + 1) / (i + 2)

# Distinct drivers → cartesian nested loops
product = (i + 1) * (j + 1)
```

### Three Execution Modes

| Mode | Syntax | Behavior |
|------|--------|----------|
| **Last Value** | `x = i` or `x = arr[i]` | Loop runs, x = last yielded value |
| **Accumulate** | `x = x + i` | Loop runs, x accumulates |
| **Collect** | `arr = [i * 2]` | Loop runs, collects to array |

### Statement gates and value-position `&&`

A condition before a statement value is a shared statement gate. It admits or
rejects each point of the statement's outer iteration domain for every RHS
expression and output. If a point is rejected, no sibling RHS evaluation,
collector append, carried update, or output commit runs for that point. Any
RHS-local ranges run inside the admitted points.

An `&&` inside a value has narrower scope. It evaluates its right side lazily
when the left yields and propagates failure only through that value. It does
not gate sibling RHS expressions or the statement's shared iteration domain.
The planned `[i && [matrix[i][j]]]` construction uses this local meaning to bind
a nested range domain. Its collector-placement and fallback rules belong to
[Pluto Range Semantics](Pluto%20Range%20Semantics.md#deferred-nested-range-construction)
and remain deferred until PIR represents those scopes directly.

### Conditional Assignment

Guard expressions with conditions:

```python
# Maximum value
res = arr[i] > res arr[i]

# Conditional update
res = i * i > 10 i
```

Desugars to: `if (condition) res = expression`

---

## Why Pluto's Model is Unique

Pluto sits in a "Sweet Spot" for parallel computing:

1. **Value Semantics (like R/Matlab)** make reasoning about concurrent code easy. "If I have `x`, I own `x`."
2. **Explicit Collection** makes every allocation and materialization boundary visible.
3. **Loop Syntax Ranges (Unique)** provide clean iteration without lazy complexity.
4. **Named Driver Reuse (Unique)** makes user intent explicit — repeated use
   of one range name shares one loop.
5. **Defined Empty Domains (Unique)** give fresh and existing destinations
   predictable behavior.

It combines the **safety of R** with the **performance of Rust/Zig** and the **expressiveness of Julia**.

---

## Function Semantics

Functions in Pluto have clear semantics:

### Parameters and Outputs

```python
res = sum(a, b)
    res = a + b
```

- **Parameters**: Input values (passed by value for scalars)
- **Outputs**: Independently staged result slots. An existing destination
  supplies the initial value, while a fresh destination starts at its type's
  zero value. The real destinations are committed only after every sibling
  right-hand side has been evaluated.
- **No name overlap**: Parameters and outputs must have distinct names

When a caller destination and a function's declared output use different
representations of a compatible value (for example, owned versus static
strings, or an empty array type versus a concrete-rank array), the callee sees
the zero value of its declared representation. A per-output write marker tells
the caller whether to commit that adapted value. If the function does not
write the output, the caller's staged value is preserved. This avoids treating
one ownership or shape representation as if it were another.

### Call Site

```python
res = sum(res, 5)
# - Parameter 'a' receives value of 'res'
# - Parameter 'b' receives 5
# - Staged output 'res' starts with the caller destination's existing value
# - Body executes: res = a + b
# - Result commits back to the caller's res after sibling RHS evaluation
```

### Range Parameters

```python
res = process(a, i)
    res = a * i
```

**Semantically:**
```c
for (int64_t i_val = 0; i_val < N; i_val++) {
    res = a * i_val;
}
```

**Key:** The function body is evaluated once per yielded range value. Whether
the compiler places that loop around the call or in a specialized callee is an
implementation detail.

### Per-Yield Evaluation

When a statement contains range variables, its source-level meaning is
per-yield evaluation:

```python
i = 0:5
result = Square(i) + i
```

**Desugars to:**
```c
for (int64_t i_val = 0; i_val < 5; i_val++) {
    result = Square(i_val) + i_val;
}
```

- ✅ Operators work on the current yielded values
- ✅ Function bodies evaluate once per yielded value
- ✅ No special "range-aware" operations
- ✅ Simple, unified model

### Composition Using Functions

For complex expressions with named intermediates, use functions:

```python
i = 0:5
res = 0
res = res + compute_ratio(i)
    numerator = i + 1
    denominator = i + 2
    res = numerator / denominator
```

This avoids the issue where intermediate assignments execute immediately:

```python
i = 0:5
x = i + 1        # Loop NOW: x = 5 (scalar)
y = i + 2        # Loop NOW: y = 6 (scalar)
res = res + x / y # No loop! Just scalar addition of 5/6
```

Functions keep intermediates within the loop context.

---

## Memory Management

- **Scope-based deallocation**: Memory freed when variables go out of scope
- **No GC pauses**: Deterministic cleanup
- **COW optimization**: Arrays copied only when modified
- **No range-view lifetimes**: Range-indexed access is finalized or collected
