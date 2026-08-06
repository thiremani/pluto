# Pluto PIR Capability Matrix

**Status:** Step 1 inventory, 2026-08-06

**Purpose:** The migration unit for [the PIR plan](./Pluto%20IR%20Plan.md) is a
capability combination, not a dispatcher branch. This file enumerates the
reachable combinations, what each currently routes to, which tests cover it,
which step migrates it, and the notable removable helpers it currently uses.
The actual helper-release inventory is plan §16 Step 9; the capability router
keys on the same columns.

## 1. Axes

| Axis | Values |
| --- | --- |
| Gate | none, scalar, ranged |
| RHS flags (composable) | ordinary, conditional, checked, ranged, collector, call |
| Callee output effect (call rows) | all-`MustWrite`, any-`MayWrite` |
| Value kind | scalar, heap, multi-output, self-referential, Range descriptor, struct, table |
| Target kind | local, function output, discard (`_`), global constant, none (print) |
| Statement form | assignment, declaration, print |
| Domain role | none, RHS-local, shared gate, collector-local, function-owned, callee-owned |

**Callee output effect is a whole-call property.** A call with any `MayWrite`
output defers to Step 6 as a unit — argument evaluation, tuple failure, and
ownership are shared across its outputs, so individual slots cannot migrate
independently.

RHS flags compose: one RHS can be conditional, checked, ranged, collector, and
a call at once (`tests/math/func.spt` carries all six across its statements).
Only reachable combinations are listed; rectangles are collapsed where routing,
disposition, and cutover step are identical across a range of values.

Print has **no gate axis**: `ast.PrintStatement` carries only an expression, so
its conditionality comes entirely from cond-expressions inside its arguments.
A gated print (`arr[oob] val1, val2`) is proposed future syntax that does not
parse today; it gets its own axis value and rows when that feature lands, as
its own PR before Step 6.

**Domain-role note.** `LoopInside` is the callee's flag, and the call site owns
the loop when it is *false*: `compileCallExpression` selects
`compileDirectCallWithRanges`/`compileIndirectCallWithRanges` under
`!info.LoopInside && len(info.Ranges) > 0`
([compiler.go:3175](../compiler/compiler.go:3175) for the direct-return ABI,
[:3183](../compiler/compiler.go:3183) for the indirect one).
`LoopInside=true` — callee-owned iteration — lowers through `compileCallInner`
**only for direct returns**; indirect and multi-output callees use
destination-seeded staged outputs instead.

Print never delegates argument iteration to a callee: the solver forces the
synthetic print call's `LoopInside` false. That is separate from a print
*inside* a function body, which is function-owned whenever a Range parameter
drives that body.

## 2. Reachable combinations

Disposition: **R** retain as-is, **S** simplify during migration, **D** delete
the behavior (needs its own PR). Coverage counts `.spt`/`.pt` files.

The final column lists **notable PIR-removable orchestration helpers** a row
uses; it is deliberately non-exhaustive. Primitives that survive migration —
call lowering, loop and guard emission, storage — are not listed, and a helper
can be deleted only once its last row has migrated. The helper-to-release-step inventory is plan §16 Step 9 (9a
statement-only, 9b orchestration); read the two together rather than treating
any single row as a deletion trigger.

| # | Gate | RHS flags | Value kind | Target | Form | Domain role | Callee effect | Legacy route | Disp | Existing tests | Missing | Step | Notable removable helpers |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | none | ordinary | scalar | local | assign | — | — | `compileAssignments` | R | `arithmetic`, `op`, `unary`, `numeric_literals`, `zero_div` | — | 3 | `compileAssignments` |
| 2 | none | ordinary | heap | local | assign | — | — | `compileAssignments` → `commitAssignments` | R | `mem/mem.spt`, `str`, `array_concat`, `cond_copy` | — | 4 | `commitAssignments` |
| 3 | none | ordinary | multi-output | local | assign | — | — | `compileAssignments`, arity via `newExprAssign` | R | `partial_returns`, `math/div`, `mem/mem.spt` | — | 4 | `exprAssign` machinery |
| 4 | none | ordinary (swap, dup source) | heap | local | assign | — | — | `commitAssignments` copy/move marking | R | `mem/mem.spt:64,78` | — | 4 | `markCopyRequirements`, `freeExprOldValues`, `deepCopyIfNeeded` |
| 5 | none | ordinary | scalar | blank (`_`) | assign | — | — | ordinary binding, CFG-exempt | **S** (real sink, §4) | `discard` | repeated blanks, mixed types, repeated statements, gated/ranged blank | own PR, then 3 | §4 |
| 5b | none | ordinary | heap | blank (`_`) | assign | — | — | ordinary binding, CFG-exempt; duplicate heap blanks alias and leak/abort (§4) | **S** (real sink, §4) | — | heap blank, duplicate heap blanks | own PR, then 4 | §4 |
| 5c | none | call | scalar, heap, multi-output | blank (`_`) | assign | — | split: all-`MustWrite` → 4, any-`MayWrite` → 6 | discarded call outputs keep their yield/write validity for cleanup, and the whole-call rule applies unchanged — an any-`MayWrite` call defers to Step 6 even when every output is discarded | **S** (real sink, §4) | — | scalar, heap, and multi-output call-output discard; duplicate blanks on one call | own PR, then 4 (all-`MustWrite`) / 6 (any-`MayWrite`) | §4 |
| 6 | none | call | scalar (direct return) | local | assign | — | split: all-`MustWrite` → 4, any-`MayWrite` → 6 | `compileCallExpression` → `compileCallInner` | R | `math/rec.spt`, `math/div` | — | 4, 6 | — |
| 6b | none | call | heap, multi-output (indirect return) | local | assign | — | split: all-`MustWrite` → 4, any-`MayWrite` → 6 | destination-seeded output slots via `compileIndirectCallIntoStagedOutputs` | R | `const_args/*`, `output_refinement`, `mem/mem.spt` | — | 4, 6 | — |
| 7 | none | checked | scalar, heap | local | assign | — | — | `compileExprAssigns` bounds bit → `commitAssignmentsPerExpr` | R | `array/oob_skip`, `mem/leak/oob_paths` | — | 5 | `commitAssignmentsPerExpr` |
| 7b | none | checked, conditional (fallback) | scalar, heap | local | assign | — | — | **rejected today**: `arr[oob] \|\| -1` fails "logical OR in value position requires a conditional left operand"; Step 5 adds a fallback-specific rule (checked-access root immediately left of `\|\|`) without widening `conditionPropagates` | **S (decided behavior change; semantics doc "Checked-access fallback")** | — | regressions when implemented: `x = arr[oob] \|\| -1` → `-1`, in-bounds zero → `0`, heap `sarr[oob] \|\| "d"` | 5 | condLHS spine |
| 7c | none | checked, conditional (fallback, propagated) | scalar, multi-output | local | assign | — | both (call form) | comparison- and call-propagated fallback: `arr[oob] > 0 \|\| -1`, `Id(arr[oob]) \|\| -1` — the failure travels through a propagator before the resolver | **S (decided; semantics doc)** | — | regressions when implemented | 6 | condLHS spine |
| 7d | — | checked, conditional (fallback) | scalar, heap | none | print | — | — | print-position fallback: `arr[oob] \|\| -1, val1` emits `-1 val1` once per-slot printing exists | **S (decided; semantics doc)** | — | regressions when implemented, incl. a heap-valued print fallback | 6 | condLHS spine |
| 7e | none | checked, conditional (fallback), ranged | scalar | local | assign | RHS-local | — | ranged checked fallback: the fallback resolves per iteration inside the loop nest | **S (decided; semantics doc)** | — | regressions when implemented | 7 | condLHS spine, ranged staging |
| 7f | none | checked, conditional (fallback), collector | scalar | local | assign | collector-local | — | collector-cell fallback: in `[arr[oob] \|\| -1]` the `\|\|` resolves before the cell's zero-fill | **S (decided; semantics doc)** | — | regressions when implemented | 8 | collector rewrite, condLHS spine |
| 8 | none | ordinary | Range descriptor | local | assign | — (no domain) | — | plain value copy; the solver clears `Ranges`/`HasRanges`, so this is not an active ranged RHS | R | `range_finalize:2-21` (literal, identifier copy, empty, reassign), `compiler/solver_test.go` | — | 3 | `compileAssignments` |
| 8b | none | call | Range descriptor | local | assign | — (no domain) | split: all-`MustWrite` → 4, any-`MayWrite` → 6 | call lowering + indirect-return ABI, not descriptor copying | R | `range_finalize:38` (`makeRange`), `mem/gate_heap` | conditionally-written Range return (any-`MayWrite`) | 4, 6 | — |
| 9 | none | ranged | scalar, self-ref | local | assign | RHS-local | — | `compileAssignments` → expression loop nest (passes nil conditions) | R | `math/range_expr`, `math/range.spt`, `range_shadow.spt`, `cond/domain_activation` | — | 7 | `compileAssignments`, `withCollectorPreparedLoopNest`, `compileCondOperands` |
| 10 | none | ranged, checked | scalar | local | assign | RHS-local | — | as #9 + `withLoopNestVersioned` affine probe | S | `array/affine_bounds_stmt`, `math/affine_bounds_expr` | — | 7, fast path 10 | affine decision helpers |
| 11 | none | collector, ranged | scalar, heap | local | assign | collector-local | — | `compileArrayExpression` → `compileArray` → `withCollectorDomain` | S | `range`, `array/array_capture`, `mem/gate_heap` | — | 8 | collector rewrite |
| 11b | none | collector, call, ranged, conditional, checked | scalar | local | assign | collector-local | both; any-`MayWrite` also needs Step 6 — cutover stays 8 | a call inside a collector cell, its arguments possibly conditional/checked/ranged | S | `math/func.spt:58-69` (`[Square(arr[1:3] > 3)]`, `[Square(arrSelf[1:4])]`) | nested any-`MayWrite` call in a collector — every cited call is `Square`, all-`MustWrite`; add a conditional direct or indirect call fixture when this rectangle migrates | 8 | collector rewrite, condLHS spine |
| 12 | none | call, ranged | scalar, heap, multi-output | local | assign | call-site loop (`LoopInside=false`) | both; any-`MayWrite` also needs Step 6 call-result — cutover stays 7 | `compileDirectCallWithRanges` / `compileIndirectCallWithRanges` | S | `math/func_range`, `math/func_array_range`, `math/func_nested_range` (calls with collector arguments), `mem/mem_alias_refine.spt` | — | 7 | `compileAssignments`, `compileCondExprValue`, `withCollectorPreparedLoopNest` |
| 13 | none | call, ranged | scalar, heap, multi-output | local | assign | callee-owned (`LoopInside=true`) | both; any-`MayWrite` also needs Step 6 — cutover stays 7 | callee body iterates: `compileCallInner` for direct returns, destination-seeded staged outputs for indirect and multi-output | R | `math/acc.spt`, `math/acc_desc`, `array/array_range.spt:89-96` (heap via indirect ABI) | — | 7 | — |
| 14 | none | ordinary | scalar (direct return) | function output | assign | — | — | ordinary function-body statement; a direct `I64`/`F64` output is an SSA value with **no** runtime write flag | R | `math/acc.pt` | — | 4 | — |
| 14i | none | ordinary | heap, multi-output (indirect return) | function output | assign | — | — | ordinary function-body statement; an indirect output has a runtime write flag set on commit | R | `mem/mem_alias_refine.pt`, `mem/mem.pt` | — | 4 | — |
| 14a | scalar | ordinary, call | scalar, heap, multi-output | function output | assign | — | both | gated function-body statement: the output is conditionally written, which is what makes the callee `MayWrite` at its boundary (`IsEven`/`IsOdd` conditionally write their indirect output pair) | R | `math/math.pt`, `math/rec.pt`, `mem/mem_cmp_lhs.pt` | — | 6 | `compileCondStatement` |
| 14c | none | collector | heap | function output | assign | collector-local | — | a body-local collector domain, i.e. a collector driven by a range created inside the body rather than by a parameter | R | — | **uncovered**: `cache_reuse.pt` uses fixed literals, `array_scalar_assign.pt` is parameter-driven (row 14b) | 8 | collector rewrite |
| 14b | none/scalar | ordinary, checked | scalar, heap | function output | assign | function-owned | — | body driven by a `Range`/`ArrayRange` **parameter**, whose domain wraps the whole body and may execute zero times — this is what weakens the output to `MayWrite` at the boundary | R | `array/array_range.pt` | — | 7 | — |
| 14e | none/scalar | ordinary, collector, checked | scalar, heap | function output | assign | function-owned + collector-local | — | as 14b with an inner collector: `ArraySetAdd(1:4)` runs a function-owned outer domain around a collector-local `[0:i]`, so full cutover needs collectors | R | `array/array_scalar_assign.pt` (`ArraySetAdd`), `array/array_func.pt` | — | 8 | collector rewrite |
| 14d | none | ordinary, checked, ranged | scalar | function output | assign | RHS-local (in function) | — | a range created **inside** the body drives one statement; the output is still `MayWrite` at the boundary because that local range can be empty — only the blanket function-owned weakening is absent | R | `math/dependent_range.pt` (`j = (i + 1):n`) | — | 7 | — |
| 15 | scalar | ordinary | scalar, multi-output | local | assign | — | — | `compileCondStatement` | S | `assign`, `initialize`, `zero_val`, `partial_returns` | — | 6 | `compileCondStatement` |
| 16 | scalar | ordinary, call | heap, multi-output | local | assign | — | both (Step 6 either way) | `compileCondStatement` + `prePromoteConditionalCallArgs` | S | `cond_copy`, `mem/mem_str.spt`, `math/math.pt` | — | 6 | staging family |
| 17 | scalar | ordinary | self-referential | local | assign | — | — | `compileCondStatement` + `aliasCondDests` | S | `tests/cond/expr_forms` | — | 6 | `aliasCondDests` |
| 18 | scalar | collector | scalar, heap | local | assign | collector-local | — | `compileCondStatement` → ordinary collector inside the IF block | S | `mem/cache_reuse/cache_reuse.pt` | scalar-gated heap collector | 8 | collector rewrite |
| 19 | scalar/none | conditional | scalar | local | assign | — | — | `compileCondExprStatement` → `compileCondExprValue` | S | `cond/value_cond_expr`, `cond/expr_forms`, `math/func.spt` | — | 6 | `compileCondExprStatement` |
| 20 | scalar/none | conditional | multi-output (slot-aligned) | local | assign | — | — | `compileCondExprStatement` → `compilePerSlotAssign` | S | `cond/value_cond_expr`, `cond/logical_and` | — | 6 | `compilePerSlotAssign` |
| 21 | scalar/none | conditional, checked | scalar, heap | local | assign | — | — | `compileCondExprStatement` + bounds guard | S | `array/oob_skip`, `cond/logical_and` | — | 5, 6 | condLHS spine |
| 22 | scalar/none | conditional, ranged (logical tree) | scalar | local | assign | RHS-local | — | `compileCondExprStatement` → `stageCondRangedExpr` | S | `cond/logical_and:143,413` | — | 7 | ranged staging |
| 23 | ranged | ordinary | scalar, self-ref | local | assign | shared gate | — | `compileCondRangedStatement` → `stageCondRangedAssignments` | S | `array/cond_accum`, `cond/condition_boundary` | — | 7 | `compileCondRangedStatement` |
| 24 | ranged | collector | scalar, heap | local | assign | shared gate + collector-local | — | `compileCondRangedStatement` → `newStatementArrayCollector` | S | `array/cond_accum`, `cond/domain_activation` | — | 8 | `statementArrayCollector` trio |
| 25 | ranged | conditional | scalar | local | assign | shared gate | — | `compileCondRangedIteration` → `compileCondExprValue` | S | `cond/value_cond_expr`, `array/array_expr` | — | 7 | `compileCondExprValue` |
| 26 | ranged | call | multi-output | local | assign | shared gate | both; any-`MayWrite` also needs Step 6 — cutover stays 7 | `compileCondRangedStatement` → `perSlotCommittable` | S | `cond/value_cond_expr:740` | — | 7 | `perSlotCommittable` |
| 27 | ranged | call | heap | local | assign | shared gate | both; any-`MayWrite` also needs Step 6 — cutover stays 7 | `compileCondRangedStatement` → stage temp | S | `mem/gate_heap` | — | 7 | ranged staging |
| 28 | ranged | checked | scalar | local | assign | shared gate (affine index) | — | ranged gate over an affine access | S | `array/cond_accum:416,420` | — | 5, 7 | affine decision helpers |
| 29 | — | ordinary | scalar, heap | none | print | — | — | `compilePrintStatement` direct arm | S | `helloworld`, `str`, `1.2-report` | — | 6 | `printAllExpressions`, `compilePrintStatement` |
| 29b | — | call | scalar (direct return) | none | print | — | both; any-`MayWrite` needs the Step 4 validity variant | non-ranged direct call argument in print; a skipped result needs `{value, didWrite}` to cross the boundary | R | `math/print_func.spt:4` (`Square(3)`) | conditional direct-return argument (Step 4 prereq) | 6 | — |
| 29c | — | call | heap, multi-output (indirect return) | none | print | — | both; indirect outputs already carry write flags — no variant needed | non-ranged indirect call argument in print | R | — | **uncovered**: multi-output and heap call results printed directly | 6 | — |
| 30 | — | conditional | scalar, heap | none | print | — | — | `compileCondOperands` ANDs every argument's conditions and gates the whole `printAllExpressions` call, so one failed conditional suppresses its siblings | **S (behavior changes, §5)** | `mem/mem_cmp_lhs.spt`, `array/oob_print` | — | 6 | `compileCondOperands` |
| 31 | — | checked | scalar | none | print | — | — | no active bounds guard: prints the materialized zero | **S (behavior changes, §5)** | `array/oob_print` | a skipped outcome that itself owns a heap temporary (add with Step 6) | 5, 6 | §5 |
| 31b | — | checked, ranged | scalar, heap | none | print | call-site loop | — | `withCollectorPreparedLoopNest` around a checked access; zero per failed iteration | **S (behavior changes, §5)** | `array/oob_print` | heap-valued checked+ranged print | 5, 6, 8 | `withCollectorPreparedLoopNest` |
| 31c | none | checked, ranged | scalar, heap | local | assign | RHS-local | — | per-RHS bounds bit inside the loop nest | R | `math/func_array_range_oob`, `mem/leak/oob_paths` | — | 5, 7 | `commitAssignmentsPerExpr` |
| 32 | — | ranged | scalar | none | print | call-site loop | — | `withCollectorPreparedLoopNest` wraps the print in the argument's loop nest | S | `print_range`, `math/range.spt` | — | 8 | `withCollectorPreparedLoopNest` |
| 33 | — | call, ranged | scalar (direct), heap, multi-output (indirect) | none | print | call-site loop | both; cutover stays 8 | a ranged **call argument**; print never delegates its own argument iteration to a callee, since the solver forces the synthetic print call's `LoopInside` false | R | `math/print_func` (scalar direct only) | heap and multi-output ranged calls in print — uncovered; same Step 4 (validity variant, direct) / Step 6 (per-slot) prerequisites as rows 29b/29c | 8 | `withCollectorPreparedLoopNest` |
| 33b | — | ordinary | scalar | none | print | function-owned | — | a print statement **inside a function body** whose `Range` parameter drives the body; the print runs once per admitted point of the function-level domain | R | `math/range.pt` (`Triple`; both call sites are Range-driven), `math/acc_fmt.pt` | — | 6 (inner print), 7 (function domain) | — |
| 34 | — | collector, ranged | scalar | none | print | collector-local | — | `withCollectorPreparedLoopNest` + collector rewrite | S | `print_range`, `range` | — | 8 | collector rewrite |
| 35 | n/a | n/a | struct | `.pt` global constant | declaration | — | — | `compileStructStatement` → `compileConstBinding`; not an executable statement, so outside statement PIR | R | `struct/struct.pt` | — | n/a | — |
| 35b | none | ordinary | struct value copy | local | assign | — | — | ordinary assignment lowering | R | — | **uncovered**: local struct copy `s2 = s1` | 4 | `compileAssignments` |
| 35d | none | call | struct value | local | assign | — | split: all-`MustWrite` → 4, any-`MayWrite` → 6 | call lowering | R | — | **uncovered**: struct as a parameter or output | 4, 6 | — |
| 35c | — | ordinary | struct field read | none | print | — | — | print lowering + `compileDotExpression` | R | `struct/struct.spt` | — | 6 | — |
| 35e | — | ordinary | whole struct value | none | print | — | — | print lowering | R | `struct/struct.spt:5-7` | — | 6 | — |
| 36 | none | ordinary | table literal (plain cells) | local | assign | — (no domain) | — | `compileArrayExpression` → `compileTable`, cells via `compileArrayLiteralCell`; ranged cells are rejected | R | `array/array.spt`, `array/array_func.spt` | table in the leak suite | 4 | — |
| 36f | none | conditional, checked (cells) | table literal | local | assign | — (no domain) | — | as row 36, but a conditional or checked cell routes through `compileCondExprValue` | S | — | **uncovered**: conditional and checked table cells | 5, 6 | `compileCondExprValue` |
| 36b | none | ordinary | table value copy | local | assign | — | — | ordinary assignment lowering | R | — | **uncovered**: plain table copy `t2 = t1` | 4 | `compileAssignments` |
| 36d | none | call | table value | local | assign | — | split: all-`MustWrite` → 4, any-`MayWrite` → 6 | call lowering | R | all-`MustWrite`: `array/array_func.*`; any-`MayWrite`: `array/array_func.pt:40-43` + `.spt:63-66` (`ResetTable(-1)` keeps, `ResetTable(1)` writes) | — | 4, 6 | — |
| 36c | — | ordinary | whole table | none | print | — | — | print lowering | R | `array/array.spt` | — | 6 | — |
| 36e | — | ordinary | table column read | none | print | — | — | print lowering + column access | R | — | **uncovered**: printing one column | 6 | — |
| 36g | none | ordinary | table column read | local | assign | — | — | `compileDotExpression` yields the column array, then ordinary assignment | R | `array/array.spt:107` (`scoreColumn = scores.Score`) | — | 4 | `compileAssignments` |

Whole `Struct` and `Table` values are current capabilities, lowered through
ordinary assignment, calls, and printing above. `compileTable` builds columnar
storage directly and never opens a collector domain, so tables are not a
Step 8 collector capability; ranged table cells are rejected today. Only
**field, index, column, and cell LHS targets** are future features (plan §18);
they add rows when their source feature lands and do not affect the migration
clock.

## 3. Coverage gaps closed in this step

| Row | Gap | Fixture |
| --- | --- | --- |
| 27 | Ranged gate whose value is a heap-returning call — the per-iteration free-before-overwrite path had no test | `tests/mem/gate_heap/` (leak-checked) |
| 11, 23, 24 | Empty and fully-rejected domains: what a collector and a carry commit | `tests/cond/domain_activation.spt` |
| 5 | Blank targets: **zero** coverage in the entire corpus before this step | `tests/discard.spt` |
| 11, 23 | A function-returned `Range` driving a collector domain and a shared statement gate | `tests/mem/gate_heap/` |
| 30, 31 | Out-of-bounds and failed-conditional print arguments | `tests/array/oob_print.spt` |

Confirmed by these fixtures and now normative in the plan (§7, §10):

- an ungated collector over an empty domain commits `[]` — the fixture seeds
  the destination first, so an inactive collector would print the seed instead
- a gated collector admitting no point keeps the previous target
- an empty-range carry keeps the old target — the RHS adds a constant, so a
  spurious zero-th iteration would be observable
- a gate that rejects every point advances no carry
- a ranged gate over a heap-returning call keeps the last admitted value and
  leaks nothing

"The seed is not a write" is not provable from program output; it belongs to
the Step 2 effect tests.

## 4. Decided: `_` becomes a real discard sink

`_` is an ordinary typed binding that only the CFG exempts from liveness and
dead-write checks ([cfg.go:182](../compiler/cfg.go:182)). Duplicate blank
targets are permitted in one statement, but every blank slot resolves to the
same binding, so blanks alias each other. Measured on the current build:

| Case | Result |
| --- | --- |
| `_ = 1`, `_, b = FOuter(a, u)`, `c, _ = FOuter(a, u)` | works, no leak |
| `_` at a second type in one script | compile error: `cannot reassign type to identifier. Old Type: I64. New Type: Str. Identifier "_"` |
| `_, _ = twoStr("a", "b")` | runs, **leaks 16 bytes** (`str_concat`) |
| the same statement twice | **aborts, SIGTRAP (exit 133), no output** |

**Decision: `_` becomes a real per-slot discard sink** — never bound, one
independent sink per slot, with an owned outcome consumed at the exit of the
**smallest owning region**, so a discarded heap value produced inside a domain
is released per iteration rather than accumulated across the statement. PIR
gains the `discard` target of plan §6. This fixes the aliasing, the leak, the
abort, and the type collision together, and gives multi-output calls a way to
say "not this one". The alternative — deleting the special case so `_` is an
ordinary identifier — was rejected because it leaves no spelling for discarding
an output.

The outcome behind a discard keeps its type, arity position, and `YieldEffect`
so cleanup can be derived, but the discard publishes no target `WriteEffect`
and no CFG event; it needs no third write-lattice state.

The trade accepted: `_` stays a reserved special form across parser, solver,
CFG, PIR, and lowering.

Implementation lands in its own PR with the semantics-doc and rejection-test
changes, and with fixtures for repeated blanks, blanks at mixed types,
heap-valued blanks, repeated statements, and blanks under gates and ranges.
The evidence above is manual; the bug is still present in the working tree, and
the helper `twoStr` used to reproduce it is not in the repository.

## 5. Decided: printing is per-slot, not whole-line

Print lowering runs with no active assignment bounds guard, so today:

- an out-of-bounds print argument **materializes its zero and still prints**
- a failed conditional argument in the same position **prints nothing**
  ("the target-less case of propagation", per `compilePrintStatement`)

Those two failure kinds disagree, and neither matches assignment. **Decision:
print desugars to one single-slot emission per flattened output slot**
(plan §3) — it is deliberately *not* one N-ary call, which would make the
call-merge rule suppress the whole line. A failed argument suppresses only its
own emission, exactly as `arrVal, val1, val2 = arr[oob], x * y, x + y` keeps
`arrVal` and commits its siblings. So `arr[oob], val1, val2` prints `val1` and
`val2`.

Whole-line atomicity was considered and rejected: it would make print the one
construct where a failed outcome silences its siblings.

Two of today's behaviors change in Step 6, not one. Besides the OOB zero, a
failed *conditional* argument currently suppresses its successful siblings too,
because `compileCondOperands` ANDs every argument's conditions and gates the
whole `printAllExpressions` call. Both are pinned in
`tests/array/oob_print.spt`, whose multi-argument cases are what distinguish
per-slot from whole-line suppression.

Line-level suppression would instead belong to a **gated print**
(`arr[oob] val1, val2`, rejecting the region without evaluating siblings). That
syntax does not exist: `PrintStatement` has no gate field and the form does not
parse. It is proposed future syntax, and it needs its own parser/AST/solver PR,
semantics-doc entry, and capability rows before Step 6 — including the rule
that the gate tests the yielded/in-bounds bit, not the value.

A per-slot skip also cannot cross a direct-return call boundary today, since a
direct `I64`/`F64` result carries no validity bit (plan §15).

## 6. Correction to the plan's deletion order

Step 1 was expected to confirm that migrating assignments and prints frees the
conditional and collector machinery. **It does not.** These helpers have
callers in *ordinary expression lowering*:

| Helper | Expression-side callers |
| --- | --- |
| `withCollectorPreparedLoopNest` ([collect.go:99](../compiler/collect.go:99)), and all of `collect.go` transitively | `compileInfixRanges` ([compiler.go:2118](../compiler/compiler.go:2118)), `compilePrefixRanges` (:2462), `compileDirectCallWithRanges` (:3124), `compileIndirectCallWithRanges` (:3142), `compileArrayRangeRanges` ([array.go:1126](../compiler/array.go:1126)) |
| `compileCondOperands` ([cond.go:883](../compiler/cond.go:883)) | `compiler.go:2129`, `:2468`, `array.go:1127` |
| `compileCondExprValue` ([cond.go:873](../compiler/cond.go:873)) | `compileArrayLiteralCell` ([array.go:441](../compiler/array.go:441)), `compiler.go:3126`, `:3149` |
| the `extractSlotConds` family and the condLHS frame | reached from the two helpers above; `compileInfixExpression` also reads the frame directly ([compiler.go:1845](../compiler/compiler.go:1845)) |
| `withCondRangeLoop` ([cond.go:940](../compiler/cond.go:940)) | `withCollectorDomain` ([array.go:377](../compiler/array.go:377)) |
| `withCondBranch` ([cond.go:1479](../compiler/cond.go:1479)) | `commitCallOutputAdapters` ([compiler.go:2360](../compiler/compiler.go:2360)) |
| all of `loop.go` and `bounds.go` | ranged expression lowering and `array.go` indexing throughout |

**Resolution (plan §16 rule 5):** expression-side orchestration migrates into
nested PIR regions during Steps 6-8, and plan Step 9 splits into 9a
(statement-only helpers) and 9b (orchestration helpers, deleted after the
expression paths migrate). Primitives — arithmetic and comparison emission,
storage, loop and guard emission — survive either way.

One refinement to that split: `evalConditions`, `andGates`, and `compileGate`
belong in **9a** despite `withCondRangeLoop` being shared. Its guarded arm runs
only when `condExprs` is non-empty, and the statement path
([cond.go:1051](../compiler/cond.go:1051)) is the only source of a non-empty
`condExprs` — `array.go:284` passes nil and `collect.go:62` merely forwards.
So the arm, its parameter, and that trio all die with the statement path, while
`withCondRangeLoop` survives as loop-nest emission.

## 7. Dead routing found while tracing

Cleanup candidates, independent of PIR:

- `compileGate`'s nil return ([cond.go:36](../compiler/cond.go:36)) is
  unreachable: the solver diverts every range-driver condition to
  `compileCondRangedStatement` before `compileConditions` runs, and rejects
  non-failable conditions outright.
- `compileCondOperands`'s `baseCond` parameter is always `llvm.Value{}` at all
  five call sites.
- `branchCond`'s `onFalse` parameter receives an empty closure at both call
  sites.
- `compileCondOperands` and `compileCondExprValue` compute the same thing for
  `*ast.CallExpression` nodes; only the infix/prefix callers need the
  skip-the-node-itself variant.

## 8. Remaining thin rows

Deferred deliberately — each is cheap to add when its migration step starts:

- struct as a parameter, output, collector cell, or under a gate; no struct
  test exists in the leak suite despite `Str` fields (row 35)
- tables under a ranged gate or in the leak suite (row 36)
- a dependent range (`j = (i + 1):n`) combined with a collector and a gate in
  one callee
- a scalar-gated heap collector (row 18)
- ranged gate plus a blank slot, once §4 is executed
