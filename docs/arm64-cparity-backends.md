# ARM64 C-parity backend comparison

This experiment compares two ways to remove the small SIMD call boundaries in
the C-aligned parser. It does not change the default Go parser or the shared
`internal/native/kernels` interfaces. The reference is the source-expanded
C-parity parser, not the earlier default Go implementation.

## Implementations

All measured Go variants enable `sonic_cparity,sonic_cparity_inline`. The latter
mechanically expands the C-style Go helpers; it changes neither the compiler nor
its inline budget. The additional backend tags are mutually exclusive.

| Backend | Additional tag | Implementation |
| --- | --- | --- |
| Blocks | None | Original 32-byte string, 64-byte whitespace, 32-byte copy and 16-byte raw-number assembly leaves |
| Fused | `sonic_cparity_fused` | One assembly call decodes a complete string; whitespace cache misses enter a repeated-block refill operation |
| Pure Go | `sonic_cparity_purego` | All four leaves use exact 64-bit SWAR classification/bit packing or Go loads and stores |

The fused string operation includes masks, event ordering, cursor advancement,
escape decoding, Unicode conversion and overlapping copies. Its value interface
returns consumed bytes, decoded length and escape state. An escape-state value
of -1 preserves the caller's prior flag on an initial control-character error.
Returning offsets preserves the `go:noescape` contract without pointer-valued
results. Whitespace's two scalar probes and cache hits remain in Go; only cache
misses cross the assembly boundary. The raw-number mask leaf remains unchanged
and does not execute in the traced Twitter invocation.

The pure Go implementation keeps C's exact per-byte masks. It uses a zero-byte
test without cross-byte borrow false positives and multiplication to pack lane
bits. Its 32-byte copy reads all four words before any store, including when
source and destination overlap. It does not use custom assembly for these four
operations; this is not a claim that the entire Sonic library or Go runtime is
assembly-free.

`scripts/cparity/expand.go -backend={blocks,fused,purego}` selects the appropriate
source definitions. It supports the multiple and named results and void copy
operation used here, preserves argument evaluation and local scopes, and rejects
unsupported mixed short declarations. Generated files are checked in. The
original blocks parser's generated body is unchanged; only its build constraint
now excludes the two alternative backends.

## Correctness and compiler checks

Both final variants exactly match the retained C oracle's 8,892 records, including
error positions, cursor/state, partial tape and modified string bytes. The oracle
normalizes only C-unspecified null/bool payload words. Its SHA256 remains
`d8618b2bf9b49d485dfad6956f340edc0eee73e8588c2e5b4053261d7161a004`.
This finite corpus is not a proof for every possible input.

Focused tests cover every lane/byte value and randomized unaligned SWAR inputs,
overlapping copies, string block boundaries, Unicode surrogate retries, error
priority, escape-state preservation, and whitespace cache behavior. C-parity and
decoder tests pass with default and optimized/VM settings. The final expanded
implementations also pass C-parity tests with stock Go 1.26.1 and Go 1.20.14.
All three source-generation modes reproduce their checked-in output.

One compiler distinction matters: being eligible for inlining in a small caller
does not guarantee inlining inside the large expanded parser. The first pure Go
build retained 1,288 calls to `cParitySWARSpaceMask` and executed 168,581
instructions. Explicitly expanding that helper reduced the count to 145,166.
The remaining small SWAR helpers disappear in the final generated machine code.
No compiler-level fix or inline-budget change is included.

## Dynamic instruction counts

The measurement uses Tango 1.0.1153 / Go 1.26.4 on ARM64 Neoverse-N2, compiler
defaults, `CGO_ENABLED=0`, and `-ldflags=-checklinkname=0`. There is no PGO or
compiler patch. GDB counts original executed instructions in the third warmed
`parseImpl` invocation, following its callees until return. Debugger elapsed time
is never used as a benchmark result.

The input is the same 13,035-byte Twitter document, SHA256
`ddce57c7d74998f87f4901f1d471999ac5fcaf9f00337b5498b993d520ace970`,
with CopyString enabled, ValidateString disabled, and binding mode. The retained
C binary is unchanged. The rebuilt blocks control reproduces its earlier count
exactly.

| Implementation | Instructions | Change vs blocks | Change vs C |
| --- | ---: | ---: | ---: |
| Generated C | 78,780 | -23.90% | — |
| Blocks | 103,515 | — | +31.40% |
| Fused | 103,626 | +0.11% | +31.54% |
| Pure Go | 145,166 | +40.24% | +84.27% |

The original backend enters its custom assembly leaves 635 times: 472 string
classifications, 161 whitespace classifications and two copies. Fused performs
414 complete-string calls and 161 refill calls, totaling 575. Pure Go performs
none of those calls in this invocation. This input has many short strings, so
merging the block loops alone removes relatively few calls.

Disjoint instruction categories explain why fewer calls do not guarantee a
lower total:

| Category change vs blocks | Fused | Pure Go |
| --- | ---: | ---: |
| Explicit SP/FP stack loads/stores | -1,689 | -7,354 |
| Calls and returns | -120 | -1,270 |
| Arithmetic and other operations | +1,307 | +45,196 |
| Constant/move instructions | +552 | +4,221 |
| Comparisons and branches | +137 | +6 |
| Other loads/stores | -76 | +852 |
| **Total** | **+111** | **+41,651** |

These are observed code-generation differences, not a claim that every stack
access is a spill or that each category is independently removable. The pure Go
scalar work exceeds its savings in boundary instructions; the fused version is
effectively tied with blocks on this input's instruction count.

## Timing protocol

Timing runs separately from GDB and compilation, pinned to CPU 16 with
`GOMAXPROCS=1`, `SONIC_NO_ASYNC_GC=1` and normal runtime GC. Each comparison uses
eight serial rounds, balanced across the four implementations, at 300 ms per
benchmark. CPU affinity does not reserve the CPU. Raw samples, medians, ranges
and paired ratios are retained; tiny differences are not treated as established
performance wins.

`ParseBatch` restores input and parser state outside the timer and measures the
resident parser. `PrepareAndParse` includes input preparation. The decoder
benchmarks additionally include node-to-object conversion and destination
allocation. Stage times are not additive. Synthetic long-string, escaped-string
and long-whitespace inputs exercise repeated work; they are not traffic weights.

Timing results are recorded in `output/arm-native-go-cparity-backends/` alongside
the binaries, raw logs, instruction traces, category accounting and source hashes.

Median elapsed time in microseconds per operation:

| Workload | C | Blocks | Fused | Pure Go |
| --- | ---: | ---: | ---: | ---: |
| Twitter binding, ParseBatch | 8.988 | 14.518 | 13.894 | 14.500 |
| Twitter binding, PrepareAndParse | 9.210 | 14.560 | 14.035 | 14.653 |
| Compact binding, ParseBatch | 6.277 | 11.447 | 10.771 | 10.218 |
| Compact binding, PrepareAndParse | 6.415 | 11.397 | 10.838 | 10.314 |
| Twitter full binding decode | 25.893 | 30.936 | 30.514 | 30.898 |
| Twitter full generic decode | 48.938 | 54.555 | 54.100 | 54.543 |

The paired median changes versus blocks for ParseBatch are -4.14% (fused) and
-0.12% (pure Go) on Twitter binding, and -5.92% and -10.68% on Compact binding.
The pure Go implementation's higher instruction count therefore does not imply
a proportional time regression: it is effectively tied on Twitter ParseBatch and faster
on Compact. The fused implementation's full-decode gains are small compared
with its parser-only gains. These observations do not establish an exclusive
microarchitectural cause; instruction count is not a substitute for timing.

The synthetic fixtures use generic mode and the same CopyString option and
ParseBatch checks. Exact dynamic instruction counts, including parser wrappers
and executed helpers, are:

| Workload | C | Blocks | Fused | Pure Go |
| --- | ---: | ---: | ---: | ---: |
| LongString | 9,128 | 14,000 | 9,141 | 24,275 |
| EscapedString | 53,393 | 90,705 | 49,285 | 106,617 |
| LongWhitespace | 1,545 | 2,052 | 1,705 | 8,378 |

The workload trace uses the same third-entry convention and counting rules as
Twitter, with fixture-specific input length and hash checks. Its elapsed debug
time is excluded from performance results. Their median benchmark times are in
microseconds:

| Workload | Input bytes | C | Blocks | Fused | Pure Go |
| --- | ---: | ---: | ---: | ---: | ---: |
| LongString | 8,194 | 1.055 | 1.435 | 1.081 | 2.170 |
| EscapedString | 5,122 | 3.553 | 12.274 | 3.145 | 12.534 |
| LongWhitespace | 2,053 | 0.220 | 0.263 | 0.237 | 0.665 |

Paired median time changes versus blocks:

| Workload | Fused | Pure Go |
| --- | ---: | ---: |
| LongString | -24.84% | +51.34% |
| EscapedString | -74.38% | +2.12% |
| LongWhitespace | -9.72% | +153.40% |

These fixtures show the benefit of keeping repeated scanning, escape handling
and copying inside a single assembly call. They also show why replacing SIMD
with scalar Go uniformly is not a general improvement, despite its Compact
result. The fused escaped-string case is faster than the retained C baseline in
this measurement; that result is limited to the stated synthetic input.

## Reproduction

Select the intended Go toolchain and regenerate the chosen source expansion:

```sh
go run ./scripts/cparity/expand.go -backend=fused
go run ./scripts/cparity/expand.go -backend=purego

go test -tags=sonic_cparity,sonic_cparity_inline,sonic_cparity_fused \
  -ldflags=-checklinkname=0 ./internal/decoder/optdec -run '^TestCParity' -count=1
go test -tags=sonic_cparity,sonic_cparity_inline,sonic_cparity_purego \
  -ldflags=-checklinkname=0 ./internal/decoder/optdec -run '^TestCParity' -count=1
```

The timing runner is `scripts/cparity/compare_backends.py`. Exact binary paths,
benchmark selections, controlled environment and reproduction commands accompany
the results in `output/arm-native-go-cparity-backends/measurement-plan.txt`.
