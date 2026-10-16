# Sonic AST RISC-V64 optimization and convergence

## Current implementation (2026-10-16)

The production changes are in `ast/decode.go`, `ast/api.go`,
`ast/api_compat.go`, and `ast/parser.go`. They retain bounded eight-byte string
and container scanning, exact quote masks, scalar escape handling with spaced
recovery probes, specialized object-key decoding, and the existing small
whitespace/opener shortcuts. The native key wrapper keeps native validation;
the generic implementation preserves the previous error and cursor behavior.
No compiler or standard-library implementation is changed.

The convergence pass removes the second copy of the quote-parity and bracket-depth
word processor in `skipPair`. After a closing quote, a successful recovery probe
returns to the normal word scanner. This can reread one word, but structural
parsing and first-balanced-close handling now have one implementation. The
simple loop for continuing inside a string remains: completely merging that
loop also was measured and rejected because several sparse/early-escape query
workloads became about 20–30% slower. The selected implementation keeps that
existing loop.

`skipPair` shrinks from 278 to 231 source lines. Its compiled body shrinks from
1,250 to 986 bytes with local Go 1.26.4, and from 1,206 to 956 bytes with local
Go 1.27. The stack frame remains 48 bytes; no scanner helper calls are introduced.
The shared scanner retains eight-byte bounds checks, little-endian lane order,
actual-byte verification for delimiter candidates, and input lifetime protection.
Escape probes still back off from 8 to 128 scalar bytes. `firstSlash` names the
initial recovery trigger; it is a probe-scheduling watermark, not quote state.

Duplicate test oracles now use the existing independent scalar references, and
Linux protected-page tests share one fixture. Three files were merged into
existing test files, reducing the added test-file count from 19 to 16 and removing
157 lines overall. All Test, Benchmark, and Fuzz entry points are retained;
benchmark bodies and inputs are unchanged.

## Comparison with the version before convergence

The baseline here is the accumulated AST optimization immediately before this
cleanup, **not** upstream Sonic or the original deployed service. Both variants
use the same consolidated test suite. Benchmarks run natively on CPU 2 with one
benchmark CPU, `checkptr=0`, the same
`GORISCV64=rva22u64,zacas,zfa,zicond,misaligned_fast`, and
`SONIC_NO_ASYNC_GC=1` (only disables the suite's forced-GC loop; ordinary GC stays
enabled). Execution alternates baseline/candidate order. Values below are
medians in ns/op; a positive change means slower.

Primary results use local Go 1.26.4; supporting results use local Go 1.27.
The service profile was collected before these optimizations. Its Tango build
is `go1.26.4.bm.1.0.1066_test-X:swisstable,aggressiveopt`, whereas the local
Go 1.26.4 build reports `bm.1.0.0` and does not accept `aggressiveopt`. This is
a controlled local comparison, not an exact reproduction of the service build
or a production speedup claim.

| Workload | Before, Go 1.26.4 | Converged, Go 1.26.4 | Time change | Go 1.27 time change |
| --- | ---: | ---: | ---: | ---: |
| Parser | 5,707.0 | 5,778.0 | +1.2% | -0.1% |
| ParseOne | 10,979.0 | 11,025.0 | +0.4% | -0.4% |
| GetOne | 4,466.0 | 4,526.0 | +1.3% | +0.2% |
| GetFull | 10,660.0 | 10,538.0 | -1.1% | +0.7% |
| GetWithManyCompare | 2,269.0 | 2,275.0 | +0.3% | +1.3% |
| Skip container, early escape | 712.0 | 709.2 | -0.4% | +6.0% |
| Skip container, sparse escapes | 1,059.0 | 912.6 | -13.8% | +18.4% |
| Skip container, dense escapes | 1,506.0 | 1,506.0 | +0.0% | -0.0% |
| Many short escaped fields | 1,053.0 | 1,239.0 | +17.7% | +8.4% |

The primary matrix contains 120 benchmark rows with five 200 ms samples per
version; the Go 1.27 supporting matrix contains the same rows with three samples.
Bytes/op and allocations/op match in every sample across each pair. The selected
version favors one structural implementation over the copied recovery kernel;
it is not a uniformly faster implementation.

The short-escaped-field workload is a deliberate tradeoff: it repeatedly closes
an escaped string and resumes structural scanning, so it exercises the removed
specialization particularly often. Its complete-query result is reported above,
not hidden among scanner microbenchmarks. Other workload differences should not
be attributed to one instruction without further evidence: unchanged routines
also show timing shifts. These synthetic workloads are not weighted by the
service's input distribution.

The largest positive query-time changes in the complete matrix are:

| Local toolchain | Query workload | Before ns/op | After ns/op | Change |
| --- | --- | ---: | ---: | ---: |
| Go 1.26.4 | `BenchmarkGetManyShortEscapedFields` | 1,053.0 | 1,239.0 | +17.7% |
| Go 1.26.4 | `BenchmarkGetSelectedContainers/emptyContainers/compact/validate=false` | 408.2 | 456.9 | +11.9% |
| Go 1.26.4 | `BenchmarkGetScanArrayIndex/emptyContainers/spaced/missing` | 1,082.0 | 1,168.0 | +7.9% |
| Go 1.26.4 | `BenchmarkGetScanArrayIndex/mixed/spaced/missing` | 1,345.0 | 1,446.0 | +7.5% |
| Go 1.27 | `BenchmarkGetEscapeSpacing/spacing127/offset0` | 777.6 | 923.4 | +18.8% |
| Go 1.27 | `BenchmarkGetEscapeSpacing/spacing127/offset3` | 776.7 | 921.1 | +18.6% |
| Go 1.27 | `BenchmarkGetScanWorkload/skipContainer/sparseEscape` | 794.2 | 940.4 | +18.4% |
| Go 1.27 | `BenchmarkGetEscapeSpacing/spacing255/offset0` | 683.1 | 803.2 | +17.6% |

Raw per-sample ranges and all rows are retained in the local comparison report.


## Validation and reproduction

The selected frozen sources pass full AST tests in default and optimized/VM
environments with Go 1.26.4 (`checkptr=0`) and Go 1.27 (`checkptr=1`). The latter
also passes 15 seconds each of `FuzzKeyScanSearch`, `FuzzSkipPair`, and
`FuzzSkipPairQuoteMasks`. These runs use a regular test binary without coverage
instrumentation, so they are mutation tests without coverage guidance.

Focused tests pass on native RISC-V `rva20u64` with Zbb disabled, arm64 under
QEMU, and s390x under QEMU. The s390x run disables AST inlining and GC because
of existing toolchain/runtime limitations; it is a limited big-endian check.
amd64 compilation passes, but that binary was not executed here. No claim of
a passing whole-repository suite is made; earlier runs exposed existing RISC-V
native/JIT dependencies and compatibility failures, recorded below.

Source hashes tie the installed files to the benchmark and correctness builds.
Local snapshots, per-sample results, code generation, and validation logs are in
`/tmp/sonic-ast-convergence/`. They are evidence for this run, not build inputs
required by the repository. To run a fresh comparison, preserve both source
versions and build each using the same Go binary and benchmark files:

```bash
export GORISCV64=rva22u64,zacas,zfa,zicond,misaligned_fast
GO_REVIEW=/data00/wangboyao/tango1.26/go/bin/go
"$GO_REVIEW" test -c -gcflags=all=-d=checkptr=0 -ldflags=-checklinkname=0 \
  -o /tmp/sonic-ast-review.test ./ast
SONIC_NO_ASYNC_GC=1 taskset -c 2 /tmp/sonic-ast-review.test \
  -test.run='^$' -test.cpu=1 -test.count=5 -test.benchtime=200ms -test.benchmem \
  -test.bench='^(BenchmarkParser_Sonic|BenchmarkParseOne_Sonic|BenchmarkGetOne_Sonic|BenchmarkGetFull_Sonic|BenchmarkGetScanWorkload|BenchmarkGetManyShortEscapedFields)$'
```

## Comparison with the community baseline (2026-10-16)

This comparison restores all twelve AST production files to upstream commit
`5ce94543c910f7d64aacbf46f5eefae81daee56a` and compares them with the installed
converged version. Other dependencies and all benchmark files are held fixed.
The reference `decode.go` is byte-identical to local tags v1.15.2 and v1.15.4.
The profile labels its dependency v1.15.2 and matches the scalar scanner shape
with a two-line offset; the exact deployed commit is still unknown.

Both versions use local Go 1.26.4, the ISA above, checkptr=0, CPU 2, one benchmark
CPU, and five alternating-order samples of 200 ms. All 68 benchmark rows retain
the same bytes/op and allocations/op in every sample. The current inputs match
the converged version's validated hashes; the reference also passes complete
AST tests in default and optimized environments.

The following are generated inputs exercising the profiled paths, not a replay
of business JSON and paths. A ratio above 1 means faster. Query workloads include
path location, selected-value validation, and copying, except GetOne (no copy).
Returning a large selected value shifts the cost toward validation and copying;
it should not be interpreted as a pure skipPair measurement.

| Query workload | Community us/op | Current us/op | Speedup |
| --- | ---: | ---: | ---: |
| GetOne (no copy) | 25.002 | 4.521 | 5.53x |
| Skip numeric array | 2.542 | 0.539 | 4.71x |
| Skip nested records | 7.784 | 2.049 | 3.80x |
| Nested path | 10.252 | 2.460 | 4.17x |
| Skip embedded JSON text | 3.987 | 0.550 | 7.25x |
| Skip long early-escaped string | 8.469 | 0.709 | 11.94x |
| Return nested container | 9.621 | 5.974 | 1.61x |
| Return plain long string | 3.704 | 1.655 | 2.24x |
| Many short escaped fields | 1.901 | 1.237 | 1.54x |
| Skip densely escaped text | 1.528 | 1.504 | 1.02x |
| Return densely escaped string | 4.153 | 4.732 | 0.88x |
| Alternating short escaped strings | 2.127 | 2.340 | 0.91x |

The existing Parser and ParseOne workloads improve by 5.70x and 5.19x; these
are lazy parsing workloads, not full recursive DOM materialization. GetFull,
which validates the root with no path lookup and no result copy, improves by
1.59x. Dense returned strings regress by 13.9%, and alternating short escaped
strings regress by 10.0%; input distribution therefore matters. The earlier
17.7% short-escaped-field regression was relative to the pre-convergence optimized
version: this same converged workload is still 1.54x faster than the community
baseline.

For profile `source0_douyin.pack.platform_202610161030_981014`, disjoint scanner
self CPU is 28.34 s for skipPair and 3.53 s for skipString, out of 258.76 s total.
Their combined 12.32% of total CPU is 92.00% of AST CPU (34.64 s). Cumulative
searchKey time includes these scanners and must not be added again. Fallback
encoding/json work is excluded from the optimization target and held fixed.

Let r_pair and r_string be old/new CPU ratios for the same *business-weighted
scanner work*. With other work unchanged:

`new total CPU = 226.89 + 28.34/r_pair + 3.53/r_string`.

| Assumed speedup of both scanners | Total CPU reduction for fixed work | AST CPU speedup |
| --- | ---: | ---: |
| 2x | 6.16% | 1.85x |
| 4x | 9.24% | 3.23x |
| 6x | 10.26% | 4.29x |

These scenarios are sensitivity calculations, not measured service gains or
inferred input weights. Do not substitute a complete-query speedup or an
arithmetic mean of benchmark ratios for a business-weighted scanner ratio.
Even eliminating both scanners entirely saves at most 12.32% direct total CPU
with all other work fixed. CPU reduction is not a latency or throughput claim.
The exact service compiler and JSON/path distribution are unavailable; dense
escape-heavy traffic can reduce the benefit. Local build-flavor differences are
described above.

Raw samples, source hashes, API/fixture mapping, and the conditional model are
in `/tmp/sonic-ast-community-comparison/` (`report.json`, `comparison.txt`, and
`benchmark-scope.txt`). No production source changed in this comparison.

## Historical investigation and initial scanner implementation

The remainder records earlier prototypes and the initial eight-byte scanner.
Its source versions, implementation descriptions, measurements, and validation
apply to those historical snapshots, not the converged implementation above.
In particular, permanent scalar fallback and the earlier small code sizes no
longer describe the current scanner.

This investigation used `/data00/wangboyao/tango/go/bin/go` (`go1.27.0 linux/riscv64`) with exactly `GORISCV64=rva22u64,zacas,zfa,zicond,misaligned_fast`. The Go source checkout was at `b29c18cf7d18f2d3d1f3a089f04867b83e1fdfb6`; Sonic was at `5ce94543c910f7d64aacbf46f5eefae81daee56a`. Measurements ran natively on this RISC-V host, whose reported ISA includes Zbb, Zbs, Zacas, Zfa, and Zicond. The CPU frequency governor reported `performance`.

The largest demonstrated opportunity is changing AST scanning from one byte per iteration to eight bytes per iteration. In the existing `BenchmarkGetOne_Sonic`, a CPU profile attributed 96.09% of samples directly to `ast.skipPair`. A temporary SWAR prototype using CTZ reduced this benchmark's median time from 24.749 microseconds to 6.085 microseconds. This is an algorithm-and-code-generation improvement under the same ISA settings, **not a measurement of the isolated benefit of enabling Zbb**.

The initial experiments described below used Go overlays in `/tmp`. That initial implementation changed only Sonic's `ast/decode.go` and added scanner tests and benchmarks. No Go compiler or standard-library implementation files were changed. Historical source line numbers below refer to the baseline checkout.

### Initial implementation snapshot: AST word scanning

The patch changes `skipPair` and `skipString` in `ast/decode.go` to scan eight bytes at a time. An explicit little-endian load keeps byte ordering portable; every load is guarded by eight remaining bytes. `skipPair` checks actual candidate bytes before changing quote/depth state because the subtract-and-mask idiom can produce extra bits through borrowing. It preserves the existing permissive skip behavior, including backslashes outside strings, rather than introducing stricter JSON validation. All scan exits keep the input string alive.

The first block containing a backslash switches to an independent scalar loop for the rest of that value. This avoids repeatedly testing blocks of densely escaped text. It deliberately sacrifices word scanning after an early escape, even if the remaining suffix is ordinary ASCII. The scalar `skipPair` tail also checks quote state before comparing delimiters. This conservative design retains the selective-query improvement while avoiding the large dense-escape regressions of the initial prototypes; its Parser/ParseOne results are smaller than those of the aggressive prototypes.

The exact requested compiler/ISA emits one eight-byte load (C.LD, the compressed LD encoding) per word and uses Zbb ORCB and CTZ. `skipString` is 332 bytes and `skipPair` is 512 bytes, with two static ORCB sites and one CTZ site each. Neither calls a binary/bits helper on this target. These numbers describe generated code, not the isolated contribution of Zbb to performance.

#### Initial snapshot measurements

Baseline and final binaries contain the same tests and benchmarks; an overlay restores only the original `ast/decode.go` for baseline. Both use the compiler and ISA specified at the top of this report. Measurements run natively on CPU 2 with `-test.cpu=1`, five samples, and `SONIC_NO_ASYNC_GC=1` (disables the test suite's forced-GC busy loop, not normal automatic GC). Existing benchmarks use 300 ms per sample; scanner microbenchmarks use 150 ms. Values are medians, in ns/op. Bytes/op and allocations/op are unchanged in every benchmark.

| Existing benchmark | Original | Final | Original / final |
| --- | ---: | ---: | ---: |
| `BenchmarkParser_Sonic` | 32,919.0 | 21,262.0 | 1.55x |
| `BenchmarkParseOne_Sonic` | 57,294.0 | 27,319.0 | 2.10x |
| `BenchmarkGetOne_Sonic` | 24,737.0 | 5,299.0 | 4.67x |
| `BenchmarkGetFull_Sonic` | 16,186.0 | 10,859.0 | 1.49x |
| `BenchmarkParseEmpty_Sonic` | 897.8 | 848.6 | 1.06x |
| `BenchmarkGetWithManyCompare_Sonic` | 2,925.0 | 2,830.0 | 1.03x |

These are repository workloads, not a claim of the same speedup for the entire profiled application. The microbenchmarks expose the remaining tradeoffs:

| Scanner input | Original | Final | Interpretation |
| --- | ---: | ---: | --- |
| `BenchmarkSkipString/ascii4096` | 2,750.000 | 771.200 | Ordinary long string benefits from word scanning |
| `BenchmarkSkipString/escapedDense` | 905.500 | 915.600 | Approximately unchanged; 1.1% slower |
| `BenchmarkSkipString/escapedEarlyLongTail` | 2,755.000 | 2,756.000 | No meaningful improvement after early escape |
| `BenchmarkSkipPair/escapedDense` | 1,305.000 | 1,055.000 | Scalar quote-state ordering also helps |
| `BenchmarkSkipString/empty` | 3.019 | 3.339 | About 0.32 ns slower |
| `BenchmarkSkipString/ascii4` | 6.010 | 6.678 | About 0.67 ns slower |
| `BenchmarkSkipPair/emptyObject` | 4.007 | 5.009 | About 1.00 ns slower (25%) |

#### Initial snapshot validation

- The complete AST suite passes in both default and `SONIC_USE_OPTDEC=1 SONIC_USE_FASTMAP=1 SONIC_ENCODER_USE_VM=1` environments, with the requested ISA. This includes scalar-reference differential tests, boundary/malformed input cases, a protected-page overread test, and a Searcher regression with escaped strings and nested containers before the requested field.
- Ten seconds each of `FuzzSkipString` and `FuzzSkipPair` pass, at 1,500,894 and 1,414,627 executions respectively. This toolchain reports no coverage instrumentation on riscv64, so these runs do not provide coverage-guided fuzzing.
- Native `rva20u64` focused tests pass, including a run with `GODEBUG=cpu.zbb=off`. On that target, word reads expand into byte loads and CTZ has runtime dispatch to a generic fallback.
- arm64/QEMU focused tests pass with normal automatic GC. s390x/QEMU focused tests pass only with the same baseline workarounds (`ast=-l`, `GOGC=off`): the unmodified baseline already triggers a compiler ICE and GC failures without them. This is a limited big-endian algorithm check, not full s390x support validation.
- Root `go test ./...` still fails in the same ten packages as the original baseline. The only failing test is the existing `TestConfigUseUnicodeErrors`; the other packages have missing riscv64 native/encoder/JIT dependencies. The failure sets are identical before and after the patch.

New tests and benchmarks are in `ast/skip_scan_test.go`; Linux guard-page coverage is in `ast/skip_scan_linux_test.go`. Full comparison data, logs, and assembly are in `/tmp/sonic-riscv64-impl/`: `final-benchmark-summary.json`, `final-{baseline-,}{main,micro}.bench`, `final-validation-summary.json`, `final-*.log`, `final-skipString.s`, `final-skipPair.s`, and `cross/`. These temporary artifacts are not required by the implementation.

The command used for that initial snapshot was:

```bash
export GORISCV64=rva22u64,zacas,zfa,zicond,misaligned_fast
export GOCACHE=/tmp/sonic-riscv64-review/gocache
GO_REVIEW=/data00/wangboyao/tango/go/bin/go
"$GO_REVIEW" test -c -ldflags=-checklinkname=0 -o /tmp/sonic-ast-final.test ./ast
SONIC_NO_ASYNC_GC=1 taskset -c 2 /tmp/sonic-ast-final.test \
  -test.run='^$' -test.cpu=1 -test.count=5 -test.benchtime=300ms -test.benchmem \
  -test.bench='^(BenchmarkParser_Sonic|BenchmarkParseOne_Sonic|BenchmarkGetOne_Sonic|BenchmarkGetFull_Sonic)$'
```

The original scanner can be reconstructed from `git show 5ce94543c910f7d64aacbf46f5eefae81daee56a:ast/decode.go` and supplied through a Go build overlay. The final comparison used `/tmp/sonic-riscv64-impl/final-baseline.json` for that overlay. Subsequent sections record the earlier investigation and prototype measurements, not this final patch's results.

### Actual execution paths

`go list` selected Sonic's root `compat.go` and `ast/api_compat.go` for this target. Root Marshal/Unmarshal delegate to `encoding/json`, with Sonic's configuration and stream wrappers. AST decode/skip operations use the Go functions in `ast/decode.go`; quoting uses `quoteString` in `ast/encode.go`. AST's interface-valued encoding still delegates to `json.Marshal`, and UTF-8 validation delegates to `utf8.ValidString`.

This particular Go build defaults to the **jsonv2 implementation behind the encoding/json v1 API**. Although `go env GOEXPERIMENT` was empty, `go list -f '{{.GoFiles}}' encoding/json` selected `v2_decode.go`, `v2_encode.go`, `v2_indent.go`, `v2_inject.go`, `v2_options.go`, `v2_scanner.go`, and `v2_stream.go`. The older `encoding/json/decode.go` and `encode.go` are therefore not the relevant implementation evidence for this run.

| Location | Observed code and opportunity |
| --- | --- |
| `ast/decode.go:328`, `skipPair` | One-byte loads and branches for backslash, quote, and matching delimiters. Highest priority for lazy parsing and searching. |
| `ast/decode.go:292`, `skipString` | One-byte quote/backslash detection. Suitable for word scanning and first-event location. |
| `ast/parser.go:421`, `searchKey` | Unmatched values use `skipFast`, explaining why `skipPair` dominates search. |
| `ast/decode.go:42`, `skipBlank`; `ast/parser.go:116`, `lspace` | Scalar whitespace tests. A separate Zbs matching issue is described below. |
| `ast/encode.go:28`, `quoteString` | Scalar ASCII and `SafeSet` checks. A word scanner must also detect control and non-ASCII bytes. |
| `unquote/unquote_fallback.go:56` | Scalar initial scan and subsequent ASCII copying. Another candidate for an ASCII-span helper. |
| Go `encoding/json/internal/jsonwire/decode.go:97,142` | `ConsumeSimpleString` and `ConsumeStringResumable` still scan byte by byte. |
| Go `encoding/json/internal/jsonwire/encode.go:35,65` | `NeedEscape` and `AppendQuote` also have scalar ASCII loops. |

The baseline AST `skipString` has a 200-byte body and `skipPair` a 244-byte body. Their scanner loops contain no ORCB or CTZ. This does not establish a missing instruction-selection rule: the source algorithms do not express word operations in the first place.

`jsonwire.ConsumeSimpleString` is 86 bytes, but each ordinary byte requires an input load, a range check, table-address construction, a dependent `escapeASCII` table load, and loop control. Its generated assembly also reconstructs the table address inside the loop. A shared safe-ASCII word scanner is a more promising first intervention than scalar scheduling alone. No standard-library performance improvement was measured in this investigation.

### What this compiler already does well

The local Go backend treats `rva22u64` as providing Zbb. It already supports ANDN/ORN/XNOR, CTZ/CLZ/CPOP, REV8, MIN/MAX, and byte/halfword extension instructions. See Go `src/cmd/compile/internal/ssa/_gen/RISCV64.rules:231` and `RISCV64latelower.rules:5`; register-extension pseudos are resolved in `src/cmd/internal/obj/riscv/obj.go:5166`.

In particular, this source expression already becomes ORCB, ADDI 1, and SNEZ:

```go
func hasZero(x uint64) bool {
    return (x-0x0101010101010101)&^x&0x8080808080808080 != 0
}
```

The relevant rules are in `RISCV64.rules:764` and `RISCV64latelower.rules:41`. The probe produced a 12-byte function including return. ORCB is therefore not an absent backend feature.

The standard library also already uses Zbb in `internal/bytealg/indexbyte_riscv64.s:150` (ORCB, ANDN, CTZ), `count_riscv64.s:274` (ORCB, ANDN, CPOP), and `compare_riscv64.s:290` (CTZ). Some routines also have runtime RVV dispatch, so the presence of a Zbb branch in a binary alone does not establish which branch executes on every machine.

`unicode/utf8.ValidString`, at Go `src/unicode/utf8/utf8.go:531`, already processes ASCII in 8/16/32-byte groups. With the requested target its generated code uses direct word loads and high-bit checks. This check does not require ORCB. Likewise, AST's constant `"null"` comparison already uses a word load under `misaligned_fast`.

### Measured AST prototypes

Both prototypes replace only `skipString` and `skipPair`, plus add temporary differential tests and microbenchmarks through an overlay:

- **Boolean ORCB:** read eight bytes, test each relevant character with separate has-zero Boolean expressions, and skip eight bytes when no event exists. On a hit, execute the original scalar logic.
- **Mask + CTZ:** combine classic byte-match masks, skip an empty word, or use `bits.TrailingZeros64(mask) >> 3` to advance directly to the first event. The generated loop uses CTZ and XNOR; it does not currently use ORCB.

Within strings, `skipPair` checks quote and backslash. Outside strings it also checks the opening and closing delimiter. Both versions preserve the original handling of backslashes outside strings, EOF, and invalid input.

Measurements used CPU affinity 2, `-test.cpu=1`, `SONIC_NO_ASYNC_GC=1`, five samples per benchmark, and `-test.benchtime=300ms`. The GC setting disables the test suite's continuous forced-GC goroutine; normal GC remains enabled. Values below are medians in ns/op. All variants use identical compiler and ISA settings.

| Existing benchmark | Baseline | Boolean ORCB | Mask + CTZ | Baseline / CTZ |
| --- | ---: | ---: | ---: | ---: |
| `BenchmarkParser_Sonic` | 32,856 | 15,749 | 7,731 | 4.25x |
| `BenchmarkParseOne_Sonic` | 57,334 | 28,796 | 14,656 | 3.91x |
| `BenchmarkGetOne_Sonic` | 24,749 | 12,517 | 6,085 | 4.07x |
| `BenchmarkGetFull_Sonic` | 16,088 | 13,957 | 11,314 | 1.42x |

Bytes/op and allocations/op remained unchanged. These are the repository's existing workloads, not a universal parser-speed claim. In particular, benchmark-reported MB/s for selective access is not necessarily the number of bytes actually examined, so the comparison uses ns/op.

A three-sample reverse-order confirmation of GetOne measured medians of 6,098 ns for CTZ and 24,804 ns for baseline, consistent with the main comparison.

The CTZ version's benefit over the Boolean prototype is consistent with its code structure: it jumps directly to the event. The Boolean version can repeatedly scan overlapping eight-byte windows while advancing only one byte toward an event. No hardware-counter measurements were collected, so branch-prediction and load contributions have not been separately quantified.

The prototypes also expose remaining code-generation costs. `skipPair` grows from 244 bytes to 404 bytes in the Boolean version and 454 bytes in the CTZ version. The CTZ word path has four constant-pool loads inside the loop, versus two in the Boolean version; delimiter broadcasts are outside the loop. Investigating constant materialization/hoisting remains worthwhile.

Microbenchmarks deliberately include unfavorable inputs. In the CTZ binary, a four-character `skipString` input took 9.35 ns versus 6.01 ns for its original-function oracle; dense escaping took 1,927 ns versus 905.9 ns. For `skipPair`, dense escaping took 1,857 ns versus 1,304 ns. Long ordinary strings and sparse escapes improved substantially. These short microbenchmarks used three 100-ms samples and compare candidate and oracle within the same binary; oracle timings varied between binaries, so they should not be used for precise cross-binary ranking.

A production implementation needs a cheap short-input path and a strategy for dense events. An ORCB-based exact first-event mask followed by CTZ is a promising next experiment: it can retain direct event location while eliminating some classic-mask arithmetic and constants. That combination has not been implemented or benchmarked here.

### Concrete backend opportunities

1. **Combined has-zero predicates.** Combining two classic masks with integer OR and then testing nonzero does not trigger the current ORCB rules. A quote/backslash probe generated 66 bytes and four constant-pool loads. Expressing the predicates as Boolean OR generated two ORCB instructions, 44 bytes, and two constant-pool loads, but adds a branch. A combined predicate can use `ORCB(q) & ORCB(s)` and test whether the result is all ones. This is a demonstrated matching gap for new SWAR code, not a pattern already present in the scalar Sonic scanner.

2. **First matching byte.** `TrailingZeros64(classicMask) >> 3` emits CTZ but retains classic-mask arithmetic. A targeted rewrite of the complete byte-index expression could use `CTZ(^ORCB(x)) >> 3`. The whole expression matters: raw CTZ values and full masks are not interchangeable.

3. **Exact masks and narrower predicates.** The tested carry-free `0x7f7f...` exact-zero mask and 32-bit has-zero predicates did not generate ORCB. They offer additional targeted rules, with explicit handling of upper bits for 32-bit inputs. They were not identified as current Sonic hotspots.

4. **Zicond obscures a Zbs pattern in whitespace testing.** `internal/utils/skip.go:83` computes `(1 << c) & spaceMask != 0`. Its actual inlined AST assembly uses SLL, a shift-range check, CZEROEQZ, and AND. Merely changing it to `(spaceMask >> c) & 1 != 0` still misses BEXT under the requested `zicond` setting. The same right-shift probe without Zicond emits BEXT. In `RISCV64latelower.rules`, conditional-mask lowering at lines 11-18 introduces CZEROEQZ before the BEXT pattern at line 82 can match. A targeted rule can preserve the range condition while moving BEXT inside CZEROEQZ. **BEXT is Zbs, not Zbb.** An explicit `c < 64` guard also exposes BEXT, but adds a branch; no AST speedup from this change was measured.

Correctness is especially important for these rules. The classic subtract-and-mask zero detector can mark additional higher bytes due to cross-byte borrowing: `x=0x0100` is an example. Existence testing and the first matching byte remain correct, but the full mask or its population count is not an exact byte-match set. Consequently, a blanket replacement of classic masks with ORCB masks is invalid. Go's `test/codegen/bits.go:863` contains tests protecting this distinction. Also, Go shifts by 64 or more produce zero here, whereas an unguarded RISC-V BEXT wraps its index; the range condition must remain.

The architectural ORCB byte semantics and CTZ-based first-byte technique are documented in the [RISC-V bit-manipulation specification](https://docs.riscv.org/reference/isa/unpriv/b-st-ext.html) and [official assembly examples](https://docs.riscv.org/reference/isa/unpriv/bitmanip-examples.html).

### Prioritized work list after the application flame graph

The supplied flame graph shows `sonic.Get -> GetCopyFromString -> Searcher.GetByPathCopy -> Parser.getByPath -> searchKey -> skipFast -> skipValueFast -> skipPair`, with nearly equal widths along this branch. Source inspection connects this specifically to skipping unmatched object/array values while searching for a key (`ast/parser.go:422`, `ast/decode.go:385`). These are inclusive samples from the same call chain, not additive costs. A cropped image does not establish the percentage of total application CPU or the existence of repeated queries.

The following order is an engineering estimate for that Get/AST workload, based on observed relevance and likely impact. Only the first item has measured end-to-end improvement; the later rows are not independently measured speedups.

| Order | Work | Modification location | Evidence and expected impact |
| --- | --- | --- | --- |
| 1 | Word scanning with first-event location, prioritizing skipPair and also covering skipString | Sonic `ast/decode.go:328,292` | Highest confidence and impact. Combined prototype improved existing workloads 1.42–4.25x; GetOne 4.07x. No isolated attribution between the two functions. |
| 2 | Recognize first matching byte expressions, including combined character masks | Go `src/cmd/compile/internal/ssa/_gen/RISCV64.rules` and `RISCV64latelower.rules` | Incremental opportunity after row 1. Preserve CTZ location while using ORCB to reduce mask arithmetic and constants. Not benchmarked. |
| 3 | Recognize combined has-zero Boolean predicates | Same RISC-V rules | Alternative consumer to row 2: existence testing rather than location. Missing ORCB matching demonstrated by probes; gains cannot simply be added to row 2. |
| 4 | Investigate repeated constant loads, table-address construction, and hot-loop spills | Go `src/cmd/compile/internal/ssa/`; loop structure in Sonic scanners and jsonwire | Observed code-generation costs. Specific responsible pass and profitable change not established. Requires value-placement/register-pressure analysis, not just an ISA rewrite. Overlaps partly with removing constants through ORCB. |
| 5 | Fold bounded bit tests through CZEROEQZ to BEXT | RISC-V rules; original use is Sonic `internal/utils/skip.go:83` | Directly relevant to existing IsSpace, but lower profile weight. Include the original left-shift form, not only right-shift probes. Zbs, not Zbb; retain out-of-range semantics. |
| 6 | Batch ordinary ASCII scanning/copying during unquote | Sonic `unquote/unquote_fallback.go:56,61,149` | Source-confirmed scalar loops; benefit depends on actual unquoting. Not a demonstrated hotspot in the supplied branch. |
| 7 | Batch long whitespace runs while keeping zero/one-space cases cheap | Sonic `ast/decode.go:42`, `ast/parser.go:116` | Candidate for formatted JSON. Low expected value for compact JSON; no prototype or measured gain. Separate from the single-byte BEXT rule. |
| 8 | Investigate digit-run classification and repeated scan before numeric conversion | Sonic `internal/utils/skip.go:16`, `ast/decode.go:132,182` | Source-level candidate only. skipPair does not call these numeric parsers for numbers inside skipped containers. Numeric conversion paths must preserve overflow and syntax behavior. |
| 9 | Recognize carry-free exact zero-byte mask patterns | RISC-V rules | Probe-confirmed omission; current scalar hotspot does not contain this pattern. Low current priority; potentially useful for future scanners. |
| 10 | Recognize 32-bit has-zero predicates | RISC-V rules | Probe-confirmed omission; no current hotspot identified. Possible four-byte tail application, not yet evaluated. |

Short-input and dense-event handling are required parts of row 1, not another additive optimization. Current prototypes regress on those distributions. A production version must tune the scalar path and avoid repeated overlapping word detection. More unrolling or hand-written assembly has not been shown necessary.

The following are separate workload branches and should move up when their APIs dominate. They are not lower-impact conclusions for all Sonic users merely because the provided flame graph is a Get operation.

| Branch priority | Work | Modification location | Evidence and constraints |
| --- | --- | --- | --- |
| High for Marshal/Unmarshal | Shared ordinary-ASCII word scanning for string decoding and quoting | Go `src/encoding/json/internal/jsonwire/decode.go:97,123,142`; `encode.go:35,65` | ConsumeSimpleString, ConsumeStringResumable, NeedEscape, and AppendQuote have scalar loops. Treat this as one scanning capability used in multiple places, not four additive speedups. No end-to-end gain measured. |
| High for AST string encoding | Batch safe ASCII in quoteString | Sonic `ast/encode.go:28` | Source-confirmed scalar SafeSet loop. Preserve control-character and U+2028/U+2029 behavior. Does not accelerate ordinary Sonic Marshal's standard-library fallback. |
| Lower, profile first | Reduce AppendQuote hot-loop state spills by separating complex handling | Go `src/encoding/json/internal/jsonwire/encode.go:65`; potentially compiler value placement | Spill observed, but profitability of splitting Unicode/escape handling is unknown. Overlaps with both shared word scanning and row 4. |

Shared jsonwire scanners need distinct classification policies. ConsumeStringResumable permits ordinary HTML characters, whereas ConsumeSimpleString intentionally rejects `<`, `>`, and `&` on its simple-success path for v1/v2 compatibility. NeedEscape conservatively assumes HTML/JS escaping; AppendQuote obeys its option flags. Non-ASCII handling must retain each function's validation, replacement, and error behavior.

Two conditional caller-level opportunities should be checked separately:

- If a caller issues multiple Get requests against the same document, consider one traversal/multiple-path extraction or reusing a parsed Node. The potential gain could be large, but repeated queries are not established by the image. Reusing only Searcher does not fix this: `ast/search.go:73` resets its position on every lookup.
- If copied results are large and allocations/copying appear in the profile, evaluate the existing non-copy API at the caller. Copying occurs at `ast/search.go:94` after searching, so this cannot remove skipPair work. Retaining references also changes input-memory lifetime. No copy bottleneck has been measured here.

There is no newly demonstrated need to reimplement UTF-8 ASCII validation or add basic Zbb support to bytealg: the local Go already has word validation, ORCB/CTZ/CPOP paths, and the ordinary single-mask 64-bit has-zero rewrite. Compiler rule changes should update the rule sources and regenerate `rewriteRISCV64*.go`, with code-generation and semantic tests, rather than editing only generated files.

### Validation and reproduction

The original AST and UTF-8 tests passed with the specified compiler; unquote has no package tests. Both combined prototypes passed the complete AST suite, including the added differential tests. The CTZ prototype also passed with `SONIC_USE_OPTDEC=1 SONIC_USE_FASTMAP=1 SONIC_ENCODER_USE_VM=1`; on riscv64 these settings still select the fallback implementation.

Differential tests use snapshots of the original functions as oracles, covering lengths, offsets, eight-byte boundaries, quote/backslash positions, control/high bytes, nested delimiters, malformed endings, and 100,000 random inputs per scanner. This establishes agreement with current scanner behavior, not stricter JSON validation. Every word read is guarded by at least eight remaining bytes. `misaligned_fast` does not authorize reads past the end of a string. CTZ byte indexing assumes the target's little-endian layout.

The experiment artifacts remain under `/tmp/sonic-riscv64-review/`:

- `metadata.json`, `ast.s`, extracted `skipString.s` and `skipPair.s`, and `getone.cpu`.
- `baseline-controlled.bench`, `pair-bool.bench`, `pair-ctz.bench`, and `micro-{bool,ctz}.bench`.
- `pair-{bool,ctz}.s`, extracted scanner assembly, and test logs.
- `ast-swar-prototypes/overlay_string_pair_{bool,ctz}.json`, complete replacement Go files, and `swar_oracle_test.go`.
- Compiler probes and their detailed findings: `/tmp/sonic-zbb-probe/analysis.txt` and adjacent `.go`/`.s` files.
- Standard-library assembly: `/tmp/sonic-stdlib-review/{json,jsontext,jsonwire,utf8,bytealg}.s`.

From the Sonic repository, reproduce the CTZ experiment with:

```bash
export GORISCV64=rva22u64,zacas,zfa,zicond,misaligned_fast
export GOCACHE=/tmp/sonic-riscv64-review/gocache
GO_REVIEW=/data00/wangboyao/tango/go/bin/go
REVIEW=/tmp/sonic-riscv64-review

"$GO_REVIEW" test -c -ldflags=-checklinkname=0 \
  -overlay="$REVIEW/ast-swar-prototypes/overlay_string_pair_ctz.json" \
  -gcflags=github.com/bytedance/sonic/ast=-S \
  -o "$REVIEW/pair-ctz.test" ./ast 2> "$REVIEW/pair-ctz.s"

SONIC_NO_ASYNC_GC=1 taskset -c 2 "$REVIEW/pair-ctz.test" \
  -test.run='^$' \
  -test.bench='^(BenchmarkParser_Sonic|BenchmarkParseOne_Sonic|BenchmarkGetOne_Sonic|BenchmarkGetFull_Sonic)$' \
  -test.benchmem -test.count=5 -test.benchtime=300ms -test.cpu=1

SONIC_NO_ASYNC_GC=1 "$REVIEW/pair-ctz.test" -test.v
```

These are historical prototype commands. With the follow-up source now present, build the original baseline with `/tmp/sonic-riscv64-impl/final-baseline.json` as the overlay and use distinct output paths such as `ast.test` and `ast.s`. To reproduce the Boolean version, substitute its overlay and use `pair-bool.test` and `pair-bool.s`. The `/tmp` artifacts are local investigation outputs and may be removed by temporary-directory cleanup.
