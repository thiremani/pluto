# Pluto Module Design

**Status:** design. None of this is implemented yet: today a directory is the
unit of compilation, `pt.mod` names the module path, and a module cannot use
another. This document records the decisions taken so far for modules,
dependencies and versioning, and the questions still open.

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

## Dependencies

A module lists the modules it uses in its `pt.mod`, each by its module path
and the minimum release it needs (see "Choosing releases"), optionally with
an alias. There are no import statements: every `.pt` and `.spt` file in the
module can use every dependency its `pt.mod` lists. Code names a dependency
by its alias, or else by the last element of its path, so two dependencies
whose paths end alike need an alias for at least one. A module that uses
both `github.com/you/math v2.3.5` and `github.com/thiremani/math v1.2.3`
might name the second `tmath`. An alias of `.` brings a dependency's names
in without a prefix, which only a program's root module may do (see "Names
without a prefix").

A `pt.mod` also names the oldest compiler the module builds with, as
`go.mod`'s `go` line does. The standard library ships with the compiler and
is never listed (see "The standard library"), so a module that starts using
a standard function added in a later compiler raises that minimum.

Beside `pt.mod`, a module's `pt.sum` records the checksums of the releases
its builds use (see "Checksums").

## The standard library

The standard library ships with the compiler, as Go's does: a compiler
release fixes its version, and no `pt.mod` lists it. It is a set of
libraries. The core ones, `int`, `float`, `str`, `array`, `complex` and
`math`, are aliased as `.` by default, so code writes `Int(23.2)`; a
library added later, such as one for files or JSON, takes its own name as
its default alias (`json.Parse`). There is no `std` prefix, so
`std.Int(23.2)` is an error and every standard function has one spelling. A
`pt.mod` may give any standard library another alias, such as `s` for
`str`, and may write out a default, though it need not.

The libraries named for a type hold what belongs to it, such as `str`'s
`Upper`, `complex`'s `Conj` and the conversions `Int("23")` and
`Float("2.5")`, which take the place of a `Parse` for each type. A function
that serves several types lives in exactly one library, since a template
serves every type its body supports and a name may be defined only once
(see "Names without a prefix"): `math` holds `Abs`, `Min`, `Max` and `Sqrt`
for every number type.

Libraries that should change faster than the compiler are ordinary
modules, as Go's `golang.org/x` modules are.

## Names without a prefix

A module uses names without a prefix from three places: its own functions,
the standard libraries aliased as `.`, and any dependency aliased as `.`. A
name and number of inputs may be defined only once among them, as a
module's own functions already must be: a second definition is an error
that names both, never a shadow. A module that wants its own `Int` gives
`int` an alias in its `pt.mod` and writes `int.Int` for the standard one. A
standalone program, with no `pt.mod` to re-alias in, renames its function
instead.

A module sees the standard library as of the minimum compiler its `pt.mod`
names. A function added in a later compiler is not in scope until the
module raises that minimum, so a compiler release never breaks a module by
adding a name, and a clash with a new name appears when the module's author
raises the minimum. Go records the release that added each standard
function, and since Go 1.23 `go vet` flags one newer than a module's `go`
line.

A dependency may be aliased as `.` only in a program's root module, and
publishing refuses a release whose `pt.mod` does so. A dependency's names
can grow in any Minor release, which another module's requirement may
select (see "Choosing releases"), so a clash inside a published module
would break builds whose authors cannot fix it. In a program's own
`pt.mod`, the clash appears in its author's build, where they can rename or
re-alias. The standard libraries need no such limit, since a module sees
their names as of its own minimum compiler.

## Namespaces and major versions

The symbols for a dependency's functions, operators, constants and structs
carry its module path and the release the build uses, as in
`github.com/you/math/v2.3.5`. The C ABI already mangles a version like any
other path segment (its example is `example.com/math/v1.2.3`). A struct type
mangled into a symbol should carry its module path too, as the C ABI spec's
qualified types do (§3.2); that is still to be built, since the compiler
mangles a struct by its name alone today (`Struct.Mangle`). With both:

- Different major versions of one module can be linked into the same
  program, and each dependent moves to a new major when it chooses to.
- Struct types from different major versions are distinct: a `v1` `Person`
  is not a `v2` `Person`, so the two don't mix where one type is required,
  such as a variable that already holds one, or an array's elements. A
  template still accepts either when its body works for it.
- Within a major, a program holds the one release that "Choosing releases"
  selects, so minor and patch releases replace each other, and each must be
  compatible with the releases before it in that major line. The minor and
  patch in a symbol never tell two releases in one program apart; they keep
  code compiled against one release from calling another's functions or
  reading its constants. They do not catch a changed struct layout that
  reaches code through a module whose own release stayed the same;
  "Choosing releases" covers that.

## Choosing releases

Releases are chosen by minimal version selection, as in Go. Each `pt.mod`
records the minimum release it needs of each dependency, and a build uses,
for each module and major, the highest minimum that any module in the build
records. There is no lock file: the `pt.mod` files alone decide the releases
a build uses. A published release never changes, its `pt.mod` included, so a
module that needs other releases of its dependencies publishes a new release
of its own, and the same root `pt.mod` selects the same releases every time.
If `M` v1.0.0 requires `D` v1.2.0 and its author wants `D` v1.3.0, they
publish `M` v1.0.1, and a root that requires `M` v1.0.0 still builds with
`D` v1.2.0 until its own `pt.mod` asks for more. "Checksums" describes how a
build knows that a release holds what was published.

The selection decides a build's dependencies, not its binary. The binary
also depends on the root module's own source, the compiler version, the
platform and build settings such as `PLUTO_TARGET_CPU`, whose `native` and
`portable` settings already give different binaries on one machine and
compiler. With all of those the same, the binary is the same only if
compiling and linking are deterministic.

A selection belongs to its root, not to each module in the build: inside
another module's build, a module may get newer releases of its own
dependencies than its `pt.mod` names, when another module in that build
needs them. `M` v1.0.0, requiring `D` v1.2.0, builds against `D` v1.2.0 in
one program and against `D` v1.3.0 in a program that also requires that.
That is safe because minor and patch releases keep the contract that
publishing checks, but code compiled against one struct layout must
not run with another. A module's compiled code is therefore cached by
everything it was compiled from: its source, the compiler and build
settings, and every release it depends on, directly or through other
modules, as Go's build cache folds each dependency's build identity into a
package's own. Direct dependencies are not enough. If `A` requires only `B`
v1.0 and reads the fields of a `D.Person` that `B.Make()` returns, a build
that selects a newer `D` changes what `A` compiles to, while `A`'s
requirements and the symbols it uses stay the same: return types are not
mangled, and `B.Make` is still `B` v1.0's. One release of a module can
therefore have several builds on a machine.

## Checksums

A module's `pt.sum`, committed beside its `pt.mod`, records the checksums of
the releases its builds accept: of each dependency's `pt.mod` that minimal
version selection reads, checked before its requirements are used, and of
each selected module's source, checked before it is compiled. `pt.mod`
selects releases and `pt.sum` only verifies them; it neither selects nor
locks a version. The developer and dependency commands write `pt.mod`;
Pluto's tooling writes `pt.sum`, which grows with the whole dependency
graph. A checksum new to `pt.sum` is checked against the release's record
(below) before it is written. A mismatch with a recorded checksum fails the
build, and an ordinary build never overwrites one.

A release's record is its entry in the checksum log, with the proxy's copy
of its source (see "Hosting"): its module path and version, the checksums
of its source and its `pt.mod`, and the commit it came from. Once a release
is recorded, its contents are fixed: a tag moved to other contents fails
every build that checks the log. A Git tag alone cannot give that
guarantee, a Pluto-specific one such as `pluto/v1.2.3` included, since any
tag can be moved or deleted: a tag names a release, and the record vouches
for its contents. The record matters most on first use, when a project has
no `pt.sum` entry to compare with and would otherwise accept whatever a
moved tag points at; Go checks new entries against its checksum database
for the same reason. `pt.sum` still earns its place beside the record: it
travels with the repository, verifies sources fetched from a mirror or a
cache, and would expose a record that later changed.

## Hosting

Modules are hosted as Go's are. A module's code lives in its own
repository, which its module path names, and a release is a version tag
there: nothing is uploaded and no name is registered. A module path begins
with a domain, so the name belongs to whoever controls that domain or
repository, and there is no shared namespace of short names to squat.

Two shared services, both optional, make releases durable and the same for
everyone, as Go's module proxy and checksum database do:

- A proxy keeps a copy of each release it serves, so a moved tag or a
  deleted repository changes nothing for builds that fetch through it.
- A public, append-only checksum log records each release's checksums, and
  builds check new `pt.sum` entries against it. Before recording a release,
  the log reruns publishing's comparison of API manifests, which needs only
  the source, and refuses a version number the changes do not allow, such
  as a Minor release that removes a function. The rules then hold for a tag
  pushed without Pluto's tooling too. Rerunning the previous release's
  tests is left to publishing.

A private module skips both services, and its builds rely on `pt.sum`
alone.

## C callers

Pluto modules call each other through the versioned symbols described
above, which change with every release. C code calls a module through
stable wrappers instead, whose ABI holds for a whole major line:

- A wrapper's symbol keeps the major, or an equivalent such as a versioned
  library name, but not the minor and patch that Pluto's symbols carry, or
  every release would rename it.
- Any struct layout a C caller can see is part of that ABI, whether the
  struct is passed by value or by pointer: a caller compiled against an old
  header could declare `Person p` and pass `&p`, and a release that added a
  field would write past it. A wrapper therefore hands out structs as opaque
  handles, like C's `FILE *`: the header declares the type without its
  fields, the wrapper creates and frees it, and C reads and sets its fields
  through functions. Every struct then stays free to gain a field in a Minor
  release.

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
its contract: a build compiles every module against the releases it selects
(see "Choosing releases"), so code compiled against one layout never meets
another, and C callers see a struct only as an opaque handle (see "C
callers"). When zero is not a safe value for the new field in existing
constants, the change breaks behavior and is Major. Reordering a struct's
fields is Major too, since a literal that lists every field must use the
definition's order.

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
7. Tags the release and, for a public module, has the checksum log record
   it, so that its record is made from the contents these steps checked
   (see "Hosting").

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

## Programs without a module

A directory with no `pt.mod` in it or above it compiles as a standalone
program: its own `.pt` and `.spt` files, with the standard library that
ships with the compiler. A program that uses any other module needs a
`pt.mod` that declares it, and a reusable module always has one. The
compiler keeps the standard library in a directory such as `PT_ROOT`, but
any other module installed there reaches a program only through a
`pt.mod`: otherwise installing or updating one would change what an
existing script compiles to, and two machines could build different
programs from the same source. Today the compiler stops when it finds no
`pt.mod`, so this is still to be built.

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

- How `pt.mod` spells a dependency, an alias for a dependency or a standard
  library, and the minimum compiler, and how code writes a function,
  constant or struct from a dependency.
- Who runs the proxy and the checksum log, and how a private module opts
  out of them, as Go's `GOPRIVATE` does.
- Whether raising the minimum compiler, which using a newer standard
  function requires, is a Minor change or, as the table's row on dropping a
  supported compiler version now makes it, a Major one.
- What module path a standalone program's symbols and cache use, with no
  `pt.mod` to name one.
- How a publisher declares a compatible addition, a break or a bug fix,
  with any migration note, and asks for a higher category than the changes
  require.
- Backports: how a publisher picks the maintenance line a release updates,
  with numbering still automatic, and what happens when the computed version
  already exists, such as a Minor on a line whose next minor is out.
- Whether `pt.mod` writes a dependency's major in its path, as Go's `/v2`
  does, or only in its version, as in `github.com/you/math v2.3.5`.
  With the version alone, a module that uses two majors of one dependency
  lists it twice and aliases one; with the path, whether `v0` and `v1` carry
  one, and a dependency's default name skips the `/vN`.
- What `0.y.z` releases promise: the categories above, under which a break
  published after `0.4.2` is `1.0.0`, or SemVer's looser rule, under which
  anything may change before `1.0.0`.
- Which functions get a stable C wrapper: a per-function choice for C
  callers, separate from how Pluto modules call each other.
- The inferred input requirements described under "What the contract
  covers", if rerun tests and declared breaks prove too weak.
