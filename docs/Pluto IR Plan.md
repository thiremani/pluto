# Pluto Statement IR (PIR) Plan

**Status:** Accepted 2026-08-05 — roadmap in §16; implemented through Step 3
(minimal statement slice)

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
ABI. The CFG never reads PIR — both consume the solver's effect summary
independently (§15).

PIR may refer to solved AST expressions, but LLVM lowering must not reclassify
their range, conditional, OOB, collector, affine, or commit behavior.

**ABI note.** Per-slot effects are an internal analysis, never a public ABI
classifier. Every exported direct `I64`/`F64` return keeps its hidden seed
parameter: a body-only edit must never change the C prototype of a
type-mangled symbol. A seedless variant requires a distinctly named private
clone behind the stable entry point.

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
output tuple old. For each admitted point, `PrintPlan` evaluates and finishes
every flattened argument outcome, ANDs their yielded bits, and invokes print
**exactly once, only if every slot yielded** — an unresolved argument emits
neither content nor newline. A `fallback` resolves before this boundary, so
`a, arr[oob] || -1, arr2[oob] || -2, b` emits `a -1 -2 b`, while
`a, arr[oob], arr2[oob], b` emits nothing at all.
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

The suppression *outcome* for a failed conditional matches today, but the
model is **eager**: `PrintPlan` evaluates and finishes every argument in
source order before ANDing the yielded bits, whereas today
`compileCondOperands` evaluates the extracted conditions first and compiles
the sibling arguments only on the success branch. Sibling evaluation is
therefore a Step 6 **behavior change**: a sibling with observable effects — a
callee that itself prints, an owned heap temporary — now runs, and is
released, even when the invocation is suppressed. The other Step 6 change is
the OOB case, whose materialized zero becomes suppression under the same
call-level validity rule (§9).

Argument evaluation is **strict** for every invocation, print included: an
argument's side effects — including I/O once the language has it — happen
in source order whether or not the invocation is suppressed, for
`x = F(io(), arr[oob])` exactly as for a print. Suppression is a property of
the invocation's output, never of argument evaluation. This is a Step 6
behavior change for ordinary calls as much as for print: today both evaluate
the extracted conditions first and their sibling arguments only on the
success branch, so `r = Pair(Loud(a), c > 5)` currently never runs `Loud`.
The regressions in §17 pin both together.

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
| `gate` | Admit or reject one shared statement iteration for its whole region |
| `require` | Lazily evaluate a local value region only when its left outcome yields (the value-position `&&`, as `fallback` is the value-position `||`) |
| `fallback` | Lazily evaluate an alternative for missing outcomes |
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
| `output(name)` | Function output binding; a commit on an indirect output also updates its runtime write flag (direct scalar outputs have none) |
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
`target <- outcome` mapping, and the lowerer must not reconstruct it from
names, result order, or LLVM values.

A commit group follows one transfer contract:

1. Every RHS outcome is produced against the pre-commit binding snapshot.
2. The complete outcome-to-target mapping is known before any target changes.
3. Moves, copies, and retained borrows are planned across the whole group.
4. All mappings take effect simultaneously in Pluto semantics.
5. Replaced values are released only after no mapped outcome can still
   reference or consume them.

For example, `a, b = b, a`:

```text
%to_a = eval I64 b
%to_b = eval I64 a

commit
    a <- %to_a
    b <- %to_b
```

For owned heap values this may lower to an ownership swap without deep copies.
If one owned source feeds multiple targets, at most one consumer takes it; the
others require a derived copy.

The same snapshot rule holds across a call boundary: in `a = F(a)` the callee
reads the pre-call value through its read-only input for the whole call,
while its output writes land in the destination-seeded staging slot and reach
`a` only at commit. `tests/alias_input` pins this for direct scalars, heap
strings, and arrays (`y = x * 2` then `y = y + x` yields 15 for `a = 5`, not
20); Step 4's call lowering must preserve it.

## 7. Loop-Carried State

A ranged assignment that reads its own LHS needs explicit carried state, so
that iteration `n + 1` observes iteration `n` while the real target is
committed only after the domain finishes:

```text
statement update_sum_arr
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
        sum <- %sum_final
        arr <- %arr_final
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
An enclosing gate that already admitted the region does not reach back to
resolve a later failure inside it.

For assignment, a failed statement gate blocks every sibling write, while an
OOB in one ordinary RHS leaves only that RHS's target unchanged.

Print has **no gate**: `ast.PrintStatement` carries only an argument list.
Its failure resolver is the **invocation boundary** (§3): print is one N-ary
call, so an argument failure that no closer `||` resolves suppresses the
complete invocation and all of its output, by the same call-merge rule that
keeps `Id(arr[oob])`'s tuple old. A failed *conditional* argument already
suppresses the emission today — every argument's conditions are ANDed into
one gate around it — and that **outcome is retained**, but evaluation becomes
eager (§3): today the gate also skips *evaluating* the sibling arguments,
while `PrintPlan` evaluates every argument before ANDing. Step 6 therefore
changes two observable things: the OOB case — today `arr[oob], val1, val2`
prints `0 val1 val2`, afterward nothing — and sibling side effects, which now
occur even on a suppressed invocation. The suppression is silent by design;
whether a debug build should report a suppressed invocation is a language
decision outside this plan, which neither requires nor precludes it.

A gated print such as `arr[oob] val1, val2` — rejecting the whole line without
evaluating the siblings — is **scheduled syntax, not current language**: it
lands in its own PR before Step 6 (§16).
It does not parse: the parser builds a `PrintStatement` only from a plain
expression list, and the solver rejects an OOB read as a statement condition.
Adding it is a parser/AST/solver feature with its own semantics-doc entry,
tests, and capability rows, and it must specify that the gate tests the
access's yielded/in-bounds bit rather than its value, so an in-bounds zero
still admits the region.
Once print arguments are strict (§3), the gate is the only spelling of "do
not evaluate the rest", and it gives print the same gate-versus-value pair
that assignments already have.

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

Checked accesses have one canonical representation — `eval` with an explicit
`[on-oob=...]` scope — regardless of which legacy path produced them. A
**caller-side** failure — a failed invocation or argument — is atomic: the
call's whole output tuple keeps its old values. A call that actually ran may
omit individual outputs independently (callee-internal non-writing, §15);
per-slot divergence otherwise exists only where slots carry independent
conditions.

## 10. Collectors

```text
statement collect_result
    prepare
        %result_collector = collector [I64]

    execute
        domain %i = range 0, n
            %cell = eval I64 data[%i] [on-oob=skip]
            collect %result_collector <- %cell [policy=append-yielded]

    finish
        %result_final = finish [I64] %result_collector

    commit
        result <- %result_final
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
start.

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
value (§4); a commit mapping reads `target [: Type] <- value`,
its type shown in the expanded view; square brackets carry declarative
policies, not executable code. A
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
reset). Validation compares this spelling for now; Step 4 replaces that with
the compiler's directional binding-compatibility relation.

The renderer covers exactly the node kinds the router admits and rejects any
other as an ICE, so each step that widens the router adds the renderer and
golden for its new node kinds. `commit` and `advance` carry no mode keyword:
both are always simultaneous (§14).

```text
statement assign_x
    source "x = a > 0 && data[i] || -1"

    execute
        %result = fallback I64
            primary
                %condition = eval I64 a > 0 [yield=scalar]
                %selected = require I64 %condition
                    %loaded = eval I64 data[i] [on-oob=skip]
                    yield %loaded
                yield %selected
            otherwise
                %default = eval I64 -1
                yield %default

    commit
        x <- %result
```

The `statement` header carries a display label derived from the targets
(`assign_a_b`); it is not unique and nothing references it — plans are
emitted in source order and have no independently referenceable textual
identity. Two views: `-emit-pir` is the concise semantic plan; `-emit-pir=expanded` adds
result shapes, target mappings, access IDs, affine forms, collector and carry
details, ownership annotations, and derived release points. Compiler temporary
names and node IDs stay hidden in the concise view.

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

Validator failures are ICEs and include the source statement and the smallest
relevant PIR excerpt.

## 15. Solver Effects

Effects are solver-side facts consumed independently by the CFG and by PIR.
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
every slot of that call becomes `MayYield` together. But the callee's own
output slots keep **independent** `WriteEffect`s: one conditional output does
not make its siblings conditional. A slot is `MustWrite` exactly when its
outcome is `MustYield` and no enclosing gate, empty domain, or resolver policy
weakens it.

**Direct-return calls carry no validity bit today.** An indirect output has a
runtime write flag, but a direct `I64`/`F64` result is a single value seeded
from the destination (`compileCallInner`), so a callee that wrote nothing
returns the seed and the caller cannot tell "skipped" from "yielded the seed".

Two failure sources must not be conflated:

- **Caller-side failure** — the invocation or an argument failed, as in
  `Id(arr[oob])`. The call never meaningfully ran, the call-merge rule applies,
  and the whole tuple is `MayYield` regardless of return mode.
- **Callee-internal non-writing** — the callee ran but did not write this
  output. A per-output fact, not a tuple fact.

The transfer rule keeps the two failure sources separate — conceptually
`rawYield[i] = invocationYield && didWrite[i]`. A caller-side failure makes
`invocationYield` false: every slot skips, the commit is skipped, and the
target stays `MayWrite`. `x = Id(arr[oob])` with an all-`MustWrite` `Id` is
`MayWrite` — a caller-side failure is **never** converted into a seeded
`MustYield`. Only after a successful invocation does seed resolution apply,
as a **contextual resolver at the assignment boundary** for a direct
`MayWrite` output resolved at an existing target: the seed *is* the keep-old
outcome, so the consumer of the seeded result sees `MustYield` for the
callee-internal component. Print and every other failure-propagating context
consume the raw, validity-carrying result and see the skip.

Boundary resolution implies an **implicit read of the destination seed**, and
only where the dependency is real: after a successful invocation, at an
*existing* target whose direct callee output is `MayWrite`, resolved at `=`.
A fresh destination, a discard, a nested or targetless call, or an
all-`MustWrite` callee reads nothing. Step 2A records this as a `ReadsSeed`
fact on the call site — the CFG is untouched in 2A — and Step 2B converts the
fact into an ordinary CFG read event, so a `MustWrite` classification cannot
let backward liveness kill the prior value.

The validity-carrying result comes from a **private direct-call variant**
behind the stable seeded entry point (§1). The clone **keeps the seed
parameter** — the seed is a real input, serving as the initial loop-carried
state of a ranged body and as the self-reference value — and returns
`{value, didWrite}` (aggregate or out-flag; an internal ABI detail the
lowerer owns), where `value` equals the seed whenever `didWrite` is false.
The public seeded symbol keeps its exact prototype and delegates: it calls
the clone and returns `value`, which preserves today's
`didWrite ? value : seed` behavior by construction, including recursive and
output-self-referential bodies, whose internal calls may target the clone
directly. The builder selects the variant for every consumer that needs
failure propagation — Step 6's print invocation, where the raw bit joins the
aggregate all-arguments-yielded condition, and any future failure-propagating
context — while plain assignments keep the cheaper seeded entry. For a callee whose body is driven by a function-owned
`Range`/`ArrayRange` domain, `didWrite` is the OR of the per-iteration writes.
The variant lands in Step 4; letting print treat a resolved seed as always
yielded was rejected, since an unwritten output would then print stale data
instead of suppressing the invocation.

### Convergence and publication

**Folding a body into an output summary.** A declared output's body summary is
the sequential fold of the body's per-slot effects for that output, in
statement order: the output starts `MayWrite` (nothing has written it), a
`MustWrite` statement slot sets it to `MustWrite`, and a `MayWrite` statement
slot leaves it unchanged — a later conditional write cannot un-guarantee an
earlier unconditional one. A boundary `MustWrite` obtained by reading the
destination seed (`ReadsSeed`) also leaves the summary unchanged: preserving
an earlier value does not prove that the body wrote one. The published
`BodyOutputEffects` deliberately stop before a call-owned domain: a `Range` or
`ArrayRange` parameter controls whether
the scalar body executes, not what the body does when it executes. Each call
combines that reusable body summary with its solved domain. A provably
non-empty literal can therefore preserve `MustWrite`, while an empty or unknown
domain weakens the call to `MayWrite`. A range created *inside* the body still
weakens only the outputs its statements drive — a possibly empty local range
makes those slots `MayWrite`, so `UpperTriRowTail` publishes `MayWrite` for
`res` — while unrelated outputs keep their effects.

**Finite specialization closure.** SCC settlement assumes discovery has already
produced a finite graph. It cannot catch
`f(T) -> f(Array<T>) -> f(Array<Array<T>>) -> ...`: every specialization is a
new node, so Tarjan never runs. Before Step 2B, discovery caps active
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

After discovery closes, effects and CFG share one
`specializationCallGraph` for each stable newly walked batch. Its dense,
batch-local primary-call edges drive effect SCC settlement; its separate
source-ordered mangled direct-call keys include settled callees and distinct
scalar companions actually ensured for lowering. CFG reuses node enumeration
and persistent reachability, not Tarjan state, effect worklists, or lattice
working vectors.

**Fixed point.** Function-body output effects, and only those, need one: a
recursive callee can refine its outputs after types settle, and
`TypeScriptFunc` today converges on types alone. Condense the typed
specialization call graph into its strongly connected components and process
them **callee-first** in reverse topological order — this is what lets a
component assume every callee outside it has already published. Within one
component:

1. Seed every member's outputs with a provisional `MustWrite` working vector. A
   recursive call reads that provisional value — which is why `Uncomputed`
   cannot be a lattice element, as there would be nothing to read.
2. Iterate the component, recomputing outputs from rebuilt statement effects
   and callees' current values. Callees outside the component contribute their
   published summaries.
3. Weaken monotonically, `MustWrite → MayWrite` only. The working vector
   **persists across body walks** within the component; it is not cleared with
   `FuncInfo.Vars`.
4. Stop when nothing changes — at most one weakening per slot.
5. Publish one coherent snapshot for the whole component at once.

An `Invalid` output blocks publication for its entire component; provisional
values are never read outside it.

Only reusable function specializations publish, through `FuncInfo.Settled`,
which requires type convergence, the effect fixed point, complete direct-call
keys, and a non-nil cached specialization CFG result. Every batch CFG result is
staged and installed before any member becomes settled; a diagnostic-bearing
result is complete and settles just like an empty successful result. A script
root owns its current compilation facts and is consumed immediately after
solving, so it needs no settled-publication step. Reading an unpublished or
`Invalid` effect, or a settled specialization with no CFG result, is an ICE,
never a default.

Snapshot analysis such as `FuncInfo.Vars` is cleared and rebuilt whenever a
body is walked and becomes reusable only once settled. `ExprCache` is not a
precedent for append-only analysis: stable AST entries are overwritten while
generated rewrite nodes may add entries across walks. Issue #71's
compile-order-dependent wrong output came from reusing some body facts while
discarding others. Specialization CFG success or diagnostics are likewise
cached on `FuncInfo` and replayed when a later script reuses a settled body.

### CFG consumption

`.pt` functions run `AnalyzeFuncs` once before any specialization exists. That
pass is structural only: explicit use-before-definition, illegal input/global
writes, unused inputs, syntactically unassigned outputs, formatting structure,
and discard behavior. It collects all reads before publishing a statement's
destinations, so a fresh `x = x + 1` cannot define its own RHS. An unknown main
format marker remains literal text; malformed specifiers and missing dynamic
width/precision variables on a resolved marker remain structural errors.

After a stable specialization batch reaches its effect SCC fixed point, each
node runs effect-sensitive CFG dataflow exactly once and caches its diagnostics.
For a let, event order is condition reads, RHS reads, `ReadsSeed` destination
reads, then sparse `StatementEffect.Writes` mapped by `TargetIndex`; all reads
therefore observe the simultaneous assignment's pre-commit snapshot. Print
arguments contribute ordinary reads even though prints have no statement
effect entry. An unreachable template gets structural and parser checks only:
effects cannot be derived without types. Consequently, a library-only package
whose templates are never instantiated receives no dead-store or
write-after-write diagnostics in that build.

The two diagnostics consume effects differently:

- *Backward (dead store).* A write is dead when its destination is not live
  afterward, and that holds for `MayWrite` as well as `MustWrite`. What
  `MayWrite` changes is the **kill**: it does not kill the preceding value's
  liveness, because that value may survive.
- *Forward (write-after-write).* Reporting that a write overwrites an unused
  value requires **both** writes to be `MustWrite`. This cures the former
  conditional-write false positive that forced tests to interleave reads merely
  to silence it. A prior seed overwritten by a proven-`MustWrite` call output
  without being read is instead a true positive: remove the seed or read it
  explicitly when its value is semantically required.

After a script solve succeeds, CFG first treats the script as a zero-input,
zero-output template for structural validation, then runs effect-sensitive
dataflow over the typed body with a fresh scope. Structural diagnostics do not
gate that typed pass: its already-collected explicit reads are reused so one
compile reports all independent CFG diagnostics without repeating formatting
errors. The compiler then traverses
the script's complete
direct-callee keys and each cached `DirectCallees` slice depth-first with a
visited set. Every specialization remains analyzed and cached independently;
the script presents their diagnostics as a first-seen, source-stable set union
keyed by source location and message, so two type variants of one template do
not print the same defect twice. Warm scripts need no body rewalk, and
unreachable cached diagnostics never leak. Persistent edges use
`CallParamTypes` plus a distinct
`ScalarCallParamTypes` key only when the final source-call `ExprInfo` records
that the companion was actually ensured; shared-cache membership alone never
creates reachability.

Inside a function body, domain ownership decides which statements an empty
domain can skip. A `Range` or `ArrayRange` **parameter** establishes a
function-level domain driving the whole body, contributing one shared effect at
the function boundary rather than making every statement that reads it
independently conditional. A locally created range owns only the statements it
drives — `res = a[i * n + j]` under a body-local `j = (i + 1):n` has an
RHS-local domain, not a function-owned one.
Structural template CFG deliberately makes neither distinction — typed
per-specialization effects resolve it when the function becomes reachable.

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
   collector, call — one RHS can be several at once); callee output effect for
   call rows (all-`MustWrite` versus any-`MayWrite`); value kind
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
and ranges. **Step 2 is complete.** Gated print syntax is a required
separate feature PR before Step 6 (§9, §16); any other language change
likewise gets its own PR with its semantics-doc and rejection-test updates.

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
  the duplicated syntactic effect classifiers are deleted.

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
- **Calls split by callee effect and argument yield.** A call that looks
  ordinary can still have independently `MayWrite` outputs, which needs
  per-output keep-old handling — a Step 6 capability — and an argument whose
  conditional or checked failure remains unresolved makes the invocation
  `MayYield`, which needs Step 6's strict argument evaluation (§3). So Step 4
  migrates calls whose outputs are **all** `MustWrite` **and** whose arguments
  are all `MustYield`; any other call defers **as a whole** to Step 6, because
  argument evaluation, tuple failure, and ownership are shared across its
  outputs and individual slots cannot migrate separately. Callee output effect
  and argument yield are therefore router axes, recorded in the capability
  matrix (rows 6, 6b, 6c).
- The private validity-carrying direct-call variant (§15), so an unwritten
  direct-return result can suppress Step 6's print invocation instead of
  printing its seed.

### Step 5: Checked accesses and OOB scope (~1-2 weeks)

- Checked access and per-RHS skip scope are foundational, not a late
  optimization: every ordinary assignment already creates per-RHS bounds state
  (`compileExprAssigns`) whose failure selects keep-old versus commit
  (`commitAssignmentsPerExpr`). Plans record explicit OOB scopes (§9), and the
  bounds-bit idiom reduces to generic guard emission driven by them.
- Checked-access fallback becomes legal here (decided; see the semantics doc):
  `x = arr[oob] || -1` assigns `-1`, the fallback testing the yielded bit and
  never the value. The solver currently rejects the spelling —
  `conditionPropagates` excludes checked failure, conflicting with §15's
  checked = `MayYield` / fallback composition. **This introduces the PIR
  `fallback` operation in restricted form** — a checked-access left side with
  an ordinary alternative, in assignment position, with regression tests.
  Step 6 extends the same operation to full value conditionals and print
  position; it does not add a second one. The solver change is equally
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

### Step 6: Non-ranged gates, value conditionals, prints (~2-3 weeks)

- Prerequisite, in its own PR before this step: gated print syntax
  (`c > 5 a, b`, §9), so the lazy form keeps a spelling when value-position
  print arguments become strict — a gate rejects the line without
  evaluating it; a failed argument suppresses the line after its siblings
  ran.
- `gate` with keep-old/zero commit policies; `require`, `fallback`, `map`,
  `align`, per-slot skip, with the builder splitting every conditional node out
  of `eval`.
- Calls with any `MayWrite` output, deferred from Step 4, with per-output
  keep-old handling; and calls with an argument whose conditional or checked
  failure remains unresolved (matrix row 6c), which gain strict source-order
  argument evaluation (§3) with the `F(sideEffect(), failingArg)` and
  `F(failingArg, sideEffect())` regressions.
- All non-ranged prints lower as `PrintPlan`s (§3), including a conditional
  direct-return call argument, which needs the Step 4 validity variant.
  The conditional suppression outcome is retained, but sibling evaluation
  becomes eager and the OOB materialized zero becomes invocation suppression
  (§3, §9). Update `tests/array/oob_print.exp` in the same PR, with
  regressions for a side-effecting or owned-heap sibling of a failed
  conditional and for a failed sibling suppressing a complete multi-line
  `Struct` emission.

### Step 7: Ranges and carries, then ranged conditionals (~3-4 weeks)

- RHS-local ranges and carries first: `domain` nodes, iteration snapshots, and
  simultaneous advance per §7.
- Then ranged gates and ranged conditional combinations, including the ranged
  logical trees `compileCondExprStatement` routes to staging today; `continue`
  versus `skip` scopes become explicit.
- Extend ownership elaboration to carries. Loop emission (`withLoopNest`,
  `createLoopCore`) stays as generic mechanics driven by `domain` regions.
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
  elaboration.
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

### Step 10: Affine versioning and final sweep (~1-2 weeks)

- Move affine form recognition and whole-domain versioning (`isFastAffineAccess`,
  the decision side of `withLoopNestVersioned`) into the builder; the checked
  path stays the semantics-first fallback. Remove AST-pointer-identity
  decisions and statement bounds orchestration.
- Affine proofs use overflow-safe arithmetic throughout — coefficients,
  endpoints, negative steps, extreme `I64` bounds — extending `addInt64` and
  `mulInt64`.
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
CFG pass (restructured around settled-specialization effects), and the runtime.

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
  callee) leaves the target `MayWrite` and records no `ReadsSeed`;
  callee-internal non-writing at an existing target resolves to the seed as
  `MustYield` with a recorded `ReadsSeed`
- summaries are rebuilt per specialization walk and published only on
  `Settled`; transitive effects reach a fixed point across recursive closures;
  cached specialization CFG diagnostics replay on reuse

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
- a failed conditional argument retains today's whole-invocation suppression,
  but its siblings are now evaluated eagerly: a side-effecting callee runs
  and an owned-heap sibling is allocated and released while the line stays
  suppressed
- a failed sibling suppresses a complete multi-line `Struct` emission
- a ranged print emits one emission group per fully-yielded point; OOB
  iterations emit nothing
- a suppressed invocation still releases its owned temporaries (leak-checked)
- an unwritten direct-return argument suppresses the invocation via the
  `{value, didWrite}` variant
- ordinary calls evaluate arguments strictly too: `F(sideEffect(), failingArg)`
  runs the side effect and `F(failingArg, sideEffect())` still evaluates the
  argument after the failed one, while the output tuple keeps its old values
  (§3)

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
  (`arr = [m > 5 && m + 1/2 || -1]`). A gate never supplies a value, which is
  why `x = a > 2  3 || 7` stays an error.
  Multi-return calls (`val > 5 && Pair(m, n) || Pair(1, 2)`) and the
  seed-then-gate idiom (`a, b = 1, 2` then `a, b = val > 5 m, n`) already
  express the semantics today
- a skipped array-valued collector cell closes to a zero-filled child of its
  expected shape, while `||` may supply a shape-compatible child; the validator
  rejects a plan whose skipped child shape is neither known nor derivable
  unless an explicit fallback such as `[j && 0]` supplies it
- test contexts can become explicit statement inputs and effects

PIR stays statement-focused until a concrete feature requires cross-statement
dataflow, keeping its value proportional to the compiler complexity it
replaces.
