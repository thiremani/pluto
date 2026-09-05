# Pluto Effects and Follow-up Plan

Recorded 2026-09-05. Implementation baseline: Pluto `840b147` (PR #101).
This is a follow-up work plan, not a claim that the work below is implemented.
The immediate priority is correct incoming-output seed dependencies alongside
`MustWrite`/`MayWrite`, before expanding PIR call support.

## Current PR disposition

PR #101 is suitable for approval as the first Step 4 slice: heap ownership for
the admitted script-root assignments. Commit `840b147` changes comments and
prose only. At review, both head CI checks passed and the PR was mergeable.
Earlier independent race tests and the full leak suite passed, 76/76.

The seed, formatting, arithmetic, and collector issues below exist outside the
new slice; they do not require expanding #101. One nonblocking PR-description
phrase remains: Review Round 4 should name RHS semantic type, merged target
type, and stored type separately, as the corrected code comment already does.

## 1. Next compiler PR: seed dependency analysis

Preserve the existing seeded-output semantics and public ABI. Correct the
analysis before deciding whether a later language version should change those
semantics.

### Confirmed failure

```pluto
# seed.pt
y = MaybeIncrement(x)
    y = x > 0 x
    y = y + 1
```

An existing destination seeded with 20 produces 21 for `MaybeIncrement(-1)`;
a fresh destination produces 1. The last statement always writes, but its
value may depend on the incoming output seed.

```pluto
# seed.spt: should compile and print 21
a = 20
a = MaybeIncrement(-1)
a
```

The baseline incorrectly rejects the second assignment as overwriting an
unused value. Inserting `a` as a print before the call makes it compile and
print 20, then 21. This demonstrates an analysis defect under current semantics,
independent of any preference about whether seeded outputs should exist.

### Keep three facts separate

| Fact | Question | Main consumer |
| --- | --- | --- |
| Callee body write effect | Does this output receive a write whenever this scalar body executes? | Yield/write propagation and call routing |
| Callee seed-read dependency | Can executing this body read a particular output's incoming value before it is definitely replaced? | Caller dependencies, liveness, future scheduling |
| Caller boundary resolution | Does this assignment use its existing destination to resolve a non-writing result? | Keep-old semantics and call-site CFG reads |

`MustWrite` and seed dependence are independent. An unconditional overwrite
may need no old value; an unconditional computed update can need it. A
conditionally writing body can either inspect its seed or leave preservation
entirely to the caller. `MayWrite` also includes never writing: it is not a
read/write access-mode declaration or a guarantee of initialization.

Current entry points to inspect:

- `compiler/effects.go`: `StatementEffect`, `seedResolvedYield`,
  `deriveBodyOutputEffects`, and specialization/SCC publication.
- `compiler/cfg.go`: `typedStatementEvents`, which places implicit destination
  reads before write events.
- `compiler/compiler.go` and call/conditional lowering: seed setup, staged
  outputs, alias handling, and direct versus indirect returns.
- [PIR plan, section 15](./Pluto%20IR%20Plan.md): its assertion that an
  all-`MustWrite` callee reads no seed needs correction in the implementation PR.

Do not merely broaden the existing `StatementEffect.ReadsSeed` list. The
current body fold interprets entries there as boundary-only preservation and
does not count those apparent writes as real body writes. Reusing that meaning
for genuine seed-dependent computation would incorrectly downgrade an
always-writing accumulator. Retain raw body-write facts and boundary-resolution
facts separately; CFG may consume their combined relevant destination reads.

The concrete field names are an implementation choice. Start with conservative
per-incoming-output seed-read summaries; do not require a full result-dependency
matrix unless a consumer needs it. Include dependencies used to compute another
output, not just self-updates. Compose these dependencies for indirect outputs
too; only direct-result boundary resolution belongs behind a direct-ABI check.
Propagate summaries through calls and recursive components: either write-effect
weakening or seed-read growth must requeue affected callers. Publish and cache
the facts together, preserving the existing uncomputed/invalid/settled
discipline. Unknown analysis must not silently mean no seed reads.

### Acceptance criteria

- [ ] The reproducer compiles without a redundant print and produces 21; the
  fresh-target variant produces 1.
- [ ] Unconditional seed-dependent writes remain `MustWrite`, with the old
  destination live where needed.
- [ ] A definite overwrite before any read removes the incoming-seed dependency;
  a conditional overwrite does not. Copying the seed to a local first preserves
  the dependency even if the output is subsequently overwritten.
- [ ] A seed read only in a condition or printed string still counts when the
  output is later unconditionally overwritten. Seed reads are not limited to
  dependencies of the returned value.
- [ ] Nested calls, recursive summaries, multiple outputs, cross-output seed
  reads, and zero/one/many-iteration cases are covered.
- [ ] Fresh targets, discards, incompatible-storage zero seeds, caller argument
  failure, and caller-side retention keep their existing distinct behavior.
- [ ] CFG and PIR consume settled solver facts rather than independently
  rediscovering dependencies. Existing unused-write diagnostics still work.
- [ ] Direct and indirect calls preserve staging and alias-input regressions.
  Public symbols and prototypes do not change with body effects; every public
  direct scalar return retains its existing hidden seed parameter.
- [ ] Cold and warm caches agree on summaries, diagnostics, and output.
- [ ] Effect tests, CFG regression tests, ABI/IR checks, race tests, and relevant
  leak checks pass. Run the full leak suite before submitting the compiler PR.

## 2. Formatting: model `%n` as an explicit write operand

Retaining `%n` with a real write contract is a viable proposed direction. Its
destination is an effectful operand even though it appears inside formatting
syntax. This plan does not choose new source syntax or silently remove `%n`.

The baseline accepts a function that receives `x = 99`, evaluates
`"hello-x%n"`, and then returns `x`; it prints `hello` and returns 5.
`formatSpecialValue` in `compiler/format.go` checks the type and code globals,
but does not reject read-only parameters. CFG marker handling records reads.
`TestPromotedAliasTypeGap` deliberately uses this path, so its coverage needs a
replacement when the read-only rule is enforced.

Required work if `%n` is retained:

- Resolve and validate the destination as a writable location. Reject input
  parameters, constants, and unsupported targets through the normal rules.
  Identify inputs structurally; `Symbol.FuncArg` also covers writable outputs.
- Record its write separately from reads of other markers and dynamic widths
  or precisions. `%n` does not inherently read the destination's previous value.
- Describe whether execution reaches the write and whether it initializes the
  whole destination. Gating, failures before the marker, and runtime formatting
  errors must not be treated as an unconditional write by assumption.
- Specify when the write becomes visible relative to other operands, nested
  formatting, and the enclosing assignment commit. Preserve defined behavior
  or make a timing change explicit; reject conflicting combinations until their
  ordering is supported. A write summary alone does not settle snapshot rules.
- Model formatting effects on print statements and nested expressions too;
  assignment-only `StatementEffect.Writes` cannot represent all these sites.
- Set an output's runtime write marker when `%n` actually writes it. Exercise
  both print and allocated-string formatting: `sprintf_alloc` currently invokes
  `vsnprintf` twice, so sizing and output passes need an explicit effect contract.
- Do not let an unmodeled formatting write enter an ordinary PIR `eval` as if it
  were effect-free. Keep unsupported cases legacy or reject them explicitly.
- Test read-only rejection, writable locals/outputs, old-value liveness,
  repeated markers, sequencing, skipped execution, aliases, and failure paths.

An explicit formatter/count output is another possible surface design. Choose
that separately if it makes programs clearer; correctness does not require it.

## 3. C ABI: access contracts and trust

A C calling convention specifies representation and calling mechanics. Passing
a value indirectly does not imply a source-level write. Pluto-to-Pluto calls
can retain analyzed effects even when their machine-level arguments are pointers.

Arbitrary external C implementations are a trust boundary. Pluto can enforce
its call-site rules against a declared wrapper contract, but a header or pointer
type alone cannot prove that the C implementation honors it. `const T *` is useful
information, not a complete guarantee about aliases, globals, or retained pointers.

Recommended foreign-binding model:

| Contract | What the Pluto caller can assume |
| --- | --- |
| Verified/read-only wrapper | Reads declared regions; writable access must not occur through another alias either |
| Write/output wrapper | May write declared regions; definite initialization requires a separate guarantee |
| Read-write wrapper | Reads old contents and may change declared regions |
| Unknown external function | Conservative may-read/may-write plus unknown nonlocal effects; cannot be treated as pure or freely parallelized |

For an unknown pointer parameter, assume possible reads **and** writes, never
`MustWrite`. Also resolve capture/retention, freeing/ownership transfer, bounds,
returned aliases, callbacks, blocking, and global/resource effects before
exposing a safe wrapper. Passing no pointers does not prove an external function
has no side effects. Reject unsafe combinations in the safe interface rather
than assuming pessimistic dependency tracking makes arbitrary C memory-safe.

Begin with curated bindings. A wrapper may copy a read-only value into temporary
storage when the foreign contract allows that, but the wrapper must also know
the buffer bounds and lifetime. Copying alone cannot contain arbitrary memory
corruption, pointer retention, or global effects. Untrusted native code requires
an isolation boundary if enforcement rather than contractual trust is needed.

Emit LLVM memory/alias attributes only when their stronger contracts hold.
They license optimizations; they do not install runtime enforcement. `%n` is a
known, compiler-parsed operation, so it can have an exact wrapper contract even
before general foreign bindings exist. See [the current C ABI specification](./Pluto%20C%20ABI%20Spec.md)
and [ABI stability plan](./Pluto%20ABI%20Optimization%20Plan.md).

## 4. Remaining work order

| Work | Completion criterion / existing reference |
| --- | --- |
| Seed/effect correctness | Section 1; next compiler PR before broadening call routing |
| `%n` effect contract | Section 2; separate bounded change with formatting semantics updated |
| Output path protection | [Issue #80](https://github.com/thiremani/pluto/issues/80): compilation cannot overwrite source/configuration through name collisions or unsafe path resolution |
| Numeric edge behavior | Define and guard integer divide/remainder faults and invalid shift counts; audit range/count/allocation arithmetic |
| Benchmark correctness | In the sibling `bench` repo, validate every measured output, fail the run on mismatch, and prevent normal snapshot publication after failure |
| Independent PIR construction | Build plans from backend-independent binding facts without prior LLVM emission; extract shared storage-state transitions rather than maintain competing state authorities |
| Remaining PIR capabilities | Continue plan section 16 in slices; give checked failure, fallback, mixed writes, and shaped collectors explicit regression milestones |
| Collector failure propagation | [Issue #88](https://github.com/thiremani/pluto/issues/88): `[arr[9] - arr[0]]` currently yields `[-10]` for `arr = [10 20]`; preserve absence until the cell consumer applies the specified zero policy |
| Cache identity and complexity | Continue [#83](https://github.com/thiremani/pluto/issues/83) and [#90](https://github.com/thiremani/pluto/issues/90); do not create duplicate backlogs |
| Website and language-status accuracy | Section 5; a focused documentation/example pass |
| Useful application and outside users | One small numerical/data tool; observe a few programmers installing, understanding, and modifying it |

Resolve changes to seeded outputs, default zip-min array arithmetic, or strict
argument evaluation in explicit semantic PRs. Keep the accepted ABI and current
behavior coherent while implementing analysis fixes. As PIR gains scopes and
carries, preserve source origins and stable binding identities. Add explicit
effects before scheduling parallel work; byte buffers, structured errors,
resource handles, modules, and other features should follow application needs.

## 5. Website and the three foundations

The local website at `../pluto-lang.dev` already teaches all three ideas:
inference in `src/content/docs/start/mental-model.mdx`, reusable transformations
in `tour/functions/`, and simple data in `tour/structs.mdx` and
`tour/arrays/matrices-tables.mdx`. Reuse that material.

- [ ] Add a short foundations overview linking to those existing explanations.
- [ ] Demonstrate independent assignment copies with a runnable example using
  currently supported operations.
- [ ] Correct the claim that struct fields currently support arrays. Canonical
  definitions accept integer, float, and string literals; label broader field
  support as intended design until implemented.
- [ ] Use the read-only-input/explicit-output contract instead of claiming all
  functions are pure; functions can print, and `%n` requires the fix above.
- [ ] Distinguish independent value semantics from COW, eager copies, and
  ownership transfer. Correct present-tense COW/locking claims in the memory
  document; label concurrency as future work.
- [ ] Qualify zero-cost and SIMD claims. Explain compiler-managed backing
  buffers without promising that all reachable data is inline.
- [ ] Compile/run documented examples against a pinned compiler and check their
  outputs. Existing link/mapping checks and mocked UI tests are insufficient.
- [ ] Reuse the site's existing release/status and benchmark-refresh roadmap.
  The review inspected local source, not the deployment state.

## Evidence and references

The 2026-09-05 review read the supplied research PDF, three-foundations PDF, and
Markdown assessment. Recommendations in those documents were assessed as
proposals; they did not authorize implementing every suggestion.

Native probes built from `840b147` confirmed seed dependence and its false
diagnostic, `%n` parameter mutation, and the collector result above. A Python
probe against bench `019007ab` confirmed that its mismatch reporter returns
normally. Numeric guards, output paths, and struct-field limits were inspected
in source. No destructive output-collision probe was run. Local website review
used `d3a3db6`. These checks are targeted evidence, not an exhaustive safety audit.

Primary references for the foreign-boundary distinction:

- [LLVM parameter attributes and memory effects](https://llvm.org/docs/LangRef.html#parameter-attributes): effects are compiler contracts; absent function memory attributes permit reads and writes.
- [Rust FFI and safe wrappers](https://doc.rust-lang.org/nomicon/ffi.html#calling-foreign-functions): foreign declarations and implementations require explicit trust and wrapper obligations.

Use this file as the working backlog. Record the resolving PR next to each
completed item and update canonical semantics in the implementation PR; do not
let this snapshot become a competing language specification.
