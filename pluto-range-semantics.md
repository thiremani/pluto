# Pluto Range Semantics

## Core Principle: Everything is a Function

In Pluto, every operation is a function that handles its range arguments internally by iterating over them.

```
i + 1       →  Add(i, 1)
√x          →  Sqrt(x)
arr[i]      →  Index(arr, i)
[a, b]      →  ArrayLit(a, b)
Process(x)  →  Process(x)
```

When a function receives a **Range** argument, it loops internally:

```
Func(Range r, ...):
    for iter in r:
        result = <body with iter as scalar>
    return result
```

---

## Iterators: Lazy Evaluation

Any expression involving a Range returns an **Iterator** — a lazy sequence of values.

```
i = 0:5

i + 1   → Iterator yielding: 1, 2, 3, 4, 5
√i      → Iterator yielding: 0, 1, √2, √3, 2
arr[i]  → Iterator yielding: arr[0], arr[1], arr[2], arr[3], arr[4]
```

Iterators track their **source ranges**. This is crucial for determining how multiple ranges interact.

---

## Three Evaluation Modes

How an Iterator gets evaluated depends on context:

| Mode | Trigger | Behavior |
|------|---------|----------|
| **Last Value** | Assignment without self-reference | Returns final iteration value |
| **Collect** | Wrap in `[...]` | Accumulates all values into array |
| **Fold** | Assignment with self-reference | Running accumulation |

### Mode 1: Last Value

When assigning to a variable that doesn't appear in the RHS:

```
i = 0:5
x = i + 1
```

Generated code:
```
for iter in 0:5:
    x = iter + 1

// Result: x = 5 (last value)
```

### Mode 2: Collect

Wrapping an expression in `[]` collects all Iterator values:

```
i = 0:5
x = [i + 1]
```

Generated code:
```
acc = []
for iter in 0:5:
    Push(acc, iter + 1)
x = acc

// Result: x = [1, 2, 3, 4, 5]
```

### Mode 3: Fold (Accumulate)

When the LHS variable appears in the RHS, each iteration uses the previous result:

```
i = 0:5
res = 0
res = res + i
```

Generated code:
```
res = 0
for iter in 0:5:
    res = res + iter

// Trace:
// res = 0 + 0 = 0
// res = 0 + 1 = 1
// res = 1 + 2 = 3
// res = 3 + 3 = 6
// res = 6 + 4 = 10

// Result: res = 10
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

// Trace:
// iter=0: 0 * 1 = 0
// iter=1: 1 * 2 = 2
// iter=2: 2 * 3 = 6

// Result: 6
```

---

## Multiple Ranges: Nested Loops (Cartesian Product)

Different ranges produce nested loops:

```
i = 0:2
j = 0:3
x = [i + j]
```

Generated code:
```
acc = []
for iter_i in 0:2:
    for iter_j in 0:3:
        Push(acc, iter_i + iter_j)

// Result: [0, 1, 2, 1, 2, 3]
// (0+0, 0+1, 0+2, 1+0, 1+1, 1+2)
```

---

## ArrayLit: The Collector Function

`[...]` is the **ArrayLit** function — the only function that collects Iterator results:

```
ArrayLit(exprs...):
    acc = []
    for all_ranges_in_exprs:
        Push(acc, eval(exprs))
    return acc
```

### Examples

| Expression | Evaluation | Result |
|------------|-----------|--------|
| `[i]` | collect i | `[0, 1, 2, 3, 4]` |
| `[i + 1]` | collect (i+1) | `[1, 2, 3, 4, 5]` |
| `[i, i + 1]` | collect pairs | `[0, 1, 1, 2, 2, 3, 3, 4, 4, 5]` |
| `[i + j]` | collect cartesian | `[0, 1, 2, 1, 2, 3]` |

---

## Arrays and Element-wise Operations

When a function receives an **Array** (not a Range), it operates element-wise:

```
√(Array a):
    result = []
    for elem in a:
        Push(result, √elem)
    return result
```

### Key Insight: `√[i]` vs `[√i]`

Both produce the same result!

**`√[i]` where i = 0:5:**
```
1. [i] collects → [0, 1, 2, 3, 4]
2. √ receives Array → element-wise
3. Result: [0, 1, √2, √3, 2]
```

**`[√i]` where i = 0:5:**
```
1. √i returns Iterator
2. [...] collects → [0, 1, √2, √3, 2]
```

**Same result!** This is a key consistency property.

---

## Array Ranges: Indexing with Ranges

When indexing an array with a range, you get an Iterator:

```
arr = [10, 20, 30, 40, 50]
i = 0:5

arr[i] → Iterator yielding: 10, 20, 30, 40, 50
```

### Examples

| Expression | Mode | Result |
|------------|------|--------|
| `x = arr[i]` | last | `50` |
| `[arr[i]]` | collect | `[10, 20, 30, 40, 50]` |
| `x = 0; x = x + arr[i]` | fold | `150` |
| `[arr[i] * 2]` | collect | `[20, 40, 60, 80, 100]` |

---

## Function Dispatch by Type

Functions dispatch based on argument types:

| Signature | Behavior |
|-----------|----------|
| `√(Scalar)` | Returns scalar |
| `√(Range)` | Returns Iterator |
| `√(Array)` | Returns Array (element-wise) |
| `+(Array, Scalar)` | Broadcasts scalar, returns Array |
| `+(Array, Array)` | Zips arrays, returns Array |

---

## Complete Examples

### Example 1: Simple Range Expression

```
i = 0:5
x = i * 2
```

**Analysis:**
- `i * 2` → Iterator (source: {i})
- Assignment without self-ref → last value

**Result:** `x = 8`

---

### Example 2: Collecting Results

```
i = 0:5
x = [i * 2]
```

**Analysis:**
- `i * 2` → Iterator
- `[...]` → collect

**Result:** `x = [0, 2, 4, 6, 8]`

---

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

---

### Example 4: Series Calculation

```
i = 0:10
res = 0
res = res + i / (i + 1)
```

**Analysis:**
- `res` on both sides → fold mode
- `i` and `i + 1` share same iterator

**Trace:**
```
res = 0 + 0/1 = 0
res = 0 + 1/2 = 0.5
res = 0.5 + 2/3 = 1.166...
...
```

**Result:** Harmonic-like series sum

---

### Example 5: Nested Ranges with Collection

```
i = 0:3
j = 0:2
matrix = [i * 10 + j]
```

**Analysis:**
- Different ranges → nested loops
- `[...]` → collect

**Generated:**
```
for i in 0:3:
    for j in 0:2:
        Push(acc, i * 10 + j)
```

**Result:** `[0, 1, 10, 11, 20, 21]`

---

### Example 6: Function with Same Range Twice

```
i = 0:4
result = Diff(i + 1, i)

Diff(a, b):
    res = a - b
```

**Analysis:**
- Both args derive from `i` → shared iterator

**Generated:**
```
for iter in 0:4:
    a = iter + 1
    b = iter
    result = a - b
```

**Result:** `result = 1` (constant difference, last value)

---

### Example 7: Mixed Array and Range

```
arr = [10, 20, 30]
i = 0:3
x = [arr[i] + i]
```

**Analysis:**
- `arr[i]` → Iterator (source: {i})
- `+ i` → same source, shared iteration
- `[...]` → collect

**Generated:**
```
for iter in 0:3:
    Push(acc, arr[iter] + iter)
```

**Result:** `[10, 21, 32]`

---

### Example 8: Array Accumulation with Concat

```
i = 0:3
arr = []
arr = arr ⊕ [i]
```

**Analysis:**
- `arr` on both sides → fold mode
- Each iteration concatenates single element

**Trace:**
```
arr = [] ⊕ [0] = [0]
arr = [0] ⊕ [1] = [0, 1]
arr = [0, 1] ⊕ [2] = [0, 1, 2]
```

**Result:** `arr = [0, 1, 2]`

---

## Summary Table

| Expression | Sources | Mode | Result (i=0:5) |
|------------|---------|------|----------------|
| `i + 1` | {i} | Iterator | — |
| `x = i + 1` | {i} | last | `5` |
| `[i + 1]` | {i} | collect | `[1,2,3,4,5]` |
| `x=0; x=x+i` | {i} | fold | `10` |
| `√i` | {i} | Iterator | — |
| `x = √i` | {i} | last | `2` |
| `[√i]` | {i} | collect | `[0,1,√2,√3,2]` |
| `√[i]` | — | element-wise | `[0,1,√2,√3,2]` |
| `[i] + 1` | — | array op | `[1,2,3,4,5]` |
| `[i + j]` | {i,j} | collect | cartesian sum |
| `f(i, i+1)` | {i} | shared iter | last of f |
| `f(i, j)` | {i,j} | nested | last of f |

---

## The Complete Model

```
┌─────────────────────────────────────────────────────────────────┐
│  1. Every operation is a function                               │
│  2. Functions with Range args return Iterators                  │
│  3. Iterators track their source ranges                         │
│  4. Same source range = shared iteration (deduplicated)         │
│  5. Different source ranges = nested loops (cartesian)          │
│  6. [...] collects Iterator → Array                             │
│  7. Assignment without self-ref → last value                    │
│  8. Assignment WITH self-ref → fold/accumulate                  │
│  9. Functions on Array → element-wise operation                 │
└─────────────────────────────────────────────────────────────────┘
```

This model is **fully consistent** — every construct follows the same rules, with `[...]` being the single mechanism for collection.
