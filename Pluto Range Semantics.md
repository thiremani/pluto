# Pluto Range Semantics

## Core Principle: Ranges as Loop Syntax

In Pluto, ranges generate loops at **statement boundaries**. When a statement contains a range variable, the compiler generates a loop, and all operations inside (operators, function calls) work on scalar iteration values.

```pluto
i = 0:5          # Range literal
x = i + 1        # Loop at statement: for i, x = i+1 → x = 5 (last value)
x += i           # Loop at statement: for i, x += i → x = 0+1+2+3+4 = 10
arr = [i * 2]    # Loop at statement: for i, collect i*2 → [0,2,4,6,8]
```

**Key Insight:** Ranges generate loops at statement boundaries. Everything inside the loop operates on scalar values per iteration.

---

## Range Basics

A range `start:stop` or `start:stop:step` defines iteration bounds:

```pluto
i = 0:5          # Iterates: 0, 1, 2, 3, 4
j = 0:10:2       # Iterates: 0, 2, 4, 6, 8
k = 5:0:-1       # Iterates: 5, 4, 3, 2, 1
```

Ranges are just values that describe iteration - they have no operations until used in expressions.

---

## Loop Generation Modes

When a range appears in an expression, the behavior depends on the operator:

### Mode 1: Assignment (Last Value)

Simple assignment generates a loop and keeps the last value:

```pluto
i = 0:5
x = i + 1
```

**Generated code:**
```c
x = 0;  // Zero-value initialization
for (int64_t i_val = 0; i_val < 5; i_val++) {
    x = i_val + 1;
}
// x = 5 (last value)
```

### Mode 2: Compound Assignment (Accumulate)

Compound operators accumulate across iterations:

```pluto
x = 0
x += i
```

**Generated code:**
```c
for (int64_t i_val = 0; i_val < 5; i_val++) {
    x = x + i_val;
}
// x = 0+1+2+3+4 = 10
```

### Mode 3: Array Literal (Collect)

Wrapping in `[...]` collects values into an array:

```pluto
arr = [i * 2]
```

**Generated code:**
```c
arr = allocate_array(5);
size_t idx = 0;
for (int64_t i_val = 0; i_val < 5; i_val++) {
    arr[idx++] = i_val * 2;
}
// arr = [0, 2, 4, 6, 8]
```

---

## Conditional Assignment

You can add a conditional guard to selectively update values:

```pluto
res = condition expression
```

This desugars to:
```c
for each iteration:
    if (condition):
        res = expression
```

### Example: Maximum Value

```pluto
arr = [3 1 4 1 5]
i = 0:5
res = arr[i] > res arr[i]
```

**Generated code:**
```c
res = 0;  // Zero-value init
for (int64_t i_val = 0; i_val < 5; i_val++) {
    if (arr[i_val] > res) {
        res = arr[i_val];
    }
}
// res = 5 (maximum)
```

### Example: Conditional Last Value

```pluto
res = i * i > 10 i
```

**Generated code:**
```c
res = 0;
for (int64_t i_val = 0; i_val < 10; i_val++) {
    if (i_val * i_val > 10) {
        res = i_val;
    }
}
// res = last i where i² > 10
```

---

## Multiple Ranges: Zipping vs Cartesian

When multiple ranges appear in the same expression, loop structure depends on **variable names**:

### Same Variable → Zip (Single Loop)

```pluto
i = 0:3
x = i + 1
y = i + 2
result = x / y
```

Both `x` and `y` reference range `i`, so they **zip**:

**Generated code:**
```c
// Single loop iterating i
for (int64_t i_val = 0; i_val < 3; i_val++) {
    result = (i_val + 1) / (i_val + 2);
}
```

### Different Variables → Cartesian (Nested Loops)

```pluto
i = 0:2
j = 0:2
result = i + j
```

**Generated code:**
```c
for (int64_t i_val = 0; i_val < 2; i_val++) {
    for (int64_t j_val = 0; j_val < 2; j_val++) {
        result = i_val + j_val;
    }
}
// Produces: 0+0, 0+1, 1+0, 1+1 (4 iterations)
```

**The Rule:** Same range variable name = related iterations (zip), different names = independent iterations (Cartesian).

---

## Array Indexing with Ranges

Using a range to index an array creates a view or loop:

```pluto
arr = [10 20 30 40 50]
i = 0:3
slice = arr[i]         # View (ArrayRange type)
values = [arr[i]]      # Collect to new array [10, 20, 30]
```

**View semantics:**
```pluto
arr[i][0] = 99         # Mutates arr[0]
```

**Loop semantics:**
```pluto
res += arr[i]          # Sum: res = 10+20+30 = 60
```

---

## Zero-Value Initialization

Variables used in range expressions auto-initialize to zero value if undefined:

```pluto
sum += i          # sum starts at 0
max = arr[i] > max arr[i]  # max starts at 0
```

This allows simple accumulation without explicit initialization.

---

## Functions with Range Parameters

When a function receives a range parameter, it generates a loop at the call site:

```pluto
res = process(a, i)
    res = a * i
```

**Desugars to:**
```c
for (int64_t i_val = 0; i_val < N; i_val++) {
    res = a * i_val;  // Function body executes with scalar i_val
}
```

**Key:** The function receives **scalar** values per iteration, not a range type.

---

## Intermediate Values: They Become Scalars!

When you assign a range expression to a variable, the loop executes immediately and the variable stores the **last value** (scalar):

```pluto
i = 0:5
x = i + 1        # Loop executes NOW: x = 5 (scalar, not lazy)
y = i + 2        # Loop executes NOW: y = 7 (scalar, not lazy)
res = x / y      # No loop! Just: res = 5 / 7
```

**This is different from:**

```pluto
i = 0:5
res = (i + 1) / (i + 2)  # Loop at statement level
```

**Generated code:**
```c
res = 0;
for (int64_t i_val = 0; i_val < 5; i_val++) {
    res = (i_val + 1) / (i_val + 2);  // Compute per iteration
}
// res = (4 + 1) / (4 + 2) = 5/6 (last value)
```

---

## Composition Using Functions

For complex expressions, use functions instead of intermediate variables:

**Instead of trying to compose intermediates:**
```pluto
i = 0:5
x = i + 1        # x = 5 (scalar)
y = i + 2        # y = 7 (scalar)
res += x / y     # Just res += 5/7 (once, not a loop!)
```

**Use a function for composition:**
```pluto
i = 0:5
res += compute_ratio(i)
    numerator = i + 1
    denominator = i + 2
    res = numerator / denominator
```

**Generated code:**
```c
res = 0;
for (int64_t i_val = 0; i_val < 5; i_val++) {
    numerator = i_val + 1;
    denominator = i_val + 2;
    res += numerator / denominator;
}
```

**Or inline the expression:**
```pluto
i = 0:5
res += (i + 1) / (i + 2)  # Simple and clear
```

---

## Everything Inside Loops Is Scalar

When a loop is generated at the statement level, **all operations inside work on scalar values**:

```pluto
i = 0:5
result = Square(i) + i
```

**Generated code:**
```c
result = 0;
for (int64_t i_val = 0; i_val < 5; i_val++) {
    result = Square(i_val) + i_val;  // Square called with scalar!
}
```

**Key insights:**
- ✅ `Square(i)` receives a **scalar** argument (not a range)
- ✅ The `+` operator works on **scalars** (Square result and i_val)
- ✅ No special "range-aware" functions or operators needed
- ✅ Loop is outside all operations

---

## No Reassignment Issues!

Since ranges execute immediately (not lazy), reassignment is perfectly fine:

```pluto
i = 0:5
x = i + 1        # Loop executes, x = 5
i = 0:10         # OK! Reassigning i
y = i + 1        # New loop executes, y = 10
```

There's no "captured range" complexity - each expression generates its loop independently.

---

## Summary

Pluto ranges are **simple loop syntax**:

| Expression | Behavior | Result (i = 0:5) |
|------------|----------|------------------|
| `x = i + 1` | Loop, last value | `x = 5` |
| `x += i` | Loop, accumulate | `x = 0+1+2+3+4 = 10` |
| `[i * 2]` | Loop, collect | `[0,2,4,6,8]` |
| `res = i > 2 i` | Loop, conditional | `res = 4` (last where i>2) |
| `arr[i]` | View/loop | View or iterate |

**Core Design:**
- ✅ Ranges are loop syntax, not lazy types
- ✅ Expressions execute immediately
- ✅ Same variable = zip, different = Cartesian
- ✅ No reassignment restrictions
- ✅ Zero-value auto-initialization
- ✅ Simple, predictable semantics