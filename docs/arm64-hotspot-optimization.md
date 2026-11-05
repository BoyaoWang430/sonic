# ARM64 hotspot optimization within the retained boundaries

This report preserves measurements from before Runs became the normal ARM64
backend. Its build tags and decoder-selection descriptions identify the
measured versions. See [current integration and validation](arm64-native-go.md#current-retained-version)
for the present architecture selection; no custom tags are now required.


The retained changes remove the large Unicode and integral-float regressions
and reduce the mixed-number and AST string-traversal gaps. Go keeps control
flow, numeric conversion, Unicode policy, and path traversal. Assembly still
implements the existing complete string-scanning operation. No new assembly
entry, ABI argument, continuation state, or default-backend switch is added.

This follows the [full API comparison](arm64-overall-current-comparison.md).
The principal decoder results use `sonic_cparity,sonic_cparity_runs`; ordinary
untagged builds still select `parse_exec_go.go`. Common formatter and AST
changes benefit both builds. Default-decoder measurements are kept separately.

A subsequent [residual-hotspot analysis](arm64-residual-hotspots.md) records
the next retained optimizations, remaining gaps, and rejected experiments.

## Final results

All comparisons below are freshly paired runs of original C, the preceding
Go/assembly version, and the retained implementation. They use identical
fixtures and Tango 1.0.0.1153 / Go 1.26.4 on the same Neoverse-N2 host. Times
are median microseconds; changes are median within-round ns/op ratios.
Negative means faster. Ratios need not exactly match rounded displayed times.

| Scenario | Original C | Before | Retained | vs before | vs C |
| --- | ---: | ---: | ---: | ---: | ---: |
| Unmarshal, dense Unicode escapes | 3.945 | 10.476 | 3.584 | -65.75% | -9.11% |
| Marshal, integral float64 array | 12.713 | 19.991 | 9.889 | -50.55% | -22.27% |
| Marshal, mixed float64 array | 18.450 | 24.866 | 22.342 | -10.04% | +21.21% |
| Unmarshal, mixed floating-point numbers | 12.421 | 17.549 | 14.713 | -16.13% | +18.44% |
| Typed json.Number slice, long decimals | 226.983 | 308.775 | 67.595 | -78.11% | -70.23% |
| AST dense escaped-string array, compact, last index | 1.488 | 2.253 | 1.918 | -14.88% | +28.81% |
| AST dense escaped-string array, spaced, last index | 1.612 | 2.596 | 2.216 | -14.64% | +37.34% |
| AST complete scan, long escaped string | 2.190 | 2.540 | 1.537 | -39.48% | -29.82% |
| AST skip one dense escaped string | 0.745 | 0.857 | 0.576 | -32.83% | -22.78% |
| AST complete scan, long plain string | 5.498 | 3.932 | 2.105 | -46.45% | -61.72% |
| AST complete scan, float array | 18.996 | 24.187 | 22.649 | -6.53% | +19.23% |

Integral float64 values are generic JSON numbers, including values such as
`float64(1)`. This measurement does not describe typed integer formatting,
which already used the integer formatter. Dense Unicode decoding receives
JSON containing `\uXXXX` and surrogate pairs. Its corresponding Marshal
benchmark receives decoded UTF-8 text and exercises a different operation.

The json.Number fixture uses a reusable typed slice and UnmarshalFromString.
The parser clears UseNumber for this typed root, converts numbers into the
tape, and the binder retrieves the original lexemes. Its timings include
numeric conversion and binding, not just token scanning.

Public APIs in the Runs build remain close to C, with the generic Marshal improvement
also visible outside the concentrated numeric fixture:

| Public API in Runs build, ConfigDefault | C | Before | Retained | vs before | vs C |
| --- | ---: | ---: | ---: | ---: | ---: |
| Marshal generic | 39.089 | 38.090 | 35.413 | -7.04% | -9.66% |
| Marshal binding | 12.846 | 13.070 | 13.085 | +0.04% | +1.88% |
| Unmarshal generic | 32.665 | 33.450 | 33.322 | -0.36% | +2.12% |
| Unmarshal binding | 23.766 | 24.021 | 24.003 | -0.29% | +0.80% |

ConfigStd binding Marshal is 18.28% faster than C; generic and binding
Unmarshal are 5.79% and 11.94% faster. Whole-tree AST materialization improves
2.25% relative to the previous Go implementation, retaining a 6.51% gap to C.
GetFull improves another 4.04%, reaching 17.34% faster than C. GetOne improves
0.93%, retaining a 9.16% gap to C.

Allocation counts match across all versions in every measured scenario.
Median B/op differs by at most one byte in three rows; these small differences
and all raw metrics are recorded in the artifacts.

## Retained implementation

### Exact integral floats keep their integer fast path

[number.AppendFloat](../internal/native/number/format.go) recognizes exact
integers in the float64 range up to 2^53 and float32 range up to 2^24. It uses
`strconv.AppendInt` for these values, preserving the existing notation policy,
negative-zero handling, non-finite-value behavior, and float32 rounding.
Larger values remain with shortest-float formatting because the shortest
decimal can differ from the exact integer value.

The fast path lives inside the existing shared formatter. The small
`alg.F64toa/F32toa` wrappers remain unchanged and retain their inlineability.
This adds nine production lines and dedicated boundary/differential tests.

### Unicode cursors stay local for an entire operation

[cParityDecodeUnicode](../internal/decoder/optdec/parse_cparity_scan_arm64.go)
converts adjacent Unicode escapes in one Go operation. Input and output
pointers are passed and returned by value; Runs publishes them once. A
256-byte table combines four-digit hex validation and decoding.

The preceding helper updated `*src` and `*dst` for each code point. Disassembly
confirmed repeated write-barrier checks and helper calls. The retained
Unicode operation is 560 bytes of code, with no internal calls or pointer
write-barrier branches; its hex helper is inlined. The previous helper was
1,088 bytes. These static observations explain eliminated work, but are not
a new complete dynamic-instruction attribution.

Adjacent Unicode escapes were already batched across the assembly boundary
before this change. The improvement must not be described as eliminating an
assembly round trip per character. C's replacement policy, invalid-escape
cursor, partial output, and in-place decoding remain unchanged.

### Numeric operations return values and avoid replay

[cParityParseNumberValue](../internal/decoder/optdec/parse_cparity_number_arm64.go)
returns its cursor and numeric state by value. The retained Runs caller uses
this interface directly; the old pointer interface remains as a wrapper for
other experimental backends. Fast float conversion and Eisel-Lemire also
return their result and success flag instead of writing through an output
pointer.

Integer scanning accumulates at most 19 digits once. Further digits update
the decimal exponent and truncation flag. The former implementation first
overflowed the accumulator, then replayed the first 19 digits. Twenty-digit
uint64 values still use exact multiply/add overflow checks, and all original
error cursors, retained numeric values, and truncation ambiguity checks remain.

The [slow conversion](../internal/decoder/optdec/parse_cparity_decimal_arm64.go)
uses `strconv.ParseFloat` only after the existing scanner has validated a
token of at most 800 bytes and the C scratch capacity is at least 800 bytes.
This reuses the standard library's wider-significand fast path and correctly
rounded fallback. Longer tokens and smaller scratch buffers retain the old
C-style decimal implementation: its bounded-buffer behavior can differ from
strconv and is part of the experimental compatibility contract. The retained
old path uses a slice-based clear so the compiler can generate bulk zeroing.

The ordinary Go decoder also reuses a completed validation result:
[exactFloatSuffix](../internal/native/number/exact.go) accepts bounded
three-digit exponents and returns a known token end when general conversion
is still necessary. The caller skips the redundant Scan in that case. Long
exponents, mantissa overflow, and incomplete syntax keep their old fallback.

### AST optimizes the existing operations

The [string scanner](../internal/native/kernels/string_block_arm64.h) classifies
32/64-byte blocks and packs their masks in batches. It retains the first-32,
then-64 policy, bounded overlapping tail loads, escape carry, and raw suffix
cache. No-event blocks avoid unnecessary escape-parity work.

[Path traversal](../internal/native/path_go.go) keeps its cursor local and
publishes it once on return, including errors. The cached string branch is
merged into [pathBlock.string](../internal/native/path_cache_go.go), removing
one Go dispatch layer. These path changes have zero net production Go line
growth and preserve Go ownership of array/object grammar and error handling.

[scanDigitsARM](../internal/native/kernels/scanning_arm64.go) classifies the
first eight bytes using bounded word operations in Go. Long digit runs still
use the existing SIMD kernel. The first invalid-byte mask is valid because
preceding ASCII digits cannot carry or borrow between byte lanes.

Across the changed production files, Go grows by 77 physical lines and the
existing assembly header grows by 79; tests grow by 373 lines. Counts include
comments and blank lines. There are no new assembly entry points, ABI
parameters, shared parser fields, or path-cache fields.

## Remaining gaps and accepted costs

Mixed float formatting and decoding still cost 21.21% and 18.44% more than C
in their concentrated fixtures. AST dense escaped-string array queries remain
28.81% to 38.66% slower, including missing-index queries. The retained scanner
still crosses a per-string boundary and performs per-string setup, result
publication, and cache handoff while Go walks elements and delimiters.
Some cached content can be reclassified after the first escaped byte.
C can inline its narrower skip-string operation into traversal.

These are remaining implementation differences, not proof that all remaining
time is a compiler limitation. This round does not change the compiler or
copy another full floating-point conversion algorithm to chase the residual
gap. It also does not introduce a skip-N-values assembly routine that would
own type dispatch, punctuation, nested containers, counters, and error policy.

The largest measured regression against the preceding implementation is
**3.74%** in GetManyShortEscapedFields, from 0.4044 to 0.4189 microseconds
(about 15 ns). Parser_Sonic is +1.39%; compact-whitespace decoding is +1.24%.
Every other positive paired median is below 1%. These costs are retained with
the larger gains and unchanged operation boundaries. The early approximately
9% NestedContainers regression was not stable: the final same-round result is
+0.91%. It is not attributed to a specific compiler or layout issue, and no
padding or compiler workaround was added.

The ordinary untagged decoder remains a separate implementation. Its mixed
float decoding improves 10.07% but is still 67.00% slower than C; dense Unicode
remains +37.38%, and the generic small-integer array remains +23.14%. Those
figures must not be replaced with the retained Runs results. The complete
default-build table is included alongside the principal Runs comparison.

## Validation and reproducibility

Artifacts are in
[`output/arm-hotspot-optimization/`](../output/arm-hotspot-optimization/):

- [Complete final results](../output/arm-hotspot-optimization/final-table.md)
  and [CSV with allocations and ranges](../output/arm-hotspot-optimization/final-table.csv).
- `retained.patch`, source hashes/archive, actual binary hashes/build information,
  `final-groups.json`, `run-final.py`, and all paired logs.
- Separate candidate screens and source snapshots; their short screening
  results are not substituted for the final comparison.

The reference is the original C implementation at
`a7193312dec5165ec6686a9793eb308e2e642903`. All 31 shared fixture files match the
preceding measurement. The public Twitter fixture is 10,408 bytes, AST Twitter
is 13,023 bytes, and GetWithManyCompare uses its separate 701-byte synthetic
object. Public API decoding uses fresh destinations; Numbers/Whitespace use
their existing reusable typed slices. Setup and result checks stay outside
timing, and public outputs are checked against encoding/json.

The core comparison has twelve balanced 200 ms rounds. Shapes, numeric/
whitespace coverage, and AST have six balanced 200 ms rounds each. The
supplemental default-build group has six balanced 100 ms rounds. All variants
use ARM64 v8.0, CGO disabled, `GOWORK=off`, `-ldflags=-checklinkname=0`, CPU 16,
`GOMAXPROCS=1`, normal GC, `SONIC_NO_ASYNC_GC=1`, and a fixed argv[0]. No builds,
tests, or debugger runs overlap timings. CPU affinity does not isolate the
host; no traffic-weighted overall speedup or significance test is inferred.
There are 66 unique scenarios and 276 variant/scenario rows, all passing
preflight result checks and every timed repetition. Source and binary hashes
remain unchanged through the measurements.

Validation includes:

- Relevant public/native/encoder/decoder tests in default and explicit
  optdec/FastMap/encoder-VM environments.
- Stock Go 1.26.1 and Go 1.20.14 native/kernel/number/AST tests and tagged
  C-parity tests; Tango high-frequency-GC checks, including decoder tests.
- The fixed 8,892-record C-parity oracle and 40,946-record preceding-Go AST
  oracle, plus numeric, growth, boundary, and guard-page tests.
- Every UTF-16 unit, randomized surrogate sequences, malformed/overlapping
  Unicode outputs, hex-byte comparisons, exact-integer formatting boundaries,
  decimal halfway/subnormal/overflow/capacity cases, and first-invalid-byte
  word scanning.
- Short fuzz sessions: 167,708 numeric-prefix executions and 5,351 string-block
  executions; both pass.
- RISC-V native and amd64 number cross-builds. These are compile checks, not
  target execution or performance measurements.

The full tagged optdec suite retains the same 15 historical top-level
contract failures. Before/after failure blocks match after removing elapsed
times; there are no added failures. The earlier 2,181 native AST/C semantic
differences also remain outside this optimization's scope. Equivalence to the
preceding Go backend and valid-input performance checks do not establish
complete C compatibility.
