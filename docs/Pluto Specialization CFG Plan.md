# Pluto Specialization-Aware CFG Plan

Status: approved implementation plan for PIR migration Step 2B.

This is a temporary execution document. After Step 2B lands, preserve its
durable architecture in `Pluto IR Plan.md` and code comments, mark Step 2B
complete, and delete this file in the final cleanup commit. Git history remains
the record of the implementation plan.

Baseline: Step 2A merged at `92fd5ac`.

## 1. Decision Summary

Before resuming PIR implementation:

1. Land finite-specialization growth protection as a separate prerequisite PR.
2. Replace syntax-approximated function CFG diagnostics with a two-level pass:
   - structural validation once per `.pt` template;
   - effect-sensitive dataflow once per settled type specialization.
3. Keep one combined typed CFG for each script after `TypeSolver.Solve`.
4. Cache each specialization's complete direct-callee keys and CFG diagnostics,
   then replay the script's reachable specialization closure once.
5. Delete the CFG's duplicated syntactic effect classifiers.
6. Resume PIR immediately after Step 2B passes the full regression suite.

A reachable function is inspected twice at different abstraction levels, not
with duplicate checks. An unreachable function receives structural checks only.

## 2. Scope

### In scope

- Deterministic budget-based termination protection for specialization
  discovery.
- Structural-only template validation.
- Per-specialization dead-store and write-after-write analysis using settled
  `StatementEffects`.
- `ReadsSeed` conversion into ordinary pre-write CFG read events.
- Reuse of the specialization call graph's batch topology.
- Immutable specialization CFG result caching.
- Deterministic cold and warm diagnostic replay.
- Complete lowering-reachability edges, including ensured scalar companions.
- Removal of superseded syntax-based CFG write/yield classification.

### Non-goals

- A general CFG or basic-block redesign.
- New interprocedural dataflow or termination proofs.
- Cross-specialization diagnostic deduplication or diagnostic UX redesign.
- ABI, lowering, ownership, or PIR implementation changes.
- Persistent graph IDs, SCC state, or effect worklists.
- Restoring dead-store/WAW linting for unreachable untyped templates.

## 3. Required Invariants

1. `FuncInfo.Settled` implies types, variables, statement effects, body-output
   effects, direct-callee keys, and a non-nil CFG result are all published.
2. A diagnostic-bearing specialization is still settled: its diagnostics are
   a completed reusable result.
3. An empty successful CFG result is published explicitly; nil never means
   success.
4. Batch results are staged completely before any member becomes settled.
5. Effect-sensitive function CFG runs only after the batch's effect SCC fixed
   point, because `StatementEffects` can change during settlement.
6. CFG consumes effects associated with source AST statements. It never derives
   effects from compiler lowering rewrites. PIR must retain the same source-node
   identity when it later consumes `YieldEffects`.
7. Typed dataflow walks every source statement in order. `StatementEffects`
   supplies write and seed facts for let statements only; print arguments still
   contribute ordinary explicit reads even though prints have no statement
   effect entry.
8. Cached result slices are immutable after publication.
9. Warm reuse never requires a function body rewalk.
10. Reading a settled specialization with no published CFG result is an ICE.
11. Script roots are compilation-local results consumed immediately and do not
    require the reusable `Settled` publication lifecycle.

## 4. Current and Target Lifecycle

| Stage | Current | Target |
| --- | --- | --- |
| `.pt` template loading | Full syntax-approximate forward and backward CFG over every template | Structural-only validation once over every template |
| Stable specialization batch | Type convergence, effect graph/SCC settlement, then `Settled` | Build one specialization graph, settle effects, run typed CFG per node, publish all results, then `Settled` |
| Script after `Solve` | Typed CFG for script statements only | Combined typed script CFG, followed by reachable cached-diagnostic replay |
| Warm specialization | Skipped by `TypeFunc`; absent from the transient graph | Skipped by `TypeFunc`; reached through cached mangled direct-callee keys |
| Unreachable template | Receives approximate dead-store/WAW diagnostics | Receives structural diagnostics only |

The specialization CFG step must run inside every stable `TypeScriptFunc` batch.
`walkedFuncs` is cleared for each convergence closure, and a single `Solve` can
produce more than one disjoint stable batch. Deferring function CFG until the
end of `Solve` can therefore miss earlier batches.

## 5. Prerequisite: Finite Specialization Closure

Specialization discovery currently allocates and caches a new `FuncInfo` before
body inference. A chain such as
`F(T) -> F(Array<T>) -> F(Array<Array<T>>) -> ...` never reaches Tarjan or the
ordinary non-convergence diagnostic.

Land a separate prerequisite PR that adds:

- an active specialization signature chain;
- a deterministic per-`Solve` specialization budget checked before cache
  allocation;
- accounting for newly walked or unsettled discoveries, including synthesized
  scalar companions;
- no budget charge for an already settled warm-cache hit;
- a neutral budget-exhaustion error showing the configured limit and active
  signature chain, without claiming that the program is an expanding cycle.

The budget is the termination guarantee. Do not infer unbounded growth merely
by comparing two concrete signatures: a larger recursive re-entry can call a
fixed specialization next and produce a finite closure. A sound expanding-cycle
diagnostic would require proving the repeated call-site transformation, which
is deliberately deferred rather than adding fragile symbolic analysis before
Step 2B.

The budget value must be a named, documented policy constant chosen and tested
in the prerequisite PR. It must not depend on mangle length or global
`FuncCache` size.

## 6. Structural Template Pass

Run once for every function template during `CodeCompiler.Compile`.

Keep these checks:

- explicit use-before-definition in conditions, RHS expressions, and print
  arguments;
- reads from resolved formatting markers and their dynamic specifiers, while
  preserving the language rule that an unknown main marker is literal text;
- malformed specifiers and undefined dynamic width/precision identifiers
  attached to a resolved marker;
- writes to input parameters;
- writes to module constants and other global bindings;
- input parameters never explicitly read;
- outputs never syntactically assigned;
- discard structure: `_` creates no binding or CFG event.

Remove from template analysis:

- dead stores;
- write-after-write;
- backward liveness;
- all syntax-based `MustWrite`/`MayWrite` approximations.

Event extraction must become pure. The current `extractStmtEvents` inserts LHS
destinations into CFG scope before `processForwardEvents` checks the already
collected reads. Consequently, a fresh `x = x + 1` can observe its own
destination as defined. The structural pass must check all explicit reads first
and publish non-discard destinations only afterward.

No new template RHS-span classifier is needed after dead-store and WAW move to
specializations. Structural checks operate on explicit LHS targets; typed arity
and effect alignment remain solver-owned. Formatting collection returns reads
and structural errors without mutating CFG state; specialization dataflow uses
the reads but does not replay template formatting errors.

## 7. Typed Specialization Dataflow

For each newly settled specialization, scan all source statements in order.
`StatementEffects` is a let-statement-only source of write and seed facts; it is
not the statement traversal itself.

For every let statement, preserve this order:

1. explicit condition reads;
2. explicit RHS reads;
3. implicit destination reads from `ReadsSeed`;
4. sparse destination writes from `StatementEffect.Writes`.

All reads precede all writes so sibling RHS expressions observe the statement's
pre-commit snapshot and simultaneous assignment remains intact.

For every print statement, emit explicit reads for all print arguments,
including formatting-marker and dynamic-specifier reads. A print has no
destination writes, no `ReadsSeed`, and no `StatementEffect` entry. Its read
events participate in both forward and backward dataflow exactly like explicit
reads in a let, so a printed value remains live.

Map effects to CFG events and transfer rules as follows:

| Event | Forward transfer | Backward transfer |
| --- | --- | --- |
| Read | Clear the tracked unconsumed write | Make the name live |
| `MustWrite` / `Write` | Report WAW only if the tracked previous write is also `MustWrite`; record the current write | Report if not live, then kill liveness |
| `MayWrite` / `ConditionalWrite` | Do not form a WAW pair with `MustWrite`; record the current write | Report if not live, but do not kill prior liveness |

`ReadsSeed` is an ordinary read before the corresponding write. It clears the
prior forward write and restores backward liveness, preventing a seeded direct
call from diagnosing the value it intentionally preserves.

`StatementEffect.Writes` is sparse. Every entry must use `TargetIndex` to map
back to the original LHS position; never treat the effect slice as compactly
aligned with non-discard targets. A discard has no write or seed event.

Function parameters are defined at entry. Function outputs are seeded live at
exit. Specialization dataflow does not repeat template structural diagnostics.

## 8. CFG Helper Split and Classifier Removal

Refactor CFG around four focused operations:

1. pure explicit-read collection;
2. structural target validation and definition publication;
3. typed effect-event construction;
4. forward/backward dataflow transfer.

Split the current mixed helpers:

- `extractStmtEvents`: explicit reads versus structural/effect writes;
- `checkRead`: structural definedness versus dataflow read transfer;
- `checkWrite`: structural write legality versus WAW transfer;
- formatting collection: pure read events plus returned structural errors, so
  specialization analysis does not repeat template diagnostics.

After the effect-based path is active, delete:

- `destWriteKinds`
- `funcDestWriteKinds`
- `makeWriteKinds`
- `valueMaySkip`
- `funcValueMaySkip`
- `nodeMayNotYield`
- `funcNodeMayNotYield`
- `callRootMaySkip`
- `hasRangeExpr`

Update solver comments that still describe CFG as a consumer of the shared
syntactic failure walk.

## 9. Specialization Call Graph Reuse

Rename `effectGraph` to `specializationCallGraph` when Step 2B becomes its
second consumer. For each final stable batch:

1. build the graph once;
2. feed it to effect SCC settlement;
3. enumerate its nodes for independent typed CFG analysis;
4. stage and publish specialization results;
5. set every batch member's `Settled` flag last.

Reuse dense node IDs, deterministic node enumeration, effect dependency edges,
reverse caller edges, and Tarjan's callee-first SCC ordering for effect
settlement. CFG does not reuse Tarjan state, SCC worklists, or effect working
vectors.

### Two edge views

The graph needs two distinct views:

1. Batch-local effect dependencies by node ID. These follow `CallParamTypes`
   and include only unsettled nodes in the current batch.
2. Persistent lowering/replay reachability as complete mangled strings. These
   include already settled callees and each distinct scalar companion that the
   solver ensured for possible lowering.

Do not add scalar companions to effect SCC dependencies merely because they are
lowering candidates: effect derivation currently reads the specialization
selected by `CallParamTypes`.

For every nonbuiltin typed call, persistent edge collection records:

1. its `CallParamTypes` mangled key;
2. its distinct `ScalarCallParamTypes` key only when that call actually ensured
   the scalar companion.

Record the ensured-companion fact on final `ExprInfo` while solving. Do not infer
it from global `FuncCache` membership, because another script may have populated
the same key. The fact must be rebuilt on the final stable body walk rather than
remaining sticky from an earlier convergence pass. Builtins have no `FuncInfo`
and contribute no persistent edge.

Enumerate calls with one shared source-statement walker modeled on the current
`collectBodyCalls`: visit let conditions, let RHS expressions, and print
arguments, recursively including nested calls. Do not derive the call list from
`StatementEffects`, because a call used only by a print has no statement effect
entry but its specialization is still reachable and its cached diagnostics must
replay.

Each persistent target must exist and be either a current graph node or an
already settled specialization. Anything else is an ICE after a successful
solve.

## 10. Persistent Cache and Publication

Use a small immutable result owned by each `FuncInfo`:

```go
type SpecializationCFGResult struct {
	DirectCallees []string
	Errors        []*token.CompileError
}
```

Add `CFG *SpecializationCFGResult` to `FuncInfo`.

Publication rules:

- build results in temporary node-indexed storage;
- clone direct-callee and error slices before storing them;
- publish every result before setting any batch member settled;
- publish a non-nil empty result for success;
- never mutate a published result;
- settle diagnostic-bearing functions so later scripts replay rather than
  reanalyze them;
- never append function CFG diagnostics to `TypeSolver.Errors` during
  discovery.

The transient graph cannot be the replay cache: it omits settled callees, uses
batch-local IDs, and may be empty for a completely warm script.

## 11. Script Analysis and Diagnostic Replay

After `TypeSolver.Solve` succeeds:

1. run one combined structural and effect-sensitive CFG over the script;
2. collect the script root's complete mangled direct-callee keys with the same
   statement-complete helper used for function specializations, including calls
   nested in print arguments;
3. append script CFG diagnostics;
4. traverse the reachable specialization closure with a visited set keyed by
   mangled specialization name;
5. append each cached specialization's diagnostics once;
6. stop before lowering if the combined error list is nonempty.

Replay order must be deterministic:

- script call sites in source traversal order;
- stable first-occurrence edge deduplication;
- primary key before a distinct ensured scalar key;
- depth-first traversal in cached direct-callee order;
- cached per-specialization diagnostic order unchanged.

Do not iterate `FuncCache`, graph maps, or `walkedFuncs` to produce user-facing
order. Recursive, mutual, and diamond closures terminate through the visited
set. Unreachable cached diagnostics are never replayed.

The minimal implementation replays once per mangled specialization. It does not
deduplicate identical token/message pairs produced by two distinct reachable
specializations.

## 12. Implementation Map

- `compiler/types.go`
  - add `SpecializationCFGResult` and `FuncInfo.CFG`;
  - document the expanded `Settled` publication invariant.
- `compiler/cfg.go`
  - split structural and dataflow responsibilities;
  - add effect-event construction and pure read extraction;
  - remove syntactic effect classifiers after cutover.
- `compiler/effects.go`
  - rename/extract the specialization graph;
  - accept a prebuilt graph in effect settlement;
  - retain exact effect-dependency topology;
  - reuse one statement-complete call walker for lets and prints.
- `compiler/solver.go`
  - implement the specialization-growth prerequisite;
  - record ensured scalar companions on final expression facts;
  - analyze and publish CFG results inside each stable batch before `Settled`.
- `compiler/scriptcompiler.go`
  - run combined script CFG and replay the cached reachable closure.
- `compiler/*_test.go`
  - migrate untyped CFG harness cases to either structural-template tests or the
    full typed script/specialization pipeline.
- `docs/Pluto IR Plan.md`
  - record final durable behavior and mark Step 2B complete.

## 13. Test Plan

### Growth guard

- direct and mutual rank growth eventually exhaust the budget and fail with the
  active signature chain;
- budget rejection happens before the overflowing cache allocation;
- the diagnostic reports budget exhaustion without claiming proven growth;
- ordinary recursion remains accepted;
- finite polymorphic recursion remains accepted;
- a larger recursive re-entry that immediately reaches a fixed specialization
  remains accepted;
- synthesized range/scalar companions remain accepted and count
  deterministically;
- warm settled reuse does not exhaust the per-`Solve` budget.

### Structural templates

- unreachable explicit undefined reads still fail;
- unreachable input/global writes still fail;
- unused input and never-assigned output still fail;
- an unreachable local dead store or WAW does not fail until instantiated;
- fresh `x = x + 1` is undefined;
- an existing self-read and simultaneous swap remain valid;
- discard slots create no binding or event;
- formatting-marker behavior is preserved.

### Typed dataflow

- scalar and array-mask specializations of one template can produce different
  CFG results;
- `MustWrite -> MustWrite` reports WAW;
- `MustWrite -> MayWrite` and `MayWrite -> MustWrite` do not report forward WAW;
- an unused `MustWrite` and an unused `MayWrite` are both dead;
- `MayWrite` does not kill the preceding value's liveness;
- mixed per-slot effects diagnose only affected targets;
- `ReadsSeed` is observed before writes and prevents false WAW/dead-store
  diagnostics;
- print arguments contribute ordinary reads and keep printed bindings live;
- a function-local value used only by a print is not diagnosed as dead;
- output liveness prevents a final function-output write from appearing dead.

### Cache and replay

- cold analysis runs once and publishes before `Settled`;
- successful and diagnostic results both publish non-nil CFG caches;
- a fully warm script has an empty transient batch but replays cached errors;
- a new wrapper reaches an already settled callee's diagnostics;
- recursive and diamond closures replay each specialization once;
- unreachable cached diagnostics do not leak into a script;
- cold and warm diagnostic token/message order is identical;
- two type specializations retain independent CFG results.

### Call variants

- collector and shared-driver paths record ensured scalar companions;
- range-bearing paths retain the primary range specialization;
- the ensured-companion fact is rebuilt on the final stable walk;
- a scalar specialization present only because of an unrelated earlier script
  is not added as an edge;
- a user-function call reachable only through a print argument appears in the
  persistent closure and replays cold and warm diagnostics;
- builtins create no persistent edge.

### Validation

- `go test -race ./lexer ./parser ./compiler`
- `python3 test.py`
- `python3 test.py --leak-check`

## 14. Migration Hazards

- Diagnostic timing changes intentionally: unreachable templates no longer
  receive dead-store/WAW linting.
- Publishing CFG diagnostics through solver errors would short-circuit discovery
  and miss later script closures; cache first and replay after solving.
- Running CFG before effect SCC settlement can cache provisional statement
  effects.
- Compacting sparse `StatementEffect.Writes` loses discard positions.
- Treating `StatementEffects` as the statement traversal drops print reads and
  calls reachable only through print arguments.
- Placing `ReadsSeed` after its write breaks both forward and backward behavior.
- Reusing transient graph edges misses warm callees.
- Looking for scalar variants in shared `FuncCache` introduces cross-script
  reachability pollution.
- Map iteration can make diagnostics nondeterministic.
- Deferring specialization CFG until the end of `Solve` can miss earlier stable
  batches.
- Reintroducing a syntax fallback for unreachable dead-store/WAW checks restores
  the false positives Step 2B exists to remove.

## 15. PR and Commit Sequence

### PR 1: specialization discovery guard

Suggested subject:

`fix(compiler): bound specialization discovery`

Keep termination policy isolated from CFG behavior. Land active-chain reporting,
the per-`Solve` budget, and its acceptance tests. A richer expanding-cycle
diagnostic remains future work unless it can prove the repeated call-site
transformation soundly.

### PR 2: Step 2B

Review incrementally through these commits inside one PR:

1. `refactor(compiler): split structural and typed CFG events`
2. `refactor(compiler): share specialization call graph topology`
3. `feat(compiler): cache settled specialization CFG results`
4. `feat(compiler): replay reachable CFG diagnostics`
5. Final cleanup: remove syntactic classifiers, migrate tests, update the PIR
   plan, and verify the full suite.

Do not merge behavior-incomplete intermediate PRs. Review checkpoints should
keep feedback local while preserving two coherent merge units.

## 16. Effort and Stop Line

Expected engineering effort:

- specialization guard: 1-3 days;
- CFG split and effect events: 2-4 days;
- caching, replay, and call variants: 2-3 days;
- regression validation and cleanup: 1-2 days.

Total: approximately 6-10 engineering days. Calendar time may be longer because
review convergence, not implementation volume, is the primary risk.

Hold the stop line: do not generalize the CFG, redesign diagnostics, change the
ABI, or begin unrelated PIR work inside these PRs. Resume PIR immediately after
Step 2B passes validation.

## 17. Completion and Removal Checklist

Step 2B is complete when:

- the growth guard is merged;
- every template receives structural validation exactly once;
- every newly settled specialization publishes exactly one CFG result;
- scripts run one combined typed CFG and replay their reachable closure;
- print-only reads and call edges participate in typed dataflow and replay;
- warm and cold diagnostics match deterministically;
- ensured scalar companions and settled callees are present in persistent
  reachability;
- all duplicated syntactic classifiers are deleted;
- unit, integration, and leak suites pass;
- `Pluto IR Plan.md` records the implemented lifecycle and marks Step 2B
  complete;
- durable invariants are present in types and publication code comments.

After those conditions are met, delete this temporary plan in the final cleanup
commit.
