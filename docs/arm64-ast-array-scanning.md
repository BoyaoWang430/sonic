# ARM64 AST array scanning

This work addresses the remaining array-traversal gap after the
[string-cache handoff refinement](arm64-ast-boundary-refinement.md). The goal
remains a maintainable Go parser with focused assembly scanners. Go owns path
selection, grammar, cursors, errors, validation policy, and allocation; assembly
completes one string or container scan. The retained optdec Runs implementation
and its backend selection are outside this change.

Artifacts are under
[`output/arm-ast-array-optimization`](../output/arm-ast-array-optimization/).
The source snapshot, candidate binaries, hashes, oracle records, raw benchmark
samples, and instruction traces are preserved there.

## What the baseline spends instructions on

The preceding implementation was faster than the first Go version, but dense
escaped-string array traversal still took about 2.5 times the C elapsed time.
Mixed and empty-container arrays also remained slower. These are distinct
workloads, so their instruction counts must not be treated as an AST-wide ratio.

The traces cover one warmed `ast.(*Parser).getByPath` invocation and its callees,
through return. Each query selects the last element of the compact array fixture.
They exclude result-copy work outside that parser entry. Debugger elapsed time
is not used as a benchmark measurement.

| Query | Original C instructions | Previous Go/assembly instructions |
| --- | ---: | ---: |
| Mixed-value array | 9,777 | 14,777 |
| Empty-container array | 16,184 | 21,605 |
| Dense escaped-string array | 12,767 | 39,422 |

The dominant work differs by input:

- In the dense-string array, Go's cached-event loop uses 17,202 instructions,
  or 43.6% of the total. It repeatedly locates an event, reloads the source
  byte to distinguish quote from backslash, advances the cursor, and checks
  whether another cached event remains. SIMD classification has already found
  those bytes, but Go still interprets their escape relationships individually.
- In the empty-container array, 63 complete container scans use 13,161
  instructions, or 60.9%. Each `[]` or `{}` initializes the full scanner and
  classifies up to 64 bytes, including bytes after the immediate closing bracket.
  The Go adapter and `skipContainerFast` add another 3,024 instructions.
- In the mixed array, 28 container scans use 6,125 instructions, or 41.4%.
  `GetByPath` and `skipValue` use 2,639 and 2,625 instructions respectively;
  seven numeric-tail probes add 1,043. This leaves meaningful work in both
  scanning and the Go traversal/cursor boundary.

The string kernel also constructs quote and backslash masks separately, and its
short final block classifies bytes individually. Those are implementation
choices, not consequences of Go grammar ownership. Likewise, computing results
through a mutable cursor pointer creates intermediate state publication that
an inlined C path can often avoid. These observations do not assign the whole
gap to the Go compiler.

## Candidate responsibilities

Six candidate binaries were compared with the same frozen baseline:

| Candidate | Change | Responsibility retained |
| --- | --- | --- |
| `cache-first` | Finish from the first cached quote, or hand the first cached backslash and remaining string to the complete scanner. | Go reuses plain-string classifications; assembly owns escape scanning after the handoff. |
| `cache-local` | Keep a local event mask and shift away consumed escapes instead of reloading the cache and recomputing offsets. | Go continues to interpret cached escape events. |
| `kernel-mask` | Reduce weighted quote and backslash vectors together with shared pairwise additions. | The existing complete string scan and result contract remain unchanged. |
| `kernel-tail` | Extend the mask candidate with a bounded overlapping vector load for the last partial block when the input has at least 16 bytes. | Shorter inputs retain scalar loads; no logical overread or padding is introduced. |
| `array-empty` | Recognize an immediate matching closing bracket in the Go `SkipContainer` adapter. | Go handles the trivial empty boundary; general container scanning remains in assembly. |
| `array-cursor` | Pass the value-skip cursor by value and return `(end, start/error)` together. | Go retains type dispatch and error policy, publishing the cursor once per skipped value. |

The empty-container candidate adds five production Go lines. Its focused test
varies the opening offset, checks truncated openings, and verifies that malformed
trailing text does not affect an already completed empty container. The cursor
candidate changes two Go files with zero net physical lines and no new helper.
Both preserve the relaxed traversal policy, including ignored skip errors.

The string candidates use the existing bounded scanner interface. Raw suffix
events remain raw: an escaped quote in a later token must not be filtered using
the previous token's string state. The first-escape result and EOF cursor also
remain part of the scanner contract.

## Screening evidence

The first screen covers 42 scenarios, seven rotating rounds, and 50 ms per
benchmark on CPU 16. It uses Tango 1.0.0.1153 / Go 1.26.4, ARM64 v8.0,
`GOMAXPROCS=1`, and disabled asynchronous Sonic GC. No compilation, tests, or
debugger tracing run concurrently. Seven rotations are not the full mirrored
14-round order for seven binaries; this is candidate screening, not final
confirmation or a statistical-significance claim.

The following are median within-round ns/op changes versus `before` from
[`screen-candidates/summary.json`](../output/arm-ast-array-optimization/screen-candidates/summary.json).
Negative means faster; these numbers must not be presented as final gains.

| Candidate | Representative screen result |
| --- | --- |
| `cache-first` | Dense arrays: -21.1% compact and -19.8% spaced; short selected escaped string: +5.0%. |
| `cache-local` | Dense arrays: -4.7% compact and -5.4% spaced; short selected escaped string: +0.6%. |
| `kernel-mask` | Long dense skipped string: -14.3%; compact dense array: -5.4%; long plain skipped string: +3.1%. |
| `kernel-tail` | Long dense skipped string: -16.3%; compact dense array: -6.7%; long plain skipped string: -1.9%. |
| `array-empty` | Compact empty-container array: -73.0%; compact mixed array: -29.6%. |
| `array-cursor` | Compact mixed array: -2.6%; compact dense array: -2.2%; most other effects are small. |

A second screen compares combinations using 64-byte blocks, 32-byte blocks,
and a 32-byte first block followed by 64-byte blocks. The Go handoff, cursor,
empty-container fast path, and mask/tail implementation are held constant.
It includes the preceding version and C reference: five rotating rounds of
50 ms across 42 scenarios in `screen-combined/`. Block size changes the amount
of overscanning, tail-cache reuse, and repeated escape-parity work; smaller
blocks are not assumed to be universally better.

The retained combination uses `cache-first`, the by-value cursor, the empty
container fast path, and the shared mask/vector-tail scanner. Path string scans
start with 32 bytes and continue in 64-byte blocks. The final assembly peels
that initial setup out of the loop, so later blocks do not repeat a first-block
check. The ordinary relaxed-string entry keeps its 64-byte block policy.

Fixed 32-byte blocks were rejected: they reduced dense-array work but increased
long plain-string scan time substantially by repeating classification more
often. The first-32/then-64 policy retains the smaller initial scan without
paying that cost throughout long strings. No new ABI, cache fields, mode flags,
or parser states are introduced.

The source-level boundary is explicit: cached plain strings can finish in Go;
from the first cached backslash onward, the complete scanner owns escapes and
EOF. The first escape is not consumed twice and no pending-escape state crosses
the ABI. Empty containers return in Go; nonempty containers retain the complete
scanner. Go still decides which values to skip and how to report errors.

## Why not move skip-N-values into assembly

A general bulk value skipper would need type dispatch, commas and whitespace,
nested-container handling, literal widths, element counts, and exit/error
cursors. That would move the array grammar into assembly. The current relaxed
behavior also ignores some skip errors and matches only the opening bracket's
kind inside a skipped container; counting structural commas with a generic
mixed-bracket depth would change malformed-input behavior.

Keeping that bulk operation compatible would require additional stop states
or replay through Go. It could be evaluated if later traces show per-element
call overhead dominating, but it is not needed to remove the identified cached
event loop, unnecessary empty-container classification, and mask-construction
work. A narrower future string-run scanner would still need an explicit
delimiter/exit contract and separate evidence for its maintenance cost.

## Validation and compatibility

All six independent candidates match the preceding Go implementation on all
40,946 native oracle records, including return values, exact cursors, active
grammar stacks, and token state. The deterministic JSONL SHA256 remains:

```text
d0d9890199e986afe12deb0cf9168806da295fb43b808794cbb1587a2d3e46b3
```

Candidate source hashes, build hashes, test logs, and oracle outputs are in
`cache-candidates/`, `kernel-candidates.json`, and `array-go-candidates.json`.
The new empty-container test supplements existing guard-page, offset, escape,
and malformed-path tests. Final integration validation is recorded below.

This is equivalence to the preceding Go backend, not complete C semantic parity.
The earlier C/Go audit records 2,181 existing differences, including malformed
escape acceptance and error-cursor/code differences. They are neither normalized
away nor claimed to be fixed by this performance work; see the compatibility
discussion in [the preceding refinement](arm64-ast-boundary-refinement.md).

## Final retained result

Final results are in
[`timing-final/summary.json`](../output/arm-ast-array-optimization/timing-final/summary.json)
and the [complete table](../output/arm-ast-array-optimization/final-table.md).
The final run compares the freshly built preceding implementation, the retained
main-workspace executable, and the original C executable: 42 scenarios, six
fully balanced rounds of 200 ms, CPU 16, and `GOMAXPROCS=1`. All use Tango
1.0.0.1153 / Go 1.26.4, ARM64 v8.0, CGO disabled, and
`-ldflags=-checklinkname=0`. Exec wrappers fix `argv[0]`; actual executable
hashes and build information are in `argv0/manifest.json`. No tests, compilation,
or debugger tracing ran concurrently with timings.

Timings below are medians; changes are medians of within-round ns/op ratios.
Negative means faster. Allocations and B/op match in every paired sample across
all three variants. The table uses successful last-index array queries;
missing-index results remain available in the complete table.

| Scenario | Before (µs) | Retained (µs) | Original C (µs) | vs before | vs C |
| --- | ---: | ---: | ---: | ---: | ---: |
| Dense string array, compact | 3.754 | 2.251 | 1.488 | -40.01% | +51.34% |
| Dense string array, spaced | 4.029 | 2.595 | 1.611 | -35.61% | +61.06% |
| Mixed array, compact | 1.472 | 0.996 | 1.047 | -32.32% | -4.92% |
| Mixed array, spaced | 1.762 | 1.371 | 1.149 | -22.23% | +19.32% |
| Empty-container array, compact | 2.253 | 0.606 | 1.718 | -73.09% | -64.73% |
| Empty-container array, spaced | 2.493 | 0.989 | 1.807 | -60.35% | -45.23% |
| Skip one 4KB dense escaped string | 1.016 | 0.857 | 0.745 | -15.64% | +14.98% |
| Skip one 4KB plain string | 0.503 | 0.485 | 0.629 | -3.47% | -22.73% |
| GetOne_Sonic | 3.685 | 3.667 | 3.334 | -0.48% | +10.02% |
| GetFull_Sonic | 9.325 | 9.251 | 10.795 | -0.82% | -14.14% |
| GetWithManyCompare_Sonic | 1.459 | 1.387 | 2.506 | -4.97% | -44.63% |
| Parser_Sonic | 4.566 | 4.534 | 4.374 | -0.65% | +3.70% |
| ParseSeven_Sonic | 15.444 | 15.284 | 14.175 | -0.88% | +7.84% |

The largest positive paired median versus the preceding version is +0.96%
(`returnObject/sparse127`). Short escaped selected strings are +0.05%, and
many short escaped fields are +0.37%; these are treated as essentially neutral.
There is no measured multi-percent regression in this matrix. Results on this
Neoverse-N2 host are not a guarantee for other ARM cores.

`GetFull` scans/validates and returns a raw root node. `Parser_Sonic` performs
`Parse + LoadAll` of the current layer, retaining child containers as raw nodes.
`ParseSeven` parses and performs seven nested path lookups. These operations
must not be combined into an unsupported aggregate AST speedup.

The retained fifth-entry traces use the same compact arrays, path `[63]`,
input lengths and SHA256 values as the baseline/C traces. Eight fixed benchmark
iterations let the pool warm before the counted parser invocation. Counts cover
parser entry, callees, and return; they exclude result-copy work outside that
entry. Debugger timings are kept separate from performance measurements.

| Query | C instructions | Before | Retained | vs before |
| --- | ---: | ---: | ---: | ---: |
| Dense escaped-string array | 12,767 | 39,422 | 26,691 | -32.29% |
| Mixed-value array | 9,777 | 14,777 | 11,519 | -22.05% |
| Empty-container array | 16,184 | 21,605 | 8,568 | -60.34% |

In the dense array, `stringCached` falls from 17,202 to 2,604 instructions.
The complete string scanner rises from 12,471 to 14,844: it now processes the
escapes formerly consumed in Go, and may reclassify part of a cached prefix.
The total still falls by 12,731 instructions. Container assembly calls fall
from 63 to zero in the empty-container array and from 28 to 14 in the mixed
array. These counts explain the source-level improvements without claiming
that each instruction has the same execution cost.

The remaining dense-string gap is real: the parser still executes 2.09 times
C's instructions and takes about 1.51–1.62 times C's elapsed time in the array
cases. The complete scanner accounts for 55.6% of retained instructions; Go
still performs per-value traversal and helper calls. C can inline its intrinsic
scan into traversal and share setup across values. Cache reclassification,
result bookkeeping and those call boundaries are implementation differences;
the residual gap is not a measurement of a pure Go-compiler penalty.

Spaced mixed arrays remain 17.4–19.3% slower than C, even though compact mixed
arrays are slightly faster. Their whitespace and per-element Go control flow
remain a separate target. No general array parser or skip-N-values state
machine has been added to assembly to close these residual gaps.

The final implementation preserves all 40,946 oracle records and passes:

- Tango native/kernel/AST/decoder tests with VM options and `GOGC=1`.
- The root API, AST, decoder, encoder and other ARM-usable main-module packages
  in both default and optimized/VM runs. Five x86/JIT packages fail to build on
  ARM due to missing loader ABI definitions; the unchanged baseline reproduces
  the same five failures. The logs retain these failures rather than labeling
  the full repository run successful.
- Native/kernel/AST tests with stock Go 1.26.1 and Go 1.20.14.
- The retained optdec Runs C-parity regression tests.
- Guard-page and malformed/truncated-input regressions, plus 89,319 fuzz
  executions using an independent byte reference for the block scanner.
- RISC-V `rva22u64` native and kernel test-binary cross-builds. They were not run
  on RISC-V hardware, so no RISC-V performance result is claimed.

The raw `./...` invocation also discovers saved source snapshots under the
ignored `output/` directory. `test-main-packages.json` records the real module
package list used to separate those artifacts from source-package validation.
All validation logs remain under the artifact directory.

The block-scan tests now compare against the independent byte reference, since
the ordinary and block assembly entries share code. A previous assertion that
a quote at absolute offset 63 must return zero suffix width was removed: with
an initial 32-byte block, the next block spans `[32,96)`, so `[64,96)` is a valid
cached suffix. Exact raw events, bounded widths, EOF/error state and guard-page
checks remain in place.

Counting physical lines, including comments and blanks but excluding tests,
the three changed production Go files total 431 → 426 lines (net -5).
The shared assembly header totals 146 → 176 lines (net +30), counted once even
though it expands into two entries. Tests add 19 net lines. There are no new
assembly entry points or cache/state fields. AST package sources and every
optdec source remain byte-identical to the initial snapshot.
