# Compatibility, Ownership, and Contract Matrix

This document defines Sonic's platform matrix, four-layer compatibility contract, memory ownership rules, and backend capability boundaries. Architectural rationale is recorded in [`docs/adr/0001-architecture-convergence.md`](docs/adr/0001-architecture-convergence.md).

## 1. Platform, Go Version, and Backend Matrix

| Layer | Target | Go Versions | Execution Backend & Semantics |
|---|---|---|---|
| **Accelerated Native** | `amd64` (AVX2 / SSSE3 / SSE2), `arm64` (NEON) | Go 1.20 – 1.27 (certified) | Shared Sonic Go core (`OptDec` + `VM` + `ast`) + handwritten Plan9 SIMD assembly kernels (`sonic.APIKind == sonic.UseSonicJSON`) |
| **Accelerated Native (RISC-V)** | `riscv64` (`GORISCV64=rva22u64` / `rva23u64`) | Go 1.26 – 1.27 (certified) | Shared Sonic Go core (`OptDec` + `VM` + `ast`) + RVA22 scalar / RVV 1.0 vector kernels (`sonic.APIKind == sonic.UseSonicJSON`) |
| **Scalar Reference Core** | 64-bit targets with scalar kernel mode / uncertified Go toolchains (staged in S8) | Go 1.20+ | Shared Sonic Go core + pure-Go bounded scalar kernels (`sonic.APIKind == sonic.UseSonicJSON`; identical Sonic product semantics without SIMD assembly) |
| **Explicit Stdlib Adapter** | `-tags=sonic_compat` or legacy fallback targets (`386`, `arm`, `ppc64le`, `s390x`, `wasm`, `rva20u64`) | Go 1.20+ | Standard library `encoding/json` adapter (`sonic.APIKind == sonic.UseStdJSON`) |

- **Minimum Go version**: Go 1.20.
- **Zero `//go:linkname` and zero runtime JIT**: Sonic uses no private Go runtime symbols, no `loader` module, no runtime machine-code generation, and no executable heap pages (`W^X`-safe).

## 2. Four-Layer Compatibility Contract (`ConfigStd` vs `encoding/json`)

Compatibility claims are evaluated across four explicit layers rather than a single binary claim:

| Layer | Guarantee under `ConfigStd` (`UseSonicJSON`) | Intentional / Documented Differences |
|---|---|---|
| **1. Decoded/Encoded Value on Valid Input** | Aligned with `encoding/json` (classic v1 / Go 1.27 `DefaultOptionsV1`), including single-step `float32` rounding, `,string` `strconv` rules, duplicate-key overwrite/merge, and sorted/HTML-escaped marshal output. | Invalid UTF-8 bytes during `ConfigStd.Marshal` escape as `\ufffd` (`UTF8-ENC`); float map keys (`MAP-F64-DEC`) are accepted on unmarshal; named pointers to `UnmarshalJSON` follow classic v1 (`TMPTR-UNJ`). |
| **2. Input Acceptance / Rejection Set** | Strict RFC 8259 syntax validation across all consumed values, skipped unknown fields, and raw subtrees (`json.RawMessage`, `NoCopyRawMessage`, `json.Unmarshaler`), including trailing non-whitespace rejection. | `OptionUseUnicodeErrors` (`_F_disable_urc`), `CaseSensitive`, and `NoValidateJSONSkip` are explicit Sonic opt-in extensions. |
| **3. Error Types and Positions** | Syntax failures return `decoder.SyntaxError` (0-based `Pos`); type/range failures return `*decoder.MismatchTypeError`. `MarshalJSON` errors return the unwrapped user error (`MARSHAL-ERR-WRAP`). | `-tags=sonic_compat` returns `encoding/json` error types (`*json.SyntaxError` 1-based/stdlib `Offset`, `*json.UnmarshalTypeError`, `*json.MarshalerError`), except `decoder.Decoder.CheckTrailings()` which wraps into `decoder.SyntaxError`. |
| **4. Post-Error Target State & Callbacks** | Syntax errors inside a single top-level value fail **before** mutating the destination or calling `UnmarshalJSON` (`SYN-PREMUT`). Numeric range/type mismatches leave the failed slot unmutated (`0`/`nil`, never `±Inf`) while recording the first error across remaining struct/slice elements. | Trailing garbage after a complete top-level value (`{"a":1} trailing`) is detected after the first value materializes. Custom `UnmarshalJSON` error continuation follows `OptDec` (`UNJ-PARTIAL` in `docs/sonic-go127-compatibility.md`). Malformed base64 clears target `[]byte` to empty (`BASE64-ERROR`). |

## 3. Input/Output Ownership and Memory Retention Contract

| Entry Point / Option | Ownership & Retention Guarantee | Caller Responsibility |
|---|---|---|
| `Unmarshal(buf []byte, &dst)` (default) | **Detached from `buf`**: decoded `string` fields and returned error sources do not alias `buf`. Mutating or reusing `buf` after `Unmarshal` returns is safe for built-in Go targets. | Do not mutate `buf` **concurrently during** the `Unmarshal` call. Custom `UnmarshalJSON` implementations that retain their `[]byte` argument must copy it themselves per `encoding/json` rules. |
| `UnmarshalString(s string, &dst)` (`CopyString == false`, default in `ConfigDefault`) | **Zero-copy string borrowing**: unescaped JSON strings may slice directly into immutable `s`. | If `s` was constructed via an unsafe `[]byte -> string` cast by the caller, the caller must keep the backing bytes immutable for the lifetime of decoded strings; if retaining one small field from a multi-MiB `s` long-term, enable `CopyString: true`. |
| `CopyString: true` / `decoder.OptionCopyString` | Decoded strings are detached from the input JSON and allocated from bounded per-decode string slabs (`<= 16 KiB` per slab; strings `> 1 KiB` allocate standalone). | Retaining one decoded string pins at most its bounded `<= 16 KiB` slab, never the entire multi-MiB input document. |
| `sonic.NoCopyRawMessage` | Borrows raw JSON slice only when input is `string` (`UnmarshalString` / `Decoder`) and `CopyString` is disabled; copies when `CopyString` is enabled or via `Unmarshal([]byte)` copy mode. | Treat `NoCopyRawMessage` as read-only; use `json.RawMessage` when standalone ownership is required. |
| `StreamDecoder` (`NewDecoder(io.Reader)`) | Forces `OptionCopyString` internally because the stream buffer compacts and slides across `Decode` calls; returned values and errors never dangle into recycled stream scratch. | Standard `io.Reader` stream usage; note `StreamDecoder` buffers and validates each complete top-level JSON value before materialization (not constant-memory for a single multi-GiB value). |
| `Marshal` / `Encode` vs `EncodeInto` | `Marshal` / `Encode` return a freshly detached `[]byte` owned by the caller (internal scratch returns to bounded `sync.Pool` `<= LimitBufferSize`). `EncodeInto(&buf, ...)` appends directly to caller `buf` while preserving existing prefix bytes on both success and error. | None beyond normal slice ownership. |

## 4. Backend Extension Capability Matrix (`Native / Scalar Sonic` vs `sonic_compat`)

| Capability | Native & Scalar Sonic (`APIKind == UseSonicJSON`) | Stdlib Adapter (`-tags=sonic_compat`, `APIKind == UseStdJSON`) | Portability Guidance |
|---|---|---|---|
| `ConfigStd` / `Marshal` / `Unmarshal` / `Stream` / `CheckTrailings` | Supported | Supported | Drop-in portable across all backends. |
| `encoder.JSONAppender` (`AppendSonicJSON`) | Supported (priority over `MarshalJSON`) | Uses `MarshalJSON` (`encoding/json`) | Portable types implementing `AppendSonicJSON` **must also** implement semantically equivalent `MarshalJSON`. |
| `CaseSensitive`, `UseInt64`, `UseUnicodeErrors`, `ValidateString`, `NoValidateJSONSkip` | Enforced | `UseNumber` and `DisallowUnknownFields` enforced; Sonic-only parser flags fall back to `encoding/json` behavior | Branch on `sonic.APIKind` when asserting Sonic-only extension flags in cross-backend tests. |
| `ast` (`Get`, `Searcher`, `PathPlan`, `PatchByPath`) | Accelerated native / shared core | Functional pure-Go compatibility implementation | Portable across all backends. |

## 5. Public API & Policy Summary

1. **`AppendSonicJSON([]byte) ([]byte, error)`**: Opt-in buffer-appending marshaler interface (`encoder.JSONAppender`), named specifically to avoid colliding with unrelated third-party `AppendJSON` methods.
2. **Explicit `option.WithCompileMaxInlineDepth` & `EncOnlyOmitNull`**: Inline depth is respected deterministically without hidden backend overwrites. Semantic compile flags (`EncOnlyOmitNull` / `OnlyOmitNull`) participate in encoder plan identity rather than letting `Pretouch` call ordering mutate global behavior for other callers.
3. **Go Version Certification Policy**: Certified Go versions (`Go 1.20–1.27` on `amd64`/`arm64`, `Go 1.26–1.27` on `riscv64`) run full SIMD native acceleration. Uncertified newer Go versions are distinguished from certified releases: the zero-`linkname` Go + bounded-kernel architecture eliminates runtime-struct breakage, while scalar reference mode (`SONIC_MODE=scalar`) or `-tags=sonic_compat` provides deterministic fallback prior to formal release certification.
4. **AST Duplicate Keys & Indexing**: `ast.Node` preserves appearance-order slots in `linkedPairs`, updates hash indices to resolve duplicate keys to the latest live entry, and maintains tombstones (`Unset`) without moving existing `Node` addresses.
5. **Resource bounds**: Container recursion stops at `MAX_RECURSE` (`4096`); initial tape sizing, pooled buffers (`LimitBufferSize`), string slabs (`16 KiB`), interface slice chunks, overflow stacks (`4 KiB` chunks), and slice output preallocation (`4 MiB`) are all explicitly bounded.
