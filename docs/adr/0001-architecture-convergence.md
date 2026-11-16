# ADR 0001: Architecture Convergence — Shared Go Core, Bounded Kernels, and Deterministic Execution

- **Status**: Accepted
- **Date**: 2026-11-16

## Context

Sonic migrated away from architecture-specific dynamic machine-code JIT (`jitdec`, `golang-asm`, `loader`), C/Clang cross-translated assembly, and `//go:linkname` bindings into private Go runtime internals. While that foundational decision unified `amd64`, `arm64`, and `riscv64` onto a standard Go execution model with static Plan9 SIMD kernels, incremental benchmark-driven optimization accumulated secondary structural complexity:

1. Multiple hand-maintained JSON grammar implementations (`Runs`, `Schema`, `Flat`, `Giant`, `Stream`, `ast`, and `sanitize/compact`);
2. A ~4,000-line parallel `Flat` struct decoder whose staged field-index commit order required interval-overlap alias guards and failure backoff;
3. Cross-request shared mutable routing heuristics (`denseStreak`, probe stripes, `runtime.GOOS != "darwin"` thresholds, and encoder `inDirectUse` + `2ms` cooldown leases);
4. Reverse-engineering compiled VM opcodes in `stbus.go` to infer `RootPlan` capabilities, and reverse dependency from `internal/resolver` into `internal/encoder/alg`;
5. Wide Go-to-assembly struct coupling (`cParityParseRunsAsm` indexing directly into `Parser`);
6. Legacy C-parity numeric truncation (`F64-LONG-OVF`) and `-race` double `json.Marshal` callback execution (`encode_race.go`).

## Decision

### 1. Core Architectural Principles
1. **No JIT and no private Go runtime coupling**: Never use `//go:linkname`, private runtime layouts (`g`, `hmap`, `moduledata`, `rawSliceHeader`), or dynamic executable memory. Use standard `reflect`, `sync.Pool`, `unsafe.Add`/`SliceData`/`StringData` on accurate types, and static Go assembly.
2. **Go owns semantics and lifetimes; assembly owns bounded computation**: Field resolution, container hierarchy, error priority, object allocation, callbacks, and ownership live in Go. Assembly kernels operate strictly on bounded `(srcPtr, srcLen, dstCap)` windows with narrow `ScanKernelState` structs and neutral status codes.
3. **Single authoritative semantics, mechanical specialization**: JSON syntax, number conversion, string unescaping, and validation each have one authoritative specification. Hot loops may use small `go:generate` templates or batch primitives, never ad-hoc parallel parsers.
4. **Deterministic, local, stateless routing**: Execution path selection depends only on immutable type capabilities, current input length, explicit caller options, and process-init CPU ISA capabilities—never on prior request history, target memory address hashes, wall/monotonic clock leases, or `runtime.GOOS` microarchitecture proxies.
5. **Immutable compiled plans, request-local state**: Type plans (`TypePlan`, `CompiledEncoder`, `FieldIndex`) are read-only once published; cursors, stacks, and scratch buffers are request/session-local.
6. **Optimizations change cost, never observable semantics or ownership**: Removing any fast path may only change CPU/allocation cost, never accepted inputs, values, callback counts/order, or buffer retention contracts.

### 2. Key Structural Convergence Decisions
1. **Retire the standalone `Flat` parser (`B1`)**: Migrate all alias, reuse, duplicate-key, and boundary regression corpora into shared contract tests, then remove `flat_direct.go` and `flat_containers.go`. Small-payload optimizations belong inside the shared scanner/materializer (e.g., inline small scratch/tape), not in a second grammar.
2. **Unify scanning & projection (`B2`, `B4`, `B5`, `B6`, `C1`–`C3`)**:
   - Narrow `Runs` assembly ABI from `*Parser` to a compact `ScanKernelState`;
   - Unify `Giant` (`scanComplete`) and `Stream` (`Step`) onto one resumable Go scan state machine where complete inputs pass `eof = true`;
   - Express `Schema` projection as node-output modes (`Emit`, `RawSpan`, `ValidateOnly`, `RelaxedSkip`) driven by shared token/skip primitives rather than a separate grammar;
   - Separate `ScanContext`, `Document`/`Tape`, and `DecodeContext`.
3. **Unify Encoder `RootPlan` and output lifecycle (`E1`–`E5`)**:
   - Emit `CompiledEncoder{Program, RootPlan}` directly from `Compiler` (deleting bytecode pattern-matching);
   - Use one pooled scratch + detached copy output path for `Marshal` and caller buffer for `EncodeInto` (deleting `inDirectUse`, `cooldownUntilNano`, and first-element slice extrapolation);
   - Replace `-race` post-hoc `json.Marshal` re-execution with single-pass race-observable Go execution so user callbacks run once.
4. **Pure-Go scalar kernel & backend layering (`C4`, `G2`)**:
   - Provide pure-Go scalar implementations behind the bounded kernel interface so non-SIMD targets, debugging/race modes, and future uncertified Go toolchains retain Sonic's exact product semantics;
   - Keep `-tags=sonic_compat` as an explicit `encoding/json` adapter with documented capability boundaries.

### 3. Fixed Public Behavioral Contracts
- **Syntax vs semantic error ordering**: Syntax errors within the current top-level JSON value fail before mutating user targets or invoking `UnmarshalJSON`; trailing tokens after a complete top-level value are checked after that value decodes.
- **Range/type errors vs custom callbacks**: Numeric range and type mismatches leave the failed target slot unmutated (`0` / `nil`, never `±Inf`) while recording the first error across outer struct/slice elements; `UNJ-PARTIAL` behavior for custom `UnmarshalJSON` errors remains explicitly documented.
- **`NoValidateJSONSkip` boundary**: Relaxes validation only on skipped unknown subtrees; consumed raw values (`json.RawMessage`, `NoCopyRawMessage`, `json.Unmarshaler`) are always syntactically validated.
- **`AppendSonicJSON` portability**: Native Sonic extension (`JSONAppender`); portable types that also target `-tags=sonic_compat` or `encoding/json` must implement an equivalent `MarshalJSON`.
- **`EncOnlyOmitNull` & compile identity**: Semantic encoder flags (`EncOnlyOmitNull`) belong to explicit encoder plan identity so `Pretouch` call ordering never silently alters `omitempty` behavior for unrelated callers; `WithCompileMaxInlineDepth` uses one explicit default without hidden backend replacement.
- **Go version certification policy**: Certified Go versions use accelerated native kernels; uncertified Go versions are explicitly distinguished from certified ones, using scalar reference kernels or `-tags=sonic_compat` until certified.
- **AST duplicate keys & indexing**: `ast` preserves chunked `Node` address stability, appearance-order pair storage, last-wins index lookup on duplicate keys, and centralized tombstone accounting across `Set`/`Unset`/`Replace`.
- **`StreamDecoder` memory model**: Guarantees incremental `io.Reader` scanning without re-scanning consumed prefixes and validates each top-level value before materialization; does not promise `O(1)` memory for a single arbitrarily large JSON value.
