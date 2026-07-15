# Pluto Array Semantics

## Type and representation

An array type consists of a scalar leaf type and a rank. `[I64]` is rank 1,
`[[I64]]` is rank 2, and nesting continues for higher ranks. Dimension lengths
are runtime values, not part of type identity.

All ranks use one flat, row-major element buffer. Higher ranks carry their
dimension lengths beside that buffer; rows are not separately allocated.

## Literal inference

- `[]` is a rank-1 `[Empty]` value.
- `[1 2 3]` is a rank-1 `[I64]` value.
- Multiple homogeneous scalar rows infer a rank-2 array.
- Array-valued cells of one rank and shape stack into an array one rank higher.
- Multiple scalar rows with homogeneous but different column types infer an
  unnamed table. A header always produces a table.

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
    [
        1 2
        3 4
    ]
    [
        5 6
        7 8
    ]
]
```

Arrays are rectangular. Every scalar row must have the same number of cells,
and every stacked child must have the same shape. Pluto reports a shape error;
it never inserts default values for omitted cells. For example, this is invalid:

```pluto
arr = [
    1 0
    0
]
```

Ranges inside a rank-1 literal remain collectors and may determine its runtime
length. Array values are nested rather than flattened when used as cells.

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

Shape-preserving collection across multiple indexed dimensions is not yet
defined. Chained range drivers in one collector follow the ordinary flattened
range-domain semantics; adding collector brackets does not select another axis.

Array-scalar operations preserve shape. Array-array element-wise operations
require equal rank and equal inner dimensions, then zip the outer dimension to
the shorter input. Concatenation joins the outer dimension and requires every
inner dimension to match. Literal-construction mismatches are compile errors;
operation shapes that depend on runtime values are checked before proceeding.

Assigning `[]` to a concrete array empties it without changing its established
leaf type or rank. Untyped empty arrays can specialize functions and refine to
a concrete leaf type through concatenation.
