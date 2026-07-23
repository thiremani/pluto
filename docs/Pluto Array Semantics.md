# Pluto Array Semantics

## Type and representation

An array type consists of a scalar leaf type and a rank. `[I64]` is rank 1,
`[[I64]]` is rank 2, and nesting continues for higher ranks. Dimension lengths
are runtime values, not part of type identity.

All ranks use one flat, row-major element buffer. Higher ranks carry their
dimension lengths beside that buffer; rows are not separately allocated.

## Literal inference

- `[]` is a rank-1 `[Empty]` value.
- An empty block is a rank-2 `[Empty]` value with shape `[0 0]`.
- `[1 2 3]` is a rank-1 `[I64]` value.
- A one-row inline literal contributes one array axis.
- Two or more unescaped logical rows imply block layout.
- A block literal contributes row and column axes even when it contains only
  one row. A newline immediately after `[` explicitly selects that layout.
- Equal-shaped array-valued cells stack recursively into higher ranks.

A long rank-1 literal remains inline by escaping its physical newline:

```pluto
values = [1 2 3 4 5 \
          6 7 8 9 10]
```

Without the `\`, a second logical row selects block layout:

```pluto
matrix = [1 2
          3 4]
```

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
column name. Headerless literals start directly with their first data row.

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

Named columns are arrays, so `scores.Score` is `[10 12]`. A header may have no
data rows; the resulting table retains its header when printed, while each
projected column is an `[Empty]` array that prints as `[]`. Header-only tables
have `Empty` column types and can be passed to functions; a concrete table with
the same column names can later establish their leaf types. Assigning a
header-only table to an established table with the same columns clears its rows
without changing those established types.

## Indexing and operations

Each bracket indexes one outer dimension. Rank-1 indexing returns a scalar;
higher-rank indexing returns an owned array with one fewer dimension:

```pluto
cube[1]          # rank 2
cube[1][2]       # rank 1
cube[1][2][0]    # scalar
```

A range-valued index is an iteration driver, not a materialized slice. Wrap
the access in `[]` to collect its results. For a rank-2 array, this stacks the
selected rows into another rank-2 array:

```pluto
i = 0:2
selected = [matrix[i]]
```

Deferred nested range construction also uses chained indexing and is specified
in [Pluto Range Semantics](Pluto%20Range%20Semantics.md#deferred-nested-range-construction).

Array-scalar operations preserve shape. Array-array element-wise operations
require equal rank and zip every dimension to the shorter corresponding
dimension, without padding. For example, shapes `[2 3]` and `[3 2]` produce
shape `[2 2]`. An operation on two untyped empty arrays yields another untyped
empty array. Concatenation joins the outer dimension and requires equal inner
dimensions when both operands are nonempty. An empty operand contributes no
cells and imposes no inner-shape constraint; concatenation uses the nonempty
operand's inner shape. Literal-construction mismatches are compile errors;
concatenation shapes that depend on runtime values are checked before
proceeding.

### Type stability

An expression's concrete type comes only from that expression and its operands;
an assignment target, parent, or sibling cannot change it. `Unresolved` is a
temporary solver placeholder. `Empty` is fully resolved and means that an
array has no concrete leaf-type evidence, not merely that its runtime length is
zero.

With `arr = [1]`, the distinctions are:

- `[]` has type `[Empty]` and value `[]`.
- `([] + []) ⊕ ["x"]` has type `[Str]` and value `["x"]`; the inner `+`
  remains `[Empty]` and is never evaluated on strings.
- `[arr[5]]` has type `[I64]` and value `[0]`; a fixed-layout literal preserves
  its cell and zero-fills an out-of-bounds read.
- `arr[0] > 5 [arr[0]]` has type `[I64]` and value `[]`; the false statement
  gate filters the value without erasing its leaf type.

Assigning `[]` to an established concrete array similarly empties the value
without changing that binding's leaf type or rank.
