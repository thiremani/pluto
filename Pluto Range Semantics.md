# Pluto Range Semantics

## Core Principle: Lazy Evaluation & Snapshots

In Pluto, range operations are **Lazy** (they create recipes, not values) and use **Snapshot Semantics** (they capture values, not variables).

```
i = 0:5     →  Range{0 5}
x = i + 1   →  OutRange{ Source: i  Op: Add(1) }
```

When a function receives a **Range** argument, it returns a **Recipe** (Iterator) rather than executing immediately.

---

## Iterators: Lazy Evaluation

Any expression involving a Range returns an **Iterator** — a lazy sequence of values.

```
i = 0:5

i + 1   → Iterator yielding: 1 2 3 4 5
√i      → Iterator yielding: 0 1 √2 √3 2
arr[i]  → Iterator yielding: arr[0] arr[1] arr[2] arr[3] arr[4]
```

Iterators track their **source ranges**. This is crucial for determining how multiple ranges interact.

---

## Three Evaluation Modes

How an Iterator gets evaluated depends on context:

| Mode | Trigger | Behavior |
|------|---------|----------|
| **Lazy Recipe** | Assignment to variable | Stores the computation recipe. No loop runs. |
| **Collect** | Wrap in `[...]` | Accumulates all values into a new Array. |
| **Iteration** | Usage in `for` / `print` | Runs the loop and consumes values. |

### Mode 1: Lazy Recipe (Snapshot)

When assigning to a variable, we capture the **Recipe** and the **Snapshot** of the inputs.

```
i = 0:5
x = i + 1
```

**Analysis:**
*   `x` becomes `OutRange`.
*   `x` captures the bounds `0:5` (Value Capture).
*   **Safety:** If `i` is reassigned later, `x` remains unchanged.

### Mode 2: Collect

Wrapping an expression in `[]` forces immediate execution:

```
i = 0:5
x = [i + 1]
```

**Result:** `x` is `[1 2 3 4 5]` (Array).

### Mode 3: Iteration

Using the variable in a context that needs values triggers the loop:

```
x       # Implicit print triggers iteration
```

---

## Range Deduplication: Same Range = Same Iterator

When the same range appears multiple times, all references share a single loop:

```
i = 0:3
x = Process(i, i + 1)
```

Both arguments derive from range `i`, so they share one loop:

```
Process_i(Range r):
    for iter in r:
        a = iter           // first arg
        b = iter + 1       // second arg (same iter!)
        result = a * b
    return result
```

---

## Multiple Ranges: Nested Loops (Cartesian Product)

Different ranges produce nested loops:

```
i = 0:2
j = 0:3
x = [i + j]
```

**Result:** `[0 1 2 1 2 3]` (Cartesian Product)

---

## ArrayLit: The Collector Function

`[...]` is the **ArrayLit** function — the primary mechanism to turn Iterators into Arrays.

| Expression | Evaluation | Result |
|------------|-----------|--------|
| `[i]` | collect i | `[0 1 2 3 4]` |
| `[i + 1]` | collect (i+1) | `[1 2 3 4 5]` |
| `[i i + 1]` | collect pairs | `[0 1 1 2 2 3 3 4 4 5]` |
| `[i + j]` | collect cartesian | `[0 1 2 1 2 3]` |

---

## Arrays and Element-wise Operations

When a function receives an **Array** (not a Range), it operates element-wise and returns a new Array.

### Key Insight: `√[i]` vs `[√i]`

Both produce the same result!

**`√[i]` where i = 0:5:**
1.  `[i]` collects → `[0 1 2 3 4]`
2.  `√` receives Array → element-wise
3.  **Result:** `[0 1 √2 √3 2]`

**`[√i]` where i = 0:5:**
1.  `√i` returns Iterator
2.  `[...]` collects → `[0 1 √2 √3 2]`

---

## Array Ranges: Views vs Copies

This is a critical distinction in Pluto.

| Expression | Type | Semantics | Behavior |
|------------|------|-----------|----------|
| `s = arr[i]` | **ArrayRange** | **View** (Ref) | Reference to `arr`. `s[:] = 1` mutates `arr`. |
| `s = [arr[i]]` | **Array** | **Copy** (Value) | New Array. Independent of `arr`. |

### Example: Mutation via View

```
arr = [10 20 30 40 50]
i = 0:2
s = arr[i]      # View

s[:] = 99       # Mutates arr
arr             # [99 99 30 40 50]
```

---

## Function Dispatch by Type

Functions dispatch based on argument types:

| Signature | Behavior |
|-----------|----------|
| `√(Scalar)` | Returns scalar |
| `√(Range)` | Returns `OutRange` (Lazy Recipe) |
| `√(Array)` | Returns Array (element-wise) |
| `+(Array, Scalar)` | Broadcasts scalar, returns Array |
| `+(Array, Array)` | Zips arrays, returns Array |

---

## Complete Examples

### Example 1: Lazy Range Expression

```
i = 0:5
x = i * 2
```

**Analysis:**
- `i * 2` → `OutRange` (Recipe)
- Assignment → Stores recipe.

**Result:** `x` is a lazy object. Printing `x` yields `0 2 4 6 8`.

### Example 2: Reassignment Safety (Snapshot)

```
i = 0:5
x = i + 1
i = 100:105
x
```

**Analysis:**
- `x` captured the **value** `0:5`.
- Changing `i` does not affect `x`.

**Result:** `1 2 3 4 5`

### Example 3: Running Sum (Fold)

```
i = 1:5
sum = 0
sum = sum + i
```

**Analysis:**
- `sum` appears on both sides → fold mode

**Trace:**
```
sum = 0 + 1 = 1
sum = 1 + 2 = 3
sum = 3 + 3 = 6
sum = 6 + 4 = 10
```

**Result:** `sum = 10`

### Example 4: Loop Fusion

```
i = 0:1000
x = i + 1
y = x * 2
z = [y]
```

**Analysis:**
- `x` and `y` are lazy.
- `z` triggers collection.
- Compiler generates **one single loop**.

**Result:** Fast execution, no intermediate arrays.

---

## Summary Table

| Expression | Sources | Mode | Result (i=0:5) |
|------------|---------|------|----------------|
| `i + 1` | {i} | Lazy | Recipe |
| `x = i + 1` | {i} | Lazy | Stores Recipe |
| `[i + 1]` | {i} | Collect | `[1 2 3 4 5]` |
| `arr[i]` | {i} | View | Reference to `arr` |
| `[arr[i]]` | {i} | Collect | Copy of elements |
| `x=0; x=x+i` | {i} | Fold | `10` |

---

## The Complete Model

```
┌─────────────────────────────────────────────────────────────────┐
│  1. Assignment is Copy (Snapshot)                               │
│  2. Ranges are Lazy Recipes (OutRange)                          │
│  3. Arrays are Values (COW)                                     │
│  4. Slices are Views (ArrayRange) - The only Reference          │
│  5. [...] collects Iterator → Array                             │
│  6. Functions on Array → Element-wise (Eager)                   │
│  7. Functions on Range → Lazy Recipe                            │
└─────────────────────────────────────────────────────────────────┘
```

This model is **fully consistent** — safe defaults (Values) with powerful opt-in Views.
