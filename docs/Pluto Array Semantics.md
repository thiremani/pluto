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
- An inline literal contributes one array axis.
- A block literal, where `[` is followed by a newline, contributes row and
  column axes even when it contains only one row.
- Equal-shaped array-valued cells stack recursively into higher ranks.

A long rank-1 literal remains inline by escaping its physical newline:

```pluto
values = [1 2 3 4 5 \
          6 7 8 9 10]
```

Without the `\`, an inline literal cannot start another logical row. Put a
newline immediately after `[` to select block layout instead.

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
projected column is an untyped empty array that prints as `[]`. Header-only
tables can be printed and projected, but cannot be passed to functions until
their column types are established.

## Indexing and operations

`array[i]` indexes the outer dimension. Rank-1 indexing returns a scalar;
higher-rank indexing returns an owned array with one fewer dimension. Chained
indexing therefore works naturally.

A range-valued index is an iteration driver, not a materialized slice. Wrap
the access in `[]` to collect its results. For a rank-2 array, this stacks the
selected rows into another rank-2 array:

```pluto
i = 0:2
selected = [matrix[i]]
```

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

Assigning `[]` to a concrete array empties it without changing its established
leaf type or rank. Untyped empty arrays can specialize functions and refine to
a concrete leaf type through concatenation.

## Planned rank-N range features

### Grouped multi-axis indexing

Shape-preserving selection across multiple indexed dimensions is not yet
implemented. It is deferred until ranged collectors are represented in PIR.
The planned syntax is grouped indexing:

```pluto
i = 0:3
j = 0:3
k = 0:3

submatrix = [matrix[i j]]
plane = [cube[1 j k]]
column = [cube[i 1 1]]
```

A grouped access containing at least one range is a lazy selection view. Its
rank is:

```text
source rank - number of scalar indices
```

Unspecified trailing axes are retained. If a grouped access contains a range,
the leftmost range drives iteration. Each yield has one less rank than the
selection; the surrounding collector applies its ordinary rule and stacks the
yields, restoring the selection rank. It does not need a special
"materialize without adding a dimension" case.

For example, a rank-3 `cube` follows this ladder:

```pluto
cube[1]          # rank 2
cube[1 2]        # rank 1
cube[1 2 0]      # scalar
[cube[i j k]]    # rank 3
[cube[1 j k]]    # rank 2
[cube[1 2 k]]    # rank 1
```

Grouped indexing preserves axes. Chained range indexing, such as
`[matrix[i][j]]`, keeps the existing flattened range-domain behavior. Mixed
grouped and chained indexing should initially be rejected.

### Nested range construction

Grouped indexing selects from an existing array. Computed rectangular values
need a separate construction rule:

```pluto
i = 0:3
j = 0:3
submatrix = [i && [matrix[i][j]]]
```

Here value-position `&&` binds the outer `i` domain locally, the inner collector
owns `j`, and the outer collector stacks the rows. It never becomes a statement
gate. A skipped array-valued child contributes a zero-filled child of the known
inner shape, while `||` may supply an explicit shape-compatible fallback.

The domain-binding, collector-placement, and fallback rules are specified in
[Pluto Range Semantics](Pluto%20Range%20Semantics.md#deferred-nested-range-construction).
This construction remains deferred until PIR represents range ownership and
collector boundaries directly.
