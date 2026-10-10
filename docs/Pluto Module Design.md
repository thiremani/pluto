# Pluto Module Design

**Status:** design. None of this is implemented yet: today a directory is the
unit of compilation, `pt.mod` names the module path, and there are no
imports. This document records the decisions taken so far for modules,
imports and versioning, and the questions still open.

## What a module is

A module is everything under one `pt.mod`. Each directory's `.pt` files
define functions, operators, structs and constants, named by the module path
plus that directory's path within the module (see the
[C ABI spec](<Pluto C ABI Spec.md>)). Scripts (`.spt`) build programs;
nothing calls them, so they are not part of a module's contract.

## No visibility

Pluto has no public/private rule for functions, operators, constants or
structs, and no exported/unexported spelling. Functions share no mutable
state, so calling any function, a helper included, cannot break another. The
usual reason to hide functions, protecting shared state and its invariants,
does not arise.

Everything a module defines is therefore part of its contract with other
modules. A module that wants to stop offering something removes it in its
next major release.

## Namespaces and major versions

Another module reaches a module through a namespace: its module path,
carrying the module's major version, as in `github.com/you/math/v2`. The C
ABI already mangles such path segments into every symbol, so:

- Different major versions of one module can be linked into the same
  program, and each dependent moves to a new major when it chooses to.
- Struct types from different major versions are distinct: a `v1` `Person`
  is not a `v2` `Person`, so the two don't mix where one type is required,
  such as a variable that already holds one, or an array's elements. A
  template still accepts either when its body works for it.
- Minor and patch releases share their major's namespace and replace each
  other, so each must be compatible with the releases before it in that
  major line.

## Version numbers

Releases are numbered `Major.Minor.Patch`, as in
[SemVer](https://semver.org/). A release's category is the highest any of
its changes requires, and publishing picks the next version in it: from
`2.4.7`, that is `2.4.8` for a Patch, `2.5.0` for a Minor and `3.0.0` for a
Major. Publishing detects the changes declarations show, and the publisher
declares the rest: a compatible addition the comparison cannot see, such as
a template that now accepts more argument types, as Minor, and a breaking
change as Major. The publisher never chooses a number, but may ask for a
higher category than the changes require, never a lower one.

| Change | Minimum |
|---|---|
| Documentation, tests, formatting | Patch |
| A body change that keeps every promised behavior: a compatible bug fix, an optimization, a refactor | Patch |
| Add a function, operator, constant or struct | Minor |
| Add a function under an existing name with a new number of inputs | Minor |
| Add a field to a struct | Minor |
| Accept more argument types, keeping every call that worked | Minor |
| Remove or rename a function, operator, constant, struct or field | Major |
| Change a function's number of inputs or outputs | Major |
| Change a constant's value or type | Major |
| Change a field's type, or the order of a struct's fields | Major |
| Narrow the argument types a function accepts, or change the result type of a call that worked | Major |
| Change the meaning or order of a function's inputs or outputs | Major |
| Break documented behavior, or drop a supported platform or compiler version | Major |
| Update a dependency | Whatever the update changes in this module's contract |

A function is identified by its name and its number of inputs. A new input
count under an existing name is therefore a new function, and changing a
function's input count removes one function and adds another.

Constants promise their exact typed values, so correcting a wrong constant
is a Major change. Values are compared, not spellings: `1.0` becoming `1e0`
is no change. A struct constant keeps its value as long as every field it
had keeps its type and value, so a field added in a Minor release is no
change either. Fields hold numbers and strings today; if they come to hold
structs, the same comparison applies to those fields in turn.

A struct type is defined by its constant that lists the most fields, as in
`p = Person` with `: name age`. A new field is added there, and every other
constant of that type takes the field's zero value. A struct may gain a
field in a Minor release because struct literals name their fields and may
leave some out. A struct's printed form and memory layout are not part of
its contract, and dependents compile from source. When zero is not a safe
value for the new field in existing constants, the change breaks behavior
and is Major. Reordering a struct's fields is Major too, since a literal
that lists every field must use the definition's order.

When a module's functions take or return a dependency's structs, changes to
those structs reach the module's own callers, so a dependency update is
judged by what it changes in the module's contract, not by the dependency's
version number.

## What the contract covers

The publishing checker compares what a module's declarations state:

- each function's and operator's name and its number of inputs and outputs
- each constant's type and value
- each struct's name and its ordered field names and types
- the compiler versions and platforms the module supports

It does not compare the argument types each function accepts. A template's
body decides which types it accepts and what it returns for them, and a
template exists to work for every type its body supports, so tests are
representative examples and need not try every type. A Minor or Patch
release must still not narrow the accepted types or change a result type for
a call that worked. The checker enforces only the rules it can detect from
declarations; representative tests and the publisher cover the rest:

- Publishing reruns the previous release's tests against the new one. They
  catch what their assertions, or operations sensitive to type, expose:
  changing `F(x)` from returning `x` to returning `x + 0.0` makes `F(1)` a
  float, yet both versions print `1`.
- A publisher who knows of a narrowing or a changed result type declares it
  as a break, which makes the release Major. A template that now accepts
  more argument types is declared as a compatible addition, which makes it
  Minor.

A narrowing that escapes both shows up later as a type error in a
dependent's build: a breaking change the release should have declared, not
one publishing caught.

A later addition could summarize, from each body, what each input must
support (operators, indexing, field names, and the functions it is passed
to) and flag a release that adds a requirement. That would check how general
a template is without listing types.

Other behavior is the publisher's to declare. Changing `Double(x)` from
`x * 2` to `x * 3` keeps every declared fact; the previous release's tests
catch it only if they check `Double`'s result.

## Publishing

Publishing a release:

1. Builds the module's API manifest: the facts listed above.
2. Compares it with the release it updates: `2.4.8` with `2.4.7`, and a
   backported `2.3.5` with `2.3.4`.
3. Works out the release's category: the highest that any detected change
   or any declared change requires. The publisher declares what the
   comparison cannot see: a compatible addition, which needs a Minor
   release, and a break, which needs a Major one.
4. Runs the module's own tests, which must all pass, and the previous
   release's tests, whose failures must each be explained: by a change the
   comparison found, such as a function a Major release removes; by a break
   the publisher declares, which needs a Major release; by a bug fix the
   release notes state; or by something outside the contract, such as a
   struct's printed form. An unexplained failure stops publishing.
5. Requires every function's body to run at least once in the module's own
   tests, with any argument types. This is execution, not reachability:
   `[Double(0:0)]` reaches `Double` but never runs its body. It shows that
   every function runs, not that every function is needed: one that only
   its own test calls still passes.
6. Numbers the release: the next version in that category after the
   release it updates, or in a higher category the publisher asks for. A
   lower category is refused, and a version number is never reused.

Publishing reports the version it picked and the changes that decided it:

```text
Publishing 3.0.0, a Major release.

Constant STATUS_OK changed from 0 to 1.
```

Asking for a lower category is refused with the same reason:

```text
Cannot publish a Minor release.

Constant STATUS_OK changed from 0 to 1.
This requires a Major release: 3.0.0.
```

## Major releases

A Major release may remove or change anything. Nothing is marked as going
away ahead of time, so compiling never warns about it, and each dependent
moves to the new major when it chooses to. Release notes are generated
from the comparison, so a publisher never writes out changed functions,
constants or fields: they list the removals and changes it detects,
together with the changes the publisher declares and any migration steps
they need. A module's own documentation can give advance notice of what a
later major will remove.

## While developing

None of this makes ordinary compilation stricter. A function nothing calls,
or no test runs, still compiles:

- An optional report lists the functions that the chosen scripts and tests
  cannot reach, and names the scripts and tests it started from.
- Running the tests measures which function bodies ran. A function counts as
  covered once its body runs with any argument types; coverage of each type
  specialization and of each branch is reported separately.
- A project's CI decides whether either report fails the build.

## Open questions

- Import syntax, and how code names another module's functions.
- Where a release's version is recorded, and how a dependent chooses a
  release within a major line: a lock file, or the minimum version every
  dependent needs.
- How a publisher declares a compatible addition, a break or a bug fix,
  with any migration note, and asks for a higher category than the changes
  require.
- Backports: how a publisher picks the maintenance line a release updates,
  with numbering still automatic, and what happens when the computed version
  already exists, such as a Minor on a line whose next minor is out.
- How the major version is spelled in the module path, including whether
  `v0` and `v1` carry one.
- Which functions get a stable C wrapper: a per-function choice for C
  callers, separate from how Pluto modules call each other.
- The inferred input requirements described under "What the contract
  covers", if rerun tests and declared breaks prove too weak.
