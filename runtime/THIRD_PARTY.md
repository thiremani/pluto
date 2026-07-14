# Runtime Dependency Policy

Pluto-owned runtime code is licensed under `Apache-2.0 WITH LLVM-exception`.
Generated programs statically link parts of that runtime.

Classify every proposed dependency by where it is used:

- Compiler-only dependencies do not become part of generated programs.
- Always-linked runtime dependencies affect every distributed generated program.
- Feature-linked dependencies affect only programs that use that feature.

Prefer runtime dependencies that do not add notice or reciprocal-license
requirements to ordinary generated programs, such as public-domain code,
CC0-1.0, 0BSD, MIT-0, or code with an explicit compiler/runtime exception.
Other dependencies require a documented review of their output-distribution
terms before adoption.

Third-party licenses are never replaced or relaxed by Pluto's license. Record
each runtime dependency's name, source, license, linkage scope, and downstream
obligations in this file.

## Current Dependencies

None.
