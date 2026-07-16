# Pluto Statement IR (PIR) Plan

**Status:** Proposed for review

**Scope:** A typed, structured execution plan for one Pluto statement

**Primary motivation:** Make ranges, conditions, conditional values, bounds
failures, collectors, loop-carried updates, affine versioning, and final LHS
assignment explicit before LLVM lowering

**Related semantics:** [Pluto Conditional Value Semantics](./Pluto%20Conditional%20Value%20Semantics.md), [Pluto Range Semantics](./Pluto%20Range%20Semantics.md), [Pluto Memory Model](./Pluto%20Memory%20Model.md)

## 1. Decision

PIR v1 should be a **statement execution plan**, not a general-purpose value IR.
It answers four questions for each statement:

1. What state or collectors must exist before evaluation?
2. Over which ranges and conditions should the statement execute?
3. Which values yield, skip, accumulate, or advance on each iteration?
4. What final values are set on the LHS after evaluation finishes?

The pipeline becomes:

```text
Pluto source
  -> lexer -> tokens
  -> parser -> AST
  -> semantic solver -> typed/resolved AST + ExprInfo
  -> CFG/dataflow validation (use-before-definition, dead writes)
  -> PIR statement builder -> statement plans
  -> PIR validator -> validated plans
  -> PIR-to-LLVM lowering -> LLVM IR
  -> LLVM optimization
  -> native object emission (.o)
  -> linker + Pluto runtime objects
  -> executable
```

Responsibilities stay decoupled: the solver owns types, ranges, output
shapes, and whether an expression may yield conditionally; the CFG owns
cross-statement dataflow legality (use-before-definition, dead writes,
write-after-write); PIR owns how an already-valid statement executes; LLVM
owns SSA, storage, optimization, and ABI. The CFG never reads PIR regions —
it consumes a small solver-recorded write summary, and PIR consumes the same
summary independently. The summary is **per LHS slot**, not per statement,
because a mixed assignment can conditionally skip one target while another
always writes: `a, b = arr[i], i + 1` is `[]WriteEffect{MayWrite, MustWrite}`
(the indexed read is bounds-guarded, the arithmetic always lands). The solver
derives each slot's final effect from shared statement conditions, possibly
empty ranges, conditional-yield propagation, potentially failing checked
accesses, and the nearest resolver. A checked or conditional outcome is
`MayWrite` unless a fallback or closing policy resolves every failure path
before the final set; for example, `x = arr[i] > 0 || 0` is `MustWrite`, while
`x = arr[i] > 0 || other[j] > 0` remains `MayWrite`. This matches the compiler's
existing solver-then-CFG order, so migration requires no pass reordering.

PIR may refer to solved AST expressions, but LLVM lowering must not reclassify
their range, conditional, OOB, collector, affine, or commit behavior.

## 2. Deliberate Abstraction Level

PIR records source-language execution decisions:

- Pluto types such as `Int`, `String`, and `Array(Int)`
- LHS targets and simultaneous assignment groups
- range bindings and nesting order
- statement gates and lazy value-position `&&`
- fallback and yield/skip behavior
- per-value, per-slot, per-element, and per-iteration outcomes
- checked accesses and the scope affected by OOB
- loop-carried values and collectors
- affine access forms and versioning decisions
- final keep-old, zero-fill, append, or last-yield policies

PIR does not contain:

- LLVM types such as `i64`
- `llvm.Value`, LLVM blocks, or target-specific layout
- SSA registers, phi nodes, allocas, pointers, loads, or stores
- register-versus-memory decisions
- ABI details or concrete cleanup blocks

Immutable plan nodes and stable IDs are sufficient. LLVM remains responsible for
machine-level SSA and storage. A fuller value IR should be introduced only if
Pluto later needs substantial cross-statement optimization before LLVM.

## 3. Statement Lifecycle

Every assignment plan has four ordered phases:

| Phase | Responsibility |
| --- | --- |
| `prepare` | Establish carried values, collectors, targets, and range inputs |
| `execute` | Run versioned ranges, statement gates, value-position `&&`, fallbacks, yields, skips, collections, and carried updates |
| `finish` | Close collectors and select final carried or collected outcomes |
| `set` | Apply final outcomes to all LHS targets simultaneously |

The phases describe semantics, not allocations. For example, `carry sum` in
the prepare phase does not require a stack slot; it means that reads of `sum`
inside the statement observe the statement's current carried value.

## 4. Core Vocabulary

PIR should use structured control plus a small Pluto-specific vocabulary:

| Operation | Meaning |
| --- | --- |
| `eval(expr)` | Evaluate a solved Pluto expression or ordinary expression fragment |
| `carry` | Declare state that may advance across iterations (prepare phase) |
| `collector` | Declare a logical collection result before its loops (prepare phase) |
| `for` | Iterate one resolved range domain |
| `if` | Apply ordinary structured control |
| `gate` | Admit one shared statement iteration; rejection suppresses every RHS outcome and iteration update |
| `value-and` | Lazily evaluate a local value region only when its left outcome yields |
| `fallback` | Lazily evaluate an alternative for missing outcomes |
| `map` | Apply ordinary expression work to yielded child outcomes |
| `align` | Apply explicit slot, zip-min, or broadcast alignment |
| `yield` | Produce a value from the current value or cell region |
| `skip` | Produce no value; the failure propagates to the nearest resolving region |
| `continue` | Reject the rest of one range iteration |
| `break` | Exit a loop because of source-language break semantics |
| `collect` | Add a yielded cell according to the collector policy |
| `advance` | Replace loop-carried state at the end of an iteration |
| `drop` | Derived at region exit: free an owned outcome no consumer took (printed in expanded PIR, never authored by the builder) |
| `finish` | Close a carry or collector into a final outcome |
| `set` | Assign final outcomes to semantic LHS targets |

Every operation corresponds to a documented language rule in the semantics
docs; a new operation requires its rule to be written there first, so the
vocabulary cannot grow ahead of the language.

`for`, `if`, `break`, and `continue` alone are not enough. A `skip` is
distinct from `continue`: one RHS may fail while sibling RHS expressions still
update during the same iteration. A `skip` names no scope of its own — it propagates outward to
the nearest resolving region (a `fallback`, a collector cell boundary, an
`advance`, or the final `set`), mirroring the language rule that a failure
propagates to its nearest resolver. It must remain visible to a surrounding
`fallback` before any coarser region resolves it.

## 5. Plan Results

Each value-producing plan node has an abstract outcome:

| Property | Examples |
| --- | --- |
| Outputs | `Int`, `(Int, String)`, `Array(Int)` |
| Domain | scalar, fixed output slots, array elements, range iterations |
| Yield shape | always, scalar condition, per-slot bits, element mask, per-iteration |

Zero is never a missing-value marker. A successful comparison may yield zero,
so value and yield information remain conceptually separate even though PIR does
not expose machine bits.

`eval` leaves may retain references to typed AST nodes. The builder splits out
operations that affect evaluation strategy, including ranges, lazy `&&`/`||`,
conditional propagation, and collectors. Ordinary arithmetic and calls can stay
inside `eval` or `map` regions and continue to use the existing expression
compiler.

## 6. LHS Targets and Final Set

PIR calls LHS locations **targets**, not places or memory addresses:

| Target | Meaning |
| --- | --- |
| `local(name)` | Local or output binding |
| `field(base, field)` | Resolved struct field |
| `index(base, expression)` | Resolved array element target |
| `column(table, column)` | Future table column target |
| `cell(table, row, column)` | Future table cell target |

Targets are evaluated exactly once at the phase required by Pluto's eventual
assignment semantics. The PIR-to-LLVM lowerer chooses pointers, copies, moves,
and cleanup paths.

All RHS expressions in one assignment group are evaluated before final `set`.
The targets are then updated simultaneously. This preserves swaps, sibling
self-references, and ownership safety without exposing temporary storage in PIR.

## 7. Loop-Carried State

Ranged assignments that read their own LHS require explicit loop-carried state.
For example, repeated evaluation of:

```pluto
sum = sum + 1
arr = arr ⊕ [2]
```

must make iteration `n + 1` observe the values produced by iteration `n`, while
the real LHS targets are set only after the statement's range execution ends.

PIR models this with `carry` and `advance`:

```text
statement sum, arr
    prepare
        carry sum from local(sum)
        carry arr from local(arr)

    execute
        for i in range(0, n)
            iteration
                next sum from eval(carry(sum) + 1)
                next arr from eval(carry(arr) ⊕ [2])

                advance simultaneously
                    carry sum from next sum on-skip keep-old
                    carry arr from next arr on-skip keep-old

    finish
        final sum from carry(sum)
        final arr from carry(arr)

    set simultaneously
        local(sum) from final sum
        local(arr) from final arr
```

The `next` and `final` labels are plan-result names, not SSA registers or storage.

Loop-carried evaluation follows these rules:

1. Each iteration starts with a snapshot of every carry.
2. Every sibling RHS in that iteration reads the same snapshot.
3. RHS outcomes are evaluated before any carry advances.
4. Yielded outcomes advance their carries simultaneously at iteration end.
5. A skipped outcome keeps its prior carry while yielded siblings may advance.
6. The next admitted iteration reads the advanced carries.
7. A statement-wide rejected iteration advances no carries.
8. Nested range points advance carries in their defined lexicographic execution
   order.
9. `finish` exposes only the final carried values to `set`.

If a destination is fresh, its seed follows existing Pluto declaration and
zero-value rules. PIR must not bypass read-before-definition validation merely
to create a carry.

For owned values such as arrays and heap strings, `advance` means semantic
replacement. The backend must keep the old carried value alive while evaluating
the RHS, then transfer or copy the new value and release the replaced value only
after the iteration update is safe.

## 8. Ownership, Lifetimes, and Cleanup

Every value-producing outcome carries an ownership annotation:

| Annotation | Meaning |
| --- | --- |
| `owned` | The outcome holds heap state the plan must consume or release exactly once |
| `borrowed` | The outcome views state owned elsewhere and is never released here |

Consumers consume ownership: `set` moves an owned outcome into a target (or
copies when the source must survive), `advance` consumes it as the new carry
and releases the replaced carry only after the iteration update is safe, and
`collect` moves or copies it per collector policy.

Releases are **derived, not authored**. The builder does not place cleanup:
structured region exit implicitly discards any owned outcome no consumer
took, on every path — a skip arm, the untaken side of a `value-and` or fallback, a
rejected iteration, or region end. The validator derives the release
obligation for each owned outcome on each path and rejects a plan where one
is consumed twice or escapes its region unconsumed. Expanded PIR prints the
derived `drop` points so ownership regressions surface as plan diffs. The
generic lowerer emits the actual frees from those derived obligations.

Borrowed outcomes are never released here; they are copied exactly at the
consumer that needs ownership (a `set` into a longer-lived target), never
earlier.

Leak checks remain necessary: a correct plan can still be lowered
incorrectly, so `--leak-check` stays the runtime backstop for the lowerer.

The dead-write diagnostic is fixed upstream of PIR: the solver records a
per-target write effect (`MustWrite` versus `MayWrite` per LHS slot — it
already knows conditional yield and keep-old behavior), and the CFG consumes
that metadata — curing the misclassification of value-position conditional
writes that today forces tests to interleave reads, without the CFG ever
reading PIR. The CFG pass
itself stays: dataflow legality is not replaced by ownership checking, which
answers a different question. This fix does not depend on PIR and may land
before it.

## 9. Conditions, OOB, and Skip Scope

Failure scope must be explicit:

| Failure site | PIR action |
| --- | --- |
| Shared ranged statement condition | `continue` |
| OOB while evaluating that shared condition | `continue` |
| OOB in one ordinary RHS | `skip`, resolved within that RHS only |
| Failed value-position comparison | `skip`, available to `fallback` |
| OOB in one collector cell | `skip`, resolved at the cell boundary; the closing policy decides omit or zero-fill |
| Failed statement without a range | Final `set` applies keep-old or zero policy |

The normal per-iteration order is:

1. Enter the range point.
2. Evaluate shared statement conditions.
3. Continue the range if the shared condition rejects the point.
4. Evaluate each RHS outcome, including value-position `&&`, fallbacks, and local OOB checks.
5. Collect yielded cells and advance yielded carries simultaneously.

This ordering prevents an OOB in `a = arr[i]` from suppressing a sibling update
such as `b = i + 1`.

## 10. Collectors

Collectors have an explicit lifecycle:

```text
prepare
    collector result : Array(Int)

for i in range(0, n)
    collect result
        from eval(data[i])
        on-oob skip

finish collector result
    close append-yielded

set local(result) from collector result
```

Supported closing policies initially include:

- append only yielded cells
- zero-fill a missing fixed cell
- retain the last yielded scalar value
- apply a policy independently per output slot

Collectors and carries may coexist in one statement. A skipped collector cell
does not suppress an unrelated carried update, and a skipped carried RHS does
not suppress a sibling collector append.

## 11. Affine Bounds Versioning

Affine analysis records high-level access forms such as:

```text
array: data
iterator: i
index: 2*i + 1
domain: range(0, n)
```

The plan attaches a bounds strategy to the corresponding `for`:

```text
for i in range(0, n)
    bounds versioned
        data[2*i + 1]
```

Lowering computes one guard before the loop nest:

```text
if every recorded affine access is safe for the complete domain
    run the body with those accesses unchecked
else
    run the same body with checked accesses
```

Affine versioning does not break out of a partially executed fast loop. Switching
after some iterations could duplicate side effects, collector appends, or carry
updates. If the whole-domain guard is false, the checked version runs from the
start.

The validator must reject a versioned access when its array, range, index form,
or relevant effects can change before or during the loop. Unsupported or
non-affine accesses simply remain checked.

## 12. Default Text View

The primary PIR view should be deterministic, indentation-based structured text.
It should resemble the execution plan, not Go structs, JSON, LLVM, or a CFG.
The canonical text format uses indentation to delimit regions:

- exactly four ASCII spaces per nesting level
- no tabs
- no braces or `end` markers
- a region ends when indentation returns to its level or an outer level
- blank lines may separate phases but do not affect structure

The in-memory plan tree remains authoritative; indentation is its canonical text
representation. If PIR becomes parseable later, its lexer should translate
indentation changes into `INDENT` and `DEDENT` tokens.

For example:

```text
statement x
    source "x = a > 0 && data[i] || -1"

    execute
        fallback
            primary
                value-and
                    condition eval(a > 0)
                    then eval(data[i])
                        on-oob skip
            otherwise eval(-1)

    set local(x)
        from fallback result
        on-skip keep-old
```

Recommended views:

- `-emit-pir`: concise semantic plan with source locations and Pluto types only
  where useful
- `-emit-pir=expanded`: result shapes, target mappings, access IDs, affine forms,
  collector/carry details, ownership annotations and release points, and the
  conceptual fast/checked expansion

Compiler temporary names and stable node IDs stay hidden in the default view.
An optional graph view can be added later for deeply nested ranges, but
structured text is the review, diff, and golden-test format.

## 13. Representation Boundary

The PIR builder consumes a solved statement and produces an immutable tree of
regions and outcomes. It owns every decision currently spread across statement
dispatch, conditional-spine extraction, range preparation, collector rewrites,
bounds guards, and affine probing.

The LLVM lowerer may still call existing expression and ownership helpers for
`eval`, `set`, collector, and carry operations during migration. It must not:

- re-run predicates to choose a different statement strategy
- discover new ranges by walking the AST
- infer whether a failed check skips a value, cell, or iteration
- infer last-yield, zero-fill, or keep-old behavior from the selected helper
- rediscover which accesses are affine-fast by AST pointer identity
- re-derive whether a call handles its own iteration or the statement's loop
  nest does — the builder consumes the solver's decision
- make a strategy decision inside an `eval` region by consulting per-slot
  condition modes: the builder must have split every conditional node out of
  `eval`, a conditional mode reaching plain expression lowering is a
  validation failure, and the existing unclassified-mode assertions remain in
  the expression compiler as backstops

The intended lowering is mechanical: walk the plan in order and emit the
corresponding LLVM structure.

## 14. Validation Invariants

The PIR validator should reject a plan unless:

1. The phases appear in `prepare`, `execute`, `finish`, `set` order.
2. Every carry and collector is prepared before use and finished at most once.
3. Every range iterator is bound before an expression references it.
4. Every `skip` has an unambiguous nearest resolving region; every `continue`
   and `break` names its range.
5. Every lazy `value-and` and fallback keeps its RHS in a lazy region.
6. Outcome arity, Pluto types, domain, and yield shape match their consumers.
7. All sibling RHS expressions read the iteration-start carry snapshot.
8. All carry advances for one iteration are simultaneous.
9. A skipped carry update preserves that carry without suppressing siblings.
10. A rejected shared iteration performs no carry advance or collector append.
11. Final `set` maps each outcome to exactly one compatible target slot.
12. All targets in one assignment group are set simultaneously.
13. Every checked access has an explicit OOB scope.
14. Every unchecked access belongs to a valid whole-domain affine proof.
15. Each source expression and future nontrivial target is evaluated exactly as
    many times as the plan states.
16. The plan contains no LLVM value, machine type, pointer, register, or storage
    decision.
17. Every owned outcome is consumed at most once, and the validator derives
    exactly one release obligation for every path where it is not consumed —
    yield, skip, taken and untaken `value-and`/fallback sides, rejected iterations,
    and region end.
18. No outcome is used after it is moved or after its derived release point,
    and borrowed outcomes are copied before any consumer that outlives their
    owner.

Validator failures are compiler ICEs and should include the source statement and
the smallest relevant PIR excerpt.

## 15. Implementation Phases

### Phase 0: Semantic corpus (3-5 days)

- Record representative plain, conditional, ranged, collected, OOB, affine, and
  self-referential statements.
- Pin simultaneous sibling and loop-carried update semantics.
- Decide collector closing policies for every existing context.

### Phase 1: Plan model, printer, validator, and write effects (1-2 weeks)

- Add immutable statement, region, outcome, target, carry, collector, range, and
  access plan nodes.
- Record per-target `WriteEffect` (`MustWrite`/`MayWrite`) in the solver and
  migrate the CFG to consume it. Derive the effect after applying statement
  conditions, range execution, checked-access failure, conditional propagation,
  and nearest-resolver policies, fixing the conditional-write dead-write false
  positive independently of any lowering change.
- Build plans in shadow mode without changing LLVM generation.
- Add deterministic `-emit-pir` output and golden tests.
- Cover plain assignment, statement conditions, and one simple range.

**Go/no-go checkpoint:** the dump must explain why `compileLetStatement` selects
its current lowering without reading LLVM helper code.

### Phase 2: Conditional values, final set, and ownership (2-3 weeks)

- Add value-and, fallback, map, alignment, per-slot skip, and final-set policies.
- Add owned/borrowed outcome annotations, validator-derived release
  obligations, and generic cleanup lowering for non-ranged statements.
- Lower selected non-ranged statements from PIR using existing backend helpers.
- Differentially test old and PIR paths, including leak checks.

### Phase 3: Ranges, carried state, and collectors (2-4 weeks)

- Add prepare/execute/finish/set lowering.
- Add iteration snapshots and simultaneous carry advance.
- Extend ownership to carries (replaced-carry release after a safe update)
  and collector cells.
- Add collector initialization, cell skip policies, and finalization.
- Migrate ranged statement conditions and ranged RHS paths incrementally.

**Ownership exit criterion:** the mask sweeps and consumed-temporary marking
may be deleted only after differential leak checks pass with cleanup emitted
solely from derived release obligations.

### Phase 4: OOB and affine versioning (2-3 weeks)

- Inventory checked accesses while building the plan.
- Attach explicit OOB scopes.
- Move affine form recognition and whole-domain versioning decisions into PIR.
- Preserve the checked path as the semantics-first fallback.

### Phase 5: New targets (feature-driven)

- Add field and index targets as their source features are implemented.
- Add table targets and domains when table semantics are specified.

### Deletion discipline

Migration is complete only when the old statement machinery is gone. To avoid
a permanent dual-path world, each statement class follows one rule:

1. Build its PIR in shadow mode.
2. Add concise and expanded golden plans.
3. Differentially compare old and PIR lowering.
4. Run race, full E2E, and leak checks.
5. Switch the class to PIR.
6. Remove its old dispatcher, classifier predicates, and specialized lowering
   in the same migration PR — never parked as a fallback.

Zombie fallbacks hide plan bugs and double every future semantics change.

### Target deletion inventory (with proof gates)

The end state is one statement-plan builder, one validator, one generic plan
lowerer, the existing reusable expression/runtime/backend primitives, and no
duplicated statement classification or specialized conditional orchestration.
The inventory below is a target, not an upfront guarantee — each item is
deleted only when its phase proves the plan replaces it:

- Statement dispatch predicates (per-slot committability, spine alignment,
  logical-tree routing, ranged-condition splitting): deleted class by class as
  each migrates.
- The condLHS frame: it provides evaluate-once identity, substitution,
  comparison reuse, and temporary ownership — more than classification.
  Named plan outcomes should replace it, but because ordinary expressions stay
  inside `eval`, some value plumbing at the eval boundary may survive in
  another form. Deleting it is a Phase 2 exit criterion demonstrated by the
  prototype, not assumed.
- Staging and per-expression commit machinery: the specialized semantics are
  replaced by carries and simultaneous `set`; the lowerer still needs generic
  storage across branches and iterations, so equivalent backend mechanics
  remain — reorganized, not vanished.
- Mask sweeps and consumed-temporary marking: replaced by derived release
  obligations; gated on the Phase 3 ownership exit criterion.
- The repeated bounds-bit idiom: the semantic decisions (what an OOB skips)
  move into the plan; guard predicates, branches, and temporary guard state
  remain as generic lowering mechanics.

The read/write CFG pass is not on the inventory: it stays, improved by
consuming the solver's per-target write effects, not replaced.

A useful shadow-plan checkpoint is 1-2 weeks. Migrating the current assignment,
conditional, range, collector, and affine paths is approximately 5-9 focused
weeks and should proceed incrementally rather than block unrelated features —
but incrementally means class by class with immediate deletion, not a
long-lived parallel path.

## 16. Testing Strategy

### PIR golden tests

- solved statement -> deterministic concise PIR
- solved statement -> deterministic expanded PIR
- canonical output uses four-space region indentation and contains no tabs,
  braces, or `end` markers
- no LLVM context required
- negative tests for every validator invariant
- release points appear in expanded PIR, so ownership regressions surface as
  plan diffs before any leak-check run

### Write-effect tests

- mixed RHS expressions produce effects aligned per LHS slot, such as
  `[]WriteEffect{MayWrite, MustWrite}` for `a, b = arr[i], i + 1`
- shared conditions and possibly empty ranges produce `MayWrite` for keep-old or
  unresolved last-yield targets, while an unconditional collector or zero-fill
  closing policy can still produce `MustWrite`
- a fallback that resolves every conditional or checked-access failure produces
  `MustWrite`
- a fallback whose final alternative can still fail remains `MayWrite`
- multi-output expressions retain independent effects for each output slot

### Loop-carried tests

- `sum = sum + 1` observes the previous iteration's sum
- `arr = arr ⊕ [2]` observes and replaces the previous iteration's array
- sibling RHS expressions read the same iteration-start snapshot
- sibling carries advance simultaneously
- one skipped RHS keeps its carry while another advances
- a rejected shared condition advances no carry and appends no collector cell
- nested ranges carry state in the defined execution order
- final LHS values equal the last carried values after the loop finishes

### Differential and backend tests

- compare observable output and diagnostics between old and PIR lowering
- retain focused LLVM tests for lazy placement and affine fast/checked loops
- run `go test -race ./...`
- run `go vet ./...`
- run `python3 test.py --leak-check`

## 17. Future Extensions

The statement plan can grow without becoming a machine IR:

- field, index, column, and cell targets extend `set`
- member calls remain solved expressions inside `eval` or `map`
- source `break` and `continue` extend structured range actions
- function-result transfer can reuse outcome planning with a different final action
- conditional arrays extend domains, alignment, and yield masks
- grouped multi-axis indexing extends borrowed selection domains after PIR migration;
  scalar indices drop axes, range and omitted indices retain them, and the
  leftmost range drives the sequence of yielded subarrays
- range-left value-position `&&` can later bind an outer local domain for nested
  construction such as `[i && [matrix[i][j]]]`; it must remain local to that
  value and must not become a statement gate or implicit collector — only an
  explicit `[]` closes the bound domain into an array
- gated prints become a statement plan whose final action prints yielded
  outcomes instead of setting targets
- test contexts can become explicit statement inputs/effects

PIR should remain statement-focused until a concrete feature requires
cross-statement dataflow. That keeps its value proportional to the compiler
complexity it is intended to replace.
