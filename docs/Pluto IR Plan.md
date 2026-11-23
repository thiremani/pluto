# Pluto Statement IR (PIR) Plan

**Status:** Accepted 2026-08-05 — roadmap in §16; Steps 1-3 complete, Step 4
in progress (five slices landed: heap values and ownership elaboration,
calls and multi-output outcomes, function bodies and output targets,
ordinary prints, then block-layout literals), Step 5 in progress (first
slice landed: assignments whose value holds a checked index read)

**Scope:** A typed, structured execution plan for one Pluto statement

**Primary motivation:** Make ranges, conditions, conditional values, bounds
failures, collectors, loop-carried updates, affine versioning, and final LHS
assignment explicit before LLVM lowering

**Related semantics:** [Pluto Conditional Value Semantics](./Pluto%20Conditional%20Value%20Semantics.md), [Pluto Range Semantics](./Pluto%20Range%20Semantics.md), [Pluto Memory Model](./Pluto%20Memory%20Model.md)

## 1. Decision

PIR v1 is a **statement execution plan**, not a general-purpose value IR. It
answers four questions per statement:

1. What state or collectors must exist before evaluation?
2. Over which ranges and conditions does the statement execute?
3. Which values yield, skip, accumulate, or advance on each iteration?
4. Which final outcomes reach the LHS?

The pipeline becomes:

```text
Pluto source
  -> lexer -> tokens
  -> parser -> AST
  -> semantic solver -> typed/resolved AST + ExprInfo + effects (§15)
  -> CFG/dataflow validation (use-before-definition, dead writes)
  -> PIR statement builder -> statement plans
  -> ownership elaboration -> annotated plans
  -> PIR validator -> validated plans
  -> PIR-to-LLVM lowering -> LLVM IR
  -> LLVM optimization -> object emission -> link with runtime -> executable
```

Responsibilities stay decoupled: the solver owns types, ranges, output shapes,
and effects; the CFG owns cross-statement dataflow legality; PIR owns how an
already-valid statement executes; LLVM owns SSA, storage, optimization, and
ABI. The CFG never reads PIR. The CFG checks function templates with its own
conservative classification of the source (#123), while the script root's
checks still consume the solver's statement effects until they move to the
same classification (§15, CFG consumption).

PIR may refer to solved AST expressions, but LLVM lowering must not reclassify
their range, conditional, OOB, collector, affine, or commit behavior.

**ABI note.** Per-slot effects are an internal analysis, never a public ABI
classifier: a body-only edit must never change the C prototype of a
type-mangled symbol. #123 requires every body that runs to write every
output, so ABI 3.0 (#69) gives the hidden direct-return seed and the write
markers only to a function with a `Range` or `ArrayRange` parameter. That
depends only on whether a parameter is a range, and the complete prototype
only on the solved parameter and output types, so a prototype still never
depends on the body. A range-bearing function keeps them to report an empty
domain until Step 7 chooses one way (§16).

## 2. Deliberate Abstraction Level

PIR records source-language execution decisions: Pluto types, LHS targets and
simultaneous assignment groups, range bindings and nesting order, statement
gates and lazy value-position `&&`, fallback and yield/skip behavior, per-value
and per-slot and per-element and per-iteration outcomes, checked accesses and
their OOB scope, loop-carried values and collectors, affine access forms and
versioning decisions, and final keep-old/zero-fill/append/last-yield policies.

PIR does not contain LLVM types, `llvm.Value`, blocks, SSA registers, phi
nodes, allocas, pointers, loads, stores, register-versus-memory decisions, ABI
details, concrete cleanup blocks, or generic user-program operations such as
arbitrary `if`, `select`, or mutable assignment.

Immutable plan nodes and stable IDs are sufficient. PIR is implemented as typed
Go nodes; its text form (§12) is a deterministic rendering of the tree, not a
separately authored program. Ordinary arithmetic, calls, and indexing stay in
solved `eval` expressions until their range, failure, or ownership behavior
requires a dedicated semantic node. A fuller value IR is justified only if
Pluto later needs substantial cross-statement optimization before LLVM.

## 3. Statement Lifecycle

Every assignment plan has four ordered phases:

| Phase | Responsibility |
| --- | --- |
| `prepare` | Establish carried values, collectors, targets, and range inputs |
| `execute` | Run range domains, statement gates, value-position `&&`, fallbacks, yields, skips, collections, and carried updates |
| `finish` | Close collectors and select final carried or collected outcomes |
| `commit` | Apply final outcomes to all LHS targets simultaneously |

The phases describe semantics, not allocations: `carry sum` in `prepare` does
not require a stack slot, it means reads of `sum` inside the statement observe
the statement's current carried value.

A print plan is its own side-effecting plan type, **`PrintPlan`**, modeling
**one N-ary intrinsic invocation**. Print is a call, and the ordinary
call-merge rule applies to it: any unresolved argument suppresses the
complete invocation, exactly as an OOB argument keeps `Id(arr[oob])`'s whole
output tuple old. For each admitted point, `PrintPlan` runs every argument's
checks first, in source order (evaluation order, below); the first failure
suppresses the invocation, and only when every check passes does it finish
the remaining argument outcomes and invoke print **exactly once** — an
unresolved argument emits neither content nor newline. A `fallback` resolves
before this boundary, so `a, arr[oob] || -1, arr2[oob] || -2, b` emits
`a -1 -2 b`, while `a, arr[oob], arr2[oob], b` emits nothing at all.
Zero-filling a failed slot instead was rejected: print has no target whose
keep-old rule could resolve the missing outcome (`y = 7` then `y = c > 5`
leaves `y` at 7; only a fresh target shows the zero seed), so the only value
to invent is zero, which §5 forbids as a missing-value marker. `|| 0` spells
that placeholder explicitly, and the whole-line default keeps
`"big:", c, c > 5` a display filter.

When every argument yields, the invocation formats every slot in source
order, with one space between adjacent **ordinary single-line slots**; each
slot's formatter keeps its own internal layout, so a whole-`Struct` slot
spans several lines inside the one invocation (`tests/struct/struct.exp`).
An empty string is a successful value, not a skip: `a, "", "", b` emits `a`
and `b` separated by three spaces, distinguishable from an invocation
suppressed by failure. **The atomic unit is the invocation — one emission
group — not necessarily one physical line**: a suppressed invocation emits
none of its output, including a multi-line struct slot's, and a fully-yielded
ranged print produces one emission group per admitted point. Whether lowering
uses `printf`, a line buffer, or several writes is a backend choice, and any
single-physical-write atomicity guarantee is a lowering/runtime contract, not
plan semantics. Generic `finish` keeps its single meaning of closing carries
and collectors; `PrintPlan` needs no finish exception, since the invocation
itself is the terminal consumer, and it consumes the group's owned
temporaries — formatted strings, materialized cells — on the suppressed path
too, where elaboration derives their releases.

**Evaluation order: checks first.** A *failure scope* is the region one
resolver closes — an RHS slot, a print invocation, a collector cell, a
`fallback`'s left side — and its *checks* are the comparisons and checked
accesses whose failure reaches that resolver. Each scope runs its checks
first, in source order, each with the operands it needs evaluated by the same
rule, and a slot stops at its first failed check. The scope's other work then
runs in source order, only for the slots that passed, reusing what the checks
computed; a call that feeds several slots runs when any of them passed, and a
suppressed scope releases what its checks allocated. So
`r = Pair(Loud(a), c > 5)` runs `Loud` only when `c > 5`,
`x = F(io(), arr[oob])` runs neither `io()` nor `F`, and in a body that sets
`res = 1` first, `res = Fact(n - 1) * (n > 1)` stops recursing at `n = 1`: a
comparison guards the work beside it wherever it is written, as a gate or an
`&&` left side does. The builder makes the order explicit, emitting a scope's
checks first and placing the rest of its work in a `require` region on them
(§4).

Today's lowering already follows this order for comparisons: extraction
evaluates each one under a guard of those before it, and the other operands
run under a guard of the checks, so `a, b = Pair(1 > 2, Loud(7) > 0)` and
`(1 > 0) && ((1 > 2) + Loud(1))` call no `Loud`. The rest of the order is a
**behavior change**: a checked access becomes a check, so `Loud(1) + arr[oob]`
stops calling `Loud`, and an OOB print argument suppresses the invocation
instead of printing its materialized zero (§9); and an `||` that can't fail
becomes work, so `((1 > 9) || Loud(2)) + (1 > 2)` stops calling `Loud`.

Step 6 makes these changes for non-ranged statements and prints, including
the checked-only assignments Step 5 migrates in today's order; ranged
statements and collector cells take the order when they migrate in Steps 7
and 8. Effects follow the same order: a check's calls run before the work it
gates, so `x = io() + (io() > 0)` runs the second `io()` first, and a
statement boundary keeps text order (`v = io()`, then `x = v + (v > 0)`). The
regressions in §17 pin each change.

Prints are migration scope, not a future extension: today's
`compilePrintStatement` consumes the conditional-extraction and
collector-preparation machinery, so prints must lower from plans before that
machinery's last consumer is gone.

## 4. Core Vocabulary

| Operation | Meaning |
| --- | --- |
| `eval expr` | Evaluate a solved Pluto expression or expression fragment |
| `carry` | Declare state that may advance across iterations (`prepare`) |
| `collector` | Declare a logical collection result before its domains (`prepare`) |
| `domain` | Execute a region once per point in one resolved range domain |
| `check` | Evaluate one comparison: its outcome is the left operand, and it yields where the comparison holds |
| `gate` | Admit one shared statement iteration to its whole region where the outcome it takes yielded, and reject it elsewhere |
| `require` | Lazily evaluate a local value region only when the outcomes it requires yield: the work a failure scope's checks gate (§3) |
| `first` | Take the first of its sides that yields: the value-position `||`, a chain of them as one list however it is grouped |
| `fallback` | Lazily evaluate the next side of a `first`, where the side before it failed |
| `last` | Run its sides in order while each yields, and take the final one's value: the `&&`, in value position or over a gate's conditions, a chain of them as one list however it is grouped |
| `next` | Lazily evaluate the next side of a `last`, where the side before it yielded |
| `map` | Apply ordinary expression work to yielded child outcomes |
| `align` | Apply explicit slot, zip-min, or broadcast alignment |
| `yield` | Produce a value from the current value or cell region |
| `skip` | Produce no value; the failure propagates to the nearest resolving region |
| `continue` | Reject the rest of one range iteration |
| `collect` | Add a yielded cell according to the collector policy |
| `advance` | Replace loop-carried state at the end of an iteration |
| `drop` | Derived at region exit: free an owned outcome no consumer took (printed in expanded PIR, never authored) |
| `finish` | Close a carry or collector into a final outcome |
| `commit` | Apply one simultaneous mapping from final outcomes to LHS targets |

Every operation corresponds to a documented language rule in the semantics
docs; a new operation requires its rule to be written there first, so the
vocabulary cannot grow ahead of the language.

Generic loops and branches are intentionally absent. `domain`, `gate`,
`require`, `fallback`, and checked accesses record why control exists and
what a rejected outcome means; the lowerer emits ordinary LLVM branches and
loops. `skip` stays distinct from `continue`: one RHS may fail while sibling
RHS expressions still update in the same iteration. A `skip` names no scope of
its own — it propagates outward to the nearest resolving region (§9 lists
them), and must remain visible to a surrounding `fallback` before any coarser
region resolves it.

## 5. Plan Results

Each value-producing node has an abstract outcome:

| Property | Examples |
| --- | --- |
| Outputs | `I64`, `(I64, Str)`, `[I64]` |
| Domain | scalar, fixed output slots, array elements, range iterations |
| Yield shape | always, scalar condition, per-slot bits, element mask, per-iteration |

Conceptually an outcome is `(value, yielded)`, analogous to a circuit lane's
`(data, valid)`; `yielded` has the node's yield shape rather than necessarily
one scalar bit. This is plan-level meaning, not a Pluto tuple or runtime
layout.

Zero is never a missing-value marker — a successful comparison may yield zero —
so value and yield information stay separate. A `gate` consumes yield state as
its region enable. `require`, `map`, and `align` propagate yield state with
their data; the resolving operations consume it per §9's failure-scope table.

`eval` leaves may reference typed AST nodes. The builder splits out everything
that affects evaluation strategy — ranges, lazy `&&`/`||`, conditional
propagation, collectors — while ordinary arithmetic and calls stay inside
`eval` or `map` and continue to use the existing expression compiler.

## 6. LHS Targets and Final Commit

PIR calls LHS locations **targets**:

| Target | Meaning |
| --- | --- |
| `local(name)` | Ordinary local binding |
| `output(name)` | Function output binding. A commit writes only the output: a range-bearing function sets its indirect outputs' write markers once per executed iteration, and no other function has any (§1) |
| `discard` | A `_` slot: one independent sink per slot, never bound; see below |

Field, index, column, and cell targets are future extensions (§18).

A `discard` creates no binding and no name, but its **outcome keeps its type,
arity position, and `YieldEffect`** so ownership elaboration can derive
cleanup. The owned outcome is consumed at the exit of the smallest region that
owns it, not at statement end: a discarded heap value produced inside a domain
is released per iteration rather than accumulated across the statement. A
discard never participates in a carry and never keeps an old value, because
there is nothing to keep.

Two things are deliberately absent, and one is deliberately present. No new
**Pluto source keyword**: `_` is the existing spelling. No authored PIR
`discard` **operation**: the commit mapping to a `discard` target is an arity
and validation record (§14), while the disposal is the ordinary derived `drop`
at that smallest owning region's exit — immediate by construction and never
authored by the builder, per §8's releases-are-derived rule. What *is*
first-class is the `discard` **target variant** above, with its own textual
spelling in the rendered plan, so a dropped outcome is always a recorded
mapping rather than a missing one.

In effect terms (§15) a discard publishes **no target `WriteEffect` and no CFG
event** — there is no destination to write, read, or kill. Its absence is
structural and positional, not a third lattice state: the statement's
target-effect vector stays aligned to the LHS slots, with the discard's entry
simply absent (an aligned optional vector or a sparse `{targetIndex, effect}`
mapping — `a, _, b` keeps three positions carrying two effects, never a
compacted two-slot vector that loses the mapping). The RHS `YieldEffect` and
type vectors remain fully aligned across all slots, discards included.

Targets are evaluated exactly once, at the phase Pluto's assignment semantics
require. Ownership elaboration decides copies, moves, transfers, and cleanup
(§8); the lowerer implements those annotated decisions, choosing only
machine-level representation.

All RHS expressions in one assignment group are evaluated before `commit`, and
the mappings then apply simultaneously — preserving swaps, sibling
self-references, and ownership safety without exposing temporary storage. Every
target slot and outcome has a stable plan ID; the builder records the exact
`target = outcome` mapping, and the lowerer must not reconstruct it from
names, result order, or LLVM values.

A commit group follows one transfer contract:

1. Every RHS outcome is produced against the pre-commit binding snapshot.
2. The complete outcome-to-target mapping is known before any target changes.
3. Moves, copies, and retained borrows are planned across the whole group.
4. All mappings take effect simultaneously in Pluto semantics.
5. Replaced values are released only after no mapped outcome can still
   reference or consume them. The implemented schedule is the conservative
   one — evaluate every outcome, land every mapping, then release what
   nothing took — not the earliest safe point.

For example, `a, b = b, a`:

```text
%to_a = eval I64 b
%to_b = eval I64 a

commit
    a = %to_a
    b = %to_b
```

For owned heap values this may lower to an ownership swap without deep copies.
If one owned source feeds multiple targets, at most one consumer takes it; the
others require a derived copy.

At a call boundary, `a = F(a)` passes `a`'s value as an input that stays
stable for the whole invocation (#144): the output starts unassigned in its
own staging slot, and the lowering may place both in one slot only where no
read can tell. The real `a` changes only at the outer assignment's commit, so
sibling RHS expressions continue to read the pre-commit binding. The legacy
lowering keeps the input and the output apart within every invocation and
shares storage only between the iterations of a callee-owned range;
`tests/alias_input` pins the results. Step 4's call lowering must preserve the
external commit.

Reusing the input's buffer for the output is a later optimization, not yet
scheduled. It needs two facts: the caller can hand over the destination's
old value because nothing else in the statement reads it, and the body
consumes the input where no later read needs its contents, as at its last
read, in a one-pass compaction such as a filter, or in a runtime sort with a
consuming contract. It extends §8's consuming promotion across the call
boundary; a handover alone does not make a body run in place.

## 7. Loop-Carried State

A ranged assignment that reads its own LHS needs explicit carried state, so
that iteration `n + 1` observes iteration `n` while the real target is
committed only after the domain finishes:

```text
assign sum, arr
    prepare
        %sum_carry = carry I64 sum
        %arr_carry = carry [I64] arr

    execute
        domain %i = range 0, n
            %sum_next = eval I64 %sum_carry + 1
            %arr_next = eval [I64] %arr_carry ⊕ [2]

            advance
                carry %sum_carry from %sum_next [on-skip=keep]
                carry %arr_carry from %arr_next [on-skip=keep]

    finish
        %sum_final = finish I64 %sum_carry
        %arr_final = finish [I64] %arr_carry

    commit
        sum = %sum_final
        arr = %arr_final
```

A carry is `(value, updated)` state **scoped to the domain that owns it**.
Within that domain a read of the LHS resolves to the carry, never to the
unchanged external target; a sibling driven by its own RHS-local domain neither
reads nor advances another sibling's carry. The rules:

1. Each iteration starts from a snapshot of every carry, and every sibling RHS
   under the same shared domain point reads that same snapshot.
2. All RHS outcomes are evaluated before any carry advances; advances are then
   simultaneous, so the next admitted iteration sees the complete prior
   advance while swaps and self-references still see one iteration-start
   snapshot.
3. A carry's `updated` bit is set only by its own successful advance. A
   skipped outcome keeps its carry without suppressing siblings; a rejected
   shared iteration advances nothing.
4. Nested range points advance in lexicographic execution order.
5. `finish` exposes only final carried values, consulting `updated`: a domain
   that admitted no point — empty range, or a gate that rejected every point —
   finishes not-updated, and `commit` keeps the old target.

The seed is carried state, not a write: it is a **borrow** of the external
target, never a copy, so a domain admitting no point costs nothing. If the
destination is fresh, its seed follows normal declaration and zero-value rules;
PIR must not bypass read-before-definition validation to create a carry.

An `advance` does not by itself make the new carry owned. The carry takes the
ownership annotation of the outcome it advances from — `unmanaged` for a
trivial value, `borrowed` for a self-reference or sibling borrow still valid at
the next iteration's start, `owned` only for an outcome holding heap state —
and elaboration materializes a borrow into a copy only when it would escape its
owner's lifetime. Consequently the borrowed external seed is **never** released
by an advance; a later advance releases the previous carry only if that carry
was `owned`; and the external target's old value is released only by a
successful final `commit`, after the RHS has finished reading it.

A carried append must not copy on every iteration. In the example, iteration
1 reads the borrowed seed and `⊕` materializes one owned copy; from iteration
2 the carry is `owned`, and because `%arr_next` is the only reader of
`%arr_carry` in the iteration and advances that same carry, elaboration
promotes the operand to a **consuming** transfer (§8): the append works in
place on the buffer it is about to replace. A ranged append is therefore one
seed copy plus n in-place appends; the amortized O(n) total additionally
needs a growth-capable buffer in the runtime, which the plan-level license
does not provide by itself.

## 8. Ownership, Lifetimes, and Cleanup

Every value-producing outcome carries an annotation:

| Annotation | Meaning |
| --- | --- |
| `owned` | Holds heap state the plan must consume or release exactly once |
| `borrowed(owner, region)` | Views state owned elsewhere, records provenance and valid lifetime, never released here |
| `unmanaged` | A trivial value (integers, floats, condition bits, range descriptors); copied freely, never released or transferred |

Consumers consume ownership: `commit` moves an owned outcome into a target (or
copies when the source must survive), `advance` consumes it per §7, `collect`
moves or copies it per collector policy, and the `PrintPlan` invocation
consumes its argument group's temporaries, on the suppressed path too. Ownership is scheduled for a complete simultaneous group,
never one mapping at a time, so an early target overwrite cannot release a
value another swap outcome, sibling, or carry still needs.

Releases are **derived, not authored**, by a dedicated elaboration pass between
the builder and the validator: build semantic PIR, elaborate ownership,
validate, lower. Structured region exit implicitly discards any owned outcome
no consumer took, on every path — a skip arm, the untaken side of a
`require` or `fallback`, a rejected iteration, or region end. Elaboration
annotates each outcome, plans transfers, copies, and materializations across
each simultaneous group, and derives one release obligation per unconsumed
path; the validator rejects a plan where an outcome is consumed twice or
escapes unconsumed. Expanded PIR prints derived `drop`, transfer, and
materialization points, so ownership regressions surface as plan diffs.

Borrowed outcomes are never released directly. Within a simultaneous commit or
advance group, a borrow from a target or carry that is itself being replaced
may be **promoted to transfer** of that owner's old value when exactly one
owning consumer takes it and no surviving outcome still needs the old owner —
this is what permits an array or string swap without deep copies. Every other
escaping borrow is copied or materialized.

The same promotion applies inside `eval`. An `eval` does not consume its
operands, but elaboration may mark one operand `[consume=%x]` when `%x` is
`owned` and `unique`, the consuming eval is its only use in the whole
iteration or group — one occurrence, not merely one reading outcome, since
an eval of `%carry ⊕ %carry` has one outcome but two reads and consuming the
first could mutate or reallocate the buffer before the second — and the
eval's outcome advances or commits into the location `%x` came from; the
lowerer may then update that buffer in place instead of copying it (§7's
carried append). The annotation is derived, printed in expanded PIR, and
validated like any other transfer (§14): an operand consumed in place is
never read again or released separately.

Exclusivity is tracked separately from ownership. `owned` records a release
obligation; `unique` records that no other handle shares the buffer. An
`owned` outcome may share its buffer through copy-on-write materialization —
it still owns its release and is not a borrow — but it is not `unique`, and
an in-place update must first detach into a private copy. Borrows stay
strictly non-owning. Expanded PIR prints both annotations.

Elaboration is new analysis, not a port: ownership does not exist before LLVM
today. `ExprInfo` carries type, range, and conditional facts only, while
`Borrowed` lives on LLVM-bearing `Symbol`s and transfer decisions happen during
code generation. Leak checks remain the runtime backstop — a correct plan can
still be lowered incorrectly.

## 9. Gates, Failure Scope, and OOB

One rule governs both: **a gate admits or rejects its entire region; a failure
inside an admitted region propagates to its nearest resolver, which by default
is its own outcome slot.** A rejected gate does not evaluate its region at all.
Inside an admitted region, each failure scope runs its checks before its other
work (§3), so a failed check skips the work beside it as well as its result.
An enclosing gate that already admitted the region does not reach back to
resolve a later failure inside it.

For assignment, a failed statement gate blocks every sibling write, while an
OOB in one ordinary RHS leaves only that RHS's target unchanged.

Print has **no gate** today: `ast.PrintStatement` carries only an argument list.
Its failure resolver is the **invocation boundary** (§3): print is one N-ary
call, so an argument failure that no closer `||` resolves suppresses the
complete invocation and all of its output, by the same call-merge rule that
keeps `Id(arr[oob])`'s tuple old. A failed *conditional* argument already
suppresses the emission today — every argument's conditions are ANDed into
one gate around it, evaluated before the sibling arguments — and that
**outcome is retained**, as is evaluating conditions before siblings, except
that checks now stop at the first failure (§3). Step 6 also changes the OOB
case: today `arr[oob], val1, val2` evaluates its siblings and prints
`0 val1 val2`; afterward the read is a check, so the line evaluates nothing
else and prints nothing. The suppression is silent by design; whether a
debug build should report a suppressed invocation is a language decision
outside this plan, which neither requires nor precludes it.

A **gated print** — `c > 5 a, b`, a condition before the values with no
comma, as an assignment's gate stands before its values — is **planned with
Step 6** (decided 2026-11-13). It rejects the whole line without evaluating
any value. That is what `c > 5 && a, b` already does, since a print is one
invocation and a failed argument suppresses it, so the gate adds no
capability. It is planned for two reasons. A statement's values then take a
gate whether or not the statement has targets, one rule where assignments
were an exception. And the line says what it means: in an assignment `&&`
guards one slot, and only the invocation boundary makes it guard a whole
print, which the line itself does not show. It was once scheduled before
Step 6, in the form `arr[oob] val1, val2`, so that print kept a lazy spelling
once arguments became strict, and was dropped when checks first (§3) made
`arr[oob], val1, val2` evaluate nothing beside the failed read. It does not
parse today: the parser builds a `PrintStatement` only from a plain
expression list. It is a parser/AST/solver feature with its own PR,
semantics-doc entry, tests, and capability rows; its plan is a `PrintPlan`
under the statement `gate` Step 6 adds for assignments. A gate admits the
conditions an assignment's gate admits. The solver rejects an OOB read as a
statement condition; where one is admitted, the gate must test the access's
yielded/in-bounds bit rather than its value, so an in-bounds zero still
admits the region.

| Failure site | PIR action |
| --- | --- |
| Shared statement gate, or an OOB while evaluating it (assignment only today) | `continue` (ranged) or reject the statement (non-ranged) |
| OOB in one ordinary RHS | `skip`, resolved within that RHS only |
| Failed value-position comparison | `skip`, available to `fallback` |
| OOB in one collector cell | `skip`, resolved at the cell boundary; the closing policy decides omit or zero-fill |
| OOB in one print argument | `skip`; a closer `fallback` may resolve it, else it suppresses the whole invocation and newline (§3) |
| Failed statement without a range | `commit` applies keep-old or zero policy |

Per-iteration order:

1. Enter the range point.
2. Evaluate shared gates; `continue` if any rejects.
3. Evaluate each RHS outcome, including `require`, fallbacks, and local OOB
   checks.
4. Collect yielded cells and advance yielded carries simultaneously.

This is what prevents an OOB in `a = arr[i]` from suppressing `b = i + 1`.

An assignment's commit resolves a skipped right-hand outcome mapping by
mapping, and each mapping whose outcome may skip records what its target
then holds: `[on-skip=keep]` for a binding that has a value, which stays as
it was, and `[on-skip=zero]` for a binding the statement introduces, which
holds its type's zero. A `_` slot keeps nothing and carries no policy. A
mapping commits where its outcome yielded, so one that always yields prints
as before, and siblings of a skipped outcome still commit.

```text
assign obA, obB
    source
        obA, obB = obArr[10], 5

    execute
        %t0 = eval I64 obArr[10] [on-oob=skip]
        %t1 = eval I64 5

    commit
        obA = %t0 [on-skip=zero]
        obB = %t1
```

Checked accesses have one canonical representation — `eval` with an explicit
`[on-oob=...]` scope — regardless of which legacy path produced them. A
**caller-side** failure — a failed invocation or argument — is atomic: the
call's whole output tuple keeps its old values. A call that actually ran
writes every output (#123), so per-slot divergence exists only where slots
carry independent conditions.

## 10. Collectors

```text
assign result
    prepare
        %result_collector = collector [I64]

    execute
        domain %i = range 0, n
            %cell = eval I64 data[%i] [on-oob=skip]
            collect %result_collector <- %cell [policy=append-yielded]

    finish
        %result_final = finish [I64] %result_collector

    commit
        result = %result_final
```

Closing policies initially: append only yielded cells; zero-fill a missing
fixed cell; retain the last yielded scalar; or apply a policy independently per
output slot.

A collector is `(value, activated)` state that activates when **its owning
region is entered** — for a top-level collector, when the statement runs
ungated or its shared gate admits at least one point; for a collector inside a
lazy `require` or `fallback` arm, only when that arm executes. An ungated
collector over an empty domain still activates and commits `[]`; a collector
whose owning region is never entered stays inactive and `commit` keeps the
previous target.

Collectors and carries may coexist: a skipped cell does not suppress an
unrelated carried update, and a skipped carried RHS does not suppress a sibling
append.

## 11. Affine Bounds Versioning

Affine analysis records high-level access forms (array, iterator, index
expression, domain) and attaches a bounds strategy to the `domain`:

```text
domain %i = range 0, n [bounds=versioned]
    access data[2*%i + 1] [affine]
```

Lowering computes one guard before the loop nest and emits fast and checked
regions as two lowerings of the same PIR domain; PIR gains no generic `if` to
expose that branch. Versioning never breaks out of a partially executed fast
loop — switching mid-domain could duplicate side effects, appends, or carry
updates — so a false whole-domain guard runs the checked version from the
start. The guard evaluates the index form at both ends of the range with
overflow checks: an end whose index overflows `I64` makes the guard false,
since a wrapped index no longer bounds the ones between (#156).

The validator rejects a versioned access whose array, range, index form, or
relevant effects can change before or during the loop. Unsupported or
non-affine accesses simply stay checked.

## 12. Default Text View

The canonical view is deterministic, indentation-based structured IR. It
borrows LLVM/MLIR surface conventions — named outcomes, operation-first syntax,
explicit operands, typed results — without their machine-level basic blocks,
phi nodes, or storage operations. Using LLVM's exact text model would force
semantic regions into blocks and phis, erasing the distinction between a gate,
a local skip, and a fallback, and requiring later code to reconstruct it.

Format rules: four ASCII spaces per level, no tabs, no braces or `end` markers;
a region ends when indentation returns to its level or an outer one; blank
lines may separate phases without affecting structure; a line that binds a
new named result reads `%result = operation Type operands`, where the type
names the bound value — `%t0 = eval I64 b`, `%selected = require I64
%condition`, `%t0 = eval I64, F64 pair` for a multi-output outcome —
while operations that bind no new `%result` (`domain`, `yield`, `collect`,
`advance`, `commit`, `gate`, `skip`, `continue`, `drop`, among others) print
no result type, even where, as with `yield`, they produce their region's
value (§4); a commit mapping reads `target = value` in both views —
`h1 = %t0`, `_ = %t1` — where the target is a binding's source name or `_`
and the value an outcome, which the `%` sets apart from a line that binds a
new result; it prints no type, the outcome's own line naming it; an `output`
target (§6) is spelled with that word first, `output res = %t0`; an eval that
holds a
checked access ends with its out-of-bounds scope, named for what a failed
access does: `[on-oob=skip]` where it skips the outcome, and
`[on-oob=fallback]` where a `fallback` follows the node's side of a `first`,
which a `check` carries as an `eval` does;
each
binding mapped from an outcome that may skip ends with what it holds then,
`[on-skip=keep]` or `[on-skip=zero]` (§9), in both views, so `[on-oob=skip]`
pairs with a policy on each binding the skip reaches, while a mapping to
`_`, which keeps nothing, carries none; a `first` binds its outcome on a line
of its own, `%t0 =`, and the selection follows one level in: a `first I64`
line, which names the whole selection and its type, then a `fallback` label
for each side after the first, with every side one level further in, so the
labels line up, the sides line up, and no column depends on a name's width;
no side carries a `yield` line, a side's value being its last outcome; a
side is spelled as the node it is — an `eval`, a `check`, or a `last`, an
`&&` of its own — and a `first` or a `last` binds an outcome only where a
mapping can take it, so a `last` that is a side of a `first` binds none and
its head line sits where the outcome line would have gone, its own sides
staying one level in from it (decided 2026-11-20);
square brackets otherwise carry declarative policies, not
executable code. The expanded view puts each eval slot's ownership before
its type — the word `owned` or `borrowed`, and nothing for an unmanaged slot,
which carries no obligation: `%t0 = eval I64, owned Str Pair(x)`,
`%t0 = eval borrowed Str h`. The word is a fact about the slot and no part
of its type. A `first` line carries none: the selection has no slot of its
own, and each side's eval carries its own. A borrow's owner is its operand
when that is the owner's bare
name; any other borrow names its owner, `borrowed(h)`. The expanded view
also appends a mapping's
derived transfer — `[move]`; `[copy]` when the target gets its own owned
copy, of a borrowed outcome or of an unmanaged value entering a heap-typed
binding; `[move]` on a borrowed outcome means the target takes the
owner's old value, the borrow having been promoted to transfer (§8); nothing
for a plain store; a mapping fed by a `first` names a transfer for each side
where they differ, `[move %t1] [copy %t2]`, and one word where they agree —
and follows the mappings with the derived releases,
`drop %t1` for a discarded owned outcome, `drop %t0 [on-skip]` for what an
owned outcome built before it skipped, `drop %t1 [on-fallback]` for what a
side of a `first` built before its fallback ran, and `drop x [old]` for a
replaced target's old value, in the order they run (§8). Each names the
value it frees, which exists on one path only: a skipped outcome's target
kept its old value, and an outcome that yielded was moved into its target.
Read the two
kinds of annotation differently: an eval's states an outcome's ownership
obligation, not how it was produced — an `owned` column read and an
`owned` concatenation both allocated and copied, and the annotation says
neither — and a mapping's states
how the destination acquires the value. Assignment always has value
semantics; a bare store of an unmanaged struct, a `[copy]`, and a `[move]`
are three implementations of it, and copies made inside an `eval` are not
shown. A
`%name` is a plan outcome or binder: builder temporaries are `%t<N>`, with
`t` followed by digits reserved for them; every other outcome name is a
Pluto identifier — the language's own Unicode-aware class, so a target `π`
carries as `%π_carry` and no escaping is ever needed, since identifiers
cannot contain PIR punctuation. Generated names are opaque display labels,
`%sum_carry`, `%sum_next`, `%sum_final`: the defining operation says what
the value is, and nothing parses a name. Every `%` name — outcomes, carries,
collectors, and domain binders — comes from one per-plan namespace filled in
deterministic plan-construction order: builder temporaries claim their
reserved `%t<N>` names directly, and every other requested name that
matches `t[0-9]+` or collides with an already-allocated name receives the
smallest `_<K>` suffix that frees it, so a range named `t0` or `sum_carry`
never collides with a temporary or a carry; `<N>` and `<K>` are ASCII
digits (§14). A value
reference is `(outcome ('#' slot)? | binding) ('.' field)*` — `#N` selects
one slot of a multi-output outcome, MLIR-style, and a `.` following a value
reference is field or column access, `person.name`, `%person_carry.name`,
or `%t0#1.name`, exactly as `person.name` in the source (a `.` inside a
float literal is not an access). A `%name` denotes a plan-local outcome or binder, inspired by LLVM local
identifiers and MLIR SSA values. Source bindings are bare, spelled exactly
as in the source; `_` is the discard sink, which no binding can be named;
and `@` remains unused and reserved for future PIR syntax.
An `eval` operand is the solved expression rendered without its outermost
parentheses, so nested grouping remains explicit (`a + (2 * 3)`). The complete
comma-separated result type list precedes the operand; compound types delimit
internal spaces with brackets or braces. Types print in the compiler's display
spelling — `I64`, `[I64]`, `[[I64]]`, `Table[Name:Str Score:I64]` — never the
symbol mangle, which is neither exact identity (a function mangle omits result
types; a struct mangle encodes only the nominal name and omits the field
schema) nor assignment compatibility (`StrG` into `StrH`, an empty-array
reset). Validation applies the compiler's directional binding-compatibility
relation, not this spelling: both string flavours display as `Str`, and
`StrG` into `StrH` is admitted and shown as a copy.

The renderer covers exactly the node kinds the router admits and rejects any
other as an ICE, so each step that widens the router adds the renderer and
golden for its new node kinds. An eval operand is always one physical line:
control characters inside a string literal are escaped (`"line one\nline
two"`), and a block-layout array literal — rank-2 rows, a table — is spelled
as in the source with each of its line breaks escaped the same way
(`[\n    1 2\n    3 4\n]`; decided 2026-11-13, matrix rows 2c and 36).

A plan's `source` follows the opposite rule, since it is the whole statement
rather than an operand: the label stands alone and the statement's own lines
sit one level in, so a block literal reads as it is laid out (decided
2026-11-23). What it renders is the file's own text: the lines the statement
spans, dedented to the one it starts on, each cut before a comment that line
ends with. So the parens the ast adds to spell out its own nesting are absent
(`d = Twice(d) + 1`, not `d = (Twice(d) + 1)`), and an implicit
multiplication reads as it was typed (`imp = 5a`). A statement no file spells
— one a caller built — falls back to the ast's rendering. Within a line,
everything a reader could not otherwise see is
escaped — a tab, a control, and every rune Unicode calls unprintable, the
format and separator characters among them — so a source never breaks the
layout. Two statements still render alike where they hold the same text, a
physical tab and a `\t` escape among them; what these rules rule out is two
statements that differ. Only a line terminator becomes a
line, and LF, CRLF and CR all count as one (README), so a terminator reads the
same however a file spells it and never collides with the `\r` a source asks
for by escape. A multi-line string literal is the one place the two rules
disagree about what the text holds: the source shifts its continuation to the
section's indent, and the operand beside it is exact.

`commit` and `advance` carry no mode keyword: both are always simultaneous
(§14).

```text
assign x
    source
        x = data[i] || other[j] || -1

    execute
        %t0 =
            first I64
                %t1 = eval I64 data[i] [on-oob=fallback]
            fallback
                %t2 = eval I64 other[j] [on-oob=fallback]
            fallback
                %t3 = eval I64 -1

    commit
        x = %t0
```

The value-position `&&` prints the same way under `last` (decided
2026-11-15): the sides in order, and the value of the final one when every
side before it yielded. Its clause word is `next`, where a `first` has
`fallback` (decided 2026-11-18). A chain is one `last` however it is grouped,
and only its final side supplies the value, so a mapping takes it by one
transfer (decided 2026-11-20).

```text
assign v
    source
        v = n > 2 && 10

    execute
        %t0 =
            last I64
                %t1 = check I64 n > 2
            next
                %t2 = eval I64 10

    commit
        v = %t0 [on-skip=zero]
```

A statement's condition is planned as a value is (decided 2026-11-18). A
comparison binds its outcome on a `check` line, and the `gate` that takes
that outcome follows on a line of its own, binding nothing, with its region
one level in. An `&&` of comparisons reaches its gate as one `last`, and an
`||` is planned to reach it as one `first`, so a gate line names one outcome
whatever the condition.

```text
assign x
    source
        x = n > 0 5

    execute
        %t0 = check I64 n > 0
        gate %t0
            %t1 = eval I64 5

    commit
        x = %t1 [on-skip=keep]
```

A plan's header is its kind followed by its targets (`assign a, b` — targets
comma-separated, since `_` is an identifier character and a joined
`assign_a_b` could not be read back); a print plan has no targets and its
header is `print`. The kind is a keyword, so the header
needs no other opening word and a target may carry any name; the header is
not unique and nothing references it — plans are
emitted in source order and have no independently referenceable textual
identity. A function body's plans print one level in, under one header with
the specialization's display signature, `function I64 = AddOne(I64)`; a body
without a plan prints nothing. The script's plans print first, then each
specialization's block, in the order the specializations were first lowered.
Two views: `-emit-pir` is the concise semantic plan; `-emit-pir=expanded` adds
ownership annotations, derived transfers, and derived release
points, and — as the node kinds that vary them arrive — result domains and
yield shapes (§5), target mappings, access IDs, affine forms, and collector
and carry details. An annotation that every admitted node would print
identically is omitted until then. Compiler temporary names and node IDs stay
hidden in the concise view.

The in-memory tree is authoritative; the text is its rendering. PIR v1 has no
parser and is never user-authored; add one only if a concrete tooling need
justifies treating the text as an interchange format.

## 13. Representation Boundary

The builder consumes a solved statement and produces an immutable tree of
regions and outcomes. It owns every decision currently spread across statement
dispatch, conditional-spine extraction, range preparation, collector rewrites,
bounds guards, and affine probing.

The lowerer may call existing expression and ownership helpers for `eval`,
`commit`, collector, and carry work. It must not:

- re-run predicates to choose a different statement strategy
- discover ranges by walking the AST
- infer whether a failed check skips a value, cell, or iteration
- infer last-yield, zero-fill, or keep-old behavior from the selected helper
- rediscover affine-fast accesses by AST pointer identity
- re-derive whether a call handles its own iteration
- make a strategy decision inside `eval` by consulting per-slot condition
  modes — the builder must have split every conditional node out of `eval`, and
  a conditional mode reaching plain expression lowering is a validation
  failure. The existing unclassified-mode assertions stay as backstops.

Lowering is mechanical: walk the plan in order and emit the corresponding LLVM
structure.

## 14. Validation Invariants

The validator rejects a plan unless:

1. Phases appear in `prepare`, `execute`, `finish`, `commit` order; a
   `PrintPlan` omits `commit`, and its single N-ary invocation runs inside
   `execute`, once per admitted point, gated on every argument slot yielding
   (§3).
2. Every carry and collector is prepared before use and finished at most once.
3. Every range iterator is bound before an expression references it.
4. Every `skip` has an unambiguous nearest resolving region; every `continue`
   names its range.
5. Every lazy `require` and `fallback` keeps its RHS in a lazy region.
6. Outcome arity, types, domain, and yield shape match their consumers.
7. Sibling RHS expressions in a non-ranged statement read the same pre-commit
   binding snapshot.
8. Sibling RHS expressions under the same shared domain point read the same
   iteration-start carry snapshot; RHS-local domains stay independent.
9. All carry advances for one iteration are simultaneous.
10. A skipped carry update preserves that carry without suppressing siblings.
11. A rejected shared iteration performs no carry advance or collector append.
12. An assignment plan's `commit` provides exactly one type-compatible outcome
    mapping per target slot — a `discard` sink is an explicit mapping, not a
    missing one. A `PrintPlan` instead invokes print exactly once per admitted
    point when every flattened argument slot yielded; any unresolved slot
    suppresses the invocation and its entire output.
13. The lowerer consumes the recorded target-to-outcome mapping without
    rematching by name, position, or generated value.
14. All targets in one assignment group commit simultaneously.
15. Every checked access has an explicit OOB scope.
16. Every unchecked access belongs to a valid whole-domain affine proof.
17. Each source expression and nontrivial target is evaluated exactly as many
    times as the plan states.
18. The plan contains no LLVM value, machine type, pointer, register, or
    storage decision.
19. Every owned outcome is consumed at most once, with exactly one derived
    release obligation per path where it is not — yield, skip, taken and
    untaken lazy sides, rejected iterations, region end.
20. No outcome is used after it is moved or released, and a borrowed outcome
    outliving its owner is copied, materialized, or validly promoted first.
21. Replaced target and carry values stay live until every outcome in their
    simultaneous group has finished reading or consuming them.
22. A target- or carry-origin borrow is promoted to transfer only when its
    owner is replaced in the same group, exactly one owning consumer takes it,
    and no surviving outcome depends on the old owner.
23. A `[consume=%x]` annotation names an `owned`, `unique` outcome whose only
    use in its iteration or group is the consuming eval's single operand
    occurrence, and nothing uses `%x` afterwards.
24. Every `%` name — outcomes, carries, collectors, and domain binders — is
    allocated from one per-plan namespace and is unique within the plan, so
    every textual reference is unambiguous; a named node exposes its
    allocated name to validation. Names are display labels that lowering
    never parses.
25. In every failure scope, the checks run in source order, every `eval`
    that runs before a check supplies that check or an earlier one, and the
    scope's other work runs only after its checks pass (§3). Enforced from
    Step 6, which revisits the checked-only plans Step 5 builds in today's
    order; later steps build their plans this way from the start.

Validator failures are ICEs and include the source statement and the smallest
relevant PIR excerpt.

## 15. Solver Effects

Effects are solver-side facts derived for the script root, whose CFG checks
consume them; PIR consumes them as its steps migrate. Function templates use
the CFG's own classification of their text instead (#123; see CFG
consumption, below).
They are **per LHS slot**, not per statement, because a mixed assignment can
skip one target while another always writes: `a, b = arr[i], i + 1` is
`[]WriteEffect{MayWrite, MustWrite}`. The target-effect vector stays aligned to
LHS positions with discard entries structurally absent (§6), never compacted.

### WriteEffect

The semantic lattice has exactly two states:

| State | Meaning |
| --- | --- |
| `MustWrite` | Every execution of the statement commits this slot |
| `MayWrite` | Not guaranteed to write — may commit on zero or more executions |

`MayWrite` deliberately covers "writes on no execution at all", so a statically
empty range needs no third state. Ordering is `MustWrite ⊑ MayWrite`, join is
the least upper bound, chain height is one. Weakening toward `MayWrite` is the
safe direction: it silences a diagnostic rather than inventing one.

`Uncomputed` and `Invalid` are **analysis states outside the lattice** — they
track whether a result exists, not what it says. `Uncomputed` means no pass has
produced a value; `Invalid` means arity or type is unresolved and publishing is
an ICE. Neither participates in a join.

A slot is derived `MustWrite` unless statement conditions, possibly empty
ranges, checked-access failures, conditional propagation, or nearest-resolver
policy demand `MayWrite`. A checked or conditional outcome is `MayWrite` unless
a fallback or closing policy resolves every failure path **and** its enclosing
domain is guaranteed to execute: `x = arr[i] > 0 || 0` is `MustWrite` only for
a non-ranged `i` or a provably non-empty domain, since a fallback resolves a
failure within an iteration but cannot manufacture an iteration. With `i` a
possibly empty range it stays `MayWrite`, as does
`x = arr[i] > 0 || other[j] > 0` in every case. Statement-local effects are
rebuilt from scratch on every body walk.

### YieldEffect

`YieldEffect` describes whether an *expression outcome* produces a value, where
`WriteEffect` describes whether a *target slot* receives one. Yield effects are
`MustYield` or `MayYield`, per slot, aligned with `OutTypes`; write effects are
`MustWrite` or `MayWrite`.

Yield facts belong to the typed source AST nodes referenced by statements.
Compiler-local `Rewrite` nodes are lowering artifacts whose scalarization can
change their local domain, so they do not receive independent or copied
`YieldEffects`. A consumer lowering a rewrite retains the originating source
node and reads its effects there.

Composition: a checked access is `MayYield`; a `fallback` whose final
alternative is `MustYield` resolves to `MustYield`, otherwise `MayYield`;
`require` is `MayYield`; ordinary arithmetic propagates the join of its
operands.

Comparisons are **not** uniformly `MayYield` — the state follows the solved
comparison mode. A scalar value-position comparison may fail to yield. An
array comparison produces a length-preserving zero-filled mask and adds no
failure of its own, but still inherits failure from either operand; it is
`MustYield` only when both operands are. A multi-output comparison can
therefore mix both states across its slots, which is why derivation is per slot
from the solved mode and type rather than from the syntactic operator.

Calls are where the two effects interact. A failure evaluating the invocation
or its arguments suppresses the **whole tuple** — the call-merge rule — so
every slot of that call becomes `MayYield` together.

**Definite outputs (#123).** Every body that runs writes every output. The
template-level flow check (CFG consumption, below) decides this from the
function's text, the same for every argument type. Callee-internal non-writing therefore disappears, and the
transfer rule reduces to `rawYield[i] = invocationYield`. A caller-side
failure, such as the failed argument in `Id(arr[oob])`, makes every slot skip:
the commit is skipped and the target stays `MayWrite`. Otherwise every slot
yields. The one way left for an invoked call to leave its outputs unwritten is
an empty callee-owned domain, a `Range` or `ArrayRange` parameter that runs
zero iterations, which means no assignment. Until Step 7 chooses between one
"did execute" bit and a caller-side emptiness check, that case keeps the seed
resolution below.

**Seeds until Step 7.** A direct `I64`/`F64` result carries no validity bit:
a callee with a range parameter is seeded from the destination
(`compileCallInner`), so a call-owned domain that runs no iteration returns
the seed. After a successful invocation, seed resolution applies as a
**contextual resolver at the assignment boundary** for such a call at an
existing target: the seed *is* the keep-old outcome, so the consumer sees
`MustYield`. A caller-side failure is **never** converted into a seeded
`MustYield`. A nested or targetless call of such a callee is seeded with
zero; any other callee takes no seed (ABI 3.0, §1).

Boundary resolution implies an **implicit read of the destination seed**, and
only where the dependency is real: after a successful invocation, at an
*existing* target whose call owns a possibly empty domain, resolved at `=`. A
fresh destination, a discard, a nested or targetless call, or a call without
a call-owned domain introduces no implicit seed read. A declared output is
readable inside its template only after a statement that definitely assigns
it (the template check rejects an earlier read, including a formatting
marker), so the body cannot read the hidden seed through an output name. Step 2A records boundary
resolution as a `ReadsSeed` fact on the call site — the CFG is untouched in
2A — and Step 2B converts the fact into an ordinary CFG read event, so a
`MustWrite` classification cannot let backward liveness kill the prior value.

An earlier revision of this plan added a private `{value, didWrite}`
direct-call variant in Step 4, so that print and other failure-propagating
contexts could see a skipped direct return. #123 removed the skipped write
instead, so the variant is not built.

### Convergence and publication

**No body summary.** Every body that runs writes every output (#123), so a
call's slots yield exactly when the call is invoked: its arguments' yields,
joined with `MayYield` when a range that may run no iteration drives it. A
call-owned `Range` or `ArrayRange` domain controls whether the scalar body
executes, not what the body does when it executes, so a provably non-empty
literal keeps the call `MustYield` while an empty or unknown domain weakens it.
Statement effects therefore need no per-specialization summary and no fixed
point across recursive calls: the script root derives them in one pass. An
earlier revision folded each body into a `BodyOutputEffects` summary settled
callee-first over the call graph's strongly connected components; #123 made
every summary `MustWrite`, and both are gone.

**Finite specialization closure.** Settlement assumes discovery has already
produced a finite set of specializations. It cannot catch
`f(T) -> f(Array<T>) -> f(Array<Array<T>>) -> ...`: every specialization is a
new one, so discovery never closes. Before Step 2B, discovery caps active
specialization frames in one **recursive inference region** while attempting to
allocate a missing candidate. The region starts at the earliest active frame
whose function template repeats in the candidate path; flat sibling calls and
arbitrarily deep acyclic template chains do not consume the limit.
First-active-template indexes and the region boundary are maintained in constant
time, so a wide mutual cycle gets one bound rather than multiplying the limit by
its number of templates. The guard runs before cache allocation and reports the
recursive signature chain.

This is an operational cold/unsettled-discovery fuse, not a semantic program
property or proof that a particular signature transformation expands forever. A
settled cache hit requires no allocation or body walk and therefore consumes
none of the budget; warming a finite tail can change whether the resource limit
is reached. Comparing two concrete signatures is not enough to
prove growth: a larger re-entry may immediately target a fixed specialization.
A richer diagnostic is deferred until it can prove the repeated call-site
transformation. The fuse guarantees controlled compiler failure for unbounded
specialization discovery, not runtime termination; totality remains a separate
future analysis.

Only reusable function specializations publish, through `FuncInfo.Settled`,
which requires type convergence and agreement with the template's text range
summary (CFG consumption, below); a disagreement is an ICE. A script root owns
its current compilation facts and is consumed immediately after solving, so it
needs no settled-publication step.

Snapshot analysis such as `FuncInfo.Vars` is cleared and rebuilt whenever a
body is walked and becomes reusable only once settled. `ExprCache` is not a
precedent for append-only analysis: stable AST entries are overwritten while
generated rewrite nodes may add entries across walks. Issue #71's
compile-order-dependent wrong output came from reusing some body facts while
discarding others. Flow checks run once per template (CFG consumption, below),
so nothing about a specialization's diagnostics is cached or replayed.

### CFG consumption

**Flow checks per template (#123).** Output initialization, reads before
definite assignment, dead stores and write-after-write run in one pass per
template, which runs whether or not anything calls it. The pass classifies
writes from the text alone and conservatively: a statement gate, a range that
may be empty, or a value that can fail (a value-position comparison, `&&`, a
checked access, a `||` whose last alternative can fail, a call with such an
argument) makes a write possibly skipped, even where a specialization's types
would make it always write, as an array mask does. Only a range literal
written with constant, nonempty bounds always runs; a range bound to a name
may be empty. A call counts as writing every output, since its callee passes
the same check, so a caller of a rejected template is not reported again.

The pass also reads from the text which values are ranges, and so which
statements a range drives. A parameter never holds a range, since a range
argument drives the whole body; no call yields one, since a function cannot
return a range (an output assigned one is rejected at the definition, #146);
and struct fields, constants and array elements cannot hold one (`[r]`
collects `r`'s values). A value holds a range only when it is a bare range
literal or binding assigned without a gate (`x = r`, not `x = r * 2` or
`x = n > 0 r`): a gate makes its values computations, so a range in them
iterates (#146). A range literal or binding operand of `||` and `&&` is
iterated too (`n > 0 && 0:5` gives an element). A template's own text
therefore decides all of this, without looking at another template. Every
check then uses the same classification, and none waits for a type: after
`r = 0:n`, `out = r * 2` does not kill an earlier `out = prev`, does not
definitely assign `out`, and so cannot by itself make a later read of `out`
valid. Settlement panics when a specialization's Range-typed bindings
disagree with this summary.

The flow checks run after every template's structural checks: explicit
use-before-definition, illegal input/global writes, unused inputs,
syntactically unassigned outputs, formatting structure, and discard behavior.
Those collect all reads before declaring a statement's destinations, so a
fresh `x = x + 1` cannot define its own RHS. An unknown main format marker
remains literal text; malformed specifiers and missing dynamic width/precision
variables on a resolved marker remain structural errors. Alongside them, the
pass reports text it cannot classify, each an error the solver reports in any
specialization: a call that names no template, an operator whose sides do not
line up (value counts the solver rejects), and an assignment whose values do
not fill its targets. The flow checks run only once every template passes
both kinds of check, so they misread neither such a template nor a call to a
function rejected for holding a range in an output, which its callers read
as returning values. A template nothing calls gets these errors too. A name
reassigned between a range and a non-range value is left to the solver,
like any other change of type. The pass reads each name by its latest
assignment, so its flow checks follow the text as written and the solver
reports the retype only once they pass: a body with `r = n`, a print of `r`,
then `r = 0:n` and `res = r + 1` is reported only for leaving `res` possibly
unassigned. A template nothing calls is not checked for it. An error in any
template fails the code module before any script compiles.

This partly reverses Step 2B, which moved dead-store and write-after-write
checks to specializations; the per-specialization diagnostic cache and replay
are gone. Scripts move to the same classification in a follow-up (decided
2026-10-07). Until then, after a script solve succeeds, CFG treats the script
as a zero-input, zero-output template for structural validation, then runs
effect-sensitive dataflow over the typed body with a fresh scope. For a let,
event order is condition reads, RHS reads, `ReadsSeed` destination reads,
then sparse `StatementEffect.Writes` mapped by `TargetIndex`; all reads
therefore observe the simultaneous assignment's pre-commit snapshot. Print
arguments contribute ordinary reads even though prints have no statement
effect entry. Structural diagnostics do not gate that typed pass: its
already-collected explicit reads are reused so one compile reports all
independent CFG diagnostics without repeating formatting errors.

Both passes consume write kinds the same way:

- *Backward (dead store).* A write is dead when its destination is not live
  afterward, and that holds for `MayWrite` as well as `MustWrite`. What
  `MayWrite` changes is the **kill**: it does not kill the preceding value's
  liveness, because that value may survive.
- *Forward (write-after-write).* Reporting that a write overwrites an unused
  value requires **both** writes to be `MustWrite`. This cures the former
  conditional-write false positive that forced tests to interleave reads merely
  to silence it. A prior seed overwritten by a proven-`MustWrite` call output
  without being read is instead a true positive: remove the seed or read it
  explicitly when its value is semantically required. Under #123's
  template-level check, an `out = prev` default before a comparison override
  is live for every argument type, so it needs no exemption.
- *Shared inputs.* A body is analyzed once per template, with every input
  treated as its own value. Inputs are stable (#144), so sharing an input
  with an output at a call adds no reads; it can never make a body invalid,
  and a body must be valid without it:
  `out = current + 1` written twice is reported for `x = Twice(x)` as well as
  for `y = Twice(x)`, because the body never reads `out`. A body that means to
  build on its own write says so by naming the output, `out = out + 1`, which
  is readable once definitely assigned. Unused-write errors are errors rather
  than warnings throughout.

Inside a function body, domain ownership decides which statements an empty
domain can skip. A `Range` or `ArrayRange` **parameter** establishes a
function-level domain driving the whole body: it decides whether the body runs
at all, not what a body that runs writes, so the template check never treats
a parameter as a range. A locally created range owns only the statements it
drives — `t = a[i * n + j]` under a body-local `j = (i + 1):n` has an
RHS-local domain, not a function-owned one.

The CFG pass itself stays: dataflow legality is a different question from
ownership checking. This fix does not depend on PIR and lands before it.

## 16. Migration Roadmap

Pluto has no users yet, so migration optimizes for the clean end state:

1. The semantics documents are the specification; the existing suites
   (`go test -race ./...`, `python3 test.py`, `python3 test.py --leak-check`)
   are the regression baseline. Every cutover reruns them before and after; a
   difference is either a plan-path bug or a deliberate fix landing with its
   semantics-doc and `.exp` update. Never port a bug for compatibility.
2. No user-facing flag, and no fallback once PIR accepts a statement. A
   temporary capability router decides per statement whether the plan path
   supports its combination; unsupported combinations take legacy lowering. A
   build, validation, or lowering failure on an accepted statement is an ICE,
   never a silent retry. The router dies in Step 10.
3. ABI, mangling, and cache layout may change freely — the cache is
   version-keyed and nothing external links against Pluto output. The §1
   seed-parameter rule survives only as internal coherence.
4. The migration unit is a capability combination, not a dispatcher branch.
   `compileLetStatement`'s branches are not disjoint: its plain tail carries
   ranges, collectors, checked accesses, calls, and heap values;
   `compileCondExprStatement` handles ranged logical trees; `compileCondStatement`
   feeds the general assignment path. Axes: gate (none/scalar/ranged); RHS
   capabilities as **composable flags** (conditional, checked, ranged,
   collector, call — one RHS can be several at once); for call rows, whether
   a possibly empty callee-owned domain can leave the outputs unwritten (the
   only way left after #123); value kind
   (scalar/heap/multi-output/self-referential/descriptor/struct/table); target
   kind (local/output/discard); statement form.
   Each range domain also carries a role: descriptor value, RHS-local, shared
   gate, collector-local, function-owned, callee-owned. Each PR migrates a
   tested cell or rectangle, and the router keys on the same axes.
5. Legacy code is deleted with its **actual** last consumer. Step 1 found those
   are not where this plan first assumed: besides prints, ordinary expression
   lowering (ranged infix, prefix, calls, array indexing, array-literal cells)
   calls the same conditional and collector helpers, and
   `compileInfixExpression` reads the condLHS frame directly. **Resolution:**
   expression-side orchestration migrates too, as nested PIR regions in
   Steps 6-8 — a conditional array cell becomes a `require`/`fallback` region
   inside its collector, a ranged call becomes a `domain` around an `eval`.
   Leaving it in the backend would break §13, since it is exactly AST
   classification and strategy selection. Primitives survive (arithmetic and
   comparison emission, storage, loop and guard emission); classification,
   condLHS extraction, and collector rewriting do not. Step 9 splits
   accordingly. Evidence and the full consumer table live in
   [the capability matrix](./Pluto%20PIR%20Capability%20Matrix.md).

### Step 1: Inventory and corpus (~1 week) — complete

Deliverable: [the capability matrix](./Pluto%20PIR%20Capability%20Matrix.md) —
the reachable-combination table with per-row disposition, tests, cutover step,
and notable removable helpers; plus new fixtures added without changing
lowering, and the contracts this plan now records.

Decisions recorded, and mirrored in the semantics docs where they are language
rules: `_` is a per-slot `discard` sink whose owned outcome is consumed at its
smallest owning region's exit (§6) — it previously bound one shared typed
symbol that every blank in a statement aliased, so a repeated heap blank
leaked and then aborted; print is one N-ary invocation gated on every
argument yielding, retaining today's conditional suppression (§3); the
gate-versus-slot rule, with gated print marked as future syntax (§9); the carry
seed is borrowed (§7); the effect lattice, body-to-output fold, callee-first
SCC convergence, comparison and direct-call yield rules, and CFG transfer rules
(§15); an unreachable `.pt` template gets structural checks only (§15).

The `_` sink shipped in its own PR, Step 1's last prerequisite: blanks are no
longer bound or typed, so repeated blanks stay independent, a discarded
temporary is released and a discarded named value survives.
`tests/discard.spt` covers repeated blanks, mixed types, heap outcomes,
repeated statements, borrowed survival, checked access, ranged multi-output
blanks, a conditionally-writing callee on both paths, and blanks under gates
and ranges. **Step 2 is complete.** Gated print syntax is planned with
Step 6 (§9); any language change gets its own PR with its semantics-doc
and rejection-test updates.

### Step 2: Write effects on settled specializations (~1-2 weeks)

Two PRs, implementing §15.

- **2A — effect model, CFG unchanged.** `WriteEffect` and `YieldEffect` types
  with per-slot alignment (discard slots structurally absent, §6), derivation,
  SCC convergence, the `ReadsSeed` fact for boundary-resolved direct calls,
  and the publication lifecycle. Tests cover derivation, the recursive fixed
  point, lifecycle, and caching.
- **2B — specialization-aware CFG — complete.** Dead-write and
  write-after-write run on settled specializations under the transfer rules;
  `ReadsSeed` facts become ordinary pre-write CFG reads; templates keep only
  structural checks; each specialization caches immutable diagnostics and
  complete direct-call keys; scripts replay the reachable closure once; and
  the duplicated syntactic effect classifiers are deleted. #123 moved these
  diagnostics back to one conservative pass per template, and removed the
  per-specialization caches, the replay and the effect fixed point (§15).

Step 2 fixes the conditional-write false positive without depending on PIR.
The Step 2B regression and leak suites pass, so PIR implementation can resume.

### Step 3: Minimal end-to-end PIR slice (~1-2 weeks)

- The `pir` package: plan nodes, builder, validator, printer, and lowerer for
  the smallest real vertical — `eval` over unmanaged values (scalars and
  Range descriptors), local and unmanaged `discard` targets, simultaneous
  `commit`. Cut it over through the router immediately rather than building
  every future node in shadow mode first. (Heap and multi-output discard
  ownership follows in Step 4; the legacy `_` sink is fixed in its own PR
  before this step.)
- Settle the package boundary: `pir` cannot import `compiler.Type` or
  `ExprInfo` without an import cycle, so either the facts-to-plan adapter lives
  in `compiler`, or shared semantic DTOs are extracted first.
- Deterministic `-emit-pir` / `-emit-pir=expanded` output and golden tests for
  every migrated cell.

**Go/no-go:** the dump must explain a migrated statement's lowering without
reading LLVM helper code.

Step 3 is complete. The `pir` package holds the plan nodes, the structural
validator, and the deterministic renderer; the package boundary is settled
with the facts-to-plan adapter (capability router plus builder) and the plan
lowerer in `compiler`, and `pir.Type` an opaque name interface satisfied by
`compiler.Type` — no shared DTO extraction was needed. The router cuts over
script-root assignments of unmanaged values — scalars and Range descriptors —
from ordinary RHS expressions to local and discard targets; accepted
statements lower eval-then-commit through the existing expression compiler,
and everything else keeps legacy lowering. Function-body locals stay legacy
until Step 4 handles output targets. Planned statements no longer emit the
legacy per-RHS bounds-guard alloca or the dead old-value load (LLVM deleted
both); suite output is unchanged.

### Step 4: Heap values, multi-output, calls, ownership (~2-3 weeks)

- Heap and multi-output assignments, calls, swaps, duplicate sources, and
  heap/multi-output `discard` ownership (§6).
- The validator's display-spelling type equality becomes the compiler's
  directional binding-compatibility relation (`StrG` into `StrH`, an
  empty-array reset) — never mangle equality, which is neither identity nor
  compatibility.
- Field reads: `DotExpression` enters the router with struct values, so a
  scalar field read assigns through the plan path (matrix row 2b); the
  `person.name` and `%t0#1.name` goldens of §17 land here.
- The ownership elaboration pass (§8), with generic cleanup lowering and
  the consuming-operand promotion; its copy-loud goldens are listed in §17.
- **Calls split by argument yield.** #123 landed before this slice: every
  body that runs writes every output, and ABI 3.0 (#69) removed the seed and
  the write markers from functions without a range parameter, so every
  non-ranged callee is all-`MustWrite`. An argument whose conditional or
  checked failure remains unresolved still makes the invocation `MayYield`,
  which needs Step 6's checks-first evaluation (§3). So Step 4 migrates
  calls whose arguments are all `MustYield`; any other call defers **as a
  whole** to Step 6, because argument evaluation, tuple failure, and
  ownership are shared across its outputs and individual slots cannot migrate
  separately. Argument yield is therefore a router axis, recorded in the
  capability matrix (rows 6, 6b, 6c). The earlier plan also split on callee
  output effect and built a `{value, didWrite}` variant for print; #123
  removed both for every call without a callee-owned domain.

**First slice landed — heap values and ownership elaboration.** The router
admits heap value kinds (both string flavours, arrays, tables) and struct
values from ordinary expressions — string literals, concatenations, inline
array literals, struct field and table column reads (`DotExpression`) — into
local and discard targets. The builder annotates every outcome slot
(`unmanaged` by type, `borrowed` for a binding read, `owned` otherwise) —
a slot keeps the solver's semantic type while its ownership
follows the binding's **effective storage**, the type of the value held
now: after `text = "old"` the read solves as a static string while the
binding stores a materialized heap copy, and a heap value moved, copied, or
transferred into a binding declared static keeps its flavor, so that
binding can hold heap state its declared type does not show — and every local
target with its merged target type (`TypeOwnsHeap`: that type requires
heap cleanup, so an unmanaged value stored here is copied), whether it
is fresh, and whether the value it holds owns heap state (`HoldsHeap`: a
replacement must take or release it), read from the same effective storage;
`pir.Elaborate` derives each mapping's transfer (move, copy — a moved
borrow is the promoted transfer of §8) and the statement-exit releases — a heap transfer
into a binding declared non-owning is legal and leaves it holding heap
state, which the next statement's plan reads back; `Validate` checks
every decision and applies the compiler's directional binding-compatibility
relation; the lowerer implements the recorded transfers and releases and
decides nothing; a plan lowered wrongly is the leak-checked suite's to
catch, the runtime backstop §8 names. Elaboration of a target declared
static that holds heap is pinned on hand-built plans by `pir`'s
`TestElaborate`. No fixture reaches it from source, because #150 types a
binding that copies or takes over a widened binding as heap, like the binding
it copies (Step 9b's `yieldedStorage`). The transitive case — transfer a
widened binding into another, then read and replace the second — is pinned
end to end in `tests/mem/mem.spt` for strings and by
`TestPlanGoldenEffectiveStorage`: the second binding is declared heap, so
replacing it copies the static literal and releases the old value. Heap swaps show two transfers and zero copies, a duplicate
source is taken once and copied once, and heap/struct/table copies, field
and column reads, and heap discards are pinned by goldens in
`compiler/pir_test.go` (matrix rows 2, 2b, 4, 5b, 35b, 36b, 36g).

**Second slice landed — calls and multi-output outcomes.** The router admits
a call anywhere in an ordinary expression tree, as the whole right-hand side
or under operators, when its arguments are ordinary trees themselves. No
ordinary node can fail, so every argument is `MustYield` without the router
reading the effect; a call given a range, a comparison, or a checked read
stays legacy with that argument's node (matrix rows 12, 13 and 6c;
`TestPlanRouterRejections` pins one of each). Step 5's first slice, which
admits checked reads, keeps them out of call arguments by position, since a
checked argument defers the call to Step 6; it cannot read a call's yield
effect, which the solver derives for the script root only. The call stays inside its `eval` (§5):
one eval per right-hand expression, with a slot per output, each owned when
its type holds heap state and unmanaged otherwise — a static string or a
struct output is unmanaged — and each commit mapping names its outcome and
slot, so the targets after a multi-output expression follow the recorded
mapping. The lowerer hands the call no destination. A callee without a
range parameter reads no seed (#69), its outputs land in slots of their own,
and the plan's `commit` is the only write to a target, so `a = F(a)` passes
the value from before the statement (§6, `tests/alias_input`). The bindings
a statement passes by reference move to memory before its code, as before a
legacy statement (`TestPlanPromotesReferenceArgsFirst`). Pinned by
`TestPlanGoldenCalls` (matrix rows 3, 5c, 6, 6b, 35d); a table output (36d)
plans in `tests/array/array_func`.

**Third slice landed — function bodies and output targets.** The router
runs on the body of a function specialization without a range parameter,
so its ordinary assignments plan as the script's do, a function local being
a `local` target. A target that names a function output is an `output`
target (§6). It follows a local's transfer and release rules, because the
output's binding holds a value — its slot's seed, or an earlier write —
that a replacement takes or releases; it is never fresh, and the validator
admits it only in a plan that names its function. The lowerer is unchanged:
a commit stores to the output's binding, a direct scalar output staying an
SSA value (row 14) and an indirect one writing through its slot (row 14i),
and no statement sets a write marker. A body a range parameter drives sits
under a function-owned domain and stays legacy as a whole (row 14b, planned
for Step 7), as a gated body statement does (row 14a, planned for Step 6).
Pinned
by `TestPlanGoldenFunctionOutputs` and, on hand-built plans, by `pir`'s
`TestElaborate`, `TestValidateRejects` and `TestRenderFunctionOutput`.

**Fourth slice landed — ordinary prints.** A print whose every argument is
an ordinary expression, as the router admits for an assignment's value,
lowers from a `PrintPlan` (§3; rows 29, 29b, 29c, 35c, 35e, 36c and 36e),
at the script root and in a body without a range parameter. The plan has no
`commit`: one `print` invocation closes `execute` and takes every argument
in source order.

```text
print
    source
        "n: -n", h, h ⊕ "!"

    execute
        %t0 = eval borrowed Str h
        %t1 = eval owned Str h ⊕ "!"
        print "n: -n", %t0, %t1 [move]
```

An argument that is a string literal is the invocation's own operand, not an
`eval`: print formats it in place, reading the bindings its markers name,
and never builds the interpolated string. As a value, a literal with
markers is that string, owned, while one without markers is static in
either position. Formatting a marker's value can still allocate a
temporary — for an array, a float, a range, a table, or a `%q` or `%x`
conversion — which belongs to the backend: it is freed after the write and
is no plan outcome. Any other argument is an `eval`, and the invocation
prints each slot of its outcome. The invocation takes each owned slot and releases it, which
the expanded view marks `[move]`, and reads any other slot where it lies;
with nothing to suppress it there is no other path, so the plan carries no
`drop`. How the line is assembled stays the backend's choice (§3): each
argument is formatted where it is evaluated, a string is released after the
write and any other value once it is formatted, as before. Prints with a
comparison, a checked read or a range in an argument stay legacy (planned
for Steps 5, 6 and 8), as does every print of a range-driven body. Pinned
by `TestPlanGoldenPrints` and, on hand-built plans, by `pir`'s
`TestRenderPrint` and `TestValidatePrint`.

**Fifth slice landed — block-layout literals.** An array literal whose rows
sit on their own lines, and a table literal, plan like an inline literal
(rows 2c and 36): the value is an owned outcome, and each cell is held to
the router's rules for any operand, so a literal with a ranged, conditional
or checked cell stays legacy as a whole (row 36f; a ranged cell in a block
literal is #67). Only the text form needed deciding (§12): the operand is
the literal as the source spells it, with each line break escaped.

```text
assign scores
    source
        scores = [
          : Name Score
            "Ada" 10
        ]

    execute
        %t0 = eval Table[Name:Str Score:I64] [\n  : Name Score\n    "Ada" 10\n]

    commit
        scores = %t0
```

A literal printed directly plans too, as an owned outcome the invocation
takes. The literal itself lowers through the same code as before. Pinned by
`TestPlanGoldenStructAndTable` and `TestPlanRouterRejections`.

**Remaining in Step 4:** field and column
reads of a widened binding (a struct whose string fields widened to heap
storage), whose lowered value follows the effective storage rather than the
solved type and so stay legacy — the follow-up resolves the field's
ownership against the receiver's effective storage, as identifier reads
already do, once `compileDotExpression` types the field by that storage as
well; the `%t0#1.name` renderer golden, which
needs an outcome-referencing operand; and the `unique` annotation and
consuming promotion, which have no in-place consumer before Step 7's carried
append.

### Step 5: Checked accesses and OOB scope (~1-2 weeks)

- Checked access and per-RHS skip scope are foundational, not a late
  optimization: every ordinary assignment already creates per-RHS bounds state
  (`compileExprAssigns`) whose failure selects keep-old versus commit
  (`commitAssignmentsPerExpr`). Plans record explicit OOB scopes (§9), and the
  bounds-bit idiom reduces to generic guard emission driven by them.
- Checked accesses migrate here in today's order, as the matrix's row 7
  retains it: an out-of-bounds read is still tested where it is read, so
  `y = Loud(1) + arr[oob]` still calls `Loud`. Testing it first needs Step 6's
  `require` regions, so Step 6 revisits these plans (§3).
- Checked-access fallback becomes legal here (decided; see the semantics doc):
  `x = arr[oob] || -1` assigns `-1`, the fallback testing the yielded bit and
  never the value. The solver currently rejects the spelling —
  `conditionPropagates` excludes checked failure, conflicting with §15's
  checked = `MayYield` / fallback composition. **This introduces the PIR
  `first` operation and its `fallback` sides in restricted form** — a
  checked-access left side with an ordinary alternative, in assignment
  position, with regression tests. Step 6 extends the same operation to full
  value conditionals and print position; it does not add a second one. The solver change is equally
  restricted: `conditionPropagates` feeds `expressionCanFail`, which
  statement-gate validation and logical `&&` share, so widening it to accept
  checked accesses would prematurely legalize checked `&&` and bare checked
  statement gates. Step 5 instead adds a fallback-specific rule accepting
  only a checked-access root immediately left of `||`, leaving
  `conditionPropagates` untouched; Step 6 then generalizes checked failure
  propagation through comparisons, calls, `&&`, and exactly the gate contexts
  that are explicitly intended.
- The router records an explicit affine → force-checked rule, so affine
  accesses reach checked PIR until Step 10 restores the fast path.

**First slice landed — assignments with a checked index read.** An ordinary
assignment whose value holds an index read plans (matrix row 7, and row 5d
for a `_` target), at the script root and in a body without a range
parameter. The read stays inside its `eval`, which records the scope every
access in it has: a failed read skips the eval's outcome, every slot of it
(`[on-oob=skip]`). Each mapping of that outcome to a binding records what
its target then holds (`[on-skip=keep]` or `[on-skip=zero]`, §9), set by the
builder and checked by the validator against the outcome and the target's
freshness.

```text
assign skeep
    source
        skeep = skeep ⊕ sarr[9]

    execute
        %t0 = eval owned Str skeep ⊕ sarr[9] [on-oob=skip]

    commit
        skeep = %t0 [move] [on-skip=keep]
        drop %t0 [on-skip]
        drop skeep [old]
```

The order is today's, left to right: a read is tested where it is read and
gives a zero when it fails. The operators after it still compute with that
zero, so the outcome holds a value on the skip path too. A call does not:
it consults the eval's guard, so it runs only when every read lowered before
it was in bounds, as a literal cell is stored only then. So
`y = Loud(1) + arr[oob]` calls `Loud`, which has run by the time the read
fails, and `y = arr[oob] + Loud(1)` does not. Step 6's checks-first order
(§3) is planned to change the first of the two. `cond/value_cond_expr` pins
both at run time (`PosNotYet`, `PosValueRead`).
Elaboration derives the releases of both paths. An owned outcome moves into
its binding where it yielded and is released where it skipped
(`drop %t0 [on-skip]`); one mapped to `_` is released either way; and a
replaced value is released only where its mapping committed. A borrow takes
its owner's old value (§8) only when its own mapping and the owner's
replacement both always commit. In `a, b = b, sarr[9]` the replacement of
`b` may skip, so `a` copies; in `a, b, n = b, a, arr[9]` the swap still
moves both strings, where legacy lowering copies them: in a statement that
holds a checked read it decides each right-hand expression's copies alone,
so it never sees that the other side replaces the owner.

The validator enforces invariant 15 on every eval: an access outside a
literal cell needs the eval's scope, and a scope needs an access. An access
in a literal cell is rejected, because the cell resolves it and no plan node
records that scope yet; so is a print eval that may fail, and a scope the
plan does not define.

The lowerer emits the guard from the plan. An eval with the scope runs under
a bounds guard of its own, which each read ANDs its in-bounds bit into, and
the guard's final value is the outcome's yield bit. Before any mapping
lands, a binding with `[on-skip=keep]` moves to memory and one with
`[on-skip=zero]` starts at its zero, so both paths reach one storage. The
mappings of each outcome then land under its bit, with the skipped releases
on the other arm, and the replaced values are released under the same bit
after every mapping. Replacing a fresh binding's zero releases it, which is
the backend's way of giving the binding that zero and is no plan release.
`commitAssignmentsPerExpr` and `ensureSeededDest` still serve the legacy
statements.

Staying legacy, as before: a read in a literal cell, which zero-fills the
cell while the statement always commits (the literal stays legacy as a
whole, planned for Step 8 with the collector nodes); a read in a call
argument (row 6c, planned for Step 6); a print with a read (row 31); a read
beside a comparison or under a gate (row 21, planned for Step 6); and a
ranged read (planned for Step 7). The affine rule above is this step's later
slice.

Over the fixtures as they stood before this slice, 76 more assignments plan
and the machine code of 87 of the 88 executables is unchanged.
`mem/leak/oob_paths` differs in one statement, without which it is unchanged
too, `guardKeep, guardOther = Static(), arr[9]`: a planned call is
handed no destination (Step 4's second slice), so its static result is
copied once at the commit, where legacy lowering staged the output in a slot
typed like the target, seeded it with an empty heap string, copied the
result in and freed the seed. Pinned by `TestPlanGoldenCheckedReads` and
`TestPlanRouterRejections`, on hand-built plans by `pir`'s `TestElaborate`,
`TestValidateRejects`, `TestValidatePrint` and `TestRenderSkip`, and at run
time by `array/oob_skip`, `discard` and `mem/leak/oob_paths`.

**Second slice landed — the fallback after a checked read.** `x = arr[oob] ||
-1` assigns `-1` (matrix row 7b). The solver's rule is the fallback's own
(`fallsBackFromRead`): an index read without a range fails the left side of
`||` wherever a condition would, so a wrapped read `(arr[i] + 1) || -1` and a
chain `a[i] || b[j] || -1` are accepted. `conditionPropagates` is unchanged,
and a read still gates no statement and no `&&`. A left side only a ranged
read can fail (row 7e) stays an error.

A value that is such a `||` plans as a `first` (§4, §12), in an assignment,
when each of its sides is an expression an eval admits. A chain is one
`first` however it is grouped, since it takes the first side that yields
either way.

```text
assign s
    source
        s = sarr[9] || h

    execute
        %t0 =
            first Str
                %t1 = eval owned Str sarr[9] [on-oob=fallback]
            fallback
                %t2 = eval borrowed Str h

    commit
        s = %t0 [move %t1] [copy %t2]
        drop %t1 [on-fallback]
        drop s [old]
```

A side with a `fallback` after it must be able to fail, and its reads carry
`[on-oob=fallback]`. The last side's reads carry `[on-oob=skip]`: where it
fails too the outcome skips, and only then does the mapping carry a skip
policy, so `x = arr[9] || -1` prints `x = %t0` for a fresh target and an
existing one alike. The validator checks each of these on every `first`.

Each side keeps its own ownership, and nothing is copied where the sides
meet (the rule Step 6 plans for every conditional value): the commit takes
the side that yielded by that side's transfer, choosing by the yield bits.
A borrowed side is always copied. It supplies the value on one path only, so
it cannot take its owner's old value, which every other path would then have
to release.

The lowerer runs the sides in order, each after the first only where the one
before it failed, and stages each value and yield bit in memory, since a
side after the first runs on a path of its own. The mapping then lands on
the path of the side that yielded, with the release of what each failed side
built on the way, so its binding sits in memory: an existing one moves
there, and a fresh one starts unwritten when the `first` always yields. A
failed read can hold heap state: a row read past the end of a matrix builds
a zero row, which `drop %t1 [on-fallback]` releases.

Staying legacy: a `||` after a read anywhere but the whole of an
assignment's value, which is in a print, a literal cell, a call argument,
under an operator or a gate, and beside a comparison. The solver's rule
admits those too, and legacy lowering resolves them as the semantics doc
says: the read's failure is one of its lane's conditions there already.

Pinned by `TestFallbackAfterRead`, `TestPlanGoldenFallbacks` and
`TestPlanRouterRejections`, on hand-built plans by `pir`'s `TestElaborate`,
`TestValidateFirstRejects` and `TestRenderFirst`, and at run time by
`array/oob_fallback`.

### Step 6: Non-ranged gates, value conditionals, prints (~2-3 weeks)

- `gate` with keep-old/zero commit policies; `require`, `first` beyond a
  checked read, `last` for the value-position `&&` (§12), `map`,
  `align`, per-slot skip, with the builder splitting every conditional node out
  of `eval` and emitting each failure scope's checks before the work they
  gate (§3), including in the checked-only plans Step 5 migrated.
- Ownership of `require`, `fallback` and repeated values comes from
  elaboration (§8), as for every other outcome: each side stays a borrow of
  its binding or an owned temporary, and nothing is copied where the sides
  merge. A consumer that stores the value (a commit) copies a borrow and moves
  an owned value; one that only reads it (an `eval` operand, a print
  argument) uses either in place; and each region releases only the owned
  values no consumer took on that path. Legacy lowering instead copies each
  borrowed value into an owned result slot (`storeLogicalValue`,
  `resolveRepeatedSide`), which caused #116 and #130 and copies once per
  operator in a chain. Acceptance tests, with `s` and `t` heap strings that
  outlive the statement (a static string's copy count depends on its target,
  and a borrow of an owner the statement replaces can be promoted to a move):
  `out = n > 0 && s || t` copies `s` once, at the commit (legacy lowering
  copies it into the `&&` slot and again into the `||` slot); printing
  `c > 0 && s` copies nothing; `(c > 0 && s) ⊕ "!"` copies `s` only into the
  concatenation's result; and `x = c > 0 && s || F()` copies `s` when the
  left side yields and moves `F()`'s result otherwise, choosing by the yield
  flags rather than a runtime ownership bit.
- Need through comparison operands, the rest of #148's rule 3: an `&&` or `||`
  passes the slots its context needs into a comparison's operands, which lower
  slot by slot, so a slot nothing needs is never computed. Legacy lowering
  stops need at a comparison, because it computes every operand slot at once.
  Acceptance tests, with `A` standing for `Pair(1, 2) > Pair(0, 5)`, which
  fails its second slot, and `G` printing its argument:
  `x, y = A && ((A || G(7)) > 0)` calls no `G`, and
  `x, y = A && ((Pair(10, 10) ÷ (A || Pair(G(7), G(8)))) > 0)` calls no `G`
  and doesn't divide by the slot the `||` leaves empty. `logical_ops`'s
  `NeedStopsAtComparison` pins the legacy calls, and its `.exp` changes here. So
  does the stop test's `need_stops_at_a_comparison`: a zero divisor in that right
  side stops the program today, even for a slot an earlier check failed.
- Calls with an argument whose conditional or checked failure remains
  unresolved (matrix row 6c), which gain checks-first evaluation (§3) with
  regressions: neither `F(sideEffect(), failingArg)` nor
  `F(failingArg, sideEffect())` runs the side effect, and
  `F(sideEffect(), passingArg)` runs it once. After #123 no non-ranged call
  defers here for its callee's write effects. `value_cond_expr`'s `Pos` blocks
  pin that no call beside a failed check runs, in values, calls, cells and
  prints, and its `PosNotYet` block pins the evaluation still to change, so that
  block's `called` lines go here.
- The non-ranged prints still on legacy lowering join the ordinary ones
  Step 4 planned as `PrintPlan`s (§3), so that every non-ranged print lowers
  from a plan: a call that runs writes every output, so a call argument
  fails only through its own arguments or invocation.
  The conditional suppression outcome is retained, and an OOB read becomes a
  check, so its materialized zero becomes invocation suppression (§3, §9). Update
  `tests/array/oob_print.exp` in the same PR, with regressions for a
  side-effecting or owned-heap sibling of a failed conditional or OOB read,
  which never runs or allocates, and for a failed sibling suppressing a
  complete multi-line `Struct` emission.
- The gated print (§9) lands with this step, in its own PR: the parser, AST
  and solver first, then its `PrintPlan` under the statement `gate`.

**First slice landed — the statement gate over one comparison.**
`x = n > 0 5` plans (matrix rows 5e, 14a, 15, 16, 17). The condition is
planned as a value is (§12): the comparison is a `check`, whose outcome is its
left operand and which yields where the comparison holds, and a `gate` takes
that outcome and holds the statement's values in its region.

```text
assign s
    source
        s = MkStr(n) > "a" t

    execute
        %t0 = check owned Str MkStr(n) > "a"
        gate %t0
            %t1 = eval borrowed Str t

    commit
        s = %t1 [copy] [on-skip=keep]
        drop %t0
        drop s [old]
```

A rejected gate leaves every mapping unapplied, so each binding under a gate
carries the policy a skipped outcome gives it: `[on-skip=keep]`, or
`[on-skip=zero]` for a fresh target. A gate rejects all of a statement's
mappings or none, so it stands in no borrow's way: `a, b = n > 0 b, a` still
moves both strings. The gate reads whether its check yielded and takes
nothing, so elaboration releases what the check built (`drop %t0`), whether
or not it yielded. A read in an operand gives the check `[on-oob=skip]`: a
failed read skips the check, and the gate rejects.

The validator checks that a gate closes its region, takes the one check
before it, and admits at least one value; that a check has one slot and no
fallback scope; and that every binding under a gate carries its policy.

The lowerer compares the operands, releases what the gate only tested, and
gives each binding storage that both paths reach before they part. The values
then run and commit only where the check yielded. A target the statement
introduces gets its storage before the paths part and its name only after
the values have run, so in `fresh, text = n > 0 5, "-fresh"` the marker stays
text, as it does without a gate. Legacy lowering instead
seeds a slot for each target with a copy of the value it holds, on both
paths, and commits the slot where the paths meet, so a gated statement's
machine code changes with this slice while what it does stays.

Staying legacy: a gate that is not one comparison of one scalar slot over
operands an eval admits. That is an `&&` of comparisons, which the second
slice plans as a `last` (§12); an `||` of them, a chain and a comparison of
several slots, each planned with the node that carries it; a gate over a value that
does not plan; and a ranged gate (planned for Step 7). A mask is no gate at
all: the solver rejects an array as a statement condition.

Over the fixtures as they stood before this slice, 79 more assignments plan,
as 84 plans over the specializations of their bodies, and the machine code
of 76 of the 89 executables is unchanged. The other 13 each hold a gated
statement whose code differs as above. With the router admitting no gate,
all 89 match the code before this slice and every plan's text is unchanged,
so the slice touches no statement without a gate.

Pinned by `TestPlanGoldenGates`, `TestPlanGoldenFunctionOutputs`,
`TestPlanRouterRejections` and `TestPlanGateReleasesCheckFirst`, on
hand-built plans by `pir`'s `TestElaborate`, `TestValidateGateRejects` and
`TestRenderGate`, and at run time by `mem/mem_cmp_lhs` (`BuiltLeft`,
`BuiltRight`), `array/oob_skip` (`CondOOBSkips`), `str` (`own markers`)
and the gated statements the fixtures already held.

**Second slice landed — `last` for `&&`.**
An `&&` plans as one `last` (matrix rows 15, 19), over a gate's conditions
and in value position. Its sides run in order while each yields. A side
before the final one is a comparison, a `check` that is only tested, and the
`last` yields its final side's value where every side did.

```text
assign t
    source
        t = MkStr(n) > "a" && MkStr(x)

    execute
        %t0 =
            last Str
                %t1 = check owned Str MkStr(n) > "a"
            next
                %t2 = eval owned Str MkStr(x)

    commit
        t = %t0 [move] [on-skip=keep]
        drop %t1
        drop t [old]
```

A gate takes the `last`'s own outcome. The conjuncts of its `&&` are the
sides, each a check, so `three = n > 0 && n > 1 && n > 2 7` prints three
checks under one `last` and then `gate %t0`. A chain is one `last` however it
is grouped, as a chain of `||` is one `first`.

In value position the final side is an eval, and a mapping takes the
`last`'s value from it by one transfer. A check before it can fail, so the
binding always carries `[on-skip=keep]` or `[on-skip=zero]`, and its borrow
takes no other binding's old value: `t, u = n > 0 && u, t` copies both
strings, where the gated swap of the first slice moves both. What a tested
check built is released once it is compared (`drop %t1`), in a gate and in a
value alike. The final side runs only where the checks before it held. What
it built is moved where it yielded, and released where it ran and then
failed a read, or where it feeds a `_`.

The validator checks that a `last` has at least two sides of one slot each,
numbered after it; that every side before the final one is a check; and that
the final one fits the `last`'s type and is an eval where a mapping takes the
value, a check where a gate does.

The lowerer runs each side under the yield bit of the one before it. It
stages the final side's value in memory, with the bit that says every side
yielded and, where the final side can itself fail, the bit that says it ran.
A gate branches on the first bit as it does on a check's.

Staying legacy: a value-position `&&` whose final side is itself a
condition, which the third slice plans where it is one comparison; an `&&`
with a side of several slots; an `&&` under an operator, in a cell or a call
argument, or as a side of an `||`; and an `&&` in a print.

Over the fixtures as they stood before this slice, 40 more assignments plan,
13 gates and 27 values, each as one plan. The machine code of 87 of the 89
executables is unchanged; `cond/logical_ops` and `cond/value_cond_expr` hold
statements whose code differs, as a gated statement's did in the first slice.
With the router admitting no `last`, all 89 match the code before this slice
and every plan's text is unchanged, so the slice touches no other statement.

Pinned by `TestPlanGoldenLasts`, `TestPlanRouterRejections`,
`TestPlanGoldenPrints` and `TestPlanLastReleasesFinalSideWhereItRan`, on
hand-built plans by `pir`'s `TestElaborate`, `TestValidateLastRejects` and
`TestRenderLast`, and at run time by `cond/logical_ops` (`AndReadInBounds`,
`AndHeapReadFails`) and the statements the fixtures already held, among them
`cond/value_cond_expr`, `assign` and `math/rec`.

**Third slice landed — a comparison as a value.**
`x = a > 2` plans (matrix rows 19, 21). The comparison is a `check`, as a
gate's condition is, and the binding takes its outcome, the left operand,
where the comparison holds; where it fails, the binding carries
`[on-skip=keep]` or `[on-skip=zero]`.

```text
assign s
    source
        s = MkStr(a) > "zzz"

    execute
        %t0 = check owned Str MkStr(a) > "zzz"

    commit
        s = %t0 [move] [on-skip=keep]
        drop %t0 [on-skip]
        drop s [old]
```

A check among a gated statement's values sits in the gate's region as any
value does, so `g = n > 0 a > 2` prints a check, the gate, and a second check
one level in. An `&&` may end in one: `lb = x > 2 && x < 9` is a `last` of two
checks whose outcome is the final one's. The builder takes each side's kind
from the side itself, a comparison of one scalar slot being a check and any
other side an eval, so a gate's conjuncts and a value's sides follow one rule.

A check a mapping takes is not only tested: what it built moves into its
binding where it holds and is released where it failed
(`drop %t0 [on-skip]`), a final side's only where it ran. The comparison may
fail, so its borrow takes no other binding's old value: `t, u = u > "", t`
copies both strings.

The validator holds a check among the values to the rules of a gate's
condition and lets a mapping take its one slot; a `last` may end in a check
wherever a mapping takes its value.

The lowerer compares the operands and commits the left one where the
comparison held. A `last` stages its final check's left operand as it stages
a final eval's value, with the bit that says the side ran.

Staying legacy: a comparison of several slots and a chain; a comparison
under an operator, in a cell or a call argument, or as a side of an `||`; a
comparison with an operand an eval does not admit, such as a ranged one
(planned for Step 7) or one whose read sits in a call argument
(`Id(a[0]) > 0`); and a comparison in a print.

Over the fixtures as they stood before this slice, 69 more assignments plan,
each as one plan: 57 with a comparison among their values and 12 whose gated
value is one. The machine code of 86 of the 89 executables is unchanged;
`cond/value_cond_expr`, `mem/mem_str` and `numeric/divide` hold statements
whose code differs. With the router admitting no comparison as a value, all
89 match the code before this slice and every plan's text is unchanged, so
the slice touches no other statement.

Pinned by `TestPlanGoldenCheckValues`, `TestPlanGoldenLasts`,
`TestPlanRouterRejections` and `TestPlanLastReleasesFinalSideWhereItRan`, on
hand-built plans by `pir`'s `TestElaborate`, `TestValidateCheckRejects` and
`TestRenderCheck`, and at run time by `cond/logical_ops` (`AndEndsInCheck`)
and the statements the fixtures already held, among them
`cond/value_cond_expr`, `cond/gated_value` and `numeric/float`.

**Fourth slice landed — a `first` whose sides may be conditions.**
`x = a > 2 || 7` and `x = a > 0 && y || z` plan (matrix rows 7b, 19). A side
of a `first` takes its kind from the side itself, as a `last`'s sides do: a
comparison of one scalar slot is a `check`, a value-position `&&` is a `last`,
and any other side is an `eval`.

```text
assign t
    source
        t = MkStr(a) > "a" && MkStr(b) || h

    execute
        %t0 =
            first Str
                last Str
                    %t1 = check owned Str MkStr(a) > "a"
                next
                    %t2 = eval owned Str MkStr(b)
            fallback
                %t3 = eval borrowed Str h

    commit
        t = %t0 [move %t2] [copy %t3]
        drop %t1
        drop t [old]
```

A `first` or a `last` binds an outcome where a mapping can take it, at the top
of a value; a `last` that is a side of a `first` binds none, its value
reaching the mapping through the `first`'s own outcome, so its head sits where
the outcome line would have gone (decided 2026-11-20). The outcome IDs stay
dense in execution order through each side's own nodes, so the side after a
`last` of two sides takes the third ID. A mapping names the node each side
supplies its value from, which for a `last` side is its final side, and the
checks before that side are only tested, so what one built is released once it
is compared (`drop %t1`).

The fallback after a side resolves a failed access anywhere in that side, a
nested `last`'s own sides included, so a `check` side carries
`[on-oob=fallback]` exactly where an `eval` side does and what it built is
released where the fallback ran (`drop %t1 [on-fallback]`). A side the
`fallback` resolves must be able to fail; a `check` and a `last` always can,
and an `eval` must hold a checked access, as before. Whether the mapping
carries a skip policy follows the side no `fallback` follows: `x = a > 2 || 7`
always commits, while `x = a > 2 || a > 3` and `x = a > 9 && 1 || b > 0 && 2`
carry `[on-skip=keep]` or `[on-skip=zero]`.

The validator holds each side to the rules of its own kind and checks that a
`last` that is a side of a `first` binds no outcome, that each side's type
fits the `first`'s, and that no node inside a side is mapped.

The lowerer runs each side as before, under the bit that says the side before
it failed, and stages the value each supplies: an `eval`'s or a `check`'s
outcome in memory of its own, and a `last`'s final side's in the `last`'s own
staging. A side runs on a path of its own, so the bits the commit reads are
cleared before the sides part: the bit that says the side yielded, as before,
and, for a side that is a `last`, the bit that says its final side ran and
staged a value to release. A `last` writes the bits its caller cleared, so a
side of a `first` keeps none of its own.

Over the fixtures as they stood before this slice, 28 more assignments plan,
each as one plan: 13 with a comparison for a side and 15 with an `&&`. The
machine code of 84 of the 89 executables is unchanged; `cond/logical_ops`,
`cond/value_cond_expr`, `numeric/divide_checked`, `array/oob_fallback` and
`math/rec_infer` hold statements whose code differs, as earlier slices' did,
while `numeric/float`'s planned `ao = nan != 0.0 || -1.0` compiles to the
same code as before. With the router admitting no condition for a side, all
89 match the code before this slice and every plan's text is unchanged, so
the slice touches no other statement.

Staying legacy: an `||` as a statement's gate, which needs a `first` whose
outcome is only tested; an `||` as a side of an `&&`, as in
`(a > 9 || b > 0) && 7`, which needs a `first` that is a side of a `last`;
a side of several slots; a side whose own `&&` does not plan; and an `||`
under an operator, in a cell, in a call argument or in a print. The gate's
slice is where both nestings arrive, since each needs one of those two.

Pinned by `TestPlanGoldenFirstConditions`, `TestPlanRouterRejections` and
`TestPlanFirstReleasesLastSideWhereItRan`, on hand-built plans by `pir`'s
`TestElaborate`, `TestValidateAccepts`, `TestValidateFirstSideRejects` and
`TestRenderFirst`, and at run time by `cond/logical_ops`
(`FirstLastRanAndFailed`, `FirstLastFallbackTested`, `FirstLastDiscarded`)
and the statements the
fixtures already held, among them `cond/value_cond_expr`,
`numeric/divide_checked` and `array/oob_fallback`.

### Step 7: Ranges and carries, then ranged conditionals (~3-4 weeks)

- RHS-local ranges and carries first: `domain` nodes, iteration snapshots, and
  simultaneous advance per §7.
- Then ranged gates and ranged conditional combinations, including the ranged
  logical trees `compileCondExprStatement` routes to staging today; `continue`
  versus `skip` scopes become explicit. Each iteration's failure scopes take
  §3's checks-first order here, so a comparison with no range stops running
  before the loop of a ranged check beside it. The stop test's
  `ranged_check_before_a_divide` pins today's stop.
- Step 6's need through comparison operands, for ranged conditionals. Over
  `i = 0:3`, `x, y = Pair(i, 1) > Pair(0, 5) && ((Pair(i, 1) > Pair(0, 5) ||
  G(i)) > 0)` calls `G` on no iteration (legacy lowering calls it on 1 and 2).
- Extend ownership elaboration to carries. Loop emission (`withLoopNest`,
  `createLoopCore`) stays as generic mechanics driven by `domain` regions.
- Callee-owned domains under #123. Zero iterations mean no assignment,
  wherever the loop is placed: choose how a callee-owned loop reports an empty
  domain, one "did execute" bit or a caller-side emptiness check, and drop the
  range-parameter seed with the last `ReadsSeed` facts. When the call shares
  its destination, each iteration's input reads the previous iteration's
  result and stays stable within the iteration; a callee-owned loop carries
  that value, or the loop runs caller-side.
- Carried appends use the consuming promotion (§7): one seed copy, then n
  in-place appends. The amortized bound also needs a growth-capable runtime
  buffer, scheduled with this step.
- The plan-wide name allocator (§12) and invariant 24 for the nodes this
  step introduces: domain binders and carries expose their allocated names,
  `Validate` rejects a duplicate, and goldens pin a range named `t0`, a
  range named `sum_carry` beside target `sum`'s carry, and deterministic
  `_<K>` suffixing.

### Step 8: Collectors and remaining prints (~2-3 weeks)

- Collector initialization, cell skip policies, finalization, and nested
  collectors as plan nodes per §10; collector cells join ownership
  elaboration and take §3's checks-first order.
- Array-valued cells drop the copy legacy lowering makes of each row into a
  cell slot (#150): a named row is borrowed and a fresh row (a call, a mask,
  an `&&` or `||` copy) moves in, so `x = [a b]` copies the rows' elements
  into `x` and nothing else. A borrowed row still skips with its cell: with
  `a = [1]` and `row = [1 2]`, `x = a[5] + [row (1 > 0 && row)]` keeps `x`,
  whereas reading `row` past the failed `a[5]` would leave the rows' shapes
  unequal and abort the shape check.
- A failed array-valued cell zero-fills in the shape its operands show: an
  array it divides, a row it reads, a literal without ranges. Where they show
  none, as for a call, a `⊕` or a ranged literal, the solver rejects a cell
  that may still fail after its own fallbacks, so no closing policy takes the
  shape of the rows that yield, and legacy lowering's pending-row count
  (`failedRowsSlot`) and choice of the first yielded row's shape
  (`fixedRowShape`) are never ported. The rule lands with matrix row 7f,
  before which a cell can't take `||` after a failed read, and no later than
  collectors. It breaks programs that rely on the rows' shape. Every index
  read is `MayYield`, so `[Row(d[i])]` needs `|| [0 0 0]` or `Row(d[i] || 0)`
  even when `i` stays in bounds; `oob_skip`'s `RowsSkipCall`, `FixedZeroFill`
  and `StringRowsFill` gain a fallback, and `CallRowsNoShape` becomes the
  test of the error. §18's shapes derived from bound range domains later
  accept more programs.
- The remaining ranged and collector print paths migrate. After this step no
  statement form needs the legacy conditional or collector machinery.
- Collectors join the name allocator and invariant 24: collector nodes
  expose their allocated names, duplicate rejection covers them, and a
  golden pins a range named `result_collector` beside target `result`'s
  collector.

### Step 9: Delete the legacy machinery

Precondition: the Step 5 affine → force-checked rule has already carried every
affine-containing statement through checked PIR, so nothing is orphaned —
Step 10 only restores the optimization. Delete inside the PR that removes each
last consumer where possible; sweep the rest here, in two buckets.

**9a — statement-only helpers**, gone as soon as their statement class
migrates:

- `compileCondStatement`, `compileCondExprStatement`, and the conditional
  temp-output staging (`createConditionalTempOutputs`,
  `commitConditionalOutputs`, `aliasCondDests`, `restoreCondDests`)
- `splitCondRanges`, `compileCondRangedStatement`, `compileCondRangedIteration`,
  and the ranged staging machinery (`stageCondRangedExpr`,
  `stageCondRangedAssignments`, `commitCondRangedStages`,
  `createStageTempOutputsFor`, `commitStageTempOutputs`)
- `compileConditions`, the `statementArrayCollector` trio with its two
  exclusive array helpers (`array.go:496`, `array_nd.go:113`),
  `commitSlotValue`, `compilePerSlotAssign`/`perSlotCommittable`
- the `compileAssignments`/`exprAssign` per-expression commit machinery
  (`compileExprAssigns`, `commitAssignmentsPerExpr`, `keepPriorOnSkip`,
  `newExprAssign`). It serves far more than Step 3's plain class: heap and
  multi-output assignments (Step 4), the per-RHS bounds bit (Step 5),
  conditional lowering through `compileCondAssignments` (Step 6), ungated
  ranged assignments (Step 7), and ungated collector expressions (Step 8). It
  is deleted only after every remaining caller has migrated — Step 8 at the
  earliest — and not again in Step 10.
- `evalConditions`, `andGates`, `compileGate`, together with
  `withCondRangeLoop`'s guarded arm and its `condExprs` parameter: the
  statement path is the only source of a non-empty `condExprs`, so every
  expression-side caller passes nil and that arm dies with the statement path.
  `withCondRangeLoop` itself survives as loop-nest emission.

**9b — orchestration helpers**, deleted only after Steps 6-8 migrate ranged
infix, prefix, calls, array indexing, and array-literal cells:

- the condLHS extraction spine (`extractSlotConds`, `extractComparisonSlots`,
  `extractFallbackOrSlots`, `extractGatingAndSlots`, `logicalSlot`,
  `condTemp`), `compileCondOperands`, `compileCondExprValue`, and the condLHS
  frame itself. The frame provides evaluate-once identity, substitution,
  comparison reuse, and temporary ownership — more than classification — so
  some value plumbing at the `eval` boundary may survive in another form. Its
  deletion is demonstrated, not assumed.
- the collector rewrite machinery — all of `collect.go`
- mask sweeps and consumed-temporary marking, gated on leak checks passing
  with cleanup emitted solely from derived release obligations
- `yieldedStorage` and its uses, with #114: call arguments, `&&` and `||`
  operands and assignment targets (#150) take the whole-body storage of the
  binding they copy, so a binding that copies a widened one is typed heap and a
  later static store into it copies the literal. Elaboration reads what each
  binding holds now (`storedType`) instead; the rule is planned to be removed
  once every consumer does and #114's per-statement ownership replaces the
  whole-body join.

### Step 10: Affine versioning and final sweep (~1-2 weeks)

- Move affine form recognition and whole-domain versioning (`isFastAffineAccess`,
  the decision side of `withLoopNestVersioned`) into the builder; the checked
  path stays the semantics-first fallback. Remove AST-pointer-identity
  decisions and statement bounds orchestration.
- Affine proofs use overflow-safe arithmetic throughout — coefficients,
  endpoints, negative steps, extreme `I64` bounds — extending `addInt64` and
  `mulInt64`; the current guard already checks its endpoints at run time
  (#156).
- Delete the router and the old dispatchers. `compileLetStatement` and
  `compilePrintStatement` are the router's own entry points, so they are the
  last things to go, here. The helper-to-release-step inventory is Step 9's
  9a/9b buckets; the matrix lists per row only the removable orchestration
  helpers it uses.
- Field, index, column, and cell targets extend `commit` as their source
  features land — feature-driven, outside the migration clock. Whole struct
  and table values are already current capabilities.

### Deletion discipline

Each capability cell follows one rule:

1. Add its golden plans and E2E coverage, then run race, E2E, and leak checks
   on the legacy lowering to fix the baseline.
2. Switch the cell to PIR in the router and rerun; output and diagnostics must
   match, except where the PR deliberately fixes a legacy bug with its
   semantics-doc and `.exp` update.
3. Once the router accepts a cell, plan failures are ICEs — never a fallback.
4. Remove legacy code with its actual last consumer, in the same PR. The
   router's shrinking legacy set is the deletion checklist.

Zombie fallbacks hide plan bugs and double every future semantics change.

### End state

One statement-plan builder, one ownership elaboration pass, one validator, one
generic plan lowerer, the existing reusable primitives, and no duplicated
statement classification or specialized conditional orchestration. Surviving
reorganized rather than deleted: loop and guard emission, generic storage
across branches and iterations, the expression compiler for `eval` regions, the
CFG pass (one flow pass per template, scripts included once they move to it,
whose text-only write classification deliberately stays separate from the
solver's effects, §15),
and the runtime.

Per-step deletions are targets, not guarantees — each lands only when its step
proves the plan replaces it. The estimated steps total roughly 14-22 focused
weeks plus the unestimated Step 9 sweep — Steps 4 and 7 (ownership; ranges
and carries) carry most of the schedule risk — proceeding cell by cell with
immediate deletion at the last consumer.

## 17. Testing Strategy

### PIR golden tests

- solved statement → deterministic concise and expanded PIR
- canonical output uses four-space indentation, no tabs, braces, or `end`
- no LLVM context required
- negative tests for every validator invariant implemented by the current
  step
- derived release points appear in expanded PIR, so ownership regressions
  surface as plan diffs before any leak-check run
- copies are loud: expanded goldens assert the exact set of derived copies —
  zero for a swap, for a carried append after its seed copy, and for a
  collector move — so a silent materialization is a test failure rather than
  a later benchmark surprise
- each golden cites the plan section it pins, so spec drift surfaces as a
  citation that no longer matches
- a multi-output outcome renders per-slot selectors (`%t0#0`, `%t0#1`), and
  the renderer rejects a node kind outside the router's set; checked-access,
  call, and array-literal goldens land with the steps that admit them; a
  Unicode label and binding (`π = 3.14`, `τ = π`) are pinned now
- field access lands as three regressions: `person.name` in Step 4, which
  admits `DotExpression` with struct values; a direct pir-level renderer
  test for slot-then-field composition (`%t0#1.name`) in the same step,
  since the grammar permits it even though today's `.` requires a
  single-output receiver, so no source statement produces it; and
  `%person_carry.name` in Step 7, from a range-driven statement whose RHS
  reads a field of its own carried target — `i = 0:n` followed by
  `person = Rename(person.name, i)`

### Specialization-closure tests

- direct and mutual rank growth fail with the active signature chain, while
  ordinary recursion, finite polymorphic recursion, synthesized range/scalar
  companions, broad non-recursive specialization sets, and warm-cache reuse
  remain accepted; a wide mutual growth cycle shares one recursive-region bound,
  and a settled finite tail is explicitly outside the cold-discovery budget

### Effect tests

- mixed RHS produces per-slot effects: `[]WriteEffect{MayWrite, MustWrite}` for
  `a, b = arr[i], i + 1`
- shared conditions and possibly empty ranges produce `MayWrite` for keep-old
  or unresolved last-yield targets, while an ungated collector or cell-local
  zero-fill policy still produces `MustWrite`
- a fallback resolving every failure produces `MustWrite`; one whose final
  alternative can still fail stays `MayWrite`
- multi-output expressions keep independent per-slot effects, and an argument
  failure suppresses a call's whole tuple
- a caller-side call failure (`x = Id(arr[oob])` with an all-`MustWrite`
  callee) leaves the target `MayWrite` and records no `ReadsSeed`; a possibly
  empty callee-owned domain at an existing target resolves to the seed as
  `MustYield` with a recorded `ReadsSeed` until Step 7 replaces the seed
- #123: flow checks run once per template, including one nothing calls: a
  body that can leave an output unwritten is rejected at the definition;
  `out = x > 0` alone is rejected for every argument type; `out = prev` then
  `out = x > 0` is accepted for scalar and array `x`, with no diagnostic for
  the array specialization's dead default; after `r = 0:n`, `out = prev`
  then `out = r * 2` is accepted and `F(y, 0)` keeps `y`, while without the
  default both the body and a read of `out` after `out = r * 2` are
  rejected; `y = Helper(x)` then `out = y + 1`, where `Helper` returns a
  scalar, is a definite write; each way a value holds or iterates a range (a
  copy, a gate, a literal or binding under `&&`) agrees with the solved
  types; an output assigned a range is rejected (#146), with no
  flow error in any template, so none in a caller declared before or after
  it or calling it through a function that forwards the call; a call that
  names no template, an operator whose sides do not line up, or an
  assignment whose values do not fill its targets is reported where the
  solver reports it, with no flow error in any template; and a body that
  reassigns a name between a range and a non-range value, and passes the
  flow checks as written, gets only the solver's retype error

### Loop-carried tests

- `sum = sum + 1` observes the previous iteration; `arr = arr ⊕ [2]` observes
  and replaces it
- siblings under one shared domain point read the same iteration-start
  snapshot, while RHS-local ranges stay independent
- sibling carries advance simultaneously; one skipped RHS keeps its carry while
  another advances
- a rejected shared gate advances no carry and appends no cell
- nested ranges carry state in execution order; final LHS values equal the last
  carried values

### Commit and transfer tests

- scalar and heap `a, b = b, a` swaps preserve pre-commit values, and expanded
  PIR shows one ownership transfer rather than two deep copies
- one source mapped to multiple owning targets derives the required copy and is
  never moved twice
- each multi-output expression maps to its intended slot; one skipped target
  keeps its old value while siblings commit
- replaced heap targets stay alive until every sibling outcome is done with
  them
- a ranged swap reads one snapshot, advances both carries simultaneously, and
  exposes the advanced pair to the next iteration
- a ranged append `arr = arr ⊕ [x]` shows one seed materialization and n
  consuming in-place appends; a plan whose consumed operand has a second
  use — an eval of `%carry ⊕ %carry` — is rejected (§14.23), and an owned but
  copy-on-write-shared buffer detaches before an in-place update

### Print tests (land with Step 6)

- an unresolved OOB argument suppresses the complete invocation and newline:
  `arr[oob], val1, val2` and `a, arr[oob], arr2[oob], b` emit nothing
- a resolved fallback lets the whole line print:
  `a, arr[oob] || -1, arr2[oob] || -2, b` emits `a -1 -2 b`, and an in-bounds
  zero emits `0`
- empty strings are successful values, distinguishable from suppression:
  `a, "", "", b` emits `a` and `b` separated by three spaces
- a failed conditional or OOB argument suppresses the whole invocation before
  its siblings run: a side-effecting callee beside it never runs and an
  owned-heap sibling is never allocated, while a heap value a check computed
  is released
- a failed sibling suppresses a complete multi-line `Struct` emission
- a ranged print emits one emission group per fully-yielded point; OOB
  iterations emit nothing
- a suppressed invocation still releases its owned temporaries (leak-checked)
- ordinary calls check first too (§3): neither `F(sideEffect(), failingArg)`
  nor `F(failingArg, sideEffect())` runs the side effect, and the output tuple
  keeps its old values; `F(sideEffect(), passingArg)` runs it once; in a
  passing statement a check's call runs first, so
  `x = Loud(1) + (Loud(2) > 0)` prints `called 2` before `called 1`; and a
  recursion guarded by a comparison beside its call terminates
  (`res = Fact(n - 1) * (n > 1)`)
- checks stop at the first failure and otherwise keep source order:
  `a, b = Pair(1 > 2, Loud(7) > 0)` and `"print", 1 > 2, Loud(8) > 0` call no
  `Loud`; `y = (Loud(1) > 0) + (Loud(2) > 0)` prints `called 1` before
  `called 2`; a later failed check releases what earlier checks allocated
  (`"p", ("x" ⊕ s) > "a", 1 > 2`, leak-checked); and sibling right-hand
  sides stay separate scopes, so `c, d = 3 > 5, Loud(10)` calls `Loud` and
  sets `d` (§3)
- a check's dependencies run once: `y = (Loud(1) > 0) + 1` calls `Loud` once
  and adds 1 to its result, and with `Wrap` returning a heap string,
  `"p", Wrap(s) > "a", 1 > 2` releases `Wrap`'s result when the later check
  fails (leak-checked)
- failures stay in their scope: a failure an `||` resolves is no check of the
  scope around it, so `x = Loud(1) + ((1 > 2) || 5)` calls `Loud` and gives
  6, and a call a surviving slot needs still runs, so
  `u, v = (Pair(1, 2) > Pair(5, 0)) + Pair(Loud(2), Loud(3))` calls both,
  keeps `u` and sets `v` to 5

### Cutover and backend tests

- each cutover PR runs the full suites before and after switching its cell
- retain focused LLVM tests for lazy placement and affine fast/checked loops
- `go test -race ./...`, `go vet ./...`, `python3 test.py --leak-check`
- a performance baseline for cross-statement temporaries (`b = [f(i)]` then
  `c = [g(b[i])]`): PIR is per-statement, so `b` is materialized and never
  fused away; the baseline records that cost instead of hiding it

## 18. Future Extensions

The statement plan can grow without becoming a machine IR:

- field, index, column, and cell targets extend `commit` with the same
  snapshot semantics as every other statement: RHS reads see the pre-commit
  value and element writes land at commit, so `arr[2:7] = arr[0:5]` is
  well-defined. Disjoint target elements remove target-overlap staging and
  make the write a candidate for parallel lowering once dependence and
  effect analysis prove the iterations independent — RHS reads, calls, I/O,
  collectors, and carries can still impose order; an overlapping read and
  write range stages through a temporary unless §11's affine analysis proves
  the ranges disjoint or the copy direction safe
- member calls remain solved expressions inside `eval` or `map`
- source `break` and `continue` extend structured range actions
- function-result transfer reuses outcome planning with a different terminator
- conditional arrays extend domains, alignment, and yield masks
- range-left value-position `&&` can later bind an outer local domain for
  nested construction such as `[i && [matrix[i][j]]]`; it must stay local to
  that value and must not become a statement gate or implicit collector — only
  an explicit `[]` closes the bound domain into an array
- positional outcome groups such as `(m, n)` could later generalize `require`
  and `fallback` to tuples — `a, b = val > 5 && (m, n) || (1, 2)`. Parentheses
  are pure grouping today, so this is a real language feature (flattened slots
  versus tuple value, per-slot versus group failure, arity, ownership,
  nesting), deferred until after Step 6 handles multiple outcomes. It must
  never become statement-gate-specific fallback sugar, because gate and
  `require` are different PIR operations with different failure actions (§9):
  a rejected gate is `continue` — the region is never entered, a ranged
  collector cell is filtered out (`arr = m > 5 [m + 1/2]`), and a non-ranged
  commit keeps old — while a `require` failure is `skip`, a missing outcome
  that `fallback`, a collector's zero-fill, or `=` resolves
  (`arr = [m > 5 && (m + 1/2) || -1]`). A gate never supplies a value, which is
  why `x = a > 2  3 || 7` stays an error.
  Multi-return calls (`val > 5 && Pair(m, n) || Pair(1, 2)`) and the
  seed-then-gate idiom (`a, b = 1, 2` then `a, b = val > 5 m, n`) already
  express the semantics today
- a skipped array-valued collector cell closes to a zero-filled child of its
  expected shape, while `||` may supply a shape-compatible child. Step 8
  requires a fallback wherever the operands show no shape; this extension also
  derives shapes from bound range domains, so only a child whose shape is
  neither known nor derivable needs an explicit fallback such as `[j && 0]`.
  The solver reports a missing one, since validator failures are ICEs (§14)
- test contexts can become explicit statement inputs and effects

PIR stays statement-focused until a concrete feature requires cross-statement
dataflow, keeping its value proportional to the compiler complexity it
replaces.
