# Pluto Memory Model

This document describes Pluto's semantic model and compares it with other major languages.

## The Pluto Model (Summary)

1. **Assignment is Copy:** `a = b` creates a new independent value (Snapshot).
2. **Arrays are Values:** `arr2 = arr1` copies data (COW).
3. **Slices are Views:** `s = arr[i]` (where `i` is a Range) is a reference to `arr`.
4. **Ranges are Loop Syntax:** `x = i + 1` generates a loop, not a lazy type.
5. **Zero-Value Initialization:** Variables in range expressions auto-initialize to zero.
6. **IterName Determines Looping:** Same range variable → zip, different → cartesian.
7. **Function Arguments by Value:** Scalar parameters passed by value, outputs initialized from parameters.
8. **Function Locking:** Input arguments hold read locks, outputs hold write locks (automatic concurrency safety).
9. **Memory Management:** Automatic scope-based deallocation (no GC pauses).

---

## Comparison Table

| Feature | Pluto | Python | Rust | Go | Julia | Zig |
|---------|-------|--------|------|-----|-------|-----|
| **Assignment (`a=b`)** | **Copy** | Reference | Move / Copy | Copy | Reference | Copy |
| **Array Assign** | **Copy** (COW) | Reference | Move | Reference (Slice) | Reference | Copy |
| **Function Args** | **Value** (Scalars) | Reference | Move / Borrow | Copy (Slice Ref) | Reference | Copy |
| **Slicing (`a[:]`)** | **View** | Copy (List) / View (NumPy) | View (Slice) | View (Slice) | Copy (default) / View (`@view`) | View (Slice) |
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

```pluto
a = [1]; b = a; a[0] = 2  # b sees 1 (independent copy)

i = 0:5
x = i + 1                  # Loop executes, x = 5 (last value)
i = 0:10                   # OK! Ranges execute immediately
y = i + 1                  # New loop, y = 10
```

**Difference:** Pluto is safer and more predictable. Ranges execute immediately as loops, not lazy generators.

---

### 2. vs. Rust

**Rust:** "Ownership & Borrowing."

```rust
let a = vec![1]; let b = a;  // a is MOVED (invalidated)
let s = &a[..];              // Borrow checking prevents mutation
```

**Pluto:** "Copy on Write."

```pluto
a = [1]; b = a               # Both valid and independent
s = arr[i]                   # Runtime/Compiler checks ownership scope
```

**Difference:** Pluto is easier to use (no borrow checker fighting) but relies on COW optimization instead of static moves.

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

**Pluto:** "Arrays are Values, Slices are Views with Locking."

```pluto
arr = [1 2 3 4 5]
s = arr[0:2]                 # View (like Go)
s[0] = 99                    # Mutates arr
```

**Difference:** Pluto enforces **Read/Write Locks** on function arguments automatically, whereas Go allows data races (user must use `sync.Mutex`).

---

### 4. vs. Julia

**Julia:** "Pass-by-Sharing."

```julia
b = a                        # Aliases (like Python)
a[1:5]                       # Copy by default
@view a[1:5]                 # View (explicit)
```

**Pluto:** Slicing `arr[i]` creates a **View** by default.

**Difference:** Pluto favors Views for slicing (like Go/Rust), Julia favors Copies unless explicitly requested.

---

### 5. vs. Zig

**Zig:** "Explicit Memory."

```zig
// Arrays are values. Slices []T are pointer+len structs.
// No hidden allocations.
```

**Pluto:** Similar model. `ArrayRange` is essentially a Zig slice.

**Difference:** Pluto manages memory automatically (scope-based), Zig is manual.

---

## Range Semantics Summary

### Statement-Level Loop Generation

Ranges generate loops at statement boundaries. All operations inside work on scalar values:

```pluto
i = 0:5
x = i + 1      # Loop at statement: x = 5 (last scalar value)
y = i * 2      # Loop at statement: y = 8 (last scalar value)
z = (i + 1) / (i + 2)  # Single loop: z = 5/6 (last value)
```

No lazy types - intermediate variables store scalar results.

### IterName Determines Loop Structure

```pluto
i = 0:5
j = 0:5

# Same variable → Zip (single loop)
x = i + 1
y = i + 2
result = x / y   # Single loop over i

# Different variables → Cartesian (nested loops)
x = i + 1
y = j + 1
result = x * y   # Nested loops: i × j
```

### Three Execution Modes

| Mode | Syntax | Behavior |
|------|--------|----------|
| **Last Value** | `x = i + 1` | Loop runs, x = last value |
| **Accumulate** | `x += i` | Loop runs, x accumulates |
| **Collect** | `arr = [i * 2]` | Loop runs, collects to array |

### Conditional Assignment

Guard expressions with conditions:

```pluto
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
2. **Explicit Views (like Rust/Go)** allow high-performance mutation without copying.
3. **Loop Syntax Ranges (Unique)** provide clean iteration without lazy complexity.
4. **IterName-Based Zipping (Unique)** makes user intent explicit — same variable name means related iterations.
5. **Zero-Value Initialization (Unique)** simplifies accumulation patterns.

It combines the **safety of R** with the **performance of Rust/Zig** and the **expressiveness of Julia**.

---

## Function Semantics

Functions in Pluto have clear semantics:

### Parameters and Outputs

```pluto
res = sum(a, b)
    res = a + b
```

- **Parameters**: Input values (passed by value for scalars)
- **Outputs**: Initialized from corresponding parameters at same position
- **No name overlap**: Parameters and outputs must have distinct names

### Call Site

```pluto
res = sum(res, 5)
# - Parameter 'a' receives value of 'res'
# - Parameter 'b' receives 5
# - Output 'res' initialized to 'a' (caller's res value)
# - Body executes: res = a + b
# - Result assigned back to caller's res
```

### Range Parameters

```pluto
res = process(a, i)
    res = a * i
```

**Desugars to:**
```c
for (int64_t i_val = 0; i_val < N; i_val++) {
    res = a * i_val;  // Function receives SCALAR i_val, not range
}
```

**Key:** Functions always receive scalar values, not ranges. The loop is generated at the call site.

### Everything Is Scalar Inside Loops

When a statement contains range variables, the loop is generated at the statement level, and all operations work on scalars:

```pluto
i = 0:5
result = Square(i) + i
```

**Desugars to:**
```c
for (int64_t i_val = 0; i_val < 5; i_val++) {
    result = Square(i_val) + i_val;  // Square called with scalar!
}
```

- ✅ Operators work on scalars
- ✅ Functions receive scalar arguments
- ✅ No special "range-aware" operations
- ✅ Simple, unified model

### Composition Using Functions

For complex expressions with named intermediates, use functions:

```pluto
i = 0:5
res += compute_ratio(i)
    numerator = i + 1
    denominator = i + 2
    res = numerator / denominator
```

This avoids the issue where intermediate assignments execute immediately:

```pluto
i = 0:5
x = i + 1        # Loop NOW: x = 5 (scalar)
y = i + 2        # Loop NOW: y = 7 (scalar)
res += x / y     # No loop! Just res += 5/7
```

Functions keep intermediates within the loop context.

---

## Memory Management

- **Scope-based deallocation**: Memory freed when variables go out of scope
- **No GC pauses**: Deterministic cleanup
- **COW optimization**: Arrays copied only when modified
- **View safety**: Compiler/runtime prevents dangling references