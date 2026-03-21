# Pluto ABI Optimization Plan

**Status:** Proposed  
**Scope:** Internal Pluto call lowering, external C ABI stability, scalar fast paths, tail recursion

## 1. Problem Statement

Pluto currently uses a single conservative lowering strategy for all functions:

- all functions return `void`
- arg 0 is an `sret` pointer
- all arguments are passed by pointer

This is documented in [Pluto C ABI Spec.md](./Pluto%20C%20ABI%20Spec.md).

That design is simple and uniform, but it leaves significant performance on the table for scalar-heavy code. In particular:

- `I64` / `F64` arguments are passed indirectly even when aliasing is unobservable
- single-scalar outputs use `sret` instead of register return
- self tail recursion lowers to recursive calls plus stack traffic instead of loops

The `fib_tail` benchmark in `bench` exposes this clearly.

## 2. Design Goal

Keep Pluto's source-level semantics unchanged:

- assignments copy
- inputs are logically read-only
- outputs are logically writable results flowing back to the caller

But stop forcing every function through the same pointer-based ABI.

The long-term goal is:

- semantic model stays stable
- internal lowering becomes target-aware and performance-oriented
- external C ABI can remain stable via wrappers if needed

## 3. Key Principle: Separate Semantics from ABI

Pluto currently mixes two different concepts:

1. **Language semantics**
   - what the programmer can observe
   - e.g. assignments copy, outputs behave like caller-visible destinations

2. **Lowered calling convention**
   - how values actually move across a call boundary

These must be treated separately.

For example, a read-only `I64` input can be passed by value without changing Pluto semantics, even if the language describes the function as receiving an input "by reference" conceptually.

Likewise, a single `I64` output can be returned in a register while still behaving semantically like a Pluto output.

## 4. Recommended Long-Term Architecture

Introduce an explicit ABI classification phase after type solving and before LLVM IR emission.

Each parameter/output should be classified into one of:

- direct scalar
- direct small POD aggregate
- indirect/by-reference
- indirect/sret

This classifier should be target-aware.

### 4.1 Direct candidates

Use direct passing/return by default for:

- `I64`
- `F64`
- `Bool` / integer-width booleans
- possibly other plain scalar numeric types

### 4.2 Aggregate candidates

Use direct aggregate return only when:

- the result is plain data
- no ownership/cleanup metadata is needed
- the target ABI naturally returns it directly

This should not be implemented as a hardcoded front-end-only "`<= 16 bytes` optimization".

Instead:

- model the result as an LLVM aggregate return
- let target ABI classification determine whether it stays direct or becomes indirect

### 4.3 Indirect-only cases

Keep indirect lowering for:

- strings
- arrays and array views
- values with ownership-sensitive cleanup
- complex aggregates with target-dependent indirect return
- any case where aliasing or caller-visible storage identity matters

## 5. External ABI vs Internal ABI

This plan assumes Pluto should distinguish between:

- **internal Pluto ABI**: optimized calling convention for Pluto-to-Pluto calls
- **external C ABI**: stable exported interface documented for foreign callers

### Recommendation

- Pluto-to-Pluto internal calls should move to the classified ABI
- exported symbols may continue using the current all-pointer C ABI through wrapper thunks

This avoids forcing internal performance decisions to be constrained by long-term external compatibility.

## 6. Mangling Impact

Name mangling should continue to encode semantic types, not the physical lowered ABI.

That means:

- changing `I64` input from pointer-passed to value-passed internally does **not** require a mangling change by itself
- changing single-scalar return from `sret` to direct return does **not** require a mangling change by itself

However, the **binary calling convention** does change.

So:

- if external callers are expected to link directly against Pluto symbols, ABI versioning or wrapper exports are required
- if the optimized ABI is internal-only, mangling can remain unchanged

## 7. Priority Order

The recommended implementation order is:

### Phase 1: Scalar fast path

Add direct lowering for:

- scalar numeric inputs by value
- single scalar outputs by value

This is the highest-value general optimization.

Expected benefits:

- better register allocation
- less stack traffic
- simpler IR
- improved performance across many benchmarks, not just `fib_tail`

### Phase 2: Restricted self tail recursion

Add a narrow self-tail-recursion optimization for functions that satisfy all of:

- direct scalar params only
- single direct scalar return
- self call in tail position
- no ownership-sensitive temporaries live across the tail call
- no cleanup work required before return

Transform these into loops in IR generation.

This should be intentionally narrow at first.

Expected benefits:

- major improvement on `fib_tail`
- better codegen for accumulator-style numeric templates

### Phase 3: Small POD aggregate returns

Support direct multi-output returns for plain-data aggregates when target ABI allows.

Examples:

- `{I64, I64}`
- `{I64, F64}`
- similar small plain-data bundles

This phase should be target-aware and should not assume all small aggregates are register-returned on all platforms.

### Phase 4: Generalized ABI classification

Broaden support to:

- more scalar types
- small direct aggregates in both params and results
- methods and operators
- mixed direct/indirect signatures

### Phase 5: External ABI wrappers and versioning

Once internal ABI classification is stable:

- decide whether exported Pluto symbols keep the current documented C ABI
- if yes, add ABI wrappers
- if no, version the ABI docs explicitly

## 8. Tail Recursion Assessment

### 8.1 Restricted self tail recursion

This is considered **medium difficulty** and worth doing.

It is much easier than full general tail-call elimination because the first version can ignore:

- multi-output indirect returns
- strings
- arrays
- ownership-sensitive cleanup
- mutual recursion

### 8.2 Full tail-call elimination

This is significantly harder.

It would need to interact correctly with:

- cleanup scopes
- ownership transfer
- indirect outputs
- non-scalar params
- cross-function calling convention compatibility

That should not be the first step.

## 9. What LLVM Can and Cannot Do for Pluto

LLVM `opt -O3` already performs strong scalar promotion and cleanup, but it cannot fully recover from ABI choices that are already baked into the function signature.

LLVM can help with:

- local allocas
- scalar promotion
- CFG simplification
- loop optimizations

LLVM cannot reliably fix:

- pointer-based param ABI when a value ABI would be better
- `sret`-only return ABI when direct return is possible
- recursive call structure that should have been lowered to a loop earlier

So ABI classification and restricted tail-recursion lowering must be done in Pluto, not deferred to LLVM.

## 10. Recommended Documentation Split

To avoid future confusion, Pluto docs should clearly distinguish:

- **semantic docs**
  - how Pluto behaves at the source level
- **lowered ABI docs**
  - how generated symbols are currently called
- **optimization plan docs**
  - what the compiler intends to improve internally over time

This file is the optimization-plan layer.

## 11. Practical Recommendation

If only one optimization can be prioritized soon, do:

1. direct scalar inputs and outputs

If two optimizations can be prioritized, do:

1. direct scalar inputs and outputs
2. restricted self tail recursion

This gives the best balance of:

- compiler-wide performance gain
- implementation risk
- long-term architectural cleanliness
- improved benchmark credibility

