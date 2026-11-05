# ARM64 Go parser and native kernels

## Current retained version

The retained ARM64 implementation replaces the generated C/NEON backend with
shared Go parsing and traversal code plus handwritten Go assembly kernels.
It no longer needs SIMDe, asm2arm, or a special Clang build. The x86 C backend
is unchanged.

ARM64 and native RISC-V (RVA22 and later) use the Runs optdec parser in an
ordinary build. No custom build tag or runtime opt-in is required. Both
compile the same Go sources; only the assembly differs. The former RISC-V Go
state machine (`parse_exec_go.go`) has been removed.

| Architecture | optdec parser | Kernel selection |
| --- | --- | --- |
| ARM64 | Runs, selected normally | Handwritten scalar/NEON operations |
| Native RISC-V | Runs, selected normally (scalar Runs scanners on RVA22 and RVA23) | RVA22 scalar or RVA23 RVV |

Go owns container bookkeeping, allocation, Unicode conversion, and complex
numeric/error handling. ARM assembly processes continuous key/value runs with
focused internal scanning operations. AST path selection and grammar remain
in Go, with complete string/container scanners and other bounded kernels in
assembly.

Strict string scanning and numeric token scanning now also use complete ARM64
assembly operations, following the existing RISC-V kernel boundary. The inner
block loop, escape checks, and numeric marker state stay within one call; Go
retains mode selection, AST traversal, and value conversion. See the
[scanner boundary comparison](arm64-scanner-boundary.md) for measurements and
the distinction between structural validation and strict string validation.

The shared AST and number conversion/formatting changes also affect native
RISC-V. Its existing Go parser and scalar/RVV kernels remain selected there.
Reusing Runs would require a RISC-V implementation of its run operation and
continuation contract. ARM timing gains are not RISC-V measurements.

Start with these current results and design notes:

- [Complete scanner boundaries and their measured effects](arm64-scanner-boundary.md).
- [Latest retained results and residual gaps](arm64-residual-hotspots.md).
- [Main hotspot optimizations](arm64-hotspot-optimization.md).
- [Runs event boundaries](arm64-cparity-runs-boundary.md) and
  [private operation ownership](arm64-cparity-runs-ownership.md).
- [Complete retained timing table](arm64-retained-results/table.md),
  [CSV with timing ranges and allocations](arm64-retained-results/table.csv),
  and [measurement provenance](arm64-retained-results/provenance.json).

For default ARM validation and RISC-V build selection, use:

```bash
scripts/test-arm-native.sh --go /path/to/go
scripts/test-riscv-build.sh --go /path/to/go
```

The ARM script clears `GOFLAGS`, checks that normal source selection includes
the Runs Go frontend and assembly entry, and runs the complete applicable
package suites in both runtime configurations. It does not filter individual
failing tests. For a focused parser check on ARM64:

```bash
GOWORK=off go test -count=1 -ldflags=-checklinkname=0 \
  ./internal/decoder/optdec
```

Omit `-ldflags=-checklinkname=0` with Go versions older than 1.23. The historical
reports include earlier tagged-build contract differences; those records
describe their measured snapshots, not the validation status of the current
default integration. The earlier AST/C comparison also recorded 2,181 semantic
differences; selecting Runs does not rewrite the AST implementation.

`scripts/bench-arm-native.sh` now compares the normal ARM64 Runs backend with
its selected baseline and clears `GOFLAGS`. The generic
`scripts/cparity/compare_backends.py` runner can also compare prepared binaries.
The committed timing tables were measured before default integration, when
Runs required explicit tags. Their original tags, source archive, and binary
hashes remain in the provenance; the tables are not a new measurement of the
integration changes. Raw logs, binaries, and experiment snapshots remain local,
ignored `output/` artifacts.

### Default integration and parser contracts

Normal ARM64 integration fixes two public wrapper bugs: decoding at a nonzero
input offset now keeps source and tape offsets relative to the same suffix;
UTF-8 correction keeps the corrected JSON source separate from the mutable
string-decoding buffer and preserves the consumed prefix. The new
[ARM integration tests](../internal/decoder/optdec/parser_integration_arm64_test.go)
cover offsets, strings and raw numbers, correction, sequential decoding, and
incomplete input at the padding boundary.

The earlier 15 tagged-suite failures were reviewed individually:

- Twelve asserted details of the bounded RISC-V Go parser, including its Go
  frames, tape metadata, exact error cursors, and unpadded internal scanning.
  These checks now remain with that backend. Shared tests continue to check
  decoded values, malformed-input rejection, and public behavior on both
  architectures.
- Two exposed the public offset and corrected-source bugs fixed above.
- One required `UseUnicodeErrors`, which the RISC-V Go parser supports but
  optdec already documented as unsupported in `api_test.go`. ARM keeps the
  original optdec/C replacement policy; the extra RISC-V option check remains.

Coverage also includes shared
[large-array growth value checks](../internal/decoder/optdec/growth_values_native_test.go)
and an [ARM padded-input guard-page test](../internal/decoder/optdec/parse_runs_guard_linux_arm64_test.go).
The complete untagged optdec suite passes with Tango Go 1.26.4.
The applicable main-module suites pass in default and optimized/VM
configurations, as do the issue regressions. Root, decoder, AST, optdec,
native, kernel, and number tests also pass without tags on stock Go 1.26.1
and Go 1.20.14. RISC-V selection and RVA22/RVA23 cross-link checks pass;
these are not target execution tests. The generic regression suite could
not start in the validation environment because three external test
dependencies were missing and the module proxy timed out. No dependency
version or test expectation was changed to bypass that limitation.
The original C-oracle digest remains unchanged after normalizing only the
recorded corrected-buffer representation: C aliases corrected source and
mutable decoded storage, while the Go wrapper now owns an immutable corrected
source separately. The oracle still compares parse results, cursors, tape,
statistics, decoded bytes, and options; source ownership is checked by the
new integration tests.

## Historical implementation and measurement timeline

The remainder of this document and the older linked reports preserve the
development sequence. Their numbers describe their recorded source versions,
not the current retained version. Earlier blocks, expanded-inline, pure-Go,
fused, hybrid, whole-assembly, and independent-operation experimental tags
are not supported by the retained source; their commands and generators are
historical references. The former `sonic_cparity,sonic_cparity_runs` opt-in
is historical too; normal ARM64 builds now select Runs directly. Use the
architecture selections and current reports above.

ARM64 uses the same Go parsing and traversal framework as the native RISC-V
backend. Handwritten Go assembly supplies the ARM64 kernels. Public encoding,
decoding, AST, escaping, and UTF-8 APIs no longer import the generated
`internal/native/neon` package.

The encoder continues to use its existing VM and the decoder continues to use
optdec. This changes the native implementation beneath those components; it
does not introduce an ARM JIT or change public configuration flags.

The last default-backend results in this historical sequence are in
[Loop checks and active-frame caching](#loop-checks-and-active-frame-caching).
The subsequent [C-aligned Go parser experiment](#c-aligned-go-parser-experiment)
is opt-in and isolates source organization/inlining with fresh instruction counts.
Earlier measurements retain their original source snapshots and baselines.

## Implementation boundaries

| Layer | Shared implementation | ARM64 implementation |
| --- | --- | --- |
| Native API adapters | `internal/native/dispatch_go.go` | Calls the shared kernel API |
| JSON validation, skipping, and paths | `internal/native/*_go.go` | Uses bounded event and whitespace masks |
| optdec parse stage | `internal/decoder/optdec/parse_cparity_*.go` (Runs frontend) | `parse_cparity_runs_arm64.s`; RISC-V uses `parse_cparity_runs_riscv64.s` |
| String, whitespace, structural, and number scanning | `internal/native/kernels/*_native.go` | `scanning_arm64.go`, `string_prefix_arm64.s`, `string_relaxed_arm64.s`, `masks_arm64.s`, and `runs_arm64.s` |
| Escaping, string decoding, and UTF-8 | Shared kernel contracts | `quote_arm64.s`, `html_escape_arm64.s`, `string_decode_arm64.s`, and `utf8_arm64.s` |
| Number conversion | `internal/native/number` and Go integer formatting | Shared Go conversion routines |

The Go layer owns parsing state, errors, caches, node buffers, and allocation.
Assembly uses normal Go ABI0 arguments and returns. ARM64 NEON handles bounded
16-byte blocks; scalar code handles short inputs, tails, and irregular work.
Mask APIs classify up to 64 bytes and return packed integer masks that callers
can reuse while visiting immutable input. The ARM64 implementations form those
masks from several NEON blocks rather than relying on RVV vector-length control.
Relaxed string scanning stays inside one assembly call across blocks, while
strict scanning preserves Go escape validation and error reporting. UTF-8 uses
NEON for ASCII blocks and bounded word checks for multibyte sequences.

The kernel contracts require accesses to stay within the logical input and
output lengths. Input padding is not required. String decoding must preserve
the existing error offsets, escape handling, and output-exhaustion/resume
semantics. The Go parser keeps its node-buffer interface compatible with the
remaining optdec stages.

The generated NEON package, its `scripts/build-arm.sh` generator,
`tools/asm2arm`, and the SIMDe submodule have been removed. ARM64 no longer uses
the C-to-assembly generation path. The remaining C sources and x86 generator
continue to serve the x86 backend. The current ARM64 backend uses NEON and does
not dispatch to SVE.

ARM64 retains the existing Go 1.20 through Go 1.27 build range. Shared source
must stay compatible with Go 1.20, even though native RISC-V requires Go 1.26
and selects scalar/RVV assembly through its target profile.

## Validation

On an ARM64 host, run:

```bash
scripts/test-arm-native.sh
```

This checks that the public dependency graph includes the shared kernels and
excludes `internal/native/neon`, then runs the ARM-relevant main-module tests
and `issue_test`/`generic_test` with default and optimized/VM settings. Existing
kernel contract tests include string errors, resume boundaries, alignment,
mask reuse, and Linux guard-page coverage.
The full host run retains the toolchain's cgo setting because the regression
suite includes Go plugin tests that require cgo. Build-only mode disables cgo.
The script defaults `GOMAXPROCS` to 4; callers can override it. This avoids
excessive contention in existing GC stress tests on machines with hundreds of
logical CPUs while still exercising concurrent execution.

The script explicitly excludes the x86-only implementation packages
`internal/jit`, `internal/decoder/jitdec`, `internal/encoder/x86`,
`internal/native/avx2`, and `internal/native/sse`. Before this migration,
unfiltered `go test ./...` already failed on the ARM64 host because these
packages reach x86 loader ABI definitions (`Parameter` and `PtrSize`) that are
unavailable on ARM64. No test failures are filtered dynamically.

To link the ARM64 main-module tests with an older toolchain without running
them or loading newer workspace modules:

```bash
GOWORK=off scripts/test-arm-native.sh --go /path/to/go1.20/bin/go --build-only
```

`--build-only` also permits cross-compilation. RISC-V dispatch and compilation
remain covered by `scripts/test-riscv-build.sh`.

The migration was checked with Go 1.20.14 (public core builds and
kernel/native/number/optdec/encoder-algorithm tests) and Go 1.26.1. Cross-builds
also covered Linux RISC-V RVA22/RVA23, Darwin ARM64, and amd64. The RISC-V
selection check covered RVA20/RVA22/RVA23; the two optional-extension profile
cases were skipped because this Go 1.26 toolchain does not accept that profile
syntax. Cross-builds verify compilation and backend selection, not execution
on those targets.
With Tango Go 1.26.4, `scripts/test-riscv-build.sh` also passed all five
configurations, including both optional-extension profile cases.

On the measured ARM64 host, the validation script passed the default and
optimized main-module tests and both runs of `issue_test`/`generic_test`.
The compatibility suite passed separately. Race execution remains unverified:
the host GCC/linker could not link the Go TSAN runtime because AArch64 atomic
symbols such as `__aarch64_cas8_acq_rel` were unavailable. This was a toolchain
link failure, not a passing race test.

## Reproducing performance comparisons

Use a revision containing the generated NEON backend as the baseline. The
script archives that revision and copies the exact same
`arm64_framework_bench_test.go` fixture into the archive. It compares that
baseline with the current working tree, including uncommitted implementation
changes:

```bash
scripts/bench-arm-native.sh \
  --baseline a7193312dec5165ec6686a9793eb308e2e642903 \
  --output /tmp/sonic-arm-native-comparison --cpu 16 --build-only

# Wait until other compilation and tests are idle before measuring.
scripts/bench-arm-native.sh \
  --output /tmp/sonic-arm-native-comparison --cpu 16 \
  --measure-only --count 5 --benchtime 200ms
```

Choose a CPU allowed by the host affinity mask. `--baseline-dir DIR` can reuse
an existing baseline checkout. `--go` selects the compiler; both variants use
that same compiler. `GOCACHE` and `GOMODCACHE` can redirect cache writes when
needed.

The public workloads cover generic and typed encoder/decoder calls, AST
lookup/parse/update, strict/fast traversal, long numbers, whitespace, and UTF-8.
Native API fixtures cover short and long quoting/unquoting and HTML escaping.
Both variants run the same fixture smoke checks before timing. Measurements
run serially with `GOMAXPROCS=1`, `SONIC_NO_ASYNC_GC=1`, optional CPU affinity,
and at least five samples. Baseline/candidate order alternates between samples.

Output includes raw logs, exact commands, environment and source metadata,
dependency selection, and `medians.csv`. Its percentage column is
`(candidate ns/op / baseline ns/op - 1) * 100`: negative values mean faster.
These medians describe the measured host and workload; they are not statistical
significance tests or a claim of universal improvement. The different parser
and number-conversion implementations are part of the comparison, so results
cannot be attributed solely to the SIMD kernels.

## Standard Go measurements

These historical results describe the initial migration before the
[string-prefix and whitespace optimization](#string-prefix-and-whitespace-optimization).

Measured on 2026-10-30 using Go 1.26.1, Linux ARM64, a Neoverse-N2 host,
`GOARM64=v8.0`, and CPU 16. Both variants used `CGO_ENABLED=0`,
`GOMAXPROCS=1`, and `SONIC_NO_ASYNC_GC=1`; the five 200 ms samples alternated
baseline/candidate order. The baseline was
`a7193312dec5165ec6686a9793eb308e2e642903`; the candidate was the migration
working tree. Values below are median ns/op, with lower values better.

The result is mixed. Encoding is about 3% faster on these end-to-end workloads,
while generic and typed decoding are 29% and 53% slower. ASCII and emoji UTF-8,
long-decimal parsing, and several escaping cases improve substantially.
AST lookup, short/mixed numbers, and escaped-string scanning still regress.
This migration establishes the shared framework; it does not establish a
performance improvement across all ARM64 workloads.

### End-to-end workloads

| Workload | Generated baseline | Go/ABI0 candidate | Change |
| --- | ---: | ---: | ---: |
| Generic decode | 46120 | 59681 | +29.4% |
| Typed decode | 24997 | 38365 | +53.5% |
| Generic encode | 59417 | 57686 | -2.9% |
| Typed encode | 14126 | 13646 | -3.4% |
| AST GetOne | 3329 | 4746 | +42.6% |
| AST GetFull | 10787 | 9342 | -13.4% |
| AST ParseSeven | 13951 | 18754 | +34.4% |
| AST SetOne | 7038 | 8347 | +18.6% |

### Selected focused workloads

| Workload | Generated baseline | Go/ABI0 candidate | Change |
| --- | ---: | ---: | ---: |
| Container / fast | 3201 | 4434 | +38.5% |
| Container / strict | 36403 | 30926 | -15.0% |
| Escaped string / fast | 1123 | 1748 | +55.7% |
| Escaped string / strict | 1114 | 1566 | +40.6% |
| Short integers | 11771 | 17488 | +48.6% |
| Mixed numbers | 24882 | 40858 | +64.2% |
| Long decimals | 225920 | 61035 | -73.0% |
| UTF-8 / ASCII | 2002 | 301.2 | -85.0% |
| UTF-8 / CJK | 2112 | 2139 | +1.3% |
| UTF-8 / Emoji | 2910 | 1499 | -48.5% |
| UTF-8 / Mixed | 3168 | 3157 | -0.3% |
| Quote / short | 21.93 | 9.066 | -58.7% |
| Quote / ASCII | 810.9 | 468.7 | -42.2% |
| Unquote / escaped | 6044 | 7153 | +18.3% |
| HTML escape / escaped | 30636 | 11841 | -61.3% |

All 34 workloads have the same median B/op and allocs/op in both versions.
The [complete CSV](arm64-native-go-bench.csv) includes every workload and
allocation result. The local review artifacts are under `output/arm-native/`
(ignored by Git), including raw samples, command logs, compiler/environment
metadata, source hashes, and binary hashes. Prepared binaries and the archived
baseline are under `/tmp/sonic-arm-framework/comparison-final/` for this run.

## Tango compiler comparison

This section preserves the initial migration's Tango results. The subsequent
[three-way comparison](#string-prefix-and-whitespace-optimization) measures
the updated implementation against these same initial binaries and the
generated baseline.

The same baseline and candidate were also built with the
[requested Tango distribution](https://luban-source.byted.org/repository/scm/aarch64/bytedance.lang.tango_feature_1.0.0.1153.tar.gz).
The compiler reports `go version go1.26.4 linux/arm64`; its distribution version
is `Tango bversion go1.26.4.bm.1.0.1153_test linux/arm64`. The downloaded archive
SHA-256 is `47f628192a2bcf626433463d21f8ff31195fc921195f18ab96c6ad30e7c613ce`.

This run uses compiler defaults. No `-beast` option or PGO profile was supplied.
Its cache is separate from the standard-Go run. Host, CPU affinity, benchmark
fixtures, runtime settings, sample count, and measurement duration are the
same. Tango passed the full default/optimized ARM validation script, including
the issue and generic suites, before timing began.

The primary comparison is between the two backends built by the same compiler.
Comparing the standard-Go and Tango tables also changes the upstream Go version
from 1.26.1 to 1.26.4, so that comparison does not isolate Tango-specific
compiler changes.

To reproduce after extracting the distribution:

```bash
GOCACHE=/tmp/sonic-tango-cache scripts/bench-arm-native.sh \
  --go /path/to/tango/bin/go \
  --baseline a7193312dec5165ec6686a9793eb308e2e642903 \
  --output /tmp/sonic-arm-tango-comparison --cpu 16 --build-only

# Keep compilation and tests idle during measurement.
GOCACHE=/tmp/sonic-tango-cache scripts/bench-arm-native.sh \
  --go /path/to/tango/bin/go \
  --output /tmp/sonic-arm-tango-comparison --cpu 16 \
  --measure-only --count 5 --benchtime 200ms
```

Tango retains the mixed outcome: generic and typed decoding are 28% and 51%
slower than the generated backend, while typed encoding is 3.6% faster and
generic encoding changes by +0.7%. The compiler defaults do not remove the
remaining decoder regression. Values below are median ns/op under Tango for
both backends.

| Workload | Generated baseline | Go/ABI0 candidate | Change |
| --- | ---: | ---: | ---: |
| Generic decode | 49508 | 63497 | +28.3% |
| Typed decode | 26064 | 39297 | +50.8% |
| Generic encode | 57438 | 57824 | +0.7% |
| Typed encode | 14406 | 13888 | -3.6% |
| AST GetOne | 3337 | 4750 | +42.3% |
| AST GetFull | 10766 | 9385 | -12.8% |
| AST ParseSeven | 14288 | 18917 | +32.4% |
| AST SetOne | 7013 | 8292 | +18.2% |
| Mixed numbers | 24987 | 40905 | +63.7% |
| Long decimals | 227255 | 61330 | -73.0% |
| UTF-8 / ASCII | 1813 | 301.3 | -83.4% |
| UTF-8 / CJK | 2000 | 2139 | +6.9% |
| UTF-8 / Emoji | 2898 | 1499 | -48.3% |
| UTF-8 / Mixed | 3189 | 3159 | -0.9% |
| Quote / ASCII | 811.7 | 468.6 | -42.3% |
| Unquote / escaped | 6062 | 7125 | +17.5% |
| HTML escape / escaped | 30667 | 12105 | -60.5% |

All 34 Tango workloads have the same median B/op and allocs/op between the two
backends. The [complete Tango CSV](arm64-native-go-bench-tango.csv) preserves
all samples' medians separately from the standard-Go results. Local logs,
validation output, metadata, hashes, and compiler provenance are in
`output/arm-native-tango/`; prepared binaries and the archived baseline are in
`/tmp/sonic-arm-framework/comparison-tango/`.

## String-prefix and whitespace optimization

The follow-up changes two measured costs while retaining the shared parser
and its existing function boundaries:

- When string decoding already knows the next event's offset and the source
  and destination pointers are equal, it advances the ordinary prefix in one
  step. It keeps the copying path for distinct or compacted output buffers.
- ARM64 whitespace classification uses a shared table classifier and static
  vector constants. `NonSpaceMask`'s 64-byte refill loads and classifies four
  vectors without the previous per-vector loop. The whitespace-run skipper
  retains 16-byte steps so it can stop early on short gaps; targeted gap-length
  measurements selected this over scanning 64 bytes first. Short tails remain
  bounded and skip vector-constant initialization when fewer than 16 bytes are
  available.

The comparison below remeasures three versions with the same Tango compiler,
CPU 16, fixtures, runtime settings, and five 200 ms samples. **Baseline** is the
original generated backend; **initial** is the migration measured above;
**optimized** includes the two follow-up changes. The baseline and initial
binaries are preserved from the prior Tango run and verified by SHA-256.
Variant order rotates between samples. These are new measurements of all
three versions, rather than comparisons against timings from a different run.

The historical CSV files above remain unchanged. The
[three-way CSV](arm64-native-go-bench-optimized.csv) reports both the change
from the initial framework and the remaining difference from the generated
baseline. Raw samples, binaries, commands, and metadata are retained locally
under `output/arm-native-optimized/`; its `compare.py` prepares and measures
this three-way comparison.

Validation for the follow-up passed the full ARM test script with Tango,
including default/optimized main-module tests and issue/generic suites.
Go 1.20.14 kernel, native, number, optdec, and encoder-algorithm tests also
passed. Darwin ARM64 kernel/decoder/AST test binaries linked successfully.
The corresponding logs are saved as `optimized-tests-tango.log`,
`optimized-tests-go120.log`, and `optimized-build-darwin.log` in the review
artifact directory. The previously documented host limitation on race linking
still applies.

The two changes improve generic decoding by 4.2% and typed decoding by 7.7%
relative to the initial framework, recovering 2.659 us and 3.032 us per
operation. They remain 22.8% and 39.2% slower than the generated backend.
The parser's function boundaries, state loads/stores, and ABI0 calls remain;
this optimization does not address all of the costs identified by the
[decoder investigation](arm64-decode-regression.md).

AST results are essentially unchanged in this run. The synthetic Compact and
Pretty whitespace workloads regress by 5.1% and 3.8% relative to the initial
framework; WideIndent improves by 0.9%. The Compact input contains no JSON
whitespace, so its regression cannot be assigned directly to the whitespace
classifier. The results below preserve these tradeoffs instead of claiming
an improvement across all inputs. Values are median ns/op.

An additional isolated experiment removed equality checks made redundant in
the copying loop by the new prefix branch. Compact and Pretty changed by only
-0.12% and -0.04%, so that cleanup was not adopted; evidence is in
`output/arm-native-optimized/prefix-copy-cleanup/results.json`. A subsequent
[current-backend investigation](arm64-decode-regression.md#current-optimized-backend-remaining-regression)
localizes the main decoder gap with stage timings and hardware counters, and
uses four-way experiments to isolate the short-prefix regression. Its
experimental changes are not included in the production results below.

### End-to-end comparison

| Workload | Generated | Initial | Optimized | vs. initial | vs. generated |
| --- | ---: | ---: | ---: | ---: | ---: |
| Generic decode | 49447 | 63361 | 60702 | -4.2% | +22.8% |
| Typed decode | 26123 | 39399 | 36367 | -7.7% | +39.2% |
| Generic encode | 57843 | 55569 | 56786 | +2.2% | -1.8% |
| Typed encode | 14336 | 13883 | 13839 | -0.3% | -3.5% |
| AST GetOne | 3345 | 4746 | 4756 | +0.2% | +42.2% |
| AST GetFull | 10769 | 9373 | 9369 | -0.0% | -13.0% |
| AST ParseSeven | 14290 | 18903 | 18874 | -0.2% | +32.1% |
| AST SetOne | 7013 | 8288 | 8293 | +0.1% | +18.3% |

### Selected focused comparison

| Workload | Generated | Initial | Optimized | vs. initial | vs. generated |
| --- | ---: | ---: | ---: | ---: | ---: |
| Whitespace / Compact | 27936 | 35887 | 37703 | +5.1% | +35.0% |
| Whitespace / Pretty | 36184 | 46880 | 48681 | +3.8% | +34.5% |
| Whitespace / WideIndent | 59989 | 67863 | 67274 | -0.9% | +12.1% |
| Container / strict | 36324 | 30927 | 30786 | -0.5% | -15.2% |
| Escaped string / strict | 1114 | 1565 | 1567 | +0.1% | +40.7% |
| Mixed numbers | 24991 | 40899 | 40665 | -0.6% | +62.7% |
| Short integers | 11916 | 17757 | 17734 | -0.1% | +48.8% |
| UTF-8 / ASCII | 2095 | 301.2 | 307.1 | +2.0% | -85.3% |
| Unquote / escaped | 6055 | 7136 | 7116 | -0.3% | +17.5% |

All 34 workloads retain identical median B/op and allocs/op across the three
versions. The complete three-way CSV also includes the unchanged and regressed
encoder, UTF-8, numeric, AST, and native-operation cases.

A separate fixed-64-byte `NonSpaceMask` microbenchmark improves from 10.680 ns
to 6.690 ns (-37.4%) using five alternating 200 ms samples on CPU 16. Both of
those kernel test binaries use `CGO_ENABLED=1`; all three versions in the main
comparison use `CGO_ENABLED=0`. This isolated classifier result is kept separate
from the 34 public/native API workloads and does not imply an equivalent
end-to-end improvement. Its binary hashes, source-scope note, raw samples, and
compiler information are in `nonspace-mask-result.json` and the `*nonspace*`
logs under `output/arm-native-optimized/`.

## Go parser orchestration optimization

This follow-up changes Go code while preserving the assembly implementations.
The parser now handles token transitions in one loop, keeping the input cursor,
node buffer, frame stack, options, and statistics in local variables. Successful
and failed exits share one commit path, which publishes the complete or partial
node tape, consumed position, open-container state, and reusable frame storage.
Node writes happen directly in the loop; allocation and growth remain separate
cold paths. Frame entries retain node indices so buffer growth cannot invalidate
references to parent containers.

The no-whitespace path and cached whitespace-mask hits stay inside the loop.
Only a cache miss calls the bounded refill helper. Literal success paths compare
four bytes for `null` and `true`, or the four bytes after the initial `f` in
`false`; truncated and mismatched literals use a cold helper that preserves the
exact error position. After parsing an object key, the parser checks its colon
directly, including intervening whitespace, without another general state
dispatch. Whitespace classification continues to read immutable source bytes
while string unescaping modifies the separate decoded buffer.

A Go string fast path checks at most two bounded eight-byte windows. When it
finds a closing quote before an escape or raw control byte, it returns the plain
string's end without entering assembly. It makes no stores. Incomplete windows,
escapes, controls, and strings extending beyond those windows fall back to the
existing full string decoder, preserving its validation and error behavior.

The comparison for this change retains the generated backend and the previous
string-prefix/whitespace-optimized framework as separate references. Those
binaries and their source manifests are preserved; the historical CSV files
above are unchanged. All three versions use the same Tango compiler defaults,
ARM64 target, fixtures, CPU 16, runtime settings, and five rotating 200 ms
samples. Assembly source hashes must match the pre-change framework before the
new comparison can run.

In addition to the same 34 end-to-end and native-operation cases, the comparison
includes 12 isolated stages: Twitter and compact input, typed and generic
binding, and prepare-and-parse, resident parse batches, and object binding from
an already parsed tape. These stages measure different conditions and must not
be summed to reconstruct end-to-end time. The local reproduction harness is
`output/arm-native-go-optimized/compare.py`: run `--prepare` after source
stabilization, then `--measure` when other builds, tests, and measurements have
finished. Its artifacts retain commands, binary and source hashes, source
archives, smoke checks, and individual samples.

The added contract tests check partial tape pointers and statistics after
errors, exact cursor and container state, growth across nested objects and
arrays, escaped strings, and parser reuse across failure, success, and option
changes. A separate deterministic differential fixture compares complete parser
exit state for 1,236 valid, mutated, truncated, and deeply nested inputs under
three option sets and both binding modes: 7,416 records in total. The fixture
and runner are retained with the benchmark artifacts. The final combined
implementation matches all 7,416 pre-change records byte-for-byte and passes
the focused parser contract tests.

Final validation passed the complete Tango ARM script in default and optimized
modes, including the issue and generic suites. Go 1.20.14 tests passed for the
kernel, native, number, optdec, and encoder-algorithm packages. Darwin ARM64
kernel, decoder, and AST test binaries linked, and all five Tango RISC-V build
selection profiles passed. Logs are retained as `validation-*.log` in
`output/arm-native-go-optimized/`. The previously documented host limitation on
race linking still applies.

### Final three-way results

The [complete CSV](arm64-native-go-bench-go-optimized.csv) contains all 34
original cases and 12 stage cases, including medians, sample ranges, bytes,
and allocations. **Generated** is the original generated backend; **before**
is the framework after the string-prefix and whitespace assembly changes;
**Go optimized** adds the Go changes described above. These are fresh
measurements of all three versions, not percentages calculated from older runs.

Typed decoding falls from 36.268 us to 29.388 us (-19.0%), and generic decoding
from 60.074 us to 53.198 us (-11.4%). Both recover about 6.88 us per operation.
They remain 13.1% and 8.9% slower than the generated backend. This recovers
about 67% of the typed decoder's previous excess time and 61% of the generic
decoder's excess time in this comparison.

| Workload | Generated, ns | Before, ns | Go optimized, ns | vs. before | vs. generated |
| --- | ---: | ---: | ---: | ---: | ---: |
| Generic decode | 48872 | 60074 | 53198 | -11.4% | +8.9% |
| Typed decode | 25992 | 36268 | 29388 | -19.0% | +13.1% |
| Generic encode | 57644 | 54990 | 54683 | -0.6% | -5.1% |
| Typed encode | 14181 | 13707 | 13604 | -0.8% | -4.1% |
| AST GetOne | 3340 | 4754 | 4753 | -0.0% | +42.3% |
| AST GetFull | 10765 | 9379 | 9364 | -0.2% | -13.0% |
| AST ParseSeven | 14173 | 18776 | 18728 | -0.3% | +32.1% |
| AST SetOne | 7015 | 8269 | 8265 | -0.0% | +17.8% |

The repeated-record Compact, Pretty, and WideIndent cases improve by 19.9%,
26.0%, and 19.3% relative to the previous framework. Pretty and WideIndent are
now faster than the generated backend in this run; Compact remains 7.9% slower.
The optimization targets optdec, so the AST and independent kernel cases retain
their previous tradeoffs. Short-integer decoding improves by 9.0%, but mixed
numbers, short integers, escaped-string scanning, and escaped unquoting still
have material gaps from the generated backend.

| Workload | Generated, ns | Before, ns | Go optimized, ns | vs. before | vs. generated |
| --- | ---: | ---: | ---: | ---: | ---: |
| Whitespace / Compact | 27995 | 37719 | 30206 | -19.9% | +7.9% |
| Whitespace / Pretty | 36374 | 48493 | 35873 | -26.0% | -1.4% |
| Whitespace / WideIndent | 60741 | 67238 | 54249 | -19.3% | -10.7% |
| Mixed numbers | 24867 | 40479 | 39916 | -1.4% | +60.5% |
| Short integers | 11875 | 17711 | 16114 | -9.0% | +35.7% |
| Escaped string / strict | 1114 | 1566 | 1566 | +0.0% | +40.6% |
| UTF-8 / CJK | 2000 | 2138 | 2140 | +0.1% | +7.0% |
| Unquote / escaped | 6058 | 7129 | 7103 | -0.4% | +17.2% |

### Isolated stage results

Twitter typed prepare-and-parse falls from 19.746 us to 12.924 us (-34.5%),
while typed object binding from an already parsed tape stays at approximately
16.02 us. The parser's 6.822 us reduction is consistent with the end-to-end
typed decoder's 6.880 us improvement. The parse stage still costs 3.698 us
more than the generated parser. Generic object binding is 1.6–1.7% slower than
the previous framework in these stage measurements, although it is within
0.4% of the generated backend. The stage results therefore do not establish an
improvement in every phase.

| Stage | Generated, ns | Before, ns | Go optimized, ns | vs. before | vs. generated |
| --- | ---: | ---: | ---: | ---: | ---: |
| Twitter / typed prepare + parse | 9226 | 19746 | 12924 | -34.5% | +40.1% |
| Twitter / generic prepare + parse | 9219 | 19695 | 12958 | -34.2% | +40.6% |
| Compact / typed prepare + parse | 6432 | 13956 | 9403 | -32.6% | +46.2% |
| Twitter / typed FromDom | 16003 | 16016 | 16018 | +0.0% | +0.1% |
| Twitter / generic FromDom | 39755 | 39286 | 39914 | +1.6% | +0.4% |
| Compact / generic FromDom | 39887 | 39290 | 39950 | +1.7% | +0.2% |

All 46 measured cases retain identical median B/op and allocs/op across the
three versions. The complete CSV preserves the remaining AST, numeric,
UTF-8, and native-kernel regressions. Raw samples and final verification logs
are retained in `output/arm-native-go-optimized/`; `differential-metadata.json`
records the lossless comparison, whose source and decoded payloads use byte
slices so invalid UTF-8 is preserved exactly.

### Hardware-counter cross-check

Three alternating pairs of 100,000-iteration runs on the same CPU and immutable
binaries support the measured parser improvement. For Twitter typed
prepare-and-parse, normalized instructions fall from 221,175 to 177,540 (-19.7%),
cycles from 61,176 to 40,220 (-34.3%), and branch misses from 306.23 to 124.57
(-59.3%); aggregate IPC rises from 3.62 to 4.41. Whole typed decoding reduces
instructions by 11.4%, cycles by 19.7%, and branch misses by 36.8%. These are
user-mode whole-process totals divided by 100,000: startup, fixture setup, and
benchmark warmup/calibration remain included. They are not counters isolated
around one parser invocation. The total-branches event was unsupported, so
branch-miss counts are reported without a branch-miss rate.

Source-path replay combined with disassembly estimates that the parser's known
call boundaries fall from 4,471 to 1,305 per Twitter document. This is an
estimate, not a runtime-instrumented count; the final short-string helper still
has a real Go call. Together with the stage timings, the evidence supports less
parser work and fewer branch misses. It does not assign the remaining gap to
the compiler or establish a limit on further Go optimization. Raw counters,
commands, binary hashes, method notes, and the verification that all 60 assembly
and header files are unchanged are retained in
`output/arm-native-analysis/go-final/`, including `perf-summary.json` and
`final-conclusions.json`.

### Attribution of the residual gap

The [final-parser attribution](arm64-decode-regression.md#final-go-parser-residual-attribution)
compares the exact generated and final binaries with fresh counters and
controlled temporary variants. Twitter parsing executes 2.18 times as many
instructions and takes 1.40 times as many cycles. Manually inlining the
unchanged short-string check recovers about 0.8 us; bypassing the whitespace
Go wrapper recovers about 0.25 us; disabling optdec bounds checks in an unsafe
diagnostic recovers about 0.29 us. These are separate experiments and their
gains must not be added. Reusing the already scanned string prefix has no
material Twitter parser benefit, and a safe node-store BCE rewrite is neutral.

State representation, remaining helper boundaries, and numeric lexical
rescanning also differ from the generated parser. Their individual costs are
not fully separated, so the results do not establish a compiler performance
floor. At that point all attribution variants remained diagnostic snapshots.
The further source optimization below applies selected findings and has its
own benchmark CSV; the preceding CSV remains historical evidence.

An [exact dynamic instruction trace](arm64-decode-regression.md#exact-dynamic-instruction-accounting)
subsequently accounts for the instruction increase on the Twitter fixture:
78,780 generated versus 174,775 final parser instructions, a difference of
95,995. Extra stack loads/stores account for 39.0% of that increase and extra
comparisons/branches for 38.3%; call/return instructions themselves account for
2.7%. The main Go parser executes 28,474 stack accesses, including 11,880
stores in two frequently visited scan/dispatch sequences. These are instruction
counts, not latency attribution or proof that every operation can be removed.

The subsequent [source-versus-compiler comparison](arm64-decode-regression.md#go-source-structure-versus-compiler-behavior)
identifies the twelve stores as compiler-inserted preservation of loop-carried
values. One source fix makes numeric kind handling exhaustive and removes an
unnecessary cross-token dependency, improving Twitter parsing about 1% in an
isolated experiment. A separate direct-continuation rewrite removes branches
but increases stack traffic and regresses parsing by 8%. Source topology and
compiler spill placement therefore need to be evaluated together; neither a
blanket compiler attribution nor a mechanical switch-to-goto rewrite is
supported. The later source optimization adopts the exhaustive kind switch;
the regressing direct-continuation experiment remains unadopted.

## Further Go source optimization

This follow-up applies selected findings from the [source and compiler investigation](arm64-decode-regression.md#go-source-structure-versus-compiler-behavior). It changes Go code while retaining the native assembly implementation and the same Tango compiler defaults.

- Numeric kind handling now has an explicit impossible-case panic. Successful conversions select a fresh kind, removing the unnecessary dependency on the previous token. Malformed JSON continues through the existing error paths.
- The plain-string check is part of the parser and examines one eight-byte word, followed by an optional second word. The shared `StringSpecialMask` helper inlines; the former standalone scanner is removed. The two windows remain bounded, and escapes, controls, and short tails use the existing decoder from the original opening quote. This form also lets the generated ARM64 code reuse mask constants across the windows.
- `NonSpaceMask` accepts the already-sliced input suffix. Its result remains relative to that suffix and bounded to at most 64 bytes, including an empty suffix. The parser retains the original cache base separately.
- The parser's local data and node views have capacity equal to length, reducing independent live slice metadata. `p.nodes` retains the owning descriptor and its original capacity; only cold growth replaces it. Frame state records the continuation after a value, while temporary comma/colon scanning states remain local. Container representation and full-width counts are retained.

Common decimal suffixes now continue the already accumulated integer prefix.
A conservative exact-mantissa path performs one multiplication or division by
an exactly represented power of ten; negative zero is retained. Other inputs
restart the original scanner and `strconv` conversion at the original token
start. The C parser already accumulates the significand and exponent before
its fast conversion (`native/parse_with_padding.c:714–886`); this change closes
part of that source-level algorithmic difference. It does not replace the full
C fast-float algorithm. A separate helper contains both success and fallback,
keeping that orchestration out of the integer loop.

### Final comparison and verification

The [complete final CSV](arm64-native-go-bench-source-optimized.csv) preserves the generated backend, the previous Go parser, and this combined source change as separate references. The comparison uses the same 34 original workloads and 12 parser/binding stages, identical fixtures, Tango defaults, CPU 16, runtime settings, and five rotating 200 ms samples. Historical CSV files above remain unchanged.

| Workload | Generated, ns | Previous Go, ns | Source optimized, ns | vs. previous | vs. generated |
| --- | ---: | ---: | ---: | ---: | ---: |
| Typed decode | 25,940 | 29,488 | 27,999 | -5.05% | +7.94% |
| Generic decode | 48,878 | 53,387 | 51,625 | -3.30% | +5.62% |
| Twitter / typed prepare + parse | 9,227 | 12,928 | 11,274 | -12.79% | +22.18% |
| Compact / typed prepare + parse | 6,419 | 9,417 | 7,837 | -16.78% | +22.09% |
| Mixed numbers | 24,962 | 40,048 | 25,491 | -36.35% | +2.12% |
| Short integers | 11,943 | 16,097 | 15,869 | -1.42% | +32.87% |
| Long decimals | 227,915 | 61,745 | 64,595 | +4.62% | -71.66% |
| Whitespace / compact | 27,961 | 30,211 | 28,218 | -6.60% | +0.92% |
| Whitespace / pretty | 35,672 | 35,931 | 33,290 | -7.35% | -6.68% |
| Whitespace / wide indent | 60,447 | 54,211 | 50,925 | -6.06% | -15.75% |
| Typed encode | 14,198 | 13,686 | 13,862 | +1.29% | -2.37% |
| Generic encode | 57,666 | 54,969 | 57,328 | +4.29% | -0.59% |

All 46 cases retain the previous Go backend's bytes and allocations per
operation. Long decimals regress 4.62% (2.850 us/op): the bounded conversion
attempt fails before the existing general conversion. They remain 71.66%
faster than the generated backend. This is an accepted source tradeoff, not a
compiler issue. Short integers still trail the generated backend by 32.87%;
AST GetOne, ParseSeven, and SetOne also retain substantial earlier gaps.

The complete run also reports +4.29% generic encoding, +1.29% typed encoding,
and +1.0–1.5% for the FromDom controls. A targeted five-pair 500 ms recheck of
the same binaries reports generic encoding 55.462 to
57.334 us (+3.38%), typed encoding
+0.44%, and typed/generic FromDom +1.28%/+1.45%.
The original 46-case CSV is retained without replacing these results.
For the encoder executables, the entire `.text` section is byte-identical at
the same address, and the new numeric parsing helpers are not linked. Thus
the encoder difference is not additional parser work or changed encoder
machine-code placement. Two fixed-100,000-iteration PMU pairs show roughly
-0.15% instructions and +0.24% cycles for generic encoding; these include
process overhead. The timed runs still show a difference, so its runtime
cause is unresolved rather than dismissed or assigned to a compiler bug.
The FromDom control difference likewise remains recorded. Raw rechecks and
static binary comparisons are in `followup/` and `followup-encoder-notes.md`.

The final source passes the ARM-relevant main-module tests and issue/generic
suites in default and optimized/VM modes. All 7,416 lossless parser exit-state
records match the earlier reference byte for byte, including malformed input,
partial nodes, decoded bytes, error positions, and option combinations. New
focused tests cover bounded string loads/guard pages, resident node capacity
and backup restoration, error reuse, float bits and negative zero, exponent
and mantissa boundaries, and long decimal fallback.

Go 1.20.14 and stock Go 1.26.1 pass the five affected implementation packages
in both modes. Tango links Darwin ARM64 kernels/decoder/AST test binaries and
public packages for five RISC-V profiles; the RISC-V selection check also
passes. These are cross-compilation/link checks, not target execution. The
previously documented host race-linking limitation remains; no passing race
result is claimed. All 60 assembly/header files match the pre-change snapshot.

Larger rewrites were screened and not selected: packed numeric results,
container parent links in nodes, complete frame-state removal, and duplicated
C-style continuation blocks. Their results show that copying C control flow
or data layouts mechanically is not sufficient. The selected changes were
measured together; isolated gains are not additive.

Commands, raw samples, source and binary hashes, differential output, and validation logs are retained in `output/arm-native-go-source-optimized/`. Its `compare.py` prepares an immutable snapshot and measures only after other builds and tests are idle.

### Instructions after the combined change

One warmed Twitter parse in the unchanged final benchmark binary executes
143,548 original instructions versus 174,775 previously and 78,780 in the
generated parser. This is a 17.87% reduction, removing 32.53% of the previous
95,995-instruction gap. The exact GDB basic-block method, fixture, warmup
position and controls match the earlier investigation; binary hashes are
checked against all three benchmark executables. Debugger time is not used
as benchmark time.

| Instruction class | Generated | Previous Go | Current Go | Removed this round |
| --- | ---: | ---: | ---: | ---: |
| Stack loads/stores | 101 | 37,568 | 24,486 | 13,082 |
| Comparisons/branches | 19,736 | 56,487 | 51,084 | 5,403 |
| Moves/constants | 13,032 | 22,747 | 14,754 | 7,993 |
| Arithmetic/other (including SIMD) | 33,820 | 46,151 | 43,744 | 2,407 |
| Calls/returns | 6 | 2,635 | 1,477 | 1,158 |
| Other loads/stores | 12,085 | 9,187 | 8,003 | 1,184 |

Of the remaining 64,768 extra instructions, comparisons/branches contribute
31,348 and stack accesses contribute 24,385. These categories are not latency
shares; stack accesses include more than spills. The current parser still has
different bounded scanning, state transitions, helper contracts and fallback
algorithms from the C parser. The residual gap is not attributed entirely to
the compiler.

The shared string-decode and whitespace assembly leaves execute exactly the
same 13,165 and 7,611 instructions as before on this fixture. The removed short
string helper is now part of the parser, so its vanished function count must
not be treated as work eliminated in full.

Separate whole-process PMU measurements (100,000 fixed iterations, three
alternating pairs) corroborate the change: prepare+parse instructions fall
17.59% and cycles 12.61%; complete typed-decode instructions fall 8.84% and
cycles 4.87%. These counters include amortized process/fixture overhead and
are not region-scoped per-call counts.

Evidence is retained in `instruction-attribution/` and `pmu/` under the new
artifact directory. The source archive, per-file manifests, `this-round.diff`,
binary hashes and unchanged-assembly checks identify this exact result.

### Compiler work deferred

This change neither patches Tango nor changes its optimization flags. The investigation identifies three future compiler questions:

- **Spill placement (`ssa/regalloc.go`, `placeSpills`):** register allocation places preservation stores at common predecessor blocks, so punctuation paths can execute stores needed by other paths. Path-specific placement or splitting may help, but the legality and benefit of moving each store remain unproven.
- **Inlining and call contracts (`inline/inl.go`):** the previous short-string loop exceeded the ordinary inline budget; restructuring a smaller shared helper makes its logic inline. This does not show that globally raising budgets, or assuming every numeric enum switch is exhaustive, is a sound compiler policy. Assembly declarations still require a call.
- **Constant materialization (`ssa/regalloc.go`, rematerialization):** the loop candidate repeatedly constructed an ARM64 mask constant; the selected optional-second-window form reuses it. A target-aware choice between reconstruction and register retention may help, but the measured source change also changes branches, bounds-check opportunities, and layout. Its gain is not an isolated measurement of constant-loading cost.

The relevant passes are architecture independent, while the disassembly and performance evidence here are ARM64-specific. These observations establish mechanisms and opportunities, not a compiler performance ceiling or a measured benefit on RISC-V. The retained `compiler-notes.md` records source references and uncertainty; its earlier SSA value IDs and store counts describe their original snapshots, not the final combined parser.

## Parser-loop live-state reduction

The next source-only experiment targets the variables preserved across parser
calls. The selected change keeps the node slice descriptor in `p.nodes`,
loading it at node accesses instead of carrying its pointer and length through
the loop. Cold growth continues to update the same owner and preserve its
original backup and capacity. The whitespace cache now carries only its base
and mask. Its width is reconstructed on exhaustion as
`min(64, len(src)-base)`; initial state and scalar-only refills use an invalid
base of -64. The bounded assembly mask contract and logical EOF are retained.
Only `parse_exec_go.go` and `space_cache_native_test.go` change in this round.
Neither assembly, compiler flags, numeric conversion nor binding algorithms
change.

Four isolated candidates were screened before combining the two useful source
changes. All candidates passed focused tests in default/optimized modes and
7,416 lossless parser-state comparisons. The following percentages are from
the first matched screen, not the later final comparison or additive gains:

| Isolated candidate | Twitter parse time | Parser PMU instructions | Decision |
| --- | ---: | ---: | --- |
| Remove cached whitespace width | -0.29% | -1.15% | Evaluate in combination; generic decode +1.01% and compact-whitespace +1.82% in isolation |
| Access resident `p.nodes` descriptor | -1.28% | -1.03% | Evaluate in combination; complete decode did not consistently improve alone |
| Keep scalar continuation in a local variable | +2.15% | +0.60% | Reject |
| Reuse the already-read token byte | +2.72% | +0.96% | Reject |

The local-continuation candidate removes frame-field accesses but leaves all
nine common-entry spills. It also adds local-state preservation across string
and numeric calls. This is a concrete example of fewer source-level reads and
writes producing more total instructions. Static sites and frame sizes are
recorded separately from dynamic PMU counts; a smaller frame alone is not a
performance result.

### Final matched timing

The [final CSV](arm64-native-go-bench-control-optimized.csv) contains 16 affected
workloads/stages, including unchanged FromDom controls, for the generated C
backend, the preceding Go source, and the selected combined change. All use
Tango 1.0.1153 defaults, CPU 16, `GOMAXPROCS=1`, `SONIC_NO_ASYNC_GC=1`, identical
fixtures, and five rotating 200 ms samples. No builds, tests or debugger traces
run during timing. Bytes and allocations per operation are unchanged in all
16 cases.

| Workload | Generated, ns | Previous Go, ns | Current Go, ns | vs. previous | vs. generated |
| --- | ---: | ---: | ---: | ---: | ---: |
| Typed decode | 26,104 | 28,099 | 27,558 | -1.93% | +5.57% |
| Generic decode | 49,741 | 52,390 | 51,285 | -2.11% | +3.10% |
| Twitter typed prepare + parse | 9,221 | 11,285 | 11,029 | -2.27% | +19.61% |
| Compact typed prepare + parse | 6,433 | 7,870 | 7,650 | -2.80% | +18.92% |
| Mixed numbers | 24,908 | 25,605 | 25,131 | -1.85% | +0.90% |
| Short integers | 11,926 | 15,850 | 15,563 | -1.81% | +30.50% |
| Long decimals | 227,045 | 64,011 | 63,853 | -0.25% | -71.88% |

The earlier combined-candidate round measured typed decode at -1.08% and generic
decode at -0.28%; the final round reports -1.93% and -2.11%. The unchanged
FromDom controls also vary: +0.6–0.8% in the combined screen and -0.5–1.4% in the
final run. Consequently, the entire end-to-end timing change cannot be assigned
to this source rewrite. The parser improvement is about 2% on Twitter, with
PMU and exact instruction counts providing independent support. Both rounds
are preserved; the more favorable final medians do not replace the screen.

### Exact instruction evidence

Fresh exact traces of the final before/after benchmark binaries use the same
warmed 13,035-byte Twitter input and tracing method as the earlier analyses.
Both execute 32,331 basic blocks. Their full binary hashes match the timing
executables; the generated reference is the unchanged earlier binary.

| Instruction category | Previous Go | Current Go | Change |
| --- | ---: | ---: | ---: |
| Stack loads/stores | 24,486 | 20,285 | -4,201 |
| Other loads/stores | 8,003 | 9,358 | +1,355 |
| Comparisons/branches | 51,084 | 51,120 | +36 |
| Moves/constants | 14,754 | 14,271 | -483 |
| Arithmetic/other | 43,744 | 43,852 | +108 |
| Calls/returns | 1,477 | 1,477 | 0 |
| **Total** | **143,548** | **140,363** | **-3,185 (-2.22%)** |

The nine common-entry stores become six, removing the `nodes.ptr`, `nodes.len`,
and `spaceWidth` spill values. Each site executes 990 times: those stores fall
from 8,910 to 5,940. The ownership change deliberately exchanges some stack
traffic for explicit node-descriptor memory reads; the table counts both.
All string, numeric and assembly helper execution counts remain unchanged;
`parserSpaceRefill` itself drops from 14,747 to 14,351 instructions. This is a
measured source-level reduction in state preservation, not a compiler patch.

The combined screen's separate whole-process PMU totals, normalized by 100,000
iterations over three rotating repetitions, show -2.18% instructions and
-2.01% cycles. Those counters include amortized setup; the exact trace above
is the per-parser instruction count. The current generated-C gap remains
61,583 instructions (140,363 versus 78,780). This round removes only 4.92% of
the preceding instruction gap, and does not eliminate the major control-flow
and preservation differences or establish a limit on further Go changes.

### Validation and retained evidence

The final source passes the ARM-relevant main-module and issue/generic suites
in both default and optimized/VM modes, plus the 7,416-record lossless state
comparison. Go 1.20.14 and stock Go 1.26.1 pass the affected implementation
packages in both modes. Tango links Darwin ARM64 kernels/decoder/AST and public
packages for five RISC-V profiles; selection checks also pass. Cross-target
binaries are not executed. The existing host race-linking limitation remains.
All 60 assembly/header hashes are unchanged.

Artifacts are under `output/arm-native-go-control-optimized/`: isolated patches
and binaries, `timing-summary.json`, `pmu/`, `combination/`, `combination-pmu/`,
the final source archive/manifests and CSV, `instruction-attribution/`, and
validation logs. `ssa-baseline/README.md` maps the preceding nine stores to SSA
values and all reloads. Its dominator analysis confirms that the hot whitespace,
string and numeric paths already require the shared spill blocks under the
current one-spill-per-value allocation policy; cold growth alone does not
explain their placement. This generic compiler issue remains recorded and
unmodified.

### Where the current instruction gap remains

Reclassifying the retained final traces, rather than reusing an earlier Go
snapshot, gives the following accounting for the same warmed 13,035-byte
Twitter input. Current Go executes 140,363 instructions, compared with 78,780
for the generated C backend: 61,583 additional instructions (+78.17%).

| Instruction category | Generated C | Current Go | Extra | Share of total extra |
| --- | ---: | ---: | ---: | ---: |
| Comparisons and branches | 19,736 | 51,120 | +31,384 | 50.96% |
| Stack loads and stores | 101 | 20,285 | +20,184 | 32.78% |
| Arithmetic and other | 33,820 | 43,852 | +10,032 | 16.29% |
| Moves and constants | 13,032 | 14,271 | +1,239 | 2.01% |
| Other loads and stores | 12,085 | 9,358 | -2,727 | -4.43% |
| Calls and returns | 6 | 1,477 | +1,471 | 2.39% |
| **Total** | **78,780** | **140,363** | **+61,583** | **100%** |

These are disjoint instruction classes, not cycle shares or a partition into
independently removable source defects. Stack accesses use an SP/FP address
base and include arguments, results, local fields and frame management as well
as register spills. C also stores state in parser/tape memory. A pair load or
store counts as one instruction. Arithmetic includes SIMD operations.

**Control flow remains the largest category.** The last source change reduced
stack instructions by 4,201 but increased comparisons/branches by 36. Its
benefit therefore barely affects this largest remaining category. Current
`parseImpl` executes 35,099 comparisons/branches and `parserSpaceRefill` another
6,751. Within the main loop, the state-selection tree executes 5,902 such
instructions; the two `scan`/`ready` EOF-and-index-check sequences execute
5,940. These two sets are disjoint examples of Go work, not their net excess
over C or amounts that can all be deleted. The common gateway runs 990 times.

The Go source still routes known transitions through `scan`, `ready`, and
`switch state`: for example, a parsed colon selects `goObjectValue`, returns
to scanning, then selects that state again before parsing the value. The C
source continues directly through `obj_val`, `obj_cont`, and `obj_key` labels.
Separate Go frames, logical-length-bounded readers, helper result checks, and
retained calls also contribute work. This is broader than state dispatch or
compiler-inserted bounds checks alone. The input contracts differ: C uses
padding, while Go scanners respect logical lengths.

**Preservation across calls remains the second large category.** Main
`parseImpl` accounts for 14,256 stack instructions and helpers for 6,029.
The six remaining common slots preserve the frame pointer/length/capacity,
used-node count, and whitespace base/mask. Their 5,940 stores plus 2,152
reloads total 8,092 instructions. This is only part of stack traffic, not
20,285 instructions all attributable to those six slots. Calls themselves
add just 1,471 instructions; their larger effect includes preserving live
values, passing arguments/results, and losing opportunities to optimize
across an inlined body.

The source choices and compiler effects must be considered together. Shared
dispatch, frame representation and helper boundaries are implementation
choices; inlining, bounds-check elimination and register allocation determine
their generated cost. This does not assign the whole gap to the compiler.
The earlier direct-continuation experiment reduced comparisons/branches but
increased stack traffic and regressed timing, so replacing the switch with
labels alone is not a demonstrated fix for the current source. Its results
remain historical rather than deductions from the table above.

The reproducible category arithmetic and exact PC sets for the two current
hot-path examples are retained in
`output/arm-native-go-control-optimized/instruction-attribution/current-residual-summary.json`.
No implementation, compiler, or assembly changes accompany this attribution.

### Source-to-assembly check of control flow

The following review distinguishes source choices from compiler behavior using
the same final measured binaries. Current C parser/scanner sources match the
frozen generated baseline, and current Go parser source matches the final
benchmark snapshot. Binary hashes and source hashes were checked again. This
is a code-generation investigation, not a new timing result.

**Known continuations are redispatched by the Go source.** After a colon, Go
sets `state = goObjectValue` and returns to `scan`; after an object comma it
sets `goObjectKey` and takes the same route. C instead continues into `obj_val`
or jumps directly to `obj_key`. The actual colon path confirms the difference:

```asm
// Current Go: colon success, these four instructions each execute 307 times.
1dcd24: add x2, x14, #1
1dcd28: mov x1, x15
1dcd2c: mov x14, #5       // goObjectValue
1dcd30: b   1dc7c4        // common scan, then state dispatch
// After scanning, the state tree includes:
1dc890: cmp w14, #4
1dc894: b.hi 1dc940
1dc940: cmp w14, #5
1dc944: b.ne 1dc974

// Generated C: after colon validation and key emission.
17b0a4: ldrb w16, [x10], #1
        // inlined whitespace processing
17b1e0: cmp w16, #0x5a    // classify the token, not parser state
```

Both representative C sites execute 307 times. The C object-comma continuation
at `0x17e570` jumps straight to the key quote check at `0x17ae1c`, 269 times.
Go instead writes ObjectKey and jumps to its shared scan. C still dispatches
by parent type when closing containers; it does not eliminate all state tests.
The specific extra dispatch on already-known transitions is present in the Go
source, rather than being invented by code generation.

The shape of that dispatch also has a compiler component. Tango's
`cmd/compile/internal/walk/switch.go` merges adjacent cases with the same
destination. The eight Go states become six clauses; the generic jump-table
threshold is eight clauses, so `binarySearch` emits the observed comparison
tree. The tree executes 5,902 comparisons/branches over 990 entries. This does
not prove that a jump table would be faster, or that all 5,902 instructions
could be removed. C's token switch also uses comparisons: its advantage here
is avoiding an additional state switch, not universally superior switch
lowering.

**Bounds work includes source checks, necessary guards, and missed proofs.**
At both `scan` and `ready`, the Go source first tests equality with logical EOF
and then indexes the input. The compiler shares comparison flags but retains
the separate index guard:

```asm
1dc7c4: cmp x2, x5
1dc7c8: b.eq eof
1dc7cc: b.cs panicBounds
        // state preservation, then load src[pos]
```

Each instruction executes 990 times; `ready` has the same three-instruction
pattern. Of the combined 5,940 instructions, 3,960 implement the explicit EOF
tests and 1,980 are index-guard branches. Equality alone does not establish a
valid index. In contrast, the existing `parseColon` unsigned range check
compiles to `CMP` / `B.CS badColon` followed by a byte load, without another
index-panic branch. This demonstrates a source-form effect in the same binary,
not a requirement to remove guards or change invalid-position behavior.

The C padding reader explicitly permits unchecked reads into the sentinel
padding. Its hot colon/value reads (`0x17af40`, `0x17b0a4`) are direct byte loads
without logical-EOF tests. This is a different reader contract, not evidence
that a compiler can safely remove every logical bound from the Go version.

A narrower compiler proof miss is visible in the Go short-string fast path:
the condition `len(data)-shortAt >= 16`, together with the preceding valid
position, establishes room for the second word. Nevertheless, the second
slice retains `CMP len, pos+9` / `B.CC panicBounds` at `0x1dcac0`/`0x1dcac4`.
Both execute 294 times, totaling 588 instructions. The first word does not
retain this extra slice check. This is evidence of a bounds-proof limitation;
it is not evidence that the full control-flow difference is compiler-created.

**Whitespace has both a source-form and a lowering difference.** C's
`repeat_2` macro explicitly duplicates the two initial scalar probes. Go's
`for n := 0; n < 2; n++` remains a loop: the iteration `CMP` / `B.GE` executes
635 times, totaling 1,270 comparisons/branches. An isolated default-Tango
compile confirms that explicitly duplicating the Go probes removes this loop
control, while increasing code size. It has not been benchmarked in the full
parser and is not applied to production.

For the same four whitespace characters, C's equality-OR predicate lowers to
a bit-membership mask plus `CMP`/`CCMP`/branch, while the current Go switch
uses comparison/branch pairs. A separate Go diagnostic using exactly the
C-style OR expression changes the order but still produces a comparison chain
and `CSET`, rather than the C form. This confirms a compiler lowering
difference as well as a source-order effect. C pays for mask, shift and
constant instructions, so its fewer branches alone do not establish lower
total cost. The refill's 6,751 comparison/branch instructions are partitioned
by exact PC in the retained evidence.

The confirmed conclusion is specific: redundant redispatch and the two-probe
loop are source structures preserved in machine code; switch lowering and
bounds proofs contribute additional compiler effects; padding versus bounded
reads changes what work is required. There is no measured, exclusive
source-versus-compiler percentage for the entire 31,384-instruction control
gap. The earlier rejected direct-continuation experiment remains a warning
that removing branches can increase live-state preservation and total time.

Evidence is in `output/arm-native-go-control-optimized/control-source-review/`:
`control-logic-sites.json`, `compiler-switch-excerpt.txt`, the bounds inventory,
`space-control-counts.json`, and the isolated whitespace source, compile output
and metadata. Production Go, assembly and compiler implementation are unchanged.

## Parser phases and string call boundary

This follow-up changes Go source only, starting from the preceding
140,363-instruction parser. It keeps common scanning and uses five phases:
value, key, continuation, first array element, and first object key. A container
frame stores its immutable kind instead of a changing continuation state. After
a completed child, the parser enters the shared continuation phase rather than
restoring a saved state. Separate first-entry phases preserve empty-container
and trailing-comma semantics.

The frame slice descriptor now stays in `Parser`; only its active depth is a
local variable. A cold helper grows the backing buffer, and every exit publishes
depth/parent/partial tape before resetting the scratch slice length. Node
indices remain full-width `int`, counts remain `uint32`, and the native ABI
prefix is unchanged. This reduces live slice metadata across kernel calls, but
also introduces descriptor loads; it is not a universally better layout.

String options are converted to a mode once per parse. The fallback passes a
bounded suffix to `kernels.DecodeQuoted`, which returns relative offsets. Its
pointer-and-length signature fits the ordinary inlining budget (cost 70 under
both Tango and stock Go 1.26); the caller directly invokes the existing ABI0
string decoder. Error-offset adjustment happens on the error path. The existing
`ParseString` API remains available to other callers. No forced inlining,
compiler changes, assembly changes, or input padding were introduced.

### Final matched timing

The selected source is frozen at
`/tmp/sonic-arm-framework/go-structure-optimized-final`. Measurements use Tango
1.0.1153 / Go 1.26.4, Neoverse-N2 CPU 16, `GOMAXPROCS=1`, and
`SONIC_NO_ASYNC_GC=1`, with ordinary compiler optimization flags. Five rotating
200 ms samples compare the generated C backend, the preceding Go backend, and
this version. Builds, tests, and debugger tracing finished before timing.
Negative time changes mean faster execution.

| Workload | Generated C, us | Previous Go, us | Current Go, us | Change vs previous Go | Current vs C |
| --- | ---: | ---: | ---: | ---: | ---: |
| Twitter prepare and parse, typed | 9.222 | 11.044 | 10.582 | -4.18% | +14.75% |
| Compact prepare and parse, typed | 6.425 | 7.645 | 7.019 | -8.19% | +9.25% |
| Full typed decode | 26.088 | 27.771 | 27.240 | -1.91% | +4.42% |
| Full generic decode | 49.531 | 51.866 | 51.398 | -0.90% | +3.77% |

Generic prepare-and-parse improves by 4.17% / 8.28% for Twitter / Compact.
FromDom controls move by +0.12% to +0.29% against the previous Go version.
Reported bytes and allocations are unchanged in all 16 comparison cases.
The complete results, including number and whitespace cases, are in
[`arm64-native-go-bench-structure-optimized.csv`](arm64-native-go-bench-structure-optimized.csv).
Use this matched comparison rather than subtracting percentages from earlier
rounds with different control measurements.

The broader five-round candidate screen used the same selected implementation
and compared it with the preceding Go parser. It improved DeepArray128 by
6.54%, LongStrings128 by 11.94%, EscapedStrings128 by 8.75%, and ShortKeys128
by 2.36%. Alternating containers improved 1.10%; flat numbers and whitespace
nesting were approximately unchanged. There is a retained regression:
EmptyObjects256 takes 2,641 ns instead of 2,539 ns (+4.02%). This version is
therefore not a speedup for every input shape. The evidence is in
`output/arm-native-go-structure-optimized/five-phase/timing-summary.json`.

### Exact instruction accounting

Tracing one warmed parse of the same 13,035-byte Twitter document gives
**132,607 instructions**, down from **140,363** (-7,756, or **5.53%**).
The C baseline remains 78,780. Input hashes, successful returns, and binary
hashes were checked; the two older traces were reused only after verifying
their binaries were identical. Debugger time is not used as benchmark time.

| Exclusive instruction category | Previous Go | Current Go | Change |
| --- | ---: | ---: | ---: |
| Compare and branch | 51,120 | 49,320 | -1,800 |
| SP/FP-based loads and stores | 20,285 | 16,683 | -3,602 |
| Call and return | 1,477 | 1,193 | -284 |
| All remaining instructions | 67,481 | 65,411 | -2,070 |
| Total | 140,363 | 132,607 | -7,756 |

The old `ParseString` wrapper's 142 executions accounted for 5,964 instructions
and disappear as a separate function. The main parser falls from 93,010 to
91,218 instructions, a net reduction of 1,792. These function totals partition
the change; they do not independently measure each source edit's causal
benefit, because inlining moves work into the caller. In particular, removing
142 Go calls does not save only 284 call/return instructions: repeated argument,
mode, offset, and stack handling also changes.

The underlying work is unchanged: 142 string-decode calls execute 13,165
instructions, 241 whitespace refills execute 14,351, 155 whitespace-mask calls
execute 7,611, and 56 number parses execute 6,135. Thus this result comes from
Go orchestration, not a changed SIMD kernel. The main frame remains 288 bytes;
dynamic stack traffic falls even though its allocated frame size does not.

Within the main parser, the phase-selection decision tree falls from 5,902 to
3,516 instructions. Other main-parser comparisons and branches increase by
1,722, leaving a net reduction of only 664 in that function's control category.
Immutable container kind removes 335 writes of per-value continuation state.
The common loop-entry spill slots change from frame pointer/length/capacity,
used count, and whitespace base/mask to depth, used count, position, and
whitespace base/mask. Their accesses fall from 8,092 to 6,478; the new position
spill partly offsets removing the local slice descriptor. These exact-PC
measurements explain why simpler source control flow yields a modest total
gain rather than eliminating the previous control and stack differences.
The PC groups and slot mappings are retained in
`instruction-attribution/source-attribution.json` under this round's artifact directory.

The remaining excess over C is **53,827 instructions** (+68.33%): comparison
and branch account for 29,584 (55.0%), SP/FP accesses for 16,582 (30.8%), and
everything else for 7,661 (14.2%). Stack accesses are not all register spills.
This round removes 12.59% of the preceding instruction gap, not most of it.
The bounded-read contract, common dispatch, bounds checks, Go/ABI0 preservation,
and compiler lowering still contribute. There is no exclusive measured
source-versus-compiler percentage for this remainder; the earlier compiler
findings remain deferred, and instruction ratios do not predict cycle ratios.

### Rejected source alternatives and validation

Several simpler-looking source rewrites passed correctness checks but did not
improve the combined workload:

- Direct C-style continuations reduced normalized PMU instructions by 0.51%
  but increased cycles by 0.19%. Keeping counts in unfinished tape nodes
  increased instructions by 1.42% and cycles by 2.35%.
- Inlining scalar whitespace probes removed 86 refill calls, but Twitter
  parsing was 0.62% slower and Compact parsing 3.39% slower.
- Unsigned range guards reduced normalized instructions by 1.38%, but cycles
  increased 2.43% and Twitter parsing was 2.18% slower.
- An adjacent-empty-container probe made the empty-object case about 29%
  faster, but deep nonempty arrays became 9.76% slower in the resident-frame
  variant. A direct first-entry shortcut also increased cycles by 4.31%.

These PMU counts include amortized benchmark setup and are separate from the
exact parser trace above. Full candidate details and same-round comparisons
are retained in `output/arm-native-go-structure-optimized/experiment-notes.txt`.

Validation passed the ARM-relevant main-module and issue/generic suites in
default and optimized/VM modes, affected-package tests with Go 1.20.14 and
stock Go 1.26, and 7,416 byte-identical parser exit-state differential records.
New tests cover the relative string-decoding contract, Unicode/error modes,
empty-container tape/statistics, forced node growth, trailing commas, and
depth-limit boundaries. Darwin ARM64 and five RISC-V profiles compile and link;
these cross-target binaries were not executed. The existing race-toolchain
limitation documented above is unchanged; this round makes no new race-pass
claim.

All 42 assembly files in the before/after source manifests are unchanged.
`output/arm-native-go-structure-optimized/` contains the final source archive,
`this-round.diff`, binary/source hashes, raw timing logs, validation metadata,
and exact function/PC/category counts under `instruction-attribution/`.

## Whitespace probes and bounded string windows

This round starts from the 132,607-instruction parser and addresses two
previously identified control-flow costs in Go source. The two scalar
whitespace probes are explicitly expanded, matching C's `repeat_2` structure.
A constant membership bitmap recognizes exactly space, tab, CR, and LF; an
upper-byte guard prevents other bytes from aliasing those bits. The vector
scanner and its call boundary are unchanged.

The short-string path first establishes a bounded suffix and, when available,
a 16-byte window. Both word loads share that range proof; inputs with only
8–15 remaining bytes use the separate one-word path. This removes the second
word's redundant slice check without adding a new initial-suffix check. Both
paths preserve the full suffix for the existing string-decoder fallback and
keep logical-length-bounded reads. No assembly, compiler, or Parser-layout
changes are part of this round.

### Exact effect on the dominant instruction category

The same warmed 13,035-byte Twitter parser invocation now executes **128,589
instructions**, versus **132,607** before: **4,018 fewer (-3.03%)**. Exact traces
use the final benchmark binaries, with identical input hashes and successful
returns. Earlier traces are reused only for binaries with matching SHA-256.

| Exclusive instruction category | Before | After | Change |
| --- | ---: | ---: | ---: |
| Compare and branch | 49,320 | 45,579 | -3,741 |
| SP/FP loads and stores | 16,683 | 16,790 | +107 |
| Arithmetic and other | 41,971 | 41,097 | -874 |
| Moves and constants | 13,691 | 14,181 | +490 |
| Other loads/stores and call/return | 10,942 | 10,942 | 0 |
| Total | 132,607 | 128,589 | -4,018 |

This change primarily removes control work: comparison/branch reductions are
93.1% of the net instruction reduction. Whitespace refill falls from 14,351
to 11,563 instructions (-2,788), and the main parser from 91,218 to 89,988
(-1,230). All other traced function totals and entry counts are identical,
including 241 refills, 155 mask calls, 142 string-decoder calls, and 56 number
parses. The extra 107 stack loads reload the string start for the 107 non-key
string emissions; no additional stack stores are executed. The main stack
frame remains 288 bytes.
Those loads replace 107 register moves, so this category substitution is not
an independent addition of 107 instructions to the total.

Against C's unchanged 78,780 instructions, the remaining excess is **49,809
(+63.23%)**. Of this, comparison/branch contributes 25,843 (51.9%), stack
accesses 16,689 (33.5%), and other categories 7,277 (14.6%). This round removes
7.46% of the preceding excess. It reduces one major category but does not
resolve most of the remaining gap or the stack-preservation cost.

### Matched timing and input-shape limits

The final source snapshot is
`/tmp/sonic-arm-framework/go-hotpath-optimized-final`. The final comparison uses
the same Tango 1.0.1153 / Go 1.26.4 defaults, Neoverse-N2 CPU 16,
`GOMAXPROCS=1`, and `SONIC_NO_ASYNC_GC=1`; five rotating 200 ms samples run
after all builds, tests, and debugger work finish.

| Workload | Generated C, us | Before Go, us | After Go, us | Change vs before | After vs C |
| --- | ---: | ---: | ---: | ---: | ---: |
| Twitter prepare and parse, typed | 9.224 | 10.535 | 10.379 | -1.48% | +12.52% |
| Compact prepare and parse, typed | 6.434 | 7.029 | 6.894 | -1.92% | +7.15% |
| Full typed decode | 26.115 | 27.223 | 27.043 | -0.66% | +3.55% |
| Full generic decode | 49.639 | 51.442 | 50.677 | -1.49% | +2.09% |

Unchanged FromDom controls improve by 0.58–1.47% in this final round, while
the preceding broad screen showed essentially unchanged full decode. Therefore
the small end-to-end improvements above are not isolated causal estimates of
the parser edits. The parser-stage results and exact instruction reductions
provide stronger evidence. Bytes and allocations remain unchanged in all 16
cases. Full results are in
[`arm64-native-go-bench-hotpath-optimized.csv`](arm64-native-go-bench-hotpath-optimized.csv).

The five-round broad screen improves long-string parsing by 4.65% and escaped
strings by 3.16%; Alternating64 regresses 1.77%. Deep arrays and empty-object
arrays change by +0.54% and +0.23%, respectively, against the preceding Go
version. This does not repair the earlier empty-object regression. The public
whitespace Compact case is 1.07% slower in the final comparison; WideIndent
is 0.88% faster. These are retained tradeoffs, not an all-input speedup.

### Which earlier findings are resolved?

| Finding | Current status |
| --- | --- |
| Rewalking an already-classified ordinary prefix in string assembly | Fixed in an earlier round; no assembly change here |
| Non-inlined short-string/ParseString wrapper boundaries | Removed from this parser path in earlier rounds |
| Two-probe whitespace loop and scalar comparison chain | Replaced by expanded probes and bounded membership bitmap in this round |
| Redundant second-word short-string slice check | Removed by the bounded-window source form in this round; 588 instructions saved at that check |
| Redundant state redispatch and mutable frame state | Mitigated earlier; the 3,516-instruction phase decision tree still executes |
| Main scan/ready EOF and index checks | Remain: the two sequences still execute 5,940 instructions |
| Frame/node bounds and cross-call state preservation | Remain; the five common spill slots still account for 6,478 accesses |
| Generic compiler bounds proofs, switch lowering, spill placement | Recorded, not modified; no exclusive source-versus-compiler attribution claimed |

The current executed bounds-check inventory is 6,544 instructions, down from
7,132 by the eliminated 588-instruction string check. It includes necessary
guards as well as missed proofs and is not a fully removable budget. In the
preceding round, resident frame storage had added 804 frame-index check
instructions while the removed string wrapper eliminated 284; older counts
must not be reused as though the representation had stayed unchanged.

Two new attempts to bypass the repeated main ready check were rejected. Their
new dispatch join loses dominating range proofs and produces six extra slice
guard pairs downstream. Weighting those sites with the unchanged semantic
paths accounts for 1,912 added instructions. Accounting for the other changed
instructions predicts +1,428 / +2,137 for the two variants, consistent with
normalized PMU deltas of +1,429 / +2,137. This reconstruction is not a new exact
candidate trace, and it does not assign cycle costs to individual instructions.
The evidence is in this round's `scan-audit/scan-regression.{txt,json}`.

Moving whitespace cache state into Parser and testing comma-first continuation
also reduced normalized instructions, but did not demonstrate lower cycles;
neither is included. The earlier direct-continuation, inlined-scalar-probe,
unsigned-range-guard, and adjacent-empty shortcuts remain rejected for their
measured regressions. Testing an idea is not the same as resolving its finding.

### Validation and evidence

ARM-relevant main-module and issue/generic tests pass in default and
optimized/VM modes. All 7,416 parser exit-state differential records remain
byte-identical. New tests exhaust all 256 bytes at both scalar-probe positions
and vector boundaries; existing string-byte oracle and Linux guard-page tests
exercise the new bounded-window path. Go 1.20.14 and stock Go 1.26.1 affected
packages pass both configurations. Darwin ARM64 and five RISC-V profiles
compile/link successfully; no cross-target execution is claimed. The existing
host race-linking limitation is unchanged.

Only `parse_exec_go.go` and the new `space_scalar_native_test.go` change in this
round; all 42 assembly files are unchanged. The source archive, hashes,
`this-round.diff`, raw timing, candidate PMU screens, and validation logs are in
`output/arm-native-go-hotpath-optimized/`. Exact traces, function/category
totals, and final bounds/spill evidence are under `instruction-attribution/`.

## Loop checks and active-frame caching

Starting from the 128,589-instruction snapshot, this round changes four Go
source choices together:

- Loop phase and token variables use register-width values. Every token
  assignment still loads a byte; the wider temporary avoids repeated byte
  normalization without changing character or state semantics.
- `scan` and `ready` use a combined unsigned range guard. Exact EOF follows the
  existing error path; an invalid internal cursor still executes the original
  indexed access and panics. The common byte load continues to dominate later
  literal/string reads, preserving their range proofs.
- A normal Go `*goFrame` caches the active frame. Push refreshes it after any
  backing-array growth; pop resolves the parent from the current descriptor;
  root uses nil. Full-width indices/counts, partial tapes, and the native
  Parser ABI prefix remain unchanged.
- The inlined short-string mask computes raw-control events only when the
  existing string mode requests validation. Quote/backslash detection is
  unconditional. This matches C's option-controlled mask construction and the
  existing assembly leaf's behavior. It does not change validation settings or
  adopt C's separate malformed-input dispatch ordering. UTF-8 correction stays
  in the constructor.

### Exact instruction accounting

The same warmed Twitter parse now executes **121,382 instructions**, down from
**128,589** by **7,207 (-5.60%)**. The entire reduction is in `parseImpl`, which
falls from 89,988 to 82,781. All other traced function totals and entry counts
are unchanged, including 142 string decodes, 241 whitespace refills, 155 mask
calls, and 56 number parses. Final source/binary/input hashes are verified.

| Exclusive category | Before | After | Change |
| --- | ---: | ---: | ---: |
| Comparisons and branches | 45,579 | 43,484 | -2,095 |
| Stack loads/stores | 16,790 | 18,219 | +1,429 |
| Arithmetic and other | 41,097 | 36,320 | -4,777 |
| Moves/constants | 14,181 | 13,088 | -1,093 |
| Other loads/stores | 9,749 | 9,078 | -671 |
| Calls/returns | 1,193 | 1,193 | 0 |
| Total | 128,589 | 121,382 | -7,207 |

The exact PC groups explain the source tradeoffs:

- Combined scan/ready checks execute 3,960 instructions instead of 5,940,
  saving 1,980. Checks remain; two separate error branches become one common
  valid-path guard with cold EOF/panic handling.
- Active-frame index comparisons/guards fall from 1,608 to 266 (-1,342), with
  67 checked pushes and 66 checked parent restores retained.
- Phase/colon/digit byte normalization removes 1,378 `UBFX` instructions.
  The signed phase decision tree adds 319 validity branches, however, so phase
  dispatch itself increases from 3,516 to 3,835 instructions.
- On this validation-disabled input, the string mask skips 2,538 control-test
  instructions and adds 708 mode branches: net -1,830 for that semantic group.
- Cached `top` adds exactly 990 common stores and 439 reloads, explaining all
  1,429 additional stack accesses. Common-slot traffic increases from 6,478 to
  7,907 and the frame grows from 288 to 304 bytes. Fewer descriptor loads,
  address calculations, and checks outweigh this preservation cost overall.

These semantic groups overlap the exclusive category table and must not be
added to it. The generic direct-panic check inventory excludes the new
combined EOF/range guards; its smaller total must not be read as eliminating
all bounds work. Exact PCs and category reconciliation are in this round's
`instruction-attribution/final-summary.{txt,json,py}`.

Against C's 78,780 instructions, the remaining gap is **42,602 (+54.08%)**,
14.47% smaller than the preceding gap. Comparisons/branches contribute 23,748
(55.7%), stack accesses 18,118 (42.5%), and the remaining categories net 736
(1.7%). These are instruction classes, not causal source/compiler percentages
or a budget of freely removable work. State preservation is still unresolved,
and this round increases its measured cost.

### Matched timing, validation mode, and limitations

The frozen final source is `/tmp/sonic-arm-framework/go-loop-optimized-final`.
The final three-way comparison retains Tango 1.0.1153 / Go 1.26.4 defaults,
Neoverse-N2 CPU 16, `GOMAXPROCS=1`, `SONIC_NO_ASYNC_GC=1`, and five rotating
200 ms samples. Builds, tests, and exact tracing finish before timing.

| Workload | C, us | Before Go, us | After Go, us | Change vs before | After vs C |
| --- | ---: | ---: | ---: | ---: | ---: |
| Twitter prepare and parse, typed | 9.230 | 10.367 | 10.140 | -2.19% | +9.86% |
| Compact prepare and parse, typed | 6.427 | 6.921 | 6.803 | -1.70% | +5.85% |
| Full typed decode | 25.876 | 26.973 | 27.004 | +0.11% | +4.36% |
| Full generic decode | 48.890 | 50.565 | 51.113 | +1.08% | +4.55% |

Full decode has not demonstrated an improvement: generic decode regresses
1.08% in this final comparison, also +0.80% in the preceding broad screen.
Unchanged FromDom controls are 0.87–1.18% slower in the final binaries. This
correlated movement does not establish its cause, and should not be dismissed
as a proven parser effect or as proven noise. The confirmed gains are in
instruction count and parsing; the end-to-end cost remains a limitation.
All 16 cases retain their reported byte/allocation counts. Raw results and
sample ranges are in
[`arm64-native-go-bench-loop-optimized.csv`](arm64-native-go-bench-loop-optimized.csv).

The separate five-round broad screen improves ShortKeys128 by 8.74%,
Alternating64 by 7.42%, FlatNumbers1024 by 5.08%, EmptyObjects256 by 4.69%,
LongStrings128 by 4.66%, and WhitespaceNested by 3.77%. DeepArray128 changes
by +0.45%. The public whitespace Pretty case regresses 1.47% in the final
comparison, and Compact by 0.71%. Do not combine these percentages with an
older round's empty-object regression to infer an unmeasured cumulative gain.

An additional identical fixture enables `OptionValidateString` in every
candidate and control. With validation enabled, Twitter prepare-and-parse
improves 1.01% typed / 0.95% generic; Compact improves 0.32% / 0.40% in that
screen. The conditional mask therefore does not gain speed by disabling
validation. Those fixtures and measurements are retained with the broad screen.

### Rejected experiments, compiler record, and verification

Moving `used`, or both `used` and `depth`, into Parser reduced common stores
but increased whole-process normalized instructions by 1.26% / 0.51% and cycles
by 2.00% / 1.38%; those fields are not included. Bypassing the ready check while
keeping a common byte load preserves bounds proofs, but adds duplicate
comparisons and slightly regresses the PMU screen. Isolated unsigned guards
again save instructions without a demonstrated cycle gain; their inclusion
here is supported by the combined workload rather than an isolated claim.
The no-top combination regresses deep arrays by 6.13%, whereas the selected
combination avoids that regression. Screens are not additive causal estimates.

The previous binary also contains three executed hardware NOP sites inside
the inlined string probes, totaling 1,122 instructions (0.87%). Local generic
compiler code emits hardware NOPs for unmatched inline marks; the ARM64
lowering uses `HINT #0`. This is a documented possible mechanism, not a proven
mapping of each observed PC to a specific inline mark. Source lines and hashes
are in `source-review.txt`. This generic compiler concern is recorded only;
no compiler, flags, or assembly were changed to remove those instructions.

Default and optimized/VM ARM main-module and issue/generic suites pass. The
7,416-record exit-state comparison remains byte-identical, including invalid
input and option combinations. The new frame test forces repeated backing-array
growth from capacity one, validates partial unwinding/error tapes, and reuses
the same parser for scalar and nested documents. Existing exhaustive string
byte/option and guard-page tests pass. Go 1.20.14 and stock Go 1.26.1 affected
packages pass both configurations. Darwin ARM64 and five RISC-V profiles
compile/link; cross-target execution is not claimed. The known host race-link
limitation is unchanged.

Only `parse_exec_go.go` and `frame_top_native_test.go` change in this round;
all 42 assembly files and the native Parser layout remain unchanged. Evidence,
source archives, binary hashes, comparison scripts, rejected candidates, and
validation metadata are in `output/arm-native-go-loop-optimized/`.

## C-aligned Go parser experiment

For the subsequent comparison of larger parser-specific assembly operations and
pure Go SWAR replacements, see [ARM64 C-parity backend comparison](arm64-cparity-backends.md).
The original block-based experiment and its measurements below remain the
control for that comparison.

This opt-in experiment ports `native/parse_with_padding.c` instead of continuing
with the default Go parser's five-phase state machine. The default backend is
unchanged. Enable `sonic_cparity` for the direct port; also enable
`sonic_cparity_inline` for the source-expanded version used to approximate C's
`always_inline` organization. Both variants use standard Go and ARM64 Go assembly;
neither compiles C or needs SIMDE, ASM2ARM, or a modified compiler.

### What is aligned

- The root, object, and array paths have the same dedicated labels and
  continuations as C. Counts advance after values, and key/value visitors follow
  C's error-check ordering.
- Input has C's 64-byte initialized sentinel suffix. Readers use padded pointers;
  there are no per-token logical-length checks added by the default Go reader.
- Open nodes hold the parent link and the same temporary 24-bit child count.
  Closing a container publishes its length/span and checks depth at C's point.
  Node exhaustion returns to the wrapper, grows the initialized prefix, and
  resumes from the packed state, as in C.
- Strings use 32-byte quote/backslash/control masks, the same escape priority,
  Unicode handling, and in-place copy loops. Whitespace uses two scalar probes,
  the cached bitmap, and a fixed 64-byte classifier.
- Numeric parsing follows C's integer accumulation/overflow recovery, fraction
  and exponent accumulation, exact fast conversion, Eisel–Lemire table and
  algorithm, and native decimal fallback. It does not call `strconv` instead.
  The raw-number path also keeps C's SIMD-block/scalar-tail error ordering.

The port deliberately retains C's different malformed-input behavior, packed
count width, and Unicode-option behavior. It is an attribution experiment, not
an automatic replacement for the default parser's existing contracts. The
corrected-UTF8 allocation additionally remains rooted through `p.padded`; this
ownership adjustment does not execute in the measured ASCII fixture.

Source files are `parse_cparity_arm64.go`, `parse_cparity_scan_arm64.{go,s}`,
`parse_cparity_number_arm64.go`, `parse_cparity_number_tables_arm64.go`, and
`parse_cparity_decimal_arm64.go`, under `internal/decoder/optdec/`.

### Separate the ordinary inliner from source organization

The direct port keeps ordinary Go helper calls. C marks the corresponding
functions `always_inline`, which standard Go cannot request with a directive.
`scripts/cparity/expand.go` mechanically expands **102 call sites** into the
padded parser, including control, visitors, whitespace, strings, Unicode,
numeric conversion, and raw-number scanning. The checked-in result is
`parse_cparity_expanded_arm64.go`. Argument temporaries, scoped locals, renamed
labels, explicit return continuations, and conditional evaluation preserve the
port's semantics. Unsupported assignment forms are rejected by the generator.
No compiler flag, inline-budget change, PGO profile, or compiler patch is used.

This still cannot inline assembly instructions into Go. Four small ABI0 leaves
provide fixed-width SIMD masks/copying. Decimal fallback methods remain subject
to ordinary Go inlining; none executes in the measured Twitter invocation.
The expanded trace has only the three exercised SIMD leaves, the parser/wrapper,
and one `math.IsInf` call. Thus no Go string/space/number/Unicode helper call
remains on this measured path. SIMD calling conventions remain an explicit
code-generation boundary relative to C intrinsics.

### Exact executed instruction counts

The same third warmed parse of the 13,035-byte Twitter fixture is traced with
Tango 1.0.1153 / Go 1.26.4, using compiler defaults. The input SHA-256 is
`ddce57c7d74998f87f4901f1d471999ac5fcaf9f00337b5498b993d520ace970`.
All traces return success. Counts include the parser wrapper and every helper
executed before it returns. These are dynamic instruction counts, not wall-time
results; debugger duration is not benchmark timing.

| Implementation | Instructions | Difference from C |
| --- | ---: | ---: |
| Retained generated C | 78,780 | — |
| Current default Go parser | 121,382 | +54.08% |
| C-aligned Go, ordinary helper calls | 145,778 | +85.04% |
| C-aligned Go, source-expanded | **103,515** | **+31.40%** |

The expanded implementation saves **17,867 instructions (-14.72%)** versus the
current Go parser and removes **41.94% of its previous 42,602-instruction gap**.
The direct port alone is worse: expanding its helpers saves 42,263 instructions.
This demonstrates the importance of source organization and inlining together;
41.94% is an observed combined reduction, not an isolated percentage attributable
solely to source logic. The remaining gap is **24,735** instructions.

| Exclusive instruction category | C | Current Go | Expanded port | Expanded minus C |
| --- | ---: | ---: | ---: | ---: |
| Compare/branch | 19,736 | 43,484 | 24,352 | +4,616 |
| SP/FP stack load/store | 101 | 18,219 | 19,720 | +19,619 |
| Other arithmetic | 33,820 | 36,320 | 37,705 | +3,885 |
| Moves/constants | 13,032 | 13,088 | 7,915 | -5,117 |
| Other memory accesses | 12,085 | 9,078 | 12,548 | +463 |
| Calls/returns | 6 | 1,193 | 1,275 | +1,269 |

Stack accesses now account for 79.32% of the net gap by instruction class.
They are not all register spills or compiler defects. The exact disjoint Go
stack partition is 4,426 ABI0 parameter/result accesses, 15,286 main-parser local
state/spill accesses, and 8 wrapper accesses. Pair loads/stores count as one
instruction; the classifier identifies explicit SP/x29 addresses only.

Four cursor homes alone account for 6,451 stack instructions: outer cursor
3,922, key-string input cursor 1,240, value-string input cursor 833, and
value-string output cursor 456. The source-expanded pointer contracts still
form addresses of these locals. The current generic Go compiler's
`ssagen.canSSAName` rejects address-taken locals, matching this observed
memory-resident state. This is a concrete generic compiler/pointer-aliasing
interaction, more specific than calling every stack access a spill. It is
recorded here without modifying the compiler. Rewriting the source to eliminate
these aliases would be a separate experiment, not a measured saving in this one.

The remaining ABI0 leaves execute 472 string classifications, 161 whitespace
classifications, and two copies. Their useful SIMD work is not all overhead;
only the explicit 4,426 parameter/result accesses are identified as transport.
There are no executed hardware NOPs in the expanded trace. These results do not
establish an intrinsic Go-language tax of 31.40% or a pure compiler-only gap:
SIMD interfaces, alias promotion, register allocation, and lowering still differ.

### C oracle and reproduction

`TestCParityOracle` is compiled unchanged against the retained generated-C
backend and both Go variants. **8,892 records over 1,452 inputs match**, including
errors, positions, parent/depth, statistics, initialized node fields, decoded
bytes, corrected input, UTF8 status, and restored options. The corpus includes
truncation/malformed strings, numeric and SIMD boundaries, depth 4096/4097,
three option combinations, typed/generic modes, and selected growth/restart
cases. Null/bool payload words are unspecified by C and are normalized only in
the test comparison; the implementation is not changed to write them.

The canonical C record digest is
`d8618b2bf9b49d485dfad6956f340edc0eee73e8588c2e5b4053261d7161a004`.
This finite differential is stronger than the earlier Go-to-Go exit-state test,
but it does not prove equivalence for all inputs, every option/capacity, extreme
sizes, or C undefined-behavior domains. Dedicated numeric tests also check
conversion values, cursor positions, overflow, underflow, and raw-number error
priority. The experiment intentionally does not adopt the default Go parser's
separate partial-state tests as its C oracle.

Regenerate and run the experimental tests on ARM64:

```sh
go run ./scripts/cparity/expand.go
go test -ldflags=-checklinkname=0 -tags=sonic_cparity \
  -run='^TestCParity' ./internal/decoder/optdec
go test -ldflags=-checklinkname=0 -tags=sonic_cparity,sonic_cparity_inline \
  -run='^TestCParity' ./internal/decoder/optdec
```

Use the same tags with `BenchmarkARMAnalysisParseStages` for separate timing
experiments. No new end-to-end throughput improvement is claimed from the
instruction counts in this section. The source snapshot is
`/tmp/sonic-arm-framework/go-cparity-final`; hashes, exact trace scripts/binaries,
per-PC classification, oracle records, source review, and remaining attribution
are in `output/arm-native-go-cparity/`.

Final validation passes the C oracle and numeric tests with Tango Go 1.26.4,
stock Go 1.26.1, and Go 1.20.14. Both experimental variants also pass the
optimized/VM configuration with `GOGC=1`. The unaffected default backend passes
fresh default and optimized/VM runs of the ARM-relevant public, optdec, native,
kernel, and number packages; issue/generic suites pass with matching Tango
plugin tools. These runs use `GOMAXPROCS=2` and `SONIC_NO_ASYNC_GC=1`; the
experimental high-frequency-GC checks are separate. Unfiltered `go test ./...`
also attempts x86 JIT/loader packages that fail to build on this ARM64 host
(undefined loader ABI symbols), so an unrestricted all-package pass is not
claimed. Final-binary exact traces repeat both instruction totals and every
category above after the Go 1.20 compatibility and oracle-only adjustments.
