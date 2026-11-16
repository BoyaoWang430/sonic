# Compatibility and Migration Notes

This document summarizes the platform matrix, runtime architecture changes, and intentional API/behavioral updates introduced in the `go:linkname`-free native scanner & VM refactor.

## 1. Go Version and Architecture Matrix

| Platform | Go Versions | Backend |
|---|---|---|
| `amd64` (Linux / macOS / Windows) | Go 1.20 – 1.27 | Native `OptDec` + `VM` encoder + handwritten AVX2 / SSSE3 / SSE2 kernels |
| `arm64` (Linux / macOS / Windows) | Go 1.20 – 1.27 | Native `OptDec` + `VM` encoder + handwritten NEON kernels |
| `riscv64` (`GORISCV64=rva22u64` / `rva23u64`) | Go 1.26 – 1.27 | Native `OptDec` + `VM` encoder + handwritten RVA22 scalar / RVV kernels |
| Other platforms (`ppc64le`, `s390x`, `wasm`, `386`, `riscv64 rva20u64`, Go >= 1.28, or `-tags=sonic_compat`) | Go 1.20+ | Automatic `encoding/json` compatibility fallback (`sonic.APIKind == sonic.UseStdJSON`) |

- **Minimum Go version is Go 1.20** (Go 1.18 and 1.19 are no longer supported).
- **Zero `//go:linkname` and zero JIT**: All `//go:linkname` bindings into private Go runtime symbols, the `loader` module, and dynamic machine-code JIT execution (`golang-asm`, `cpuid`, `base64x`) have been removed. Sonic no longer requires executable heap pages (`W^X` / hardened containers) or Go runtime internal struct layout coupling.

## 2. Public API Changes

1. **Zero-copy custom marshaler interface renamed to `AppendSonicJSON`**:
   - The opt-in fast-append encoder interface was renamed from `AppendJSON([]byte) ([]byte, error)` to `AppendSonicJSON([]byte) ([]byte, error)` so existing user types with an unrelated `AppendJSON` method still honor standard `json.Marshaler` (`MarshalJSON`).
2. **`decoder.RegisterNoCopyRawMessageType` unexported**:
   - `sonic.NoCopyRawMessage` remains registered automatically at package initialization; the internal registration hook is no longer part of the public `decoder` package API.
3. **Explicit `option.WithCompileMaxInlineDepth` preserved**:
   - Explicitly passing `option.WithCompileMaxInlineDepth(n)` (including `n == option.DefaultMaxInlineDepth`) is now respected instead of being overwritten by VM default inlining depth.
4. **Expanded `ast` APIs**:
   - Added `PathPlan`, `PatchByPath`, and streaming materialization helpers on `ast.Node` / `ast.Searcher`.

## 3. Decoder and Encoder Behavioral Alignment

1. **Unified `OptDec` + `VM` semantics across `amd64`, `arm64`, and `riscv64`**:
   - `amd64` now uses the same `OptDec` decoder and `VM` encoder as `arm64` and `riscv64` instead of a separate JIT pipeline.
2. **`float32` single-step rounding and boundary alignment with `encoding/json`**:
   - Decoding into `float32` targets (scalars, struct fields, pointers, slices, maps, and `,string` fields) performs exact IEEE-754 single-step rounding matching `strconv.ParseFloat(s, 32)` (avoiding `decimal -> float64 -> float32` midpoint double rounding, accepting decimal literals that round to `±math.MaxFloat32`, and rejecting only literals that round to `±Inf`).
3. **Raw JSON subtrees, huge-exponent numbers, and error boundaries**:
   - Raw subtrees (`json.RawMessage`, `NoCopyRawMessage`, `json.Unmarshaler`) are always syntactically validated even when `NoValidateJSONSkip` is enabled (`NoValidateJSONSkip` relaxes only skipped unknown values).
   - Valid RFC 8259 decimal numbers whose exponent exceeds `float64` range (for example `1.5e+9999`) are validated syntactically during scanning and deferred as raw number tokens: they succeed in `json.RawMessage`, `NoCopyRawMessage`, `json.Unmarshaler`, `json.Number` (including pre-populated pointers inside `interface{}` or slice backing elements), and skipped unknown struct fields across all decode paths (`flat`, `schema_proj`, `runs`, `giant`, and `io.Reader` `StreamDecoder`), while producing `MismatchTypeError` during materialization when targeting `float64`, `float32`, integers, or empty `interface{}` without `UseNumber`.
   - Error-time mutation boundaries: malformed JSON syntax inside the current top-level JSON value is rejected before materializing fields or invoking `UnmarshalJSON` (trailing bytes after a complete top-level value are checked after that value finishes decoding); numeric range/type mismatches leave the failed slot unmutated (`0` rather than `±Inf`) while outer structs/slices record the first error and continue processing remaining elements (`UNJ-PARTIAL` behavior for custom `UnmarshalJSON` errors is unchanged; see `docs/sonic-go127-compatibility.md`).
4. **`,string` struct tag numeric parsing (`STRING-GONUM` / `STRING-GONUM-PLUS`)**:
   - Quoted numeric fields (`json:",string"`) delegate to Go's `strconv` numeric parsers (accepting zero-padded integers, Go hex/underscore floats, `+` prefixes, and `.5` leading decimal points while rejecting `NaN` and `±Inf`), matching `OptDec` and Go 1.27's `jsonv2`-backed `encoding/json` behavior rather than the legacy `amd64` JIT scanner.
5. **Duplicate container keys**:
   - Duplicate slice, array, map, and nested struct keys in JSON objects follow `encoding/json` overwrite/merge semantics across all decode paths (`flat`, `schema_proj`, `runs`, and `giant`).
6. **`UseNumber` bare minus rejection**:
   - Malformed bare `"-"` tokens (such as `{"":-}` or `[-]`) are rejected as invalid numbers even when `UseNumber` is enabled.
7. **`ast.ErrNotExist` sentinel identity**:
   - `ast` lookup and out-of-bounds index setters preserve direct sentinel equality (`err == ast.ErrNotExist` and `errors.Is(err, ast.ErrNotExist)`).
8. **Resource bounds on untrusted inputs**:
   - Pathological unclosed container inputs (`[[[[...`) stop at `MAX_RECURSE` (`4096`) without unbounded node-tape growth; encoder overflow stacks grow in `4 KiB` chunks; slice output preallocation is capped at `4 MiB`.

## 4. Environment Variables

- `SONIC_MODE`: Controls `amd64` SIMD ISA dispatch (`auto` by default):
  - `SONIC_MODE=noavx` or `SONIC_MODE=noavx2`: Disables AVX2 (`HasAVX2=false`) while keeping SSE2/SSSE3 active.
  - `SONIC_MODE=nossse3`: Disables both AVX2 and SSSE3 (`HasAVX2=false`, `HasSSSE3=false`) to exercise pure SSE2/scalar fallbacks.
- `SONIC_USE_OPTDEC`, `SONIC_USE_FASTMAP`, `SONIC_ENCODER_USE_VM`: Retained only for script backward compatibility; `OptDec`, `FastMap`, and the `VM` encoder are always enabled on native platforms.
