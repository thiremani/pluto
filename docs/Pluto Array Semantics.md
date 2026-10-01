# Pluto Array Semantics

## Type and representation

An array type consists of a scalar leaf type and a rank. `[I64]` is rank 1,
`[[I64]]` is rank 2, and nesting continues for higher ranks. Dimension lengths
are runtime values, not part of type identity.

Rank is unbounded in the language definition, but the current compiler
enforces a temporary implementation limit of 64, rejecting a deeper literal
with a positioned diagnostic (`array rank 65 exceeds the current compiler
limit of 64`). The limit exists because the type mangle and the array
descriptor grow with rank; it will be raised or removed with issue #90's
compact rank encoding, fixed-size descriptor, and input-complexity fuses.

All ranks use one flat, row-major element buffer. Higher ranks carry their
dimension lengths beside that buffer; rows are not separately allocated.

## Literal inference

- An empty array states its element type; see [Empty arrays](#empty-arrays).
- `[1 2 3]` is a rank-1 `[I64]` value.
- A one-row inline literal contributes one array axis.
- Two or more rows imply block layout.
- A block literal contributes row and column axes even when it contains only
  one row. A newline immediately after `[` explicitly selects that layout.
- Equal-shaped array-valued cells stack recursively into higher ranks.

### Empty arrays

An empty array states its element type with a zero value written directly
after its brackets. It has no cells, so no later line needs to supply the type:

| Literal | Type |
|---|---|
| `[]0` | `[I64]` |
| `[]0.0` | `[F64]` |
| `[]""` | `[Str]` |
| `[]x` | an array whose elements have `x`'s type |

A variable sample such as `x` is never evaluated: only its type counts, and
its dimensions are ignored. Other literal samples (`[]5`, `[]2.3`, `[].0`), a
detached sample (`[] 0`), and bare `[]` are errors.

The sample stands for one element, so rank follows the layout rule. An empty
block with a suffix is a rank-2 value with shape `[0 0]`, and an array sample
adds its rank:

```pluto
m = [
]0              # [[I64]], shape [0 0]
cube = []m      # rank 3, shape [0 0 0]
row = [[]0]     # one empty row: shape [1 0], not an empty matrix
```

A zero-row matrix keeps no column count: concatenation takes the other
operand's inner shape.

Pluto has no line continuation character, so a line break between two cells
ends the row: a rank-1 literal keeps its cells on one line, however long, and
an editor can wrap it for display. A line break inside a cell, in a nested
literal or a multi-line string, belongs to that cell. A second row selects
block layout:

```pluto
matrix = [1 2
          3 4]
```

Lines inside an open bracket have no indentation of their own. A line
continues the bracket when it is indented past the line where the bracket
opened or starts with a closing bracket. Any other line closes
each bracket it cannot continue: the bracket is reported as never closed, and
the line parses as the next statement. An assignment's value starts on the
same line as its `=`, so a multi-line literal opens its bracket there:
`m = [`, not `m =` followed by `[` on the next line.

A newline immediately after `[` explicitly selects block layout for an empty
or one-row matrix.

These two literals therefore have the same rank-2 type and value:

```pluto
a = [
    1 2
    3 4
]

b = [[1 2] [3 4]]
```

Nesting composes for higher ranks:

```pluto
cube = [
    [1 2] [3 4]
    [5 6] [7 8]
]
```

The cube is equivalent to
`[[[1 2] [3 4]] [[5 6] [7 8]]]`: the block contributes dimensions `[2 2]`
and each rank-1 cell contributes the final dimension, giving shape `[2 2 2]`.

The distinction does not depend on the number of rows. These literals have
different ranks:

```pluto
vector = [1 2 3]       # shape [3]

oneRowMatrix = [
    1 2 3
]                       # shape [1 3]
```

A block always contributes both layout axes, even when each row contains one
array-valued cell. Thus the following has shape `[2 1 2]`, not `[2 2]`:

```pluto
rows = [
    [1 2]
    [3 4]
]
```

Use `[[1 2] [3 4]]`, or the equivalent multiline scalar matrix above, for
shape `[2 2]`.

Arrays are rectangular. Every scalar row must have the same number of cells,
and every stacked child must have the same shape. Pluto reports a shape error;
it never inserts default values for omitted cells. For example, this is invalid:

```pluto
arr = [
    1 0
    0
]
```

Ranges inside an inline literal remain collectors and may determine its runtime
length. Block cells must be statically sized. Array values are nested rather
than flattened when used as cells.

### Tables

Multiple scalar rows with homogeneous but different column types infer an
unnamed table. A header always produces a table and must contain at least one
column name. The header goes on its own line after `[`, with all its column
names on that line. Headerless literals start directly with their first data
row.

The preferred layout outdents the `:` marker so the first header and first
value begin in the same column. Spacing within header and data rows is
otherwise non-semantic:

```pluto
scores = [
  : Name Score
    "Ada" 10
    "Lin" 12
]
```

Named columns are arrays, so `scores.Score` is `[10 12]`. A table without data
rows types each column with a zero value attached to its name:

```pluto
scores = [
  : Name("") Score(0)
]
```

It prints with its header, and each projected column is an empty array of its
type. Assigning it to an established table with the same columns clears that
table's rows. A header without data rows needs a type on every column, and
column types are only written on a table without rows.

## Indexing and operations

Each bracket indexes one outer dimension. Rank-1 indexing returns a scalar;
higher-rank indexing returns an owned array with one fewer dimension:

```pluto
cube[1]          # rank 2
cube[1][2]       # rank 1
cube[1][2][0]    # scalar
```

A range-valued index is an iteration driver, not a slice or view value.
An assignment root keeps the final valid selected element or subarray; wrap
the access in `[]` to collect all selected values:

```pluto
i = 0:2
last = vector[i]
selected = [vector[i]]

lastRow = matrix[i]
selectedRows = [matrix[i]]
```

`last` is the final selected element, while `selected` contains every element.
For higher-rank arrays, `lastRow` owns the final selected row and
`selectedRows` stacks every selected row.
Range-indexed access cannot be stored or printed as an internal view; it must
be consumed, finalized, or collected.

An immediate bare `array[range]` function argument may be consumed by a
specialized callee. The compiler can carry the array and range in an internal,
call-scoped descriptor and perform the iteration there. This descriptor is not
a source-level value and cannot be stored, returned, printed, or otherwise
escape the call.

Planned deferred nested range construction also uses chained indexing and is
specified in
[Pluto Range Semantics](Pluto%20Range%20Semantics.md#deferred-nested-range-construction);
it remains deferred until PIR represents those scopes directly.

Array-scalar operations preserve shape. Array-array element-wise operations
require equal rank and zip every dimension to the shorter corresponding
dimension, without padding. For example, shapes `[2 3]` and `[3 2]` produce
shape `[2 2]`. Concatenation joins the outer dimension and requires equal inner
dimensions when both operands are nonempty. An empty operand contributes no
cells and imposes no inner-shape constraint; concatenation uses the nonempty
operand's inner shape. Literal-construction mismatches are compile errors;
concatenation shapes that depend on runtime values are checked before
proceeding.

### Type stability

Types flow forward only. A binding's type is fixed by the right-hand side of
its first assignment, and an expression's type comes only from that expression
and its operands: a later assignment, an assignment target, a parent, or a
sibling cannot change it. `Unresolved` is a temporary solver placeholder while
a recursive call's result is inferred.

With `arr = [1]`, the distinctions are:

- `[]0` has type `[I64]` and value `[]`.
- `([]0 + []0) ⊕ [1.5]` has type `[F64]` and value `[1.5]`; the inner `+`
  keeps `[I64]`.
- `[arr[5]]` has type `[I64]` and value `[0]`; a fixed-layout literal preserves
  its cell and zero-fills an out-of-bounds read.
- `arr[0] > 5 [arr[0]]` has type `[I64]` and value `[]`; the false statement
  gate filters the value without erasing its leaf type.

Assigning an empty array of the binding's type empties its value. Any other
element type or rank is an error: `e = []0` followed by `e = [1.5]` is
rejected, as `e = [1]` followed by `e = [1.5]` is.

An empty array prints as `[]` (or an empty block), without its element type,
so a printed empty array does not read back as source.
