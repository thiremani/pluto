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
| Literal layout (array and table literals) | inline (one source row), block (rows on their own lines: rank-2 arrays, tables) — not a table column; folded into Value kind on rows 2c and 36 |

**Callee output effect is a whole-call property.** A call with any `MayWrite`
output defers to Step 6 as a unit — argument evaluation, tuple failure, and
ownership are shared across its outputs, so individual slots cannot migrate
independently. An *argument* whose conditional or checked failure remains
unresolved (`MayYield`) likewise defers the whole call to Step 6 — row 6c is a
cross-cutting override of every call row, whatever the target or value kind:
the failure is resolved at the invocation boundary, and strict source-order
argument evaluation (plan §3) is the Step 6 behavior change that makes the
combination migratable. A fallback that resolves every failure leaves the
argument `MustYield` and outside this rule, though the router may still defer
it until its node is supported.

RHS flags compose: one RHS can be conditional, checked, ranged, collector, and
a call at once (`tests/math/func.spt` carries all six across its statements).
Only reachable combinations are listed; rectangles are collapsed where routing,
disposition, and cutover step are identical across a range of values.

Print has **no gate axis**: `ast.PrintStatement` carries only an expression, so
its conditionality comes entirely from cond-expressions inside its arguments.
A gated print (`arr[oob] val1, val2`) is scheduled syntax that does not
parse today; it gets its own axis value and rows when it lands, in its own
required PR before Step 6.

**Domain-role note.** `LoopInside` is the callee's flag, and the call site owns
the loop when it is *false*: `compileCallExpression` selects
`compileDirectCallWithRanges`/`compileIndirectCallWithRanges` under
`!info.LoopInside && len(info.Ranges) > 0`
([compiler.go:3213](../compiler/compiler.go#L3213) for the direct-return ABI,
[:3222](../compiler/compiler.go#L3222) for the indirect one).
`LoopInside=true` — callee-owned iteration — lowers through `compileCallInner`
**only for direct returns**; indirect and multi-output callees use
destination-seeded staged outputs instead.

Print never delegates argument iteration to a callee: the solver forces the
synthetic print call's `LoopInside` false. That is separate from a print
*inside* a function body, which is function-owned whenever a Range parameter
drives that body.

## 2. Reachable combinations

Rows are grouped by statement form, and each group has two parts: a table of
the **routing axes** the capability router keys on, and a collapsible list
giving each row's legacy route, coverage, and helpers. Row numbers are stable
across both, and are not renumbered when a row is split — a letter suffix
(`5b`, `14i`) keeps existing references valid.

Disposition: **R** retain as-is, **S** simplify during migration, **D** delete
the behavior (needs its own PR). Coverage counts `.spt`/`.pt` files.

*Helpers* lists **notable PIR-removable orchestration helpers** a row uses and
is deliberately non-exhaustive: primitives that survive migration — call
lowering, loop and guard emission, storage — are omitted, and a helper can be
deleted only once its last row has migrated. The helper-to-release-step
inventory is plan §16 Step 9 (9a statement-only, 9b orchestration); read the
two together rather than treating any single row as a deletion trigger.

### Assignment statements

| # | Gate | RHS flags | Value kind | Target | Domain role | Callee effect | Disp | Step |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | none | ordinary | scalar | local | — | — | R | 3 |
| 2 | none | ordinary | heap | local | — | — | R | 4 |
| 2b | none | ordinary | struct field read | local | — | — | R | 4 |
| 2c | none | ordinary | heap (block-layout array literal) | local | — | — | R | 4 (deferred: needs a one-line eval-operand spelling, plan §12) |
| 3 | none | ordinary | multi-output | local | — | — | R | 4 |
| 4 | none | ordinary (swap, dup source) | heap | local | — | — | R | 4 |
| 5 | none | ordinary | scalar, Range descriptor | blank (`_`) | — | — | R (sink shipped, §4) | 3 |
| 5b | none | ordinary | heap | blank (`_`) | — | — | R (sink shipped, §4) | 4 |
| 5c | none | call | scalar, heap, multi-output | blank (`_`) | — | split: all-`MustWrite` outputs and `MustYield` arguments → 4, otherwise → 6 | R (sink shipped, §4) | 4 (all-`MustWrite`) / 6 (any-`MayWrite`) |
| 5d | none | checked | scalar, heap | blank (`_`) | — | — | R (sink shipped, §4) | 5 |
| 5e | scalar | ordinary, call | scalar, heap, multi-output | blank (`_`) | — | both | R (sink shipped, §4) | 6 |
| 5f | none | call, ranged | scalar, heap, multi-output | blank (`_`) | RHS-local | all-`MustWrite` | R (sink shipped, §4) | 7 |
| 5h | none | ranged | scalar, heap | blank (`_`) | RHS-local | — | R (sink shipped, §4) | 7 |
| 5g | ranged | ordinary, call | scalar, heap | blank (`_`) | shared gate | both | R (sink shipped, §4) | 7 |
| 6 | none | call | scalar (direct return) | local | — | split: all-`MustWrite` outputs and `MustYield` arguments → 4, otherwise → 6 | R | 4, 6 |
| 6b | none | call | heap, multi-output (indirect return) | local | — | split: all-`MustWrite` outputs and `MustYield` arguments → 4, otherwise → 6 | R | 4, 6 |
| 6c | none | call, conditional or checked (unresolved argument) | any call value kind | any call target | — | both | **S (eager argument evaluation, plan §3; cross-cutting override of every call row)** | 6 |
| 7 | none | checked | scalar, heap | local | — | — | R | 5 |
| 7b | none | checked, conditional (fallback) | scalar, heap | local | — | — | **S (decided behavior change; semantics doc "Checked-access fallback")** | 5 |
| 7c | none | checked, conditional (fallback, propagated) | scalar, multi-output | local | — | both (call form) | **S (decided; semantics doc)** | 6 |
| 7e | none | checked, conditional (fallback), ranged | scalar | local | RHS-local | — | **S (decided; semantics doc)** | 7 |
| 7f | none | checked, conditional (fallback), collector | scalar | local | collector-local | — | **S (decided; semantics doc)** | 8 |
| 8 | none | ordinary | Range descriptor | local | — (no domain) | — | R | 3 |
| 8b | none | call | Range descriptor | local | — (no domain) | split: all-`MustWrite` outputs and `MustYield` arguments → 4, otherwise → 6 | R | 4, 6 |
| 9 | none | ranged | scalar, self-ref | local | RHS-local | — | R | 7 |
| 10 | none | ranged, checked | scalar | local | RHS-local | — | S | 7, fast path 10 |
| 11 | none | collector, ranged | scalar, heap | local | collector-local | — | S | 8 |
| 11b | none | collector, call, ranged, conditional, checked | scalar | local | collector-local | both; any-`MayWrite` also needs Step 6 — cutover stays 8 | S | 8 |
| 12 | none | call, ranged | scalar, heap, multi-output | local | call-site loop (`LoopInside=false`) | both; any-`MayWrite` also needs Step 6 call-result — cutover stays 7 | S | 7 |
| 13 | none | call, ranged | scalar, heap, multi-output | local | callee-owned (`LoopInside=true`) | both; any-`MayWrite` also needs Step 6 — cutover stays 7 | R | 7 |
| 14 | none | ordinary | scalar (direct return) | function output | — | — | R | 4 |
| 14i | none | ordinary | heap, multi-output (indirect return) | function output | — | — | R | 4 |
| 14a | scalar | ordinary, call | scalar, heap, multi-output | function output | — | both | R | 6 |
| 14c | none | collector | heap | function output | collector-local | — | R | 8 |
| 14b | none/scalar | ordinary, checked | scalar, heap | function output | function-owned | — | R | 7 |
| 14e | none/scalar | ordinary, collector, checked | scalar, heap | function output | function-owned + collector-local | — | R | 8 |
| 14d | none | ordinary, checked, ranged | scalar | function output | RHS-local (in function) | — | R | 7 |
| 15 | scalar | ordinary | scalar, multi-output | local | — | — | S | 6 |
| 16 | scalar | ordinary, call | heap, multi-output | local | — | both (Step 6 either way) | S | 6 |
| 17 | scalar | ordinary | self-referential | local | — | — | S | 6 |
| 18 | scalar | collector | scalar, heap | local | collector-local | — | S | 8 |
| 19 | scalar/none | conditional | scalar | local | — | — | S | 6 |
| 20 | scalar/none | conditional | multi-output (slot-aligned) | local | — | — | S | 6 |
| 21 | scalar/none | conditional, checked | scalar, heap | local | — | — | S | 5, 6 |
| 22 | scalar/none | conditional, ranged (logical tree) | scalar | local | RHS-local | — | S | 7 |
| 23 | ranged | ordinary | scalar, self-ref | local | shared gate | — | S | 7 |
| 24 | ranged | collector | scalar, heap | local | shared gate + collector-local | — | S | 8 |
| 25 | ranged | conditional | scalar | local | shared gate | — | S | 7 |
| 26 | ranged | call | multi-output | local | shared gate | both; any-`MayWrite` also needs Step 6 — cutover stays 7 | S | 7 |
| 27 | ranged | call | heap | local | shared gate | both; any-`MayWrite` also needs Step 6 — cutover stays 7 | S | 7 |
| 28 | ranged | checked | scalar | local | shared gate (affine index) | — | S | 5, 7 |
| 31c | none | checked, ranged | scalar, heap | local | RHS-local | — | R | 5, 7 |
| 35b | none | ordinary | struct value copy | local | — | — | R | 4 |
| 35d | none | call | struct value | local | — | split: all-`MustWrite` outputs and `MustYield` arguments → 4, otherwise → 6 | R | 4, 6 |
| 36 | none | ordinary | table literal (plain cells) | local | — (no domain) | — | R | 4 |
| 36f | none | conditional, checked (cells) | table literal | local | — (no domain) | — | S | 5, 6 |
| 36b | none | ordinary | table value copy | local | — | — | R | 4 |
| 36d | none | call | table value | local | — | split: all-`MustWrite` outputs and `MustYield` arguments → 4, otherwise → 6 | R | 4, 6 |
| 36g | none | ordinary | table column read | local | — | — | R | 4 |

<details><summary>Routes, coverage, and helpers</summary>

- **1** — `compileAssignments`. *Tests:* `arithmetic`, `op`, `unary`, `numeric_literals`, `zero_div`. *Helpers:* `compileAssignments`
- **2** — `compileAssignments` → `commitAssignments`; **now planned** at script root for ordinary expressions (string literals including multiline ones, concatenations, inline array literals, binding reads) — block-layout literals stay legacy (rows 2c, 36). *Tests:* `mem/mem.spt` (incl. widened bindings and the empty reset into a differently typed binding), `str`, `multiline_str`, `array_concat`, `cond_copy`; goldens `TestPlanGoldenMaterialize`, `TestPlanGoldenArrays`, `TestPlanGoldenEffectiveStorage*`, `TestPlanGoldenMultilineString` in `compiler/pir_test.go`. *Helpers:* `commitAssignments`
- **2b** — `compileDotExpression` extracts the field, then ordinary assignment; **now planned** (a struct field is a constant, so the outcome is unmanaged). A field or column read whose receiver is a widened binding stays legacy (`TestPlanRouterRejectsWidenedReceiver`; `mem/mem.spt` `takenTable.Value`). *Tests:* `struct/struct.spt` (`copiedName = p.name`); golden `TestPlanGoldenStructAndTable`
- **2c** — a headerless block-layout array literal (rank-2 rows on their own lines): `compileArrayExpression` → `compileArray`, ordinary assignment. Still legacy after Step 4's first slice for the same reason as row 36: the literal prints on several lines and the eval operand has no one-line spelling yet (plan §12). *Tests:* `array/array.spt` (`oneRowMatrix`, `rank3`, `stringMatrix`)
- **3** — `compileAssignments`, arity via `newExprAssign`. *Tests:* `partial_returns`, `math/div`, `mem/mem.spt`. *Helpers:* `exprAssign` machinery
- **4** — `commitAssignments` copy/move marking; **now planned**: `pir.Elaborate` promotes a borrow to transfer when its owner is replaced in the group and copies every later borrow of that owner. *Tests:* `mem/mem.spt:64,78`; goldens `TestPlanGoldenHeapSwap`, `TestPlanGoldenDuplicateSource`, `TestPlanGoldenReplaceAndDiscard`. *Helpers:* `markCopyRequirements`, `freeExprOldValues`, `deepCopyIfNeeded`
- **5** — per-slot sink: never bound, never typed (`isDiscard`), CFG-exempt. *Tests:* `discard`; the bare Range-descriptor discard is pinned by `TestPlanGoldenRangeDiscard` in `compiler/pir_test.go`
- **5b** — per-slot sink; a discarded temporary is dropped (`drop`), a discarded named value stays borrowed; **now planned** for ordinary heap expressions: the derived `drop %tN` appears in expanded PIR. *Tests:* `discard`; golden `TestPlanGoldenReplaceAndDiscard`
- **5c** — discarded call outputs keep their yield/write validity for cleanup; the whole-call rule is unchanged, so an any-`MayWrite` call defers to Step 6 even when every output is discarded. *Tests:* `discard`: all-`MustWrite` multi-output, both scalar (`FOuter`) and heap (`twoStr`); any-`MayWrite` heap (`maybeStr`, writing and non-writing paths). *Missing:* all-`MustWrite` direct-scalar (single-output) call; any-`MayWrite` direct-scalar and multi-output callees with all outputs discarded
- **5d** — a blank needs no seed on the skip path, so `ensureSeededDest` leaves it unbound. *Tests:* `discard` (failed and admitted, scalar and heap element). *Helpers:* `commitAssignmentsPerExpr`
- **5e** — `commitConditionalOutputs` frees the blank's temp instead of binding it, on both the admitted and skipped paths. *Tests:* `discard`: all-`MustWrite` heap multi-output call (`twoStr`), gate admitting and rejecting. *Missing:* scalar-valued and ordinary-RHS gated blanks; any-`MayWrite` gated call. *Helpers:* `compileCondStatement`
- **5f** — `bindRangedTempOutputs` skips blanks, so ranged staging leaves no transient binding; the discarded value is released per iteration. *Tests:* `discard`: `_ = mkTag(i)` (single output), `_, _ = twoStr(mkTag(i), "z")` (multi-output). *Missing:* both fixtures are heap-valued, so the all-`MustWrite` scalar ranged call is uncovered, as is any-`MayWrite` with its outputs discarded. *Helpers:* `compileAssignments`
- **5h** — a ranged blank whose RHS is not a call, so the value comes from the expression loop nest rather than staged call outputs. *Missing:* **uncovered**: ordinary ranged blank such as `_ = i + 1`. *Helpers:* `compileAssignments`
- **5g** — as 5f under a ranged gate: a rejected point produces no value to discard. *Missing:* **uncovered**: blank under a ranged gate. *Helpers:* `compileCondRangedStatement`
- **6** — `compileCallExpression` → `compileCallInner`. *Tests:* `math/rec.spt`, `math/div`
- **6b** — destination-seeded output slots via `compileIndirectCallIntoStagedOutputs`. *Tests:* `const_args/*`, `output_refinement`, `mem/mem.spt`
- **6c** — an argument whose conditional or checked failure remains unresolved makes the invocation `MayYield` (call-merge, plan §9) regardless of callee effect, so the whole call defers to Step 6 even when every output is `MustWrite`; this overrides every call row above — 5c, 6, 6b, 8b, 35d, 36d, and the function-output and ranged call rows — whatever the target or value kind. An inner fallback that resolves every failure leaves the argument `MustYield` and outside this row. Today `compileCondOperands` evaluates the extracted conditions first and the sibling arguments only on the success branch, so `r = Pair(Loud(a), c > 5)` never runs `Loud`; plan §3 makes argument evaluation strict in source order. *Missing:* **uncovered** — regressions `F(sideEffect(), failingArg)` and `F(failingArg, sideEffect())` land with Step 6. *Helpers:* `compileCondOperands`
- **7** — `compileExprAssigns` bounds bit → `commitAssignmentsPerExpr`. *Tests:* `array/oob_skip`, `mem/leak/oob_paths`. *Helpers:* `commitAssignmentsPerExpr`
- **7b** — **rejected today**: `arr[oob] \|\| -1` fails "logical OR in value position requires a conditional left operand"; Step 5 adds a fallback-specific rule (checked-access root immediately left of `\|\|`) without widening `conditionPropagates`. *Missing:* regressions when implemented: `x = arr[oob] \|\| -1` → `-1`, in-bounds zero → `0`, heap `sarr[oob] \|\| "d"`. *Helpers:* condLHS spine
- **7c** — comparison- and call-propagated fallback: `arr[oob] > 0 \|\| -1`, `Id(arr[oob]) \|\| -1` — the failure travels through a propagator before the resolver. *Missing:* regressions when implemented. *Helpers:* condLHS spine
- **7e** — ranged checked fallback: the fallback resolves per iteration inside the loop nest. *Missing:* regressions when implemented. *Helpers:* condLHS spine, ranged staging
- **7f** — collector-cell fallback: in `[arr[oob] \|\| -1]` the `\|\|` resolves before the cell's zero-fill. *Missing:* regressions when implemented. *Helpers:* collector rewrite, condLHS spine
- **8** — plain value copy; the solver clears `Ranges`/`HasRanges`, so this is not an active ranged RHS. *Tests:* `range_finalize:2-21` (literal, identifier copy, empty, reassign), `compiler/solver_test.go`. *Helpers:* `compileAssignments`
- **8b** — call lowering + indirect-return ABI, not descriptor copying. *Tests:* `range_finalize:38` (`makeRange`), `mem/gate_heap`. *Missing:* conditionally-written Range return (any-`MayWrite`)
- **9** — `compileAssignments` → expression loop nest (passes nil conditions). *Tests:* `math/range_expr`, `math/range.spt`, `range_shadow.spt`, `cond/domain_activation`. *Helpers:* `compileAssignments`, `withCollectorPreparedLoopNest`, `compileCondOperands`
- **10** — as #9 + `withLoopNestVersioned` affine probe. *Tests:* `array/affine_bounds_stmt`, `math/affine_bounds_expr`. *Helpers:* affine decision helpers
- **11** — `compileArrayExpression` → `compileArray` → `withCollectorDomain`. *Tests:* `range`, `array/array_capture`, `mem/gate_heap`. *Helpers:* collector rewrite
- **11b** — a call inside a collector cell, its arguments possibly conditional/checked/ranged. *Tests:* `math/func.spt:58-69` (`[Square(arr[1:3] > 3)]`, `[Square(arrSelf[1:4])]`). *Missing:* nested any-`MayWrite` call in a collector — every cited call is `Square`, all-`MustWrite`; add a conditional direct or indirect call fixture when this rectangle migrates. *Helpers:* collector rewrite, condLHS spine
- **12** — `compileDirectCallWithRanges` / `compileIndirectCallWithRanges`. *Tests:* `math/func_range`, `math/func_array_range`, `math/func_nested_range` (calls with collector arguments), `mem/mem_alias_refine.spt`. *Helpers:* `compileAssignments`, `compileCondExprValue`, `withCollectorPreparedLoopNest`
- **13** — callee body iterates: `compileCallInner` for direct returns, destination-seeded staged outputs for indirect and multi-output. *Tests:* `math/acc.spt`, `math/acc_desc`, `array/array_range.spt:89-96` (heap via indirect ABI)
- **14** — ordinary function-body statement; a direct `I64`/`F64` output is an SSA value with **no** runtime write flag. *Tests:* `math/acc.pt`
- **14i** — ordinary function-body statement; an indirect output has a runtime write flag set on commit. *Tests:* `mem/mem_alias_refine.pt`, `mem/mem.pt`
- **14a** — gated function-body statement: the output is conditionally written, which is what makes the callee `MayWrite` at its boundary (`IsEven`/`IsOdd` conditionally write their indirect output pair). *Tests:* `math/math.pt`, `math/rec.pt`, `mem/mem_cmp_lhs.pt`. *Helpers:* `compileCondStatement`
- **14c** — a body-local collector domain, i.e. a collector driven by a range created inside the body rather than by a parameter. *Missing:* **uncovered**: `cache_reuse.pt` uses fixed literals, `array_scalar_assign.pt` is parameter-driven (row 14b). *Helpers:* collector rewrite
- **14b** — body driven by a `Range`/`ArrayRange` **parameter**, whose domain wraps the whole body and may execute zero times — this is what weakens the output to `MayWrite` at the boundary. *Tests:* `array/array_range.pt`
- **14e** — as 14b with an inner collector: `ArraySetAdd(1:4)` runs a function-owned outer domain around a collector-local `[0:i]`, so full cutover needs collectors. *Tests:* `array/array_scalar_assign.pt` (`ArraySetAdd`), `array/array_func.pt`. *Helpers:* collector rewrite
- **14d** — a range created **inside** the body drives one statement; the output is still `MayWrite` at the boundary because that local range can be empty — only the blanket function-owned weakening is absent. *Tests:* `math/dependent_range.pt` (`j = (i + 1):n`)
- **15** — `compileCondStatement`. *Tests:* `assign`, `initialize`, `zero_val`, `partial_returns`. *Helpers:* `compileCondStatement`
- **16** — `compileCondStatement` + `prePromoteConditionalCallArgs`. *Tests:* `cond_copy`, `mem/mem_str.spt`, `math/math.pt`. *Helpers:* staging family
- **17** — `compileCondStatement` + `aliasCondDests`. *Tests:* `tests/cond/expr_forms`. *Helpers:* `aliasCondDests`
- **18** — `compileCondStatement` → ordinary collector inside the IF block. *Tests:* `mem/cache_reuse/cache_reuse.pt`. *Missing:* scalar-gated heap collector. *Helpers:* collector rewrite
- **19** — `compileCondExprStatement` → `compileCondExprValue`. *Tests:* `cond/value_cond_expr`, `cond/expr_forms`, `math/func.spt`. *Helpers:* `compileCondExprStatement`
- **20** — `compileCondExprStatement` → `compilePerSlotAssign`. *Tests:* `cond/value_cond_expr`, `cond/logical_and`. *Helpers:* `compilePerSlotAssign`
- **21** — `compileCondExprStatement` + bounds guard. *Tests:* `array/oob_skip`, `cond/logical_and`. *Helpers:* condLHS spine
- **22** — `compileCondExprStatement` → `stageCondRangedExpr`. *Tests:* `cond/logical_and:143,413`. *Helpers:* ranged staging
- **23** — `compileCondRangedStatement` → `stageCondRangedAssignments`. *Tests:* `array/cond_accum`, `cond/condition_boundary`. *Helpers:* `compileCondRangedStatement`
- **24** — `compileCondRangedStatement` → `newStatementArrayCollector`. *Tests:* `array/cond_accum`, `cond/domain_activation`. *Helpers:* `statementArrayCollector` trio
- **25** — `compileCondRangedIteration` → `compileCondExprValue`. *Tests:* `cond/value_cond_expr`, `array/array_expr`. *Helpers:* `compileCondExprValue`
- **26** — `compileCondRangedStatement` → `perSlotCommittable`. *Tests:* `cond/value_cond_expr:740`. *Helpers:* `perSlotCommittable`
- **27** — `compileCondRangedStatement` → stage temp. *Tests:* `mem/gate_heap`. *Helpers:* ranged staging
- **28** — ranged gate over an affine access. *Tests:* `array/cond_accum:416,420`. *Helpers:* affine decision helpers
- **31c** — per-RHS bounds bit inside the loop nest. *Tests:* `math/func_array_range_oob`, `mem/leak/oob_paths`. *Helpers:* `commitAssignmentsPerExpr`
- **35b** — ordinary assignment lowering; **now planned** (a struct value is unmanaged: its fields are constants). *Tests:* `struct/struct.spt` (`s2 = p`, whole-struct copy); golden `TestPlanGoldenStructAndTable`. *Helpers:* `compileAssignments`
- **35d** — call lowering. *Missing:* **uncovered**: struct as a parameter or output
- **36** — `compileArrayExpression` → `compileTable`, cells via `compileArrayLiteralCell`; ranged cells are rejected. Still legacy after Step 4's first slice: a table literal is block-layout and has no one-line eval-operand spelling yet (plan §12). *Tests:* `array/array.spt`, `array/array_func.spt`. *Missing:* table in the leak suite
- **36f** — as row 36, but a conditional or checked cell routes through `compileCondExprValue`. *Missing:* **uncovered**: conditional and checked table cells. *Helpers:* `compileCondExprValue`
- **36b** — ordinary assignment lowering; **now planned** (a table read is a borrow that copies). *Tests:* `array/array.spt` (`savedScores = scores`); golden `TestPlanGoldenStructAndTable`. *Helpers:* `compileAssignments`
- **36d** — call lowering. *Tests:* all-`MustWrite`: `array/array_func.*`; any-`MayWrite`: `array/array_func.pt:40-43` + `.spt:63-66` (`ResetTable(-1)` keeps, `ResetTable(1)` writes)
- **36g** — `compileDotExpression` yields the column array, then ordinary assignment; **now planned** (the copied column is an owned outcome that moves). *Tests:* `array/array.spt:107` (`scoreColumn = scores.Score`); golden `TestPlanGoldenStructAndTable`. *Helpers:* `compileAssignments`

</details>

### Print statements

| # | Gate | RHS flags | Value kind | Target | Domain role | Callee effect | Disp | Step |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 7d | — | checked, conditional (fallback) | scalar, heap | none | — | — | **S (decided; semantics doc)** | 6 |
| 29 | — | ordinary | scalar, heap | none | — | — | S | 6 |
| 29b | — | call | scalar (direct return) | none | — | both; any-`MayWrite` needs the Step 4 validity variant | R | 6 |
| 29c | — | call | heap, multi-output (indirect return) | none | — | both; indirect outputs already carry write flags — no variant needed | R | 6 |
| 30 | — | conditional | scalar, heap | none | — | — | **S (suppression outcome retained; eager sibling evaluation is a behavior change, §5)** | 6 |
| 31 | — | checked | scalar | none | — | — | **S (behavior changes, §5)** | 5, 6 |
| 31b | — | checked, ranged | scalar, heap | none | call-site loop | — | **S (behavior changes, §5)** | 5, 6, 8 |
| 32 | — | ranged | scalar | none | call-site loop | — | S | 8 |
| 33 | — | call, ranged | scalar (direct), heap, multi-output (indirect) | none | call-site loop | both; cutover stays 8 | R | 8 |
| 33b | — | ordinary | scalar | none | function-owned | — | R | 6 (inner print), 7 (function domain) |
| 34 | — | collector, ranged | scalar | none | collector-local | — | S | 8 |
| 35c | — | ordinary | struct field read | none | — | — | R | 6 |
| 35e | — | ordinary | whole struct value | none | — | — | R | 6 |
| 36c | — | ordinary | whole table | none | — | — | R | 6 |
| 36e | — | ordinary | table column read | none | — | — | R | 6 |

<details><summary>Routes, coverage, and helpers</summary>

- **7d** — print-position fallback: `arr[oob] \|\| -1, val1` emits `-1 val1` — the fallback resolves before the invocation boundary, letting the whole line print. *Missing:* regressions when implemented, incl. a heap-valued print fallback. *Helpers:* condLHS spine
- **29** — `compilePrintStatement` direct arm. *Tests:* `helloworld`, `str`, `1.2-report`. *Helpers:* `printAllExpressions`, `compilePrintStatement`
- **29b** — non-ranged direct call argument in print; an unwritten result must suppress the invocation, needing `{value, didWrite}` to feed the all-arguments-yielded condition. *Tests:* `math/print_func.spt:4` (`Square(3)`). *Missing:* conditional direct-return argument (Step 4 prereq)
- **29c** — non-ranged indirect call argument in print. *Missing:* **uncovered**: multi-output and heap call results printed directly
- **30** — `compileCondOperands` ANDs every argument's conditions and gates the one `printAllExpressions` call. *Tests:* `mem/mem_cmp_lhs.spt`, `array/oob_print`. *Missing:* side-effecting or owned-heap sibling of a failed conditional (add with Step 6). *Helpers:* `compileCondOperands`
- **31** — no active bounds guard: prints the materialized zero today; Step 6 makes an unresolved OOB suppress the complete invocation and newline. *Tests:* `array/oob_print`. *Missing:* a suppressed invocation whose arguments own heap temporaries (add with Step 6). *Helpers:* §5
- **31b** — `withCollectorPreparedLoopNest` around a checked access; zero per failed iteration today, no line for that iteration after Step 6. *Tests:* `array/oob_print`. *Missing:* heap-valued checked+ranged print. *Helpers:* `withCollectorPreparedLoopNest`
- **32** — `withCollectorPreparedLoopNest` wraps the print in the argument's loop nest. *Tests:* `print_range`, `math/range.spt`. *Helpers:* `withCollectorPreparedLoopNest`
- **33** — a ranged **call argument**; print never delegates its own argument iteration to a callee, since the solver forces the synthetic print call's `LoopInside` false. *Tests:* `math/print_func` (scalar direct only). *Missing:* heap and multi-output ranged calls in print — uncovered; same Step 4 (validity variant, direct) / Step 6 (invocation gating) prerequisites as rows 29b/29c. *Helpers:* `withCollectorPreparedLoopNest`
- **33b** — a print statement **inside a function body** whose `Range` parameter drives the body; the print runs once per admitted point of the function-level domain. *Tests:* `math/range.pt` (`Triple`; both call sites are Range-driven), `math/acc_fmt.pt`
- **34** — `withCollectorPreparedLoopNest` + collector rewrite. *Tests:* `print_range`, `range`. *Helpers:* collector rewrite
- **35c** — print lowering + `compileDotExpression`. *Tests:* `struct/struct.spt`
- **35e** — print lowering. *Tests:* `struct/struct.spt:5-7`
- **36c** — print lowering. *Tests:* `array/array.spt`
- **36e** — print lowering + column access. *Missing:* **uncovered**: printing one column

</details>

### Declarations

| # | Gate | RHS flags | Value kind | Target | Domain role | Callee effect | Disp | Step |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 35 | n/a | n/a | struct | `.pt` global constant | — | — | R | n/a |

<details><summary>Routes, coverage, and helpers</summary>

- **35** — `compileStructStatement` → `compileConstBinding`; not an executable statement, so outside statement PIR. *Tests:* `struct/struct.pt`

</details>

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

Confirmed by these fixtures and now normative in the plan (plan §7, plan §10):

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

## 4. Shipped: `_` is a real per-slot discard sink

`_` used to be an ordinary typed binding that only the CFG exempted from
liveness and dead-write checks, while duplicate blank targets were permitted —
so every blank slot resolved to the *same* binding and blanks aliased each
other. Measured before the fix:

| Case | Before | After |
| --- | --- | --- |
| `_ = 1`, `_, b = FOuter(a, u)`, `c, _ = FOuter(a, u)` | works, no leak | unchanged |
| `_` at a second type in one script | compile error: `cannot reassign type to identifier. Old Type: I64. New Type: Str. Identifier "_"` | works |
| `_, _ = twoStr("a", "b")` | runs, **leaks 16 bytes** (`str_concat`) | no leak |
| the same statement twice | **aborts, SIGTRAP (exit 133), no output** | runs clean |

**`_` is now a per-slot sink: never bound, never typed.** The solver skips it
when binding LHS names, so it accumulates no type to collide with; `writeTo`
routes it to `drop` instead of `storeValue`; and the conditional
commit frees its temp rather than binding it. Ownership follows the borrow
rule — a discarded *temporary* is owned by the statement and released, while a
discarded value read from a *named* variable is borrowed and survives. A
discarded heap value produced inside a domain is released per iteration.
`tests/discard.spt` covers repeated blanks, mixed types, heap outcomes,
repeated statements, borrowed survival, and blanks under gates and ranges.

The alternative — deleting the special case so `_` is an ordinary identifier —
was rejected because it leaves no spelling for discarding an output. The trade
accepted: `_` stays a reserved special form across parser, solver, CFG, PIR,
and lowering.

In PIR terms the outcome behind a discard keeps its type, arity position, and
`YieldEffect` so cleanup can be derived, but the discard publishes no target
`WriteEffect` and no CFG event; it needs no third write-lattice state. Arity
still counts blank slots, so a multi-output RHS needs one blank per output.

## 5. Decided: print — one N-ary invocation, call-level atomicity

Print lowering runs with no active assignment bounds guard, so today:

- an out-of-bounds print argument **materializes its zero and still prints**
- a failed conditional argument **suppresses the whole line**
  (`compileCondOperands` ANDs every argument's conditions and gates the one
  `printAllExpressions` call)

**Decision: print is one N-ary intrinsic invocation, and the ordinary
call-merge rule applies** (plan §3). The invocation runs exactly once per
admitted point, only when every flattened argument slot yielded; an argument
failure that no closer `||` resolves suppresses the complete invocation and
its newline. A fallback resolves before the boundary:
`a, arr[oob] || -1, arr2[oob] || -2, b` emits `a -1 -2 b`, while
`a, arr[oob], arr2[oob], b` emits nothing. When every argument yields, slots
format in source order with one space between adjacent ordinary single-line
slots; a slot's formatter keeps its internal layout, so a whole-`Struct` slot
spans several lines inside the one invocation. An empty string is a
successful value, so `a, "", "", b` emits `a` and `b` separated by three
spaces, distinguishable from suppression. The atomic unit is the invocation —
one emission group — not necessarily one physical line.

The suppression outcome for a failed conditional matches today, but
evaluation becomes **eager**: today `compileCondOperands` evaluates the
conditions first and compiles sibling arguments only on the success branch,
while `PrintPlan` evaluates every argument before ANDing. Step 6 therefore
changes two observable things — the OOB materialized zero becomes invocation
suppression, and sibling side effects now occur even on a suppressed
invocation. Row 30's suppression outcome is retained while its lowering
reroutes off `compileCondOperands` and its siblings become eagerly evaluated;
the baseline for both failure kinds is pinned in `tests/array/oob_print.spt`.

Per-slot printing (a failed slot omitting only itself) was considered and
rejected in favor of the call rule: print is a call, and calls merge lanes.
Line-level suppression that skips *evaluating* siblings would instead belong
to a **gated print** (`arr[oob] val1, val2`) — scheduled syntax that
does not parse today (`PrintStatement` has no gate field) and lands in its
own required PR before Step 6, with semantics-doc entry and capability rows.

An unwritten direct-return argument cannot yet suppress the invocation, since
a direct `I64`/`F64` result carries no validity bit; the Step 4
`{value, didWrite}` variant feeds its bit into the invocation's
all-arguments-yielded condition (plan §15).

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
- a blank slot under a *ranged* gate (scalar gates, ranges, checked access, and ranged multi-output blanks are covered by `tests/discard.spt`)
