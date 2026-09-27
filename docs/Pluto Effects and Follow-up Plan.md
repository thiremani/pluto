# Pluto Effects and Follow-up Plan

Recorded 2026-09-05. Implementation baseline: Pluto `840b147` (PR #101).
This is a follow-up work plan, not a claim that the work below is implemented.
Its first priority, incoming-output seed dependencies, has since been resolved
by a language rule instead of an analysis (section 1).

## Current PR disposition

PR #101 is suitable for approval as the first Step 4 slice: heap ownership for
the admitted script-root assignments. Commit `840b147` changes comments and
prose only. At review, both head CI checks passed and the PR was mergeable.
Earlier independent race tests and the full leak suite passed, 76/76.

The seed, formatting, arithmetic, and collector issues below exist outside the
new slice; they do not require expanding #101. One nonblocking PR-description
phrase remains: Review Round 4 should name RHS semantic type, merged target
type, and stored type separately, as the corrected code comment already does.

## 1. Seed dependencies: resolved by the output-read rule

Resolved by a language rule instead of an analysis
([PR #104](https://github.com/thiremani/pluto/pull/104), superseding the closed
[PR #102](https://github.com/thiremani/pluto/pull/102)): a declared output is
readable inside its template only after it is definitely assigned, so a body
that writes `y = x > 0 x` and then `y = y + 1` is rejected at the read. The hidden seed and destination-seeded staging slots continue to
preserve outputs that are not written. A caller can explicitly connect an
input to an output by reusing the same binding: later statements then observe
writes through that output, in ordinary and ranged calls alike. Inputs are
read-only bindings, not frozen values. No per-iteration input snapshot is
needed. Sharing is a compile-time fact of each call site and lowers to a
private alias variant of the specialization, so the native calling convention
stays independent of body effects. It is not unchanged: range-bearing
variants on master carried hidden alias selectors, and removing them changes
those prototypes, recorded as ABI 2.1 in
[the C ABI specification](./Pluto%20C%20ABI%20Spec.md). Still outstanding on
that boundary: a native caller that passes one address as both an input and an
output shares them only within the called body, because a nested Pluto call
stages its outputs. A generic pointer entry that resolves unknown sharing at
run time, alongside the private variants, would close that gap; nested
staging would still need alias handling inside it. The canonical description
of the language rule is in
[the memory model](./Pluto%20Memory%20Model.md) under "Parameters and Outputs".

The storage mismatch filed as
[issue #103](https://github.com/thiremani/pluto/issues/103) is addressed by
specializing binding arguments on their merged storage type and revisiting
calls when a later assignment widens that storage. Under live-reference
semantics, `s = "a"` followed by `s, prev = FoldStr(s, "b")`, where the body
writes `out = current ⊕ item` before `seen = current`, must produce `ab ab`.
A shared output takes its input's storage inside the private alias variant,
preserving sharing without changing unrelated input types. These cases are
covered by `tests/alias_input`.

Seed-readable outputs, which would let a body read an output before assigning
it, are not planned ([issue #105](https://github.com/thiremani/pluto/issues/105)):
a function that needs its destination's previous value takes it as an input,
and the caller shares the destination with it.
[Issue #123](https://github.com/thiremani/pluto/issues/123) proposes going
further and requiring every output to be definitely assigned.

## 2. Formatting: model `%n` as an explicit write operand

Retaining `%n` with a real write contract is a viable proposed direction. Its
destination is an effectful operand even though it appears inside formatting
syntax. This plan does not choose new source syntax or silently remove `%n`.

The recorded baseline `840b147` accepts a function that receives `x = 99`,
evaluates `"hello-x%n"`, and then returns `x`; it prints `hello` and returns 5.
At that baseline, `formatSpecialValue` checks the type and code globals but
does not reject read-only parameters.

The live-reference update now rejects `%n` writes to input and iterator
parameters through `Symbol.ReadOnly`, with ordinary and ranged rejection
covered by `TestFormatCountRejectsInputParameter`. The former
`TestPromotedAliasTypeGap` no longer mutates an input; its output-position
coverage remains in `TestAliasedInputReadsOutputInVariant`. The `acc_fmt`
fixture now writes a local count. CFG marker handling still records reads,
so the formatting write effects below remain unimplemented.

Remaining work if `%n` is retained:

- Resolve and validate the destination as a writable location through the
  normal rules, including unsupported targets. Retain the implemented input
  and constant rejection; `Symbol.FuncArg` alone also covers writable outputs.
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
- Extend the existing rejection tests with writable locals/outputs, old-value
  liveness, repeated markers, sequencing, skipped execution, aliases, and
  failure paths.

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
| Seed/effect correctness | Section 1; resolved by the definite-assignment rule for output reads in [PR #104](https://github.com/thiremani/pluto/pull/104); flow-versus-slot call specialization is [#103](https://github.com/thiremani/pluto/issues/103) |
| `%n` effect contract | Section 2; separate bounded change with formatting semantics updated |
| Output path protection | [Issue #80](https://github.com/thiremani/pluto/issues/80): compilation cannot overwrite source/configuration through name collisions or unsafe path resolution |
| Numeric edge behavior | Define and guard integer divide/remainder faults and invalid shift counts; audit range/count/allocation arithmetic |
| Benchmark correctness | In the sibling `bench` repo, validate every measured output, fail the run on mismatch, and prevent normal snapshot publication after failure |
| Independent PIR construction | Build plans from backend-independent binding facts without prior LLVM emission; extract shared storage-state transitions rather than maintain competing state authorities |
| Remaining PIR capabilities | Continue plan section 16 in slices; give checked failure, fallback, mixed writes, and shaped collectors explicit regression milestones |
| String flavors in expanded PIR (after PR #101) | Presentation-only follow-up: expanded `-emit-pir` spells `StrG`/`StrH` where the concise view and diagnostics keep `Str`, via a type formatter the compiler passes to the renderer (like the compatibility function) — never wrapper type objects, which would break the validator's compiler-type assertion; recursive through arrays, tables, and struct fields (`[StrH]`, `Table[Name:StrH Score:I64]`); ownership annotations retained, since the flavor is the solver's semantic type, not current storage. Goldens: the `StrG → StrH` copy (`s = "hi"` into a heap-typed `s`) and the widened read `%t0 = eval StrG other [borrowed=other]`; plan §12 qualified accordingly |
| Self-assignment is an error (language rule, own PR) | Decided: reject an assignment whose slot maps an identifier directly back to itself — `x = x`, and `x, y = x, x` (error on the `x` slot); `x, y = y, x` stays valid. A compiler error, not a lint, matching the existing unused-write rejection. Narrow: identifier-to-same-identifier only; not `x = x + 0`, not indexed targets; gates do not change it (`x = c > 0 x` is the same no-op). Decide function outputs in the same PR — `res = res` leaves the value unchanged but counts as a write in the effect model. Implement in the solver/CFG pass beside the unused-write diagnostics, never in PIR (the router sees only accepted script-root statements). Diagnostic: "self-assignment to `x`; remove its matching target and value." Same PR rewrites the fixtures that use the pattern deliberately — `mem/mem.spt` (`d1, d2 = d1, d1`, `da1, da2 = da1, da1`) and `TestPlanGoldenDuplicateSource` — to `src, a, b = "new" ⊕ "!", src, src`, which still exercises one move and one copy of the old value; semantics-doc entry and CFG tests |
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

Native probes built from `840b147` confirmed seed dependence, which then made a
caller diagnostic wrong (#104 has since rejected such bodies), `%n` parameter mutation, and the collector result above. A Python
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
