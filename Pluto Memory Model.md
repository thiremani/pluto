# Pluto Semantics vs. Other Languages

This document compares Pluto's semantic model (Value Semantics + Lazy Views) with other major systems and scripting languages.

## The Pluto Model (Summary)
1.  **Assignment is Copy:** `a = b` creates a new independent value (Snapshot).
2.  **Arrays are Values:** `arr2 = arr1` copies data (COW).
3.  **Slices are Views:** `s = arr[i]` is a reference to `arr`.
4.  **Lazy Ranges:** `x = i + 1` captures `i` by value (Snapshot).
5.  **Function Locking:** Input arguments hold read locks, outputs hold write locks (automatic concurrency safety).
6.  **Memory Management:** Automatic scope-based deallocation (no GC pauses).

---

## Comparison Table

<table border="1" cellpadding="5" cellspacing="0">
<thead>
<tr>
<th align="left">Feature</th>
<th align="left">Pluto</th>
<th align="left">Python</th>
<th align="left">Rust</th>
<th align="left">Go</th>
<th align="left">Julia</th>
<th align="left">Zig</th>
</tr>
</thead>
<tbody>
<tr>
<td><strong>Assignment (<code>a=b</code>)</strong></td>
<td><strong>Copy</strong></td>
<td>Reference</td>
<td>Move / Copy</td>
<td>Copy</td>
<td>Reference</td>
<td>Copy</td>
</tr>
<tr>
<td><strong>Array Assign</strong></td>
<td><strong>Copy</strong> (COW)</td>
<td>Reference</td>
<td>Move</td>
<td>Reference (Slice)</td>
<td>Reference</td>
<td>Copy</td>
</tr>
<tr>
<td><strong>Function Args</strong></td>
<td><strong>Reference</strong> (Locked)</td>
<td>Reference</td>
<td>Move / Borrow</td>
<td>Copy (Slice Ref)</td>
<td>Reference</td>
<td>Copy</td>
</tr>
<tr>
<td><strong>Slicing (<code>a[:]</code>)</strong></td>
<td><strong>View</strong></td>
<td>Copy (List) / View (NumPy)</td>
<td>View (Slice)</td>
<td>View (Slice)</td>
<td>Copy (default) / View (<code>@view</code>)</td>
<td>View (Slice)</td>
</tr>
<tr>
<td><strong>Range Capture</strong></td>
<td><strong>Value</strong> (Snapshot)</td>
<td>Reference (Generator)</td>
<td>Reference (Iterator)</td>
<td>N/A</td>
<td>Reference (Iterator)</td>
<td>N/A</td>
</tr>
<tr>
<td><strong>Mutability</strong></td>
<td><strong>In-Place Only</strong></td>
<td>Mutable Objects</td>
<td>Mutable (if <code>mut</code>)</td>
<td>Mutable</td>
<td>Mutable</td>
<td>Mutable</td>
</tr>
<tr>
<td><strong>Memory Mgmt</strong></td>
<td><strong>Auto (Scope)</strong></td>
<td>Auto (GC)</td>
<td>Auto (Owner)</td>
<td>Auto (GC)</td>
<td>Auto (GC)</td>
<td>Manual</td>
</tr>
</tbody>
</table>

---

## Detailed Breakdown

### 1. vs. Python
*   **Python:** "Everything is a Reference."
    *   `a = [1]; b = a; a[0] = 2` -> `b` sees `2`.
    *   `x = (i+1 for i in iter)` -> Captures `iter` by reference.
*   **Pluto:** "Everything is a Value."
    *   `a = [1]; b = a; a[0] = 2` -> `b` sees `1`.
    *   `i = 0:5`
    *   `x = i + 1`
    *   `i = 100:105`     # i is reassigned.
    *   `x`               # 1 2 3 4 5 (UNCHANGED)
    *   Captures `i` by value.
*   **Difference:** Pluto is safer and more predictable. Python is more "dynamic" but prone to aliasing bugs.

### 2. vs. Rust
*   **Rust:** "Ownership & Borrowing."
    *   `a = vec![1]; b = a;` -> `a` is **Moved** (invalidated). `b` owns it.
    *   `s = &a[..]` -> Borrow checking prevents mutation of `a` while `s` lives.
*   **Pluto:** "Copy on Write."
    *   `a = [1]; b = a;` -> Both `a` and `b` are valid and independent.
    *   `s = arr[i]` -> Runtime/Compiler checks for ownership scope (like Rust lifetimes but simpler).
*   **Difference:** Pluto is easier to use (no borrow checker fighting) but relies on COW optimization instead of static moves.

### 3. vs. Go
*   **Go:** "Arrays are Values, Slices are References."
    *   `var a [3]int; b := a` -> Copy.
    *   `s := make([]int, 3); t := s` -> Reference (both point to same backing array).
    *   `arr := [5]int{1 2 3 4 5}`
    *   `s := arr[0:2]`
    *   `s[0] = 99`       # Mutates arr via s
    *   `arr`             # [99 2 3 4 5]
*   **Pluto:** "Arrays are Values, Functions use References."
    *   Like Go, Pluto passes references to functions to avoid copying large data.
    *   **Difference:** Pluto enforces **Read/Write Locks** on these references automatically (input args are read locks, outputs are write locks), whereas Go allows data races (user must use `sync.Mutex`).

### 4. vs. Julia
*   **Julia:** "Pass-by-Sharing."
    *   Behaves like Python. `b = a` aliases.
    *   Slicing `a[1:5]` creates a **Copy** by default (for performance safety). You must use `@view a[1:5]` to get a view.
*   **Pluto:** Slicing `arr[i]` creates a **View** by default (for performance).
*   **Difference:** Pluto favors Views for slicing (like Go/Rust), Julia favors Copies (like Python/Matlab) unless requested.

### 5. vs. Zig
*   **Zig:** "Explicit Memory."
    *   Arrays are values. Slices `[]T` are pointer+len structs.
    *   No hidden allocations.
*   **Pluto:** Similar model. `ArrayRange` is basically a Zig slice.
*   **Difference:** Pluto manages memory automatically (scope-based), Zig is manual.

---

## Why Pluto's Model is Unique

Pluto sits in a "Sweet Spot" for parallel computing:

1.  **Value Semantics (like R/Matlab)** make reasoning about concurrent code easy. "If I have `x`, I own `x`."
2.  **Explicit Views (like Rust/Go)** allow high-performance mutation without copying.
3.  **Lazy Snapshots (Unique)** solve the "Generator Aliasing" problem that plagues Python/Julia.

It combines the **safety of R** with the **performance of Rust/Zig**.
