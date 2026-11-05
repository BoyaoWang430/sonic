# ARM64 API performance against the original C backend

This report preserves measurements from before Runs became the normal ARM64
backend. Its build tags and decoder-selection descriptions identify the
measured versions. See [current integration and validation](arm64-native-go.md#current-retained-version)
for the present architecture selection; no custom tags are now required.


This report records the baseline before the subsequent
[hotspot optimization](arm64-hotspot-optimization.md), which contains the
latest retained implementation and paired results.

The current Go/assembly boundaries are worth retaining. Ordinary Twitter
Marshal/Unmarshal workloads are close to the original C implementation, with
improvements under ConfigStd. Several concentrated workloads still have large
regressions: floating-point formatting/conversion, dense Unicode escapes, and
AST queries across dense escaped-string arrays. This is a framework checkpoint;
performance and compatibility work remain.

This measurement adds a public-API benchmark fixture and this report. It does
not change production implementations or select Runs as the default backend.

## Versions and measurement

Three versions use the same Tango 1.0.0.1153 / Go 1.26.4 compiler:

| Name | Implementation |
| --- | --- |
| C | Original commit `a7193312dec5165ec6686a9793eb308e2e642903`, restored with `git archive`; existing C-generated ARM64 assembly |
| Default | Current workspace, ordinary build; optdec selects `parse_exec_go.go` |
| Runs | Same workspace with `-tags=sonic_cparity,sonic_cparity_runs`; retained optdec run boundaries |

Runs tags only select optdec sources. Encoder and AST implementations are shared
by Default and Runs. Separately linked binaries can still produce different
timings; their Marshal differences must not be attributed to a different Runs
encoder algorithm. Both original and current ARM builds already select optdec,
FastMap, and the encoder VM through initialization.

All builds use ARM64 v8.0, CGO disabled, `GOWORK=off`, and
`-ldflags=-checklinkname=0`. Measurements run serially on CPU 16 of the same
Neoverse-N2 host, with `GOMAXPROCS=1`, `SONIC_NO_ASYNC_GC=1`, normal GC, and a
fixed executable argv[0]. No compilation, tests, or debugger work overlaps
timings. CPU affinity is not exclusive host isolation.

The core API group has 12 balanced rounds of 200 ms; two public coverage groups
have six rounds each; AST has eight rounds. Each order is fully balanced for
its number of variants. Times below are median microseconds per operation.
Percentages are medians of within-round ns/op ratios; negative is faster.
No significance test or traffic-weighted aggregate is inferred.

Artifacts are in
[`output/arm-overall-current-comparison/`](../output/arm-overall-current-comparison/):

- [All 66 scenarios](../output/arm-overall-current-comparison/complete-results.md)
  and [CSV including allocations and ranges](../output/arm-overall-current-comparison/complete-results.csv).
- [Binary hashes and build information](../output/arm-overall-current-comparison/binaries.json),
  [source/fixture hashes](../output/arm-overall-current-comparison/source-setup.json),
  and `current-source.tar.gz`.
- `run.py`, the copied benchmark fixture, per-group metadata, and all timing logs.

## Public Marshal and Unmarshal

[The new fixture](../arm64_public_api_bench_test.go) calls the actual public
API, including byte-input Unmarshal's input copy and trailing-input check.
Every decode has a fresh destination; Marshal allocates its output. Setup,
warmup, and comparisons against `encoding/json` are outside timing.

The exported `testdata.TwitterJson` is **10,408 bytes**. Generic values contain
float64 numbers; binding uses `testdata.TwitterStruct`. ConfigDefault and
ConfigStd are measured separately. ConfigStd includes string validation and
copying, plus encoder sorting and HTML escaping. Historical decoder package
benchmarks use a different 13,035-byte fixture and a narrower Decode call;
their absolute times are not directly comparable with this table.

| Configuration / operation | C | Default | vs C | Runs | vs C |
| --- | ---: | ---: | ---: | ---: | ---: |
| Default / Marshal generic | 38.997 | 37.853 | -2.91% | 37.994 | -2.49% |
| Default / Marshal binding | 12.848 | 13.026 | +1.31% | 13.071 | +1.71% |
| Default / Unmarshal generic | 32.744 | 34.341 | +4.93% | 33.460 | +2.09% |
| Default / Unmarshal binding | 23.752 | 24.891 | +4.87% | 24.058 | +1.22% |
| Std / Marshal generic | 56.709 | 55.258 | -2.80% | 55.085 | -3.05% |
| Std / Marshal binding | 18.073 | 14.768 | -18.37% | 14.762 | -18.54% |
| Std / Unmarshal generic | 56.614 | 54.073 | -4.56% | 53.273 | -5.89% |
| Std / Unmarshal binding | 32.737 | 29.600 | -9.43% | 28.831 | -11.86% |

Allocation counts are unchanged in every measured scenario. Median B/op also
matches except for a 0.5 B/op difference in Std generic Unmarshal
(47,080.5 for C, 47,080 for both current builds).

## AST operations

AST uses the same implementation in Default and Runs. The AST package table
measures Default against C. Its existing Twitter fixture is **13,023 bytes**;
GetWithManyCompare uses a separate **701-byte synthetic repeated-key object**.
Whole-tree materialization is measured separately in the new public fixture
using the 10,408-byte document, so its absolute latency is not directly
comparable with the other rows.

| Operation | C | Current | vs C |
| --- | ---: | ---: | ---: |
| GetOne: path query | 3.333 | 3.671 | +10.12% |
| GetFull: full scan, return raw root | 10.749 | 9.246 | -14.00% |
| GetWithManyCompare: synthetic key search | 2.496 | 1.383 | -44.56% |
| Parser: Parse + current-layer LoadAll | 4.371 | 4.540 | +3.89% |
| ParseSeven: parse + seven nested lookups | 14.170 | 15.309 | +8.05% |
| Fresh NewRaw + Interface: whole-tree materialization | 102.402 | 111.546 | +8.96% |

The Runs-linked public binary also measures whole-tree materialization:
111.690 microseconds, +9.14% versus C. This is the same AST source implementation.

AST retains meaningful wins beyond Twitter: complete scans of many fields
improve 45.97%, tiny objects 52.12%, wide whitespace 59.05%, and long plain
strings 28.55%. Querying the last element of compact empty-container arrays
improves 64.74%. These results are separate workloads, not additive savings.

## Remaining concentrated regressions

These public API shapes use ConfigDefault, generic values, and fresh decode
targets. The numeric shape named SmallIntegers contains float64 values after
generic decoding; its Marshal measurement does not exercise typed integer
formatting. DenseUnicodeEscapes contains escape sequences on Unmarshal input;
its Marshal input is an already-decoded UTF-8 string.

| Operation / input | C | Default | vs C | Runs | vs C |
| --- | ---: | ---: | ---: | ---: | ---: |
| Marshal / 256 integral float64 values | 12.712 | 18.600 | +45.29% | 20.025 | +57.13% |
| Marshal / 256 mixed float64 values | 18.546 | 23.528 | +26.61% | 24.981 | +34.78% |
| Unmarshal / mixed floating-point numbers | 12.419 | 23.141 | +86.24% | 17.566 | +41.31% |
| Unmarshal / dense Unicode escapes | 3.940 | 5.454 | +38.43% | 10.525 | +166.83% |
| Unmarshal / many small nested containers | 49.877 | 51.301 | +2.70% | 52.317 | +4.84% |
| Unmarshal / long plain string | 1.706 | 1.563 | -8.33% | 1.782 | +4.48% |

Other shapes improve: dense ASCII-escape Marshal takes 6.102 microseconds in
Runs versus 13.563 for C (-55.08%); its Unmarshal improves 16.03%.
Runs' small-integer generic Unmarshal improves 2.12%.

The separate existing Numbers/Whitespace fixtures reuse typed slice targets
and use UnmarshalFromString; they should not be mixed with fresh-target API
allocation results. Wide-indentation decoding improves 32.12% in Runs.
LongDecimals takes 309.305 microseconds in Runs versus 227.206 for C (+36.06%),
while Default takes 63.604 (-72.00%). Although the fixture requests UseNumber,
its destination is `[]json.Number`: the parser clears UseNumber for this typed
root, converts numeric values, then the binder scans the original token for
json.Number. This comparison includes numeric conversion and binding, not
just digit scanning.

Remaining AST losses include:

| Operation / input | C | Current | vs C |
| --- | ---: | ---: | ---: |
| Dense escaped-string array, compact, last element | 1.488 | 2.252 | +51.34% |
| Dense escaped-string array, spaced, last element | 1.613 | 2.595 | +61.01% |
| Mixed array, spaced, last element | 1.149 | 1.370 | +19.29% |
| Complete scan of float array | 18.959 | 24.272 | +27.81% |
| Complete scan of long escaped string | 2.191 | 2.539 | +15.93% |

Missing-element queries on the dense escaped-string arrays are +53.49% and
+62.30%. The [previous array analysis](arm64-ast-array-scanning.md) explains
the retained operation boundaries and the instruction-count improvements
already made there.

## Source-level interpretation and next priorities

These measurements identify workloads, not a profile-based attribution of
every extra cycle. Source inspection establishes the following differences:

1. Float formatting now calls `number.AppendFloat` / `strconv.AppendFloat`
   through [float_go.go](../internal/encoder/alg/float_go.go). C used its
   Schubfach formatter and integer-valued-float fast paths. The number-heavy
   Marshal losses warrant a focused formatter investigation. Typed integer
   formatting already used `strconv.AppendInt/Uint` in both versions.
2. Runs handles Unicode in Go. Its
   [Unicode continuation](../internal/decoder/optdec/parse_cparity_runs_arm64.go)
   already batches adjacent escapes in one Go loop. The dense fixture performs
   512 Unicode handler operations; this does **not** mean 512 assembly/Go
   round trips. C inlines its Unicode handler into the generated parser;
   Default keeps Unicode in the complete assembly string decoder. The Go
   handler, its generated code, and state accesses need focused investigation.
3. All 256 mixed-number tokens leave Runs' integer fast path. The number
   operation returns to Go and processes the numeric prefix again before
   conversion. Conversion algorithms and these operation boundaries are
   candidates for the remaining decode gap; their individual costs were not
   isolated by this timing run.
4. AST's dense escaped-string traversal still pays for repeated string
   operations and Go traversal. The existing instruction analysis supports
   improving these specific operations while retaining the current grammar
   ownership, rather than immediately expanding assembly to the entire AST
   parser.

Quote, HTML escaping, and UTF-8 validation also use new assembly kernels.
Encoder VM and public API source remain unchanged. The ConfigStd wins measure
the combined paths; this report does not attribute each percentage to one
kernel without profiling.

## Validation scope

All 66 distinct scenarios passed their result checks in each applicable
version: 159 version/scenario checks before timing. Every timed repetition
also completed successfully. Public API outputs are compared with
`encoding/json` outside timing. All 31 shared fixture files match between the
restored C tree and current tree. Current `ast/*.go` files match the original
revision byte for byte; the AST changes reside below that package.

This is valid-input performance validation. Existing compatibility findings
remain recorded separately: Runs has 15 native-backend contract-test failures
with unchanged historical results, and the earlier native AST audit found
2,181 C/Go differences, including malformed-input acceptance and error
positions. See [Runs boundaries](arm64-cparity-runs-boundary.md) and
[AST validation](arm64-ast-array-scanning.md). The benchmark checks do not
replace those suites or establish complete compatibility.
