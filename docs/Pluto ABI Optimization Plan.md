# Pluto ABI Optimization Plan

**Status:** Phase 1 scalar ABI shipped internally; remaining phases proposed
**Scope:** Internal call lowering, scalar fast paths, tail recursion, external ABI stability

## 1. Problem

Pluto uses a single conservative lowering for all functions:

- all functions return `void`
- arg 0 is an `sret` pointer
- all arguments are passed by pointer

This is documented in [Pluto C ABI Spec.md](./Pluto%20C%20ABI%20Spec.md).

Simple and uniform, but costly for scalar-heavy code:

- `I64`/`F64` arguments are passed indirectly even when aliasing is unobservable
- single-scalar outputs use `sret` instead of register return
- self tail recursion lowers to recursive calls plus stack traffic instead of loops

The `fib_tail` benchmark exposes this clearly. LLVM `-O3` cannot recover from ABI choices baked into the function signature — it can promote local allocas and simplify CFGs, but it cannot fix pointer-based param ABI or `sret`-only returns. ABI classification and tail-recursion lowering must be done in Pluto.

## 2. Key Principle: Separate Semantics from ABI

Pluto's source-level semantics stay unchanged:

- assignments copy
- inputs are logically read-only
- outputs are logically writable results flowing back to the caller

These are **language semantics**. How values physically move across a call boundary is the **lowered calling convention** — a separate concern. A read-only `I64` input can be passed by value without changing Pluto semantics. A single `I64` output can be returned in a register while still behaving like a Pluto output.

## 3. Architecture

### 3.1 ABI classifier

Introduce an explicit ABI classification phase in the compilation pipeline. Each parameter/output is classified as:

| Classification            | When to use                                                  |
| ------------------------- | ------------------------------------------------------------ |
| **direct scalar**         | `I64`, `F64`, `Bool`, other plain numeric types              |
| **direct aggregate**      | small POD bundles (`{I64, I64}`) when target ABI allows      |
| **indirect/by-reference** | strings, arrays, ownership-sensitive values, aliased storage |
| **indirect/sret**         | complex aggregates, target-dependent indirect return         |

The classifier should be target-aware — don't hardcode "`<= 16 bytes` = direct".

### 3.2 Pipeline integration

The ABI classifier runs **after type solving and before LLVM IR emission**:

```text
parse → TypeSolver → [ABI classifier] → Compiler (IR emission)
```

Concretely, it hooks between `TypeLetStatement` / `TypeExpression` (which resolve types and populate `ExprCache`) and `compileLetStatement` / `compileExpression` (which emit IR). The classifier annotates each function signature with its ABI decisions, and the IR emitter reads those annotations instead of unconditionally using pointer ABI.

### 3.3 Internal vs external ABI

|                | Internal (Pluto-to-Pluto)             | External (C callers)     |
| -------------- | ------------------------------------- | ------------------------ |
| **Convention** | Classified ABI (direct scalars, etc.) | Current all-pointer ABI  |
| **Stability**  | Can change between compiler versions  | Stable, documented       |
| **Migration**  | Transparent to Pluto code             | Wrapper thunks if needed |

Name mangling encodes semantic types, not physical ABI. Changing `I64` from pointer-passed to value-passed does not require a mangling change. But the binary calling convention does change, so external callers need ABI wrappers or versioning.

## 4. Implementation Phases

### Phase 1: Scalar fast path (implemented)

Direct lowering for scalar numeric inputs and single scalar outputs.

- pass `I64`/`F64` by value instead of by pointer
- return single scalar in register instead of via `sret`
- keep function-body semantics stable by spilling direct scalar params into local addressable slots in the callee
- preserve range-bearing accumulator / empty-range behavior with hidden alias/seed state where needed

This was the highest-value initial optimization because it benefits all scalar-heavy code, not just specific patterns. It reduces stack traffic, simplifies IR, and materially improved `fib`, `fib_tail`, and `harmonic`.

**Benchmark target:** `fib`, `fib_tail`, and other call-heavy scalar code. Note: `sum` is not a useful target here — its optimized IR is already a call-free scalar loop; the current gap vs clang is loop optimization quality, not call ABI.

### Phase 2: Restricted self tail recursion (next highest priority)

Transform self-recursive calls into loops when all of:

- direct scalar params only
- single direct scalar return
- self call in tail position
- no ownership-sensitive temporaries live across the tail call
- no cleanup work required before return

Intentionally narrow — ignore multi-output, strings, arrays, mutual recursion. This is medium difficulty because the restricted form avoids all the hard ownership/cleanup interactions.

**Benchmark target:** `fib_tail` (eliminates stack growth entirely and removes the remaining recursive-call overhead after Phase 1).

### Phase 3: Small POD aggregate returns

Support direct multi-output returns for plain-data aggregates (`{I64, I64}`, `{I64, F64}`) when the target ABI allows. Model as LLVM aggregate return and let target classification decide direct vs indirect.

This is the natural next ABI expansion after Phase 2 because the classifier/lowering split from Phase 1 is already in place. The remaining work is target-aware aggregate classification, not another structural refactor.

### Phase 4: Generalized ABI classification

Broaden to more scalar types, small direct aggregates in both params and results, methods/operators, mixed direct/indirect signatures.

### Phase 5: External ABI wrappers

Once internal ABI is stable, decide whether exported symbols keep the current C ABI (add wrappers) or version the ABI docs explicitly.

### Parallel track: non-ABI loop/codegen work

`sum` suggests there is also a separate non-ABI optimization gap. Pluto's optimized IR
for that benchmark is already a call-free scalar loop, but clang still produces a much
more optimized kernel. That means `sum` should be treated as a loop/codegen quality
benchmark, not as a primary validation target for the ABI phases above.

The target-metadata quick wins have already landed far enough to make this a
different problem now. The next things to investigate here are:

- emit a more canonical counted-loop fast path for common `I64` ranges, especially `step == 1`
- preserve affine-friendly loop structure so LLVM can unroll and strength-reduce more aggressively
- keep range-heavy scalar accumulators in SSA where aliasing is provably absent, and spill only at the boundary
- continue improving bounds/versioning so fast paths stay simple in the common in-bounds case

These are worth treating as a separate optimization track because they improve
call-free kernels like `sum` and still benefit range-heavy scalar code such as
`harmonic`, without depending on more ABI surface area.

**Benchmark target:** `sum`, `harmonic`, and other call-free or loop-dominated integer kernels.

## 5. Rollout Strategy

Each phase should:

1. **Feature-flag the new ABI** — compile both old and new paths, compare program behavior and test results (not raw IR, which will differ by design)
2. **Run the full test suite** (`python3 test.py --leak-check`) against both paths
3. **Benchmark before/after** using `bench/` suite to validate the expected gains
4. **Merge internal ABI first** — external wrappers come later (Phase 5)

## 6. Practical Recommendation

If only one remaining optimization can be prioritized: **Phase 2** (restricted self tail recursion). Phase 1 is already shipped, and tail recursion is now the clearest remaining win on the benchmark set.

If two: **Phase 2 + Phase 3** (restricted tail recursion + small POD aggregate returns). That extends the current ABI work with the best continuity and risk/reward ratio.

If a parallel non-ABI effort is desired at the same time, prioritize the counted-loop fast path from the loop/codegen track rather than additional cache or tooling work.
