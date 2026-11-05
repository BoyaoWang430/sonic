# ARM64 AST string-cache handoff refinement

The subsequent [array-scanning optimization](arm64-ast-array-scanning.md)
further reduces cached-event processing, empty-container work, and string
classification cost. This document retains the preceding iteration's results.

The retained change removes a redundant short-string probe when path search has
already consumed a classified string prefix. Go continues to own path traversal,
key comparison, validation policy, cursors, and grammar state. The existing
assembly scanner consumes the remaining string and returns its boundary and any
reusable classifications. No assembly implementation, AST implementation, or
optdec/Runs implementation changes in this round.

This follows the three complete scanning kernels retained in the preceding AST
work. The baseline here is that Go/assembly version, not the original generated C
backend. Artifacts are under
[`output/arm-ast-boundary-refinement`](../output/arm-ast-boundary-refinement/).

## Retained responsibility boundary

Previously, `pathBlock.stringCached` consumed its cached quote/backslash events,
then called `stringTail` on the remaining input. `stringTail` starts with another
short Go probe before reaching the complete assembly scanner. A string could
therefore pass through a cached prefix, a second short probe, and the scanner.

After the change, exhausting the cached prefix calls `ScanPathStringBlock`
directly. A newly encountered string without applicable cached events still uses
the existing short Go probe. The boundary is based on work already completed,
without a new length or escape-density threshold.

| Component | Responsibility |
| --- | --- |
| Go path traversal | Select object keys or array indices, skip unrelated values, and choose validation policy for the target. |
| Go string cache | Consume existing raw events; resolve a crossing escape exactly once; retain the first escape offset. |
| Complete string scanner | Scan from the first unread content byte to a closing quote or EOF; return absolute offsets through the Go adapter. |
| Go cache update | Store the raw events after the closing quote, when the scanner already classified that suffix. |
| Go validator and key comparison | Preserve grammar state, error cursors, and the existing rule that malformed key escapes after a mismatch are not decoded. |

The handoff starts at an ordinary string-content position. If a backslash was
the final cached event, Go consumes both it and its escaped byte before calling
the scanner. No pending escape needs to cross the ABI. The earlier first-escape
offset wins over a later offset returned by the scanner.

The scanner's suffix contains **raw** quote/backslash events, including escaped
quotes. Its span begins at the returned string end and stays inside the logical
input. Errors return an empty suffix. These classifications remain reusable by
subsequent tokens; they are not interpreted as whitespace or numeric masks.

The implementation is in
[`path_cache_go.go`](../internal/native/path_cache_go.go); the clarified contract
is in [`path_masks_native.go`](../internal/native/kernels/path_masks_native.go).

## Five independent candidates

Each candidate started from the same baseline. The retained version contains
only `handoff`; it does not combine the other experiments.

The following observations are from the **candidate screen**, not the final
confirmation: 33 benchmarks, six rounds, 50 ms per benchmark, pinned to CPU 16,
with rotating variant order. Percentages are the median of paired ns/op ratios
against `before`; negative means faster. The screen is a selection aid, not a
confidence interval or a claim of statistical significance. See
[`screen-boundaries/summary.json`](../output/arm-ast-boundary-refinement/screen-boundaries/summary.json)
and [`candidates.json`](../output/arm-ast-boundary-refinement/candidates.json).

The later binary audit found a repeatable timing offset in the allocating long
selected-value queries even between binaries with identical machine code and
layout. Their screen percentages cannot establish a regression caused by a
boundary change. The final comparison uses a freshly built control; see below.

| Candidate | Boundary change | Screen observation and decision |
| --- | --- | --- |
| **handoff — retained** | After cached events run out, continue directly in the complete scanner while retaining its suffix cache. | Dense string array queries improved about 9.6–9.7% for compact input and 17.5–17.6% for spaced input. The short escaped selected string improved 3.4%. This removed redundant work with the smallest production change. |
| early | Hand the first backslash in a newly probed string directly to the scanner, removing Go's short escape loop. | The short escaped selected string slowed 16.8%. The existing short-string boundary remains useful. |
| canonical | Send every validated target through `SkipOne`, removing the cached-string success path and failure replay. | The short escaped selected string slowed 17.4%. The simpler target ownership did not justify that loss. |
| direct | Add architecture adapters that call the relaxed string and number entries without the shared mode dispatch. | No consistent overall benefit was established. The long selected-value timings are affected by the binary-comparison limitation above. Two architecture-specific adapters were not retained for this small potential saving. |
| no-tail | Stop storing string suffix events and keep only complete `PathMasks` caches; remove the implicit full-versus-events-only checks. | Dense array queries improved 12.6–22.1%; the apparent long selected-value losses cannot be assigned to cache removal after the binary audit. This experiment still used the existing assembly return protocol, so it did not measure a fully narrowed ABI. It remains a possible separate experiment; this round retains the existing reuse contract and the smaller, confirmed handoff change. |

The long dense escaped-string case improved only about 0.3% in the `handoff`
screen. It mostly exercises one long scanner invocation, rather than repeated
cache-to-probe transitions. This change does not establish that the remaining
gap to C in that workload has been removed.

The direct-adapter experiment also exposed an inlining-budget sensitivity:
returning a six-result call directly cost 87 in the compiler report, above its
80 budget; named result assignments followed by a bare return cost 69 and
inlined. The dispatcher itself accounted for only 15 instructions in the
preceding long dense-string trace. This compiler observation is recorded, not
treated as the main remaining performance problem or changed in production.

## Benchmark interpretation

`GetFull` performs the default grammar scan and returns a raw node. It does not
construct a decoded tree. `Parser_Sonic` performs `Parse` followed by `LoadAll`;
in this implementation `LoadAll` loads the current layer with `loadOnce`, keeping
child containers as raw nodes. It must not be described as full-tree
materialization. `LoadNode/Interface()` additionally performs recursive generic
conversion of the selected subtree.

The dense array cases traverse many unselected strings. Compact and spaced
inputs, successful and missing indices, selected short strings, and long plain,
sparse, and dense strings are separate checks. Their results should remain
separate rather than being collapsed into a single AST speedup.

## Validation

The retained version matches the unchanged Go baseline on all **40,946** native
oracle records. The deterministic JSONL digest is:

```text
d0d9890199e986afe12deb0cf9168806da295fb43b808794cbb1587a2d3e46b3
```

The oracle records `SkipOne`, `SkipOneFast`, `ValidateOne`, `GetByPath`, and
`Value` results, including cursor positions, active grammar stacks, and numeric
bit patterns. Its corpus includes truncations, byte mutations, input offsets,
vector boundaries, long keys, and depth limits. The output is retained as
[`retained-oracle.jsonl`](../output/arm-ast-boundary-refinement/retained-oracle.jsonl).

This is equivalence to the previous Go implementation. The preceding C/Go audit
recorded 2,181 existing differences, including 142 accept/reject differences on
malformed escape inputs and 10 path error-code differences. Those differences
are not normalized away or claimed to be resolved by this refinement. See
[`oracle-existing-differences.json`](../output/arm-ast-boundaries/oracle-existing-differences.json).

The new
[`TestPathCachedStringContinuation`](../internal/native/path_cache_continuation_native_test.go)
compares cached continuation against an independent scalar reference. It moves
backslashes, escaped bytes, and closing quotes across cache boundaries, checks
the first escape offset, and checks that later cached raw events remain exact.
Existing path handoff and malformed-key tests cover the consumer contracts.

Validation completed with:

- Tango Go 1.26.4: native, kernels, AST, and decoder tests with default settings;
  an optdec regression run; and native/AST/decoder tests with the optimized/VM
  settings and `GOGC=1`.
- Go 1.20 and stock Go 1.26: native and AST tests.
- RISC-V `rva22u64`: native cross-build. No RISC-V execution or performance
  conclusion is included.

The corresponding logs are `test-retained-default.log`,
`test-retained-optdec.log`, `test-retained-vm-gc.log`,
`test-retained-go120.log`, `test-retained-stock.log`, and
`build-retained-riscv64.log` in the artifact directory.

## Maintenance cost and remaining contracts

The production change adds three net physical lines in `path_cache_go.go` and
one net contract-comment line in `path_masks_native.go`, plus the focused
continuation test. It adds no cache fields, assembly entry points, architecture
dispatch, input mutation, or allocation. Assembly, AST, and optdec/Runs sources
remain unchanged in this round.

Two existing contracts still require care. First, `pathBlock` can hold complete
path classifications or an events-only string suffix; zero `nonspace` and
`boundaries` identify the latter. Consumers must not use stale masks for a new
span. Second, selected-string failures still replay through the validator to
preserve its error cursor and active grammar stack. Removing those contracts
was explored independently and was not included in this retained change.

The result is a narrower improvement in continuation ownership: Go finishes
using an existing cache, then the scanner owns the remaining scan. It does not
move AST grammar or path matching into assembly, and the assembly scanner still
contains the scalar bookkeeping needed to complete its SIMD-assisted operation.

## Final confirmation results

The final comparison is
[`timing-matched/summary.json`](../output/arm-ast-boundary-refinement/timing-matched/summary.json):
33 workloads, eight balanced rounds, 200 ms per benchmark, CPU 16,
`GOMAXPROCS=1`, and asynchronous Sonic GC disabled. Both binaries were built
with Tango 1.0.0.1153 / Go 1.26.4, ARM64 v8.0, CGO disabled, and
`-ldflags=-checklinkname=0`. They were built from the same source directory with
the same non-logic files; only the cached-string handoff differs. Exec wrappers
also fix `argv[0]`. The actual binary hashes are recorded in
`argv0/manifest.json`; the benchmark metadata hashes the wrappers themselves.
No compilation, tests, or debugger tracing ran concurrently with timings.

The table reports median timings and the median of within-round ns/op changes;
these two percentage calculations need not coincide. Negative means faster.
All allocation counts and bytes per operation match in every paired sample.

| Benchmark | Before (µs) | Retained (µs) | Paired change |
| --- | ---: | ---: | ---: |
| Dense string array, compact, last index | 4.157 | 3.753 | -9.70% |
| Dense string array, compact, missing index | 4.108 | 3.712 | -9.67% |
| Dense string array, spaced, last index | 4.886 | 4.034 | -17.51% |
| Dense string array, spaced, missing index | 4.824 | 3.969 | -17.74% |
| Selected short escaped string | 0.113 | 0.109 | -3.28% |
| Selected long plain string | 1.381 | 1.382 | -0.07% |
| Selected long dense escaped string | 1.804 | 1.808 | -0.25% |
| GetOne_Sonic | 3.697 | 3.685 | -0.34% |
| GetOneSafe_Sonic | 3.756 | 3.743 | -0.33% |
| GetFull_Sonic | 9.332 | 9.319 | -0.16% |
| GetWithManyCompare_Sonic | 1.461 | 1.459 | -0.10% |
| Parser_Sonic | 4.594 | 4.595 | +0.04% |
| ParseSeven_Sonic | 15.585 | 15.606 | +0.15% |
| Skip one long dense escaped string | 1.019 | 1.016 | -0.25% |

Outside the four dense array queries and short escaped selected string, the
paired medians range from -0.82% to +0.23%. These small changes are treated as
essentially neutral, not as independent optimization wins. Timing on this one
Neoverse-N2 host is not a guarantee for other ARM machines.

### Binary-comparison check

The first final run against the saved old executable appeared to regress long
selected-value queries by 6–9%. A second run reproduced an approximately
100 ns offset between the candidate executable and the final executable even
though their entire `.text` (3,277,908 bytes), all 9,168 function addresses and
sizes, and ordinary data sections were identical. Fixing `argv[0]` did not
remove it. A freshly rebuilt old implementation then exhibited the same offset
as the final implementation.

The saved and rebuilt old binaries also have identical machine code, symbols,
and data layout. Their source metadata differs in two bytes representing the
contract comment's added line. The audit establishes that the observed timing
offset cannot be assigned to this source optimization or instruction layout;
it does not establish the offset's underlying cause. No compiler or runtime
change was made to address it. The matched final measurements above and the
independent instruction counts are the evidence used to retain the change.

The initial results remain in `timing-retained/`, the repeated comparison in
`timing-binary-check/`, the fixed-argv diagnostic in `timing-argv0/`, and the ELF
comparison in `binary-audit/`. In particular, the initial long-query slowdown
is not presented as a measured cost of the new boundary.

### Dynamic instructions

The instruction trace counts the fifth `ast.(*Parser).getByPath` invocation
under the compact dense-string array benchmark, with eight fixed iterations.
This avoids the parser-pool refill found at the third invocation. It counts
all original instructions from parser entry through its callees to return;
the result-copy step outside that entry is excluded. Debugger timing is never
used as a performance measurement.

The identical 4,729-byte input contains 63 escaped strings followed by `73`;
the query selects index 63. Its SHA256 is
`df16fa8d7f1071224bf3bab3dd680ef58ca83ea0fa7097e0431bbe9b614c96bd`.

| Metric | Previous boundary | Retained boundary |
| --- | ---: | ---: |
| Dynamic instructions | 50,862 | 39,422 |
| `stringTail` entries | 63 | 1 |
| Instructions in `stringTail` | 21,777 | 362 |
| Instructions in `stringCached` | 13,167 | 17,202 |
| Relaxed string assembly entries | 42 | 63 |
| Instructions in relaxed string assembly | 7,434 | 12,471 |

Total instructions fall **22.49%**. This is a reduction in repeated Go probing,
not a reduction in assembly calls: more strings enter the complete scanner,
and more subsequent prefixes reuse its suffix events. The additional scanner
and cached-event work costs less than the removed short probes.

The C reference executes 12,767 instructions in this same trace. The retained
version still uses 3.09 times as many; this change does not close the whole gap.
Go's cached-event loop alone accounts for 17,202 instructions (43.6% of the
retained total). It still interprets raw events in Go, whereas C integrates
classification and traversal differently. That is an implementation/boundary
difference, not evidence of a pure compiler penalty. Further event-processing
work would need its own comparison against short-key and ordinary-string
queries before moving more state into assembly.

Raw traces and per-function counts are in `instructions/*-warm-array-*.json`.
The earlier third-entry traces are also retained; they include the same 2,195
extra pool/runtime instructions in every variant and are not the counts used
in this table.

## Current retained version versus the original C backend

The current retained executable was compared directly with the original
C-generated NEON executable in a separate, completed run: 26 workloads,
eight balanced rounds of 200 ms, CPU 16 on Neoverse-N2, `GOMAXPROCS=1`,
and asynchronous Sonic GC disabled. Both existing executables were built
with Tango 1.0.0.1153 / Go 1.26.4 and identical compiler/linker options.
Exec wrappers fix `argv[0]`; the target hashes and build information are in
`current-vs-c-binaries/manifest.json`. No compilation, tests or debugger ran
concurrently. These ratios compare samples from this run, rather than dividing
historical C timings by the later Go timings. No production source changed.

Results: [timing-current-vs-c/summary.json](../output/arm-ast-boundary-refinement/timing-current-vs-c/summary.json).
Timings below are medians; changes are medians of within-round ns/op ratios.
Negative means faster. Every paired allocation count and B/op sample matches.

| Benchmark | Original C (µs) | Current (µs) | Current vs C |
| --- | ---: | ---: | ---: |
| GetFull_Sonic | 10.775 | 9.310 | -13.58% |
| GetOneSafe_Sonic | 3.385 | 3.749 | +10.78% |
| GetOne_Sonic | 3.335 | 3.686 | +10.52% |
| GetScanArrayIndex/denseStrings/compact/last | 1.489 | 3.754 | +152.21% |
| GetScanArrayIndex/denseStrings/compact/missing | 1.435 | 3.712 | +158.78% |
| GetScanArrayIndex/denseStrings/spaced/last | 1.609 | 4.030 | +150.45% |
| GetScanArrayIndex/denseStrings/spaced/missing | 1.560 | 3.968 | +154.40% |
| GetScanArrayIndex/emptyContainers/compact/last | 1.716 | 2.257 | +31.35% |
| GetScanArrayIndex/emptyContainers/compact/missing | 1.677 | 2.204 | +31.22% |
| GetScanArrayIndex/emptyContainers/spaced/last | 1.808 | 2.494 | +38.00% |
| GetScanArrayIndex/emptyContainers/spaced/missing | 1.778 | 2.436 | +36.99% |
| GetScanArrayIndex/mixed/compact/last | 1.047 | 1.472 | +40.54% |
| GetScanArrayIndex/mixed/compact/missing | 1.007 | 1.427 | +41.71% |
| GetScanArrayIndex/mixed/spaced/last | 1.148 | 1.760 | +53.38% |
| GetScanArrayIndex/mixed/spaced/missing | 1.106 | 1.696 | +53.30% |
| GetWithManyCompare_Sonic | 2.506 | 1.461 | -41.67% |
| ParseSeven_Sonic | 14.188 | 15.608 | +9.91% |
| Parser_Sonic | 4.370 | 4.592 | +5.11% |
| PathCacheLongQueries/matchKey4096 | 0.861 | 0.593 | -31.20% |
| PathCacheLongQueries/missingAfterLongKey4096 | 0.537 | 0.407 | -24.24% |
| PathCacheLongQueries/skipKey128 | 0.127 | 0.151 | +19.79% |
| PathCacheLongQueries/skipKey4096 | 0.616 | 0.494 | -19.78% |
| PathCacheLongQueries/skipNumber4096 | 0.527 | 0.627 | +18.93% |
| PathCacheLongQueries/skipString4096 | 0.628 | 0.504 | -19.79% |
| PathCacheLongQueries/skipStringDense4096 | 0.745 | 1.016 | +36.35% |
| PathCacheLongQueries/skipStringSparse4096 | 0.648 | 0.540 | -16.78% |

The broad path query remains about 10.5% slower than C; `Parser_Sonic`
(`Parse + LoadAll` of the current layer) is 5.1% slower. `GetFull` scans and
returns a raw node 13.6% faster, and the many-key-comparison workload is
41.7% faster. These are different operations, so no aggregate AST speedup is
inferred. Array traversal remains a larger deficit: mixed arrays are
40.5–53.4% slower, empty-container arrays 31.2–38.0% slower, and dense escaped
string arrays 150.4–158.8% slower (about 2.5 times C elapsed time). The last
group improved 9.7–17.7% over the preceding Go boundary in the independent
`timing-matched` comparison; both findings hold without implying C parity.
