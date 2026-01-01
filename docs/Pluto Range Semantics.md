# Pluto Range Semantics

## Core Principle: Base Function Model

In Pluto, **every operation is a function**. Each expression has a **base function** that determines where range iteration occurs. Ranges are expanded as loops **inside** the base function's body.

### Finding the Base Function

Every expression is a function call. The base function is found by:

```
findBase(f):
    if f has exactly one argument AND that argument is a function call g:
        return findBase(g)  # descend into single-arg function
    else:
        return f  # f is base
```

**Rules:**
1. Start at the root function of the expression
2. If it has exactly **one argument** that is itself a **function call**, descend into it
3. Continue until you hit a function with multiple args, or whose single arg is a value (range, variable, literal)
4. The base function's body contains all range iteration loops

### Examples

| Expression | Tree | Base | Reason |
|------------|------|------|--------|
| `f(0:5)` | `f(range)` | `f` | single arg is value |
| `f(g(0:5))` | `f(g(range))` | `g` | descend through single-arg `f` |
| `f(g(h(0:5)))` | `f(g(h(range)))` | `h` | keep descending |
| `f(a, b)` | `f(a, b)` | `f` | multiple args |
| `a + b` | `Add(a, b)` | `Add` | multiple args |
| `-x` | `Negate(x)` | `Negate` | single arg is value |
| `-(0:5)` | `Negate(range)` | `Negate` | single arg is value |
| `√(x + y)` | `Sqrt(Add(x, y))` | `Add` | descend through single-arg `Sqrt` |
| `√(x + 0:5)` | `Sqrt(Add(x, range))` | `Add` | descend to `Add` |
| `arr[0:5] + 1` | `Add(Index(arr, range), 1)` | `Add` | multiple args |

---

## Range Basics

A range `start:stop` or `start:stop:step` defines iteration bounds:

```pluto
i = 0:5          # Iterates: 0, 1, 2, 3, 4
j = 0:10:2       # Iterates: 0, 2, 4, 6, 8
k = 5:0:-1       # Iterates: 5, 4, 3, 2, 1
```

Ranges are values that describe iteration - they expand when used in expressions.

---

## Loop Generation Inside Base Function

Once the base function is determined, all ranges in its arguments become nested loops around its body:

### Example: Simple Function Call

```pluto
i = 0:5
x = Square(i)
```

- Base function: `Square` (single arg is range value)
- Range `i` in args → loop inside Square's body

**Generated structure:**
```
Square:
    for i_val in 0:5:
        x = i_val * i_val
```

### Example: Nested Single-Arg Functions

```pluto
x = √(Square(0:5))
```

- Tree: `Sqrt(Square(range))`
- `Sqrt` has single arg `Square(...)` which is a function → descend
- `Square` has single arg `0:5` (value, not function) → **base is `Square`**

**Generated structure:**
```
Square:
    for i_val in 0:5:
        tmp = i_val * i_val
Sqrt(tmp)  # Applied to final result
```

### Example: Multiple Arguments

```pluto
x = f(0:3, 10:13)
```

- `f` has 2 args → **base is `f`**
- Both ranges become nested loops

**Generated structure:**
```
f:
    for tmp1 in 0:3:
        for tmp2 in 10:13:
            <body of f>
```

### Example: Infix with Range

```pluto
x = a + 0:5
```

- Tree: `Add(a, range)`
- `Add` has 2 args → **base is `Add`**

**Generated structure:**
```
Add:
    for i_val in 0:5:
        x = a + i_val
```

### Example: Prefix Applied to Infix

```pluto
x = √(a + 0:5)
```

- Tree: `Sqrt(Add(a, range))`
- `Sqrt` has single arg `Add(...)` which is a function → descend
- `Add` has 2 args → **base is `Add`**

**Generated structure:**
```
Add:
    for i_val in 0:5:
        tmp = a + i_val
Sqrt(tmp)  # Applied to final result
```

---

## Loop Generation Modes

When ranges expand, the behavior depends on the assignment operator:

### Mode 1: Assignment (Last Value)

Simple assignment keeps the last iteration value:

```pluto
i = 0:5
x = i + 1
```

**Generated code:**
```c
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
for (int64_t i_val = 0; i_val < 5; i_val++) {
    arr[i_val] = i_val * 2;
}
// arr = [0, 2, 4, 6, 8]
```

---

## Multiple Ranges: Nested Loops

When multiple ranges appear in the base function's arguments, they become **nested loops**:

```pluto
i = 0:2
j = 0:3
result = f(i, j)
```

**Generated structure:**
```
f:
    for i_val in 0:2:
        for j_val in 0:3:
            <body of f>
```

This produces `2 × 3 = 6` iterations (Cartesian product).

### Same Range Variable = Single Loop (Zip)

If the same range variable appears multiple times, it's a single loop:

```pluto
i = 0:5
result = i + i * 2
```

**Generated code:**
```c
for (int64_t i_val = 0; i_val < 5; i_val++) {
    result = i_val + i_val * 2;
}
```

---

## Conditional Assignment

You can add a conditional guard to selectively update values:

```pluto
res = condition expression
```

**Desugars to:**
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
res = 0;
for (int64_t i_val = 0; i_val < 5; i_val++) {
    if (arr[i_val] > res) {
        res = arr[i_val];
    }
}
// res = 5 (maximum)
```

---

## Array Indexing with Ranges

Using a range to index an array creates a view or loop:

```pluto
arr = [10 20 30 40 50]
i = 0:3
slice = arr[i]         # View (ArrayRange type)
values = [arr[i]]      # Collect to new array [10, 20, 30]
res += arr[i]          # Sum: res = 10+20+30 = 60
```

---

## Pass-by-Reference Semantics

Function arguments are passed by reference. When the same variable is used for input and output, they alias:

```pluto
rebuilt = [1 2 3]
rebuilt = Rebuild(rebuilt, 10:13)  # Input and output alias!
```

**Aliased behavior:** Changes to output affect subsequent reads of input within the same iteration.

**No aliasing (literal array):**
```pluto
result = Rebuild([1 2 3], 10:13)  # Fresh array, no aliasing
```

---

## Functions with Range Parameters

When a function receives a range parameter, iteration happens **inside** the function body:

```pluto
res = AddMul(x, 0:5)
    i = 10
    res = i * x + y  # y iterates over 0:5
```

**Generated structure:**
```
AddMul:
    for y_val in 0:5:
        i = 10
        res = i * x + y_val
```

The function receives the range, and its body contains the loop.

---

## Prefix Operators

Prefix operators (`-`, `√`, etc.) are single-argument functions. The base function rule applies:

```pluto
x = -(0:5)      # Tree: Negate(range) → base is Negate
x = √(a + 0:5)  # Tree: Sqrt(Add(a, range)) → descend to Add
```

---

## Intermediate Values Become Scalars

When you assign a range expression to a variable, the loop executes immediately:

```pluto
i = 0:5
x = i + 1        # Loop executes NOW: x = 5 (scalar)
y = i + 2        # Loop executes NOW: y = 7 (scalar)
res = x / y      # No loop! Just: res = 5 / 7
```

**For iteration over both:**
```pluto
i = 0:5
res = (i + 1) / (i + 2)  # Single loop, both computed per iteration
```

---

## Summary

| Concept | Rule |
|---------|------|
| **Base function** | Descend through single-arg function calls until multiple args or value arg |
| **Range expansion** | Nested loops inside base function's body |
| **Multiple ranges** | Nested loops (Cartesian product) |
| **Same variable** | Single loop (zip behavior) |
| **Assignment** | Last value kept |
| **Compound assignment** | Accumulate across iterations |
| **Array literal** | Collect all values |
| **Pass-by-reference** | Input/output can alias |

**Core Design:**
- Every operation is a function
- Deterministic base function selection
- Ranges expand as loops inside base function
- No side effects in functions
- Simple, predictable semantics
