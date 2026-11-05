# ARM64 decoder regression investigation

The latest source-parity experiment and exact C/Go instruction comparison are in
[C-aligned Go parser experiment](arm64-native-go.md#c-aligned-go-parser-experiment).
It is opt-in; the production backend and historical measurements below are retained.

The initial investigation below preserves the migration's original assembly
excerpts, profiles, and measurements. They predate the subsequent string-prefix
and whitespace changes. The demonstrated prefix fix and an ARM64 whitespace
classifier optimization are now applied; see the
[follow-up comparison](arm64-native-go.md#string-prefix-and-whitespace-optimization)
for results against both the initial framework and generated baseline.
The [current-backend investigation](#current-optimized-backend-remaining-regression)
below measures the remaining difference after both optimizations.

Both investigations precede the subsequent
[Go parser orchestration optimization](arm64-native-go.md#go-parser-orchestration-optimization).
Their measurements and experimental variants remain historical evidence; the
linked section documents the later implementation and its separate comparison.
For attribution of the final Go parser's remaining gap, see
[Final Go parser: residual attribution](#final-go-parser-residual-attribution).

The subsequent [Go source changes](arm64-native-go.md#further-go-source-optimization)
apply selected findings from this investigation, including the short-string
and whitespace call-boundary changes. That section contains fresh combined
measurements and instruction counts. The earlier uses of "final" in this
report refer to the preceding parser snapshot, not those later changes. The
[latest parser-loop reduction](arm64-native-go.md#parser-loop-live-state-reduction)
subsequently reduces the retained 143,548-instruction count to 140,363, with
its own source snapshot, exact traces, and matched timing.
The subsequent [parser phases and string call boundary changes](arm64-native-go.md#parser-phases-and-string-call-boundary)
reduce that count to 132,607. The new matched comparison and remaining-gap
accounting are in that section; the investigations below retain their original
snapshots and should not be read as measurements of the latest source.
The latest [whitespace and bounded-string changes](arm64-native-go.md#whitespace-probes-and-bounded-string-windows)
reduce the count further to 128,589, primarily by removing comparisons and
branches. That section also lists which earlier findings are fixed, mitigated,
still present, or rejected after measurement.
The later [loop checks and active-frame caching](arm64-native-go.md#loop-checks-and-active-frame-caching)
reduce the count to 121,382. That result trades additional stack preservation
for fewer checks, descriptor accesses, and normalization operations; its
end-to-end decode measurements and limitations are reported separately.

The Tango comparison's +28.3% generic decode and +50.8% typed decode largely
come from the shared parsing stage. The added absolute time is similar:
13.989 us for generic decode and 13.233 us for typed decode. Typed decode's
smaller baseline makes the percentage larger.

This investigation compares the exact Tango binaries from the migration
benchmark, using `go1.26.4.bm.1.0.1153_test`, Neoverse-N2 CPU 16,
`GOMAXPROCS=1`, and `SONIC_NO_ASYNC_GC=1`. It combines CPU sampling,
disassembly, isolated parser stages, and a one-change experiment. Production
implementation files were not changed during the investigation.

## Stage measurements

Identical temporary benchmarks were compiled into both versions. Both use
CopyString, as the original decoder benchmarks do. Results are medians of
five 200 ms samples per version; every stage reports zero allocations.

| Stage | Generated baseline, us | Go/ABI0 candidate, us | Added time, us |
| --- | ---: | ---: | ---: |
| Twitter prepare, typed | 0.298 | 0.297 | approximately zero |
| Twitter prepare and parse, typed | 9.214 | 22.385 | 13.171 |
| Twitter prepare and parse, generic | 9.214 | 22.431 | 13.217 |
| Twitter parse only, resident batch, typed | 8.992 | 22.464 | 13.472 |
| Compact prepare and parse, typed | 6.415 | 16.054 | 9.639 |

Preparation includes the pool lifecycle and input copy. Parse-only batches
restore input and parser state outside the timer; their storage is warm, so
these stages must not be added together to reconstruct end-to-end time.
The common 13 us parsing penalty explains most of the end-to-end difference.
This does not prove that object materialization has exactly unchanged timing.

The actual decoder fixture is 13,035 bytes, with 414 strings (307 keys),
643 emitted nodes, and 4,121 bytes of whitespace. Of the strings, 272 have
bodies shorter than 16 bytes and 412 contain no escapes. There are only 56
numeric tokens, including one floating-point token. The document is all ASCII.
The compact fixture is 8,914 bytes. Removing whitespace reduces the extra
time by about 3.5 us, but also changes block alignment and branch behavior;
that difference cannot be assigned entirely to one whitespace function.

## Assembly differences

### Redundant ordinary-string prefix loop

In `internal/native/kernels/string_decode_arm64.s`, `decode_neon_first`
already knows the prefix length up to the quote/backslash/control event.
Nevertheless, `decode_neon_prefix` walks that prefix one byte at a time even
when source and destination are identical and no copy is necessary:

```asm
decode_neon_prefix:
    MOVBU (R0), R11
    CMP R0, R2
    BEQ decode_neon_prefix_advance
    MOVB R11, (R2)
decode_neon_prefix_advance:
    ADD $1, R0
    ADD $1, R2
    SUB $1, R1
    SUB $1, R24
    CBNZ R24, decode_neon_prefix
```

The ordinary in-place path executes eight instructions per byte despite
skipping the store. This fixture produces 2,997 such byte iterations, about
24,000 loop instructions per document, based on the input and instruction
path rather than hardware instruction counters. The old parser uses the
quote mask's trailing-zero count to advance directly, then writes the node.

An isolated source copy was changed only to advance the known prefix in one
step when `R0 == R2`. String decoding tests passed. Five alternating 300 ms
samples per binary, using the same Tango compiler and CPU, gave:

| End-to-end workload | Baseline, us | Current candidate, us | Prefix fix only, us |
| --- | ---: | ---: | ---: |
| Generic decode | 49.453 | 63.388 | 60.284 |
| Typed decode | 26.052 | 39.387 | 36.604 |

This one change recovers 3.104 us and 2.783 us respectively, about 21–22% of
the observed regression in that run. It is a demonstrated contributor, not
an explanation for all the slowdown. The experimental fix was not applied to
the working-tree implementation during this investigation; it was applied in
the follow-up optimization described above. These original measurements are
preserved rather than relabeled as measurements of the updated implementation.

### Repeated calls and state spills

The old `ParseWithPadding` enters a large native parser with inlined string
processing, whitespace processing, and node stores. The new hot path crosses
several non-inlined functions:

```text
parseImpl -> step -> text -> ParseString -> stringDecode.abi0
                      -> add
```

Actual Tango code establishes an 80-byte frame in `text`, a 96-byte frame
in `ParseString`, and a 32-byte frame in `add`. It spills/reloads parser state
and offsets, marshals ABI0 arguments/results, and performs bounds checks.
These operations recur across 414 strings and 643 nodes. Stack checks are not
equivalent to taking the stack-growth slow path, and these frames are not heap
allocations. The assembly leaf itself has no stack-switching prologue.

The old string scan processes 32-byte blocks; the new NEON loop processes
16-byte blocks and initializes constants on each call. Short keys still enter
the vector path because the provided length is the remaining document length.
Block width alone is not enough to attribute a measured percentage.

### Whitespace classification and wrapper overhead

`NonSpaceMask` is not inlined. Its normal wrapper path has approximately
24 instructions, including a 48-byte frame, slice bounds check, width clamp,
ABI0 argument stores, call, and result reloads. `spaceSlow` adds another
48-byte frame, and cached blocks still pass through this Go helper.

The old whitespace path is inlined into the parser and consumes the next
nonspace byte directly. Its 64-byte refill is unrolled across four vectors;
the new kernel loops over four 16-byte blocks with more classification,
constant setup, shift/merge, and loop-control instructions.

In the five-second typed-decoder CPU profile, parsing accounts for about
55% of the candidate's sampled CPU time. `space` and its descendants account
for about 16%; `stringDecode` alone accounts for about 14.5%. `step` and `add`
have about 9.5% and 4.2% flat time. These are sample proportions, not additive
causal estimates or percentages of the regression.

## Optimization order identified by the investigation

1. Apply and validate the demonstrated no-copy prefix advance (implemented in
   the follow-up).
2. Make ordinary node writes and cached whitespace hits small enough to inline;
   separate growth/error handling from their common paths.
3. Reduce per-token state-machine call boundaries and repeated state loads.
4. Restore an efficient ARM-specific 64-byte whitespace classification path
   (implemented in the follow-up).
5. Investigate numeric and strict-escape paths with their own workloads. The
   current Go float path can scan a token again before conversion, but this
   Twitter fixture's single float cannot account for most of its regression.

These changes can retain the Go/ABI0 architecture and its standard-toolchain
build. The measurements do not require reverting to the generated backend.

## Local evidence

Artifacts are under `output/arm-native-analysis/` (ignored by Git):

- `baseline-*.cpu`, `candidate-*.cpu`, and `*.top.txt` / `*.cum.txt`: CPU profiles.
- `baseline-parser-core.asm`, `candidate-parser.asm`,
  `candidate-parse-string.asm`, `candidate-string-decode.asm`, and
  `candidate-nonspace.asm`: disassembly of the measured Tango binaries.
- `stages-medians.csv`, `stages-metadata.json`, `arm_analysis_fixture_test.go.txt`,
  and `run-stages.py`: parser-stage measurements and temporary benchmark source.
- `ablation-medians.csv`, `ablation-metadata.json`,
  `ablation-string-prefix.diff`, and `ablation-*.log`: controlled prefix experiment.
- `twitter-composition.json`, `twitter-token-counts.json`, and
  `twitter-parser-operations.json`: fixture composition and operation counts.

The Tango distribution omits `objdump`, `nm`, and `pprof` tools. Cached Go
1.20.14 inspection tools and GNU objdump were used to inspect the exact
Tango-produced binaries; the workload binaries were not rebuilt with Go 1.20.

## Current optimized backend: remaining regression

This investigation uses the current implementation after both optimizations.
It retains the same Tango compiler, Neoverse-N2 CPU 16, `GOMAXPROCS=1`, and
`SONIC_NO_ASYNC_GC=1`. It uses fresh stage measurements, profiles of the exact
previously benchmarked decoder binaries, hardware counters, and temporary
instrumented or modified source copies. Production implementation files are
unchanged by these diagnostic experiments.

### The remaining time is in parsing

The temporary stage fixture now also measures `FromDom` against a pre-parsed
input. Five alternating 200 ms samples produce these median times:

| Twitter stage | Generated, us | Current, us | Difference, us |
| --- | ---: | ---: | ---: |
| Prepare, typed | 0.298 | 0.297 | approximately zero |
| Prepare and parse, typed | 9.223 | 19.755 | +10.532 |
| Prepare and parse, generic | 9.222 | 19.746 | +10.524 |
| Parse only, resident batch, typed | 8.996 | 19.715 | +10.719 |
| FromDom, typed | 16.088 | 16.026 | -0.062 |
| FromDom, generic | 40.212 | 39.519 | -0.693 |
| Compact Twitter prepare and parse, typed | 6.422 | 14.039 | +7.617 |

Preparation and parsing report zero allocations. The two materialization
workloads retain their respective allocation counts across versions. This
localizes the remaining penalty to the parser, rather than input copying or
turning parsed nodes into Go objects. The compact Twitter case still has a
large penalty, so whitespace cannot explain it all. This compact Twitter
fixture is distinct from the repeated-record `Whitespace/Compact` benchmark.
Stage storage is warm and stage times are not an additive reconstruction of
the public decoder benchmark.

### Hardware counters confirm substantially more executed work

Linux ARM PMUv3 permits user-mode `cycles`, `instructions`, and `branch-misses`
on this host. Three alternating runs per binary use exactly 100,000 benchmark
iterations each. Medians below divide whole-process counter totals by that
iteration count:

| Workload and counter | Generated | Current | Change |
| --- | ---: | ---: | ---: |
| Full typed decode: instructions/op | 247,524 | 386,260 | +56.0% |
| Full typed decode: cycles/op | 80,306 | 112,464 | +40.0% |
| Full typed decode: branch misses/op | 227.43 | 540.25 | +137.5% |
| Prepare and parse: instructions/op | 81,588 | 221,176 | +171.1% |
| Prepare and parse: cycles/op | 28,692 | 61,080 | +112.9% |
| Prepare and parse: branch misses/op | 16.30 | 300.88 | +1745.8% |

The parser adds about 139,587 retired instructions per document; the complete
typed decoder adds about 138,736. Their close agreement independently supports
the stage timing result. Parser IPC increases from about 2.84 to 3.62; the
current implementation executes many more instructions and incurs more branch
misses, rather than showing a lower aggregate rate of instructions retired per
cycle.

Counters include process startup, fixture setup, and benchmark calibration;
they are normalized estimates, not a counter region around one parser call.
Both variants use the same fixed iteration count and environment. The `branches`
event is unsupported here, so there is no measured branch-miss rate. Neither
branch-miss counts nor static instruction counts assign a precise time share
to a particular function.

### The extra work has concrete sources

Temporary Go instrumentation and unconditional source-call paths establish
these counts on the actual Twitter fixture, for both typed and generic
parsing, without node-buffer growth:

| Operation | Invocations per document |
| --- | ---: |
| Initial `value` entry | 1 |
| `step` | 989 |
| `valueReady` | 336 |
| `text` / `ParseString` / `stringDecode` | 414 each |
| `add` | 643 |
| `literal` | 106 |
| `close` | 67 |
| `spaceSlow` | 709 |
| `NonSpaceMask` / `nonSpaceMask` | 161 each |
| `number.ParsePrefix` | 56 |

Thirteen verified or unconditional source-derived call edges total at least
4,471 function boundaries per document. This excludes materialization and
some numeric/runtime helper calls. The generated parser core has no internal
CALL instructions: much of its string, whitespace, state-transition, and
node-writing work stays inside one assembly routine.

The current assembly makes these differences visible:

- `ParseString` has a 96-byte frame and executes 42 instructions on its normal
  successful path outside the assembly body, including argument/result
  marshalling. Four input words and five result words cross its ABI0 boundary,
  414 times per document. The assembly kernel still initializes its state and
  scans the string separately for each call. Of 414 strings, 272 have bodies
  shorter than 16 bytes, making fixed overhead significant.
- The no-growth `add` path executes 26 instructions, with a 32-byte frame.
  It checks capacity, reloads the node slice, checks the store's bounds, writes
  the node, and returns. This repeats 643 times despite no growth occurring.
- `step` has a 48-byte frame. It spills/reloads the receiver and frame index
  around string-key parsing, then checks the frame index again. State-machine
  dispatch and these memory operations repeat for each transition.
- `spaceSlow` and the `NonSpaceMask` wrapper each have 48-byte frames. The
  latter executes 24 normal-path instructions outside the NEON body. The
  generated parser's source-path replay and the current parser's instrumentation
  both give 161 whitespace blocks: the difference is repeated helper/control
  work, not additional block scanning.
- Current literal validation loops over characters and bounds checks. The
  old C implementation and its actual assembly use explicit, unrolled byte
  comparisons. Attributing a single packed machine comparison to this measured
  binary would be incorrect.

The Tango compiler's diagnostics explain why these helpers survive as calls:
its ordinary inlining budget is 80, versus costs of 95 for `add`, 180 for
`spaceSlow`, 206 for `text`, 840 for `step`, 136 for `ParseString`, and 90 for
`NonSpaceMask`. The no-whitespace `space` entry already inlines at cost 75;
claiming that *all* whitespace handling is out of line would be incorrect.
These are this compiler's heuristic costs, not executed instruction counts.

The 55 integer tokens use fused, single-pass integer parsing. Only one float
takes the scanning/conversion path. Broad claims that all numbers are scanned
twice, or that floating-point conversion dominates this fixture, are not
supported. Unchanged allocations and materialization timings likewise do not
support GC or object construction as the main explanation.

The evidence supports a problem in the current parser's granularity and hot
paths: more scalar control, function boundaries, state traffic, and checks
around relatively small operations. It does not establish an unavoidable
performance limit of Go or of a Go/ABI0 architecture. The boundary counts also
do not prove that CALL instructions alone account for all 10.5 us.

### Controlled call-boundary experiments

Two independent temporary variants test the call-boundary explanation against
an unmodified control rebuilt from the same source snapshot and fixtures:

1. Expand the existing `add` logic only at its string-node call site in `text`.
   This removes 414 `add` calls on Twitter while retaining the growth condition
   and behavior; other node types still call `add`.
2. Put the no-gap and cached-mask-hit logic in a boolean helper that actually
   inlines at the three FSM call sites (compiler cost 44). Cache misses still
   use the original `spaceSlow` implementation.

All variants pass focused optdec tests and fixture correctness checks. Five
rotating 200 ms samples, CPU 16 and the same Tango settings give these median
times in microseconds:

| Workload | Control | Direct string-node write | Inline cached whitespace |
| --- | ---: | ---: | ---: |
| Twitter prepare and parse, typed | 19.764 | 18.955 | 19.202 |
| Compact Twitter prepare and parse, typed | 13.995 | 13.278 | 14.325 |
| Full typed decode | 36.282 | 35.414 | 36.069 |
| Full generic decode | 59.817 | 59.765 | 59.947 |

The direct-write edit recovers 0.809 us in parsing and 0.868 us in typed
decoding. Its full typed-decode sample ranges do not overlap. The cached-hit
edit recovers 0.562 us in Twitter parsing, but makes compact parsing 0.330 us
slower; its full typed-decode sample ranges overlap. Both generic-decode
effects are small with overlapping ranges. All allocation counts are unchanged.

These demonstrate costs in the current helper organization, but each edit
recovers less than 1 us of the approximately 10.5 us parser penalty. The edits
also change compiler register allocation and layout: for example, the direct
string-node write grows `text`'s frame from 80 to 96 bytes. Their measured
effects are not a calibration of pure CALL/RET latency. They have not been
combined, so their gains must not be added. Neither experimental edit has
been applied to production source.

### Separating the Compact/Pretty regression

Four versions were rebuilt from one current source snapshot, varying only the
prefix change in `string_decode_arm64.s` and the whitespace changes in
`masks_arm64.s`/`runs_arm64.s`. The initial assembly hashes match the recorded
initial framework; Go logic, test fixtures, and compiler settings are identical
across all four versions. Five rotating 200 ms samples give:

| Repeated-record fixture | Initial, ns | Prefix only, ns | Whitespace only, ns | Both/current, ns |
| --- | ---: | ---: | ---: | ---: |
| Compact | 35967 | 36990 | 36594 | 37647 |
| Pretty | 46815 | 48146 | 46966 | 48405 |
| WideIndent | 67660 | 68886 | 65836 | 67133 |

The prefix edit alone regresses Compact and Pretty by about 2.8%, while still
improving Twitter typed and generic decoding by 6.9% and 4.7% in this run.
Thus the prefix change has an input-dependent effect; its Twitter benefit
does not establish that removing the loop is faster for every prefix length.

The Compact fixture contains 384 one-character keys and 128 escaped values
whose first ordinary prefix is also one character. Of those 512 logical
prefixes, 511 enter the modified NEON path; the last key uses a scalar tail.
Initial and prefix-only binaries have identical `stringDecode` entry addresses
and the same 2000-byte function size, because alignment padding absorbs the
extra code. Whitespace-only changes shift that function and later parser
symbols by 128 bytes, while preserving their alignment modulo 64. These facts
exclude a whole-function relocation for the prefix-only effect, but do not
exclude branch-address, scheduling, or other microarchitectural effects.

The Compact input has no JSON whitespace, so its whitespace-only difference
cannot be treated as time spent in the whitespace classifier. The four-way
experiment attributes regressions to complete source changes, not to a single
instruction or an established hardware mechanism.

A final temporary variant handles only the no-copy, one-byte prefix with
immediate `ADD $1`/`SUB $1` pointer updates, retaining the bulk `ADD R24` path
for every other length. It passes focused string tests. Its function entry,
2000-byte size, and later parser symbol addresses match the current variant.
Five alternating 200 ms samples give:

| Workload | Current, ns | Fixed one-byte prefix, ns | Change |
| --- | ---: | ---: | ---: |
| Compact records | 37634 | 36637 | -2.65% |
| Pretty records | 48511 | 46927 | -3.27% |
| Wide-indent records | 67039 | 65845 | -1.78% |
| Twitter typed decode | 36478 | 36549 | +0.19% |
| Twitter generic decode | 60782 | 60617 | -0.27% |

This recovers most of the prefix-only short-string penalty while retaining
the Twitter result within sample variation. It supports an address-dependency
explanation: bulk pointer advancement waits for the vector mask's derived
offset, whereas a predictable one-byte path can use a constant increment.
The experiment establishes the usefulness of this specialization; it does
not directly measure CPU scheduling or prove that dependency latency is the
only mechanism. The remaining roughly 1.7% Compact difference follows the
whitespace revision even when its kernels are bypassed, which is consistent
with a code-placement effect. An exact cache/predictor explanation remains
unproven. The specialization is diagnostic and has not been applied to
production source.

### Current evidence locations

- `output/arm-native-remaining/`: stage fixture, build commands, both binaries,
  five-round stage CSV, exact decoder CPU profiles, and measurement metadata.
- `output/arm-native-analysis/current/`: current disassembly, verified operation
  counts, `call-boundary-breakdown.json`, and the root-cause evidence summary.
- `output/arm-native-analysis/current/perf-binding/` and `perf-parser/`: raw
  counter outputs, benchmark iteration logs, commands, and normalized summaries.
- `output/arm-native-remaining/inlining/`: controlled call-boundary patches,
  compiler diagnostics, assembly, matched source manifests, and timing samples.
- `output/arm-native-analysis/prefix-whitespace-attribution/`: four-way source
  diffs, binary hashes, function layouts, and raw measurements; its
  `prefix-one-experiment/` subdirectory holds the final short-prefix diagnostic.

## Final Go parser: residual attribution

This investigation applies to the final fused Go parser and its two-window
short-string path, after all previously documented production optimizations.
The remaining Twitter typed prepare-and-parse gap is 12.924 versus 9.226 us,
about 3.7 us. Typed binding from an existing tape is already effectively equal
at 16.018 versus 16.003 us. These stages have different storage conditions and
are not an additive reconstruction of full decoding.

All diagnostic edits below are isolated snapshots, not production changes.
They use the same Tango compiler, Neoverse-N2 CPU 16,
`GOMAXPROCS=1`, and `SONIC_NO_ASYNC_GC=1`. Each timing experiment has its own
matched control and five alternating/rotating 200 ms samples. Build/test work and
measurements were serialized. Differences between separate experiments must
not be summed as independent portions of the residual gap.
Compiler settings remain at defaults except for the explicitly identified
package-scoped bounds-check diagnostic.

### More executed work, rather than lower aggregate IPC

Fresh alternating runs compare the exact final and generated binaries used in
the final benchmark, rather than combining counters from earlier revisions.
Three pairs of 100,000 iterations give:

| Twitter workload / event per normalized iteration | Generated | Final Go | Change |
| --- | ---: | ---: | ---: |
| Prepare + parse / instructions | 81589 | 177540 | +117.6% |
| Prepare + parse / cycles | 28662 | 40137 | +40.0% |
| Prepare + parse / branch misses | 16.48 | 119.10 | +622.5% |
| Whole typed decode / instructions | 247650 | 342534 | +38.3% |
| Whole typed decode / cycles | 80052 | 90639 | +13.2% |

The parser's aggregate IPC rises from 2.85 to 4.42. Its roughly 96,000 extra
instructions are close to the roughly 95,000 extra instructions in whole typed
decoding. This localizes the excess work to parsing; it does not identify every
instruction's cost or turn branch-miss counts into a cycle attribution.
Counters are user-mode whole-process totals divided by 100,000, including
initialization, fixture checks, and benchmark calibration. Total branches are
unsupported on this host, so no miss rate is inferred.
Initial instruction-event sampling attempts could not map their sample buffers.
A subsequent recording succeeded, and a separate probe found that per-thread
buffers also work inside the sandbox. However, the successful recordings show
strong instruction-pointer skew, so the exact instruction attribution below
uses debugger execution traces instead of sample percentages. The aggregate
`perf stat` counters and Go CPU profiles completed successfully.

A separate four-second CPU profile of the final prepare-and-parse stage places
37.0% of flat samples in `parseImpl`, 17.2% in `ParseStringShort`, 22.4%
cumulatively in whitespace refill and its descendants, 15.8% cumulatively in
the slow string path, 4.3% in number parsing, and 2.9% in input copying.
These are shares of current execution, not shares of the performance gap or
amounts removable by deleting a wrapper. The following controlled changes test
specific hypotheses instead.

### Exact dynamic instruction accounting

To identify the bulk of the extra instructions, an additional investigation
traces the unchanged stage binaries with GDB. It follows the third invocation
of `parseImpl` on the same 13,035-byte Twitter document, after parser storage
has been warmed. Temporary breakpoints stop at basic-block terminators; the
debugger executes each control-transfer instruction individually and counts
the original fixed-width instructions along each executed straight-line block.
Both parsers return success. This is instruction accounting, not timing under
the debugger. Asynchronous Go preemption and GC are disabled for tracing.

The generated parser executes **78,780** instructions and the final Go parser
**174,775**, an increase of **95,995**. This matches the approximately 95,951
extra instructions in the separate whole-process PMU experiment to within
44 instructions. Absolute totals differ because the debugger covers the parser
invocation, while PMU also includes preparation, copying, benchmark overhead,
and process initialization. Independent source/disassembly replay exactly
matches the traced string helpers, whitespace helpers, and integer paths.

The following categories are disjoint and account for every traced instruction.
An LDP/STP pair counts as one instruction. Stack accesses mean loads/stores
addressed through SP or the frame pointer; they include local state, argument
and result slots, and frame handling, not only compiler spills.

| Instruction class | Generated | Final Go | Extra | Share of net increase |
| --- | ---: | ---: | ---: | ---: |
| Stack loads/stores | 101 | 37568 | +37467 | 39.0% |
| Comparisons and branches, excluding calls/returns | 19736 | 56487 | +36751 | 38.3% |
| Register moves / constant formation | 13032 | 22747 | +9715 | 10.1% |
| Arithmetic and remaining operations, including SIMD | 33820 | 46151 | +12331 | 12.8% |
| Calls and returns | 6 | 2635 | +2629 | 2.7% |
| Non-stack loads/stores | 12085 | 9187 | -2898 | -3.0% |
| **Total** | **78780** | **174775** | **+95995** | **100%** |

Thus explicit stack traffic and comparisons/branches explain **77.3% of the
net instruction increase**. Calls themselves are only 2.7%. This distinguishes
the cost of branch-and-link instructions from the state preservation, argument
handling, and control flow induced by the current call boundaries. These are
shares of instruction count, not shares of latency or guaranteed removable work.

The main Go `parseImpl` body alone executes **95,192** instructions, including
**28,474 stack accesses**; the helpers contribute another 9,094 stack accesses.
For a concrete hot example, the main scan gateway executes eight consecutive
stack stores on each of 990 visits, and the following ready/dispatch gateway
executes four more on each visit. Those two sequences account for **11,880
stack-store instructions**, including paths that do not call a refill helper.
The return side of 241 whitespace-refill calls adds 5,302 instructions of
reloads and register moves. These counts identify state movement around the
loop and its joins, rather than only work inside a small wrapper.

The exact final function totals are:

| Final parser region | Executed instructions |
| --- | ---: |
| Main `parseImpl`, excluding callees | 95192 |
| Short-string Go helper, 414 calls | 28958 |
| Slow string wrapper + assembly, 142 calls | 19129 |
| Whitespace refill + wrapper + assembly | 24838 |
| Number helpers and conversion, including one float | 6658 |
| **Total** | **174775** |

These are final-version totals, not independent excesses over the generated
parser, whose corresponding operations are inlined into one native body. In
particular, the NEON whitespace leaf executes only 7,611 instructions and the
55 integer conversions execute 6,092 inside `ParsePrefix`. Neither is large
enough to account for most of the 95,995 extra instructions. The short-string
helper still performs real scanning work: 28,958 instructions, of which 12,036
are classification arithmetic/constants, 708 are word loads, and the rest
cover event location, checks, loop control, address calculation, and entry/exit.

The comparison/branch increase also includes FSM dispatch, input and frame
checks, cache routing, and short-string scanning loops. It must not all be
called bounds-check overhead; the earlier package-scoped `-B` experiment
already showed only a modest latency benefit. A separate operation classifier
finds fewer SIMD/FP instructions in the final version (15,406 to 6,806),
consistent with shifting short-string work to scalar Go operations. Recasting
that work or changing state lifetime requires new correctness-preserving
experiments before assigning a recoverable time benefit.

The successful PMU instruction profile is retained as a caution: 88.6% of
`NonSpaceMask`'s samples fall on the second load after its assembly call,
although that load and RET execute equally often. Sampling would imply roughly
20,000 wrapper instructions; the exact trace and independent replay both give
**3,720**. No instruction counts in this section are inferred from those sample
percentages. The single-fixture trace establishes the source of this workload's
instruction increase; it is not a universal distribution for all JSON inputs.

### Short-string call boundary: about 0.8 us is recoverable

The current compiler does not inline `ParseStringShort`: its inline cost is
109 against a budget of 80. One Twitter document makes 414 calls, with 272
successes and 142 slow-path fallbacks. A temporary variant places the exact
same two-window Go operations directly in `parseImpl`, preserving all bounds,
escape, control-byte, and tail handling.

| Stage / workload, ns | Control | Inline short check | Change |
| --- | ---: | ---: | ---: |
| Twitter typed prepare + parse | 12903 | 12114 | -6.1% |
| Twitter typed resident parse | 12884 | 12059 | -6.4% |
| Compact Twitter typed prepare + parse | 9378 | 8442 | -10.0% |
| Whole typed decode | 29763 | 28877 | -3.0% |
| Whole generic decode | 54188 | 52978 | -2.2% |

This is about 0.8 us of recoverable Twitter parse time, roughly one fifth of
the reference 3.7 us gap. Disassembly confirms removal of the short-helper
callsite, which fixture replay reaches 414 times per document. The parser
frame stays 384 bytes, while static stack-load sites
fall from 207 to 196. Static sites are not dynamic load counts. Inlining also
changes register allocation, instruction scheduling, and code placement, so
this measures the complete boundary-removal edit, not the latency of `CALL`
alone. The helper's four-instruction construction of the backslash constant
inside its word loop remains a separate code-generation opportunity, without
a measured cycle attribution here.

The short check performs 708 eight-byte loads per Twitter document. Of the
142 misses, 140 occur after sixteen ordinary bytes and two at escapes. A
second variant remembers a fully checked prefix and starts the unchanged
assembly decoder after those sixteen bytes. This avoids 140 repeated initial
NEON blocks, or 2,240 rescanned bytes, without removing assembly calls. Twitter
prepare-and-parse changes by only -0.2% to -0.3%, with overlapping ranges;
resident parse changes by +0.2% / -0.2% for typed/generic. Compact Twitter
prepare-and-parse improves 2.1–2.6%. Duplicate prefix scanning therefore does
not materially explain the Twitter residual gap, despite being visible in
the source.

Both variants pass kernel tests, full optdec/decoder tests, and all 7,416
lossless differential records, including error positions and partial state.

### Whitespace wrapper: measurable, but much smaller than its sampled share

Source replay finds 241 refill calls and 155 `NonSpaceMask` calls per Twitter
document. A temporary variant moves the exact bounded wrapper logic into
`parserSpaceRefill` and calls the same ABI0 assembly leaf directly. All compared
builds, including the control, use the same diagnostic linkname alias for the
existing assembly symbol. Disassembly confirms no replacement ABI adapter and
no change to classification, maximum width, or assembly source.
The refill frame grows from 48 to 64 bytes; ABI0 argument stores and the
result load remain even though the extra Go call disappears.
For the normal nonzero-mask path, a bounded disassembly walk counts 16 fewer
instructions outside the identical assembly leaf: some wrapper operations
move into the caller and cannot be eliminated with the wrapper call.

Twitter typed prepare-and-parse falls from 12.970 to 12.722 us (-1.9%), a
0.248 us reduction. Whole typed decoding improves 1.8%, Pretty 1.5%, and
WideIndent 2.2%. One parser sample overlaps the control range, so the five-run
median supports a modest effect rather than a precise universal cost.
The original CPU profile assigned 12.5% flat samples to `NonSpaceMask`; that
number clearly cannot be treated as the wall time saved by removing its Go
wrapper. The assembly operation, argument preparation, and caller state
handling remain necessary.

### Bounds checks: a smaller contributor

A diagnostic build passes `-B` only to the optdec package, leaving the source,
kernel package, and assembly unchanged. It is unsafe for production and was
run only on selected valid inputs; no invalid-input tests were run under this
configuration. It removes all 17 static parser bounds-panic sites, while the
384-byte parser frame and 256 static stack-reference sites remain.

| Stage, ns | Control | optdec `-B` | Safe node-store rewrite |
| --- | ---: | ---: | ---: |
| Twitter typed prepare + parse | 12904 | 12611 | 12888 |
| Compact Twitter typed prepare + parse | 9418 | 9072 | 9426 |
| Twitter typed FromDom | 16051 | 15896 | 15951 |

The diagnostic recovers 0.293 us on Twitter (-2.3%) and 0.346 us on compact
input (-3.7%). This is much smaller than the residual parser gap. It is not a
strict bound on every possible BCE improvement: the switch also changes code
layout and other optdec functions, including FromDom.

The safe source variant moves each of the three node writes under an unsigned
index-versus-length guard and separates growth into its alternate branch. The
compiler then removes the hot store bounds checks, affecting 643 emitted nodes
per Twitter document. Its parser timing is effectively unchanged (-0.1% on
Twitter, +0.1% on compact input, overlapping sample ranges). The parser frame
grows from 384 to 400 bytes and static stack-reference sites from 256 to 261;
removing checks does not guarantee better total generated code. This variant
passes full optdec tests and all 7,416 differential records.

### Number-heavy inputs have an additional algorithmic difference

The final mixed-number benchmark remains 60.5% slower than the generated
backend in the full comparison. It contains 512 tokens: 256 floats, 128
maximum-uint64 integers, and 128 negative integers. Twitter instead contains
55 integers and just one float, `0.035`, so a float-heavy explanation cannot
account for its overall parser gap.

For a float, the current `number.ParsePrefix` first accumulates the integer
prefix, then `Scan` validates the complete token from its beginning, and
`strconv.ParseFloat` reads it again to collect the mantissa and exponent. The
generated C parser collects that information while validating and passes it
directly to `atof_fast`. The two repeated float values in this fixture fit
the multiply/divide fast-conversion path in both implementations; this is
not evidence of an inherently slower floating-point arithmetic operation.
The extra lexical work and boundaries are the concrete implementation
difference. Normal in-range integer parsing remains a single fused pass.

The ARM digit scanner also calls `digitRun` after eight scalar digits whenever
input remains, even if the next byte is a delimiter. A temporary next-byte
digit guard removes 128 zero-progress assembly calls in the mixed fixture:

| Workload, ns | Control | Digit guard | Change |
| --- | ---: | ---: | ---: |
| Mixed numbers, whole decode | 40022 | 39124 | -2.2% |
| Short integers, whole decode | 16128 | 16140 | +0.1% |
| Eight-digit raw scan | 14.68 | 12.51 | -14.8% |
| Nine-digit raw scan | 15.23 | 15.35 | +0.8% |

The mixed workload saves 0.898 us, but still retains the separate lexical
passes and most of its original regression. The nine-digit microbenchmark
shows the cost of the added guard when scanning does continue. Twitter has
no calls removed by this guard; its small measured changes must not be
attributed to eliminating those zero-progress calls.

Although the mixed benchmark targets `[]json.Number`, typed parsing clears
the generic UseNumber path in both old and new versions, so both convert
numbers before materialization. Both subsequently call `SkipNumberFast` to
recover the original spelling for `json.Number`. That common rescan is not
a newly introduced framework cost. The wrapper and numeric variants pass
kernel/number/optdec/decoder tests and the 7,416-record lossless differential.

### State representation and remaining uncertainty

The generated native body has a 160-byte frame, saves registers x19–x28 once,
and contains no internal calls across 9,530 decoded instructions. Its outer
Go wrapper still exists. The final Go parser has a 384-byte frame and still
calls string, whitespace, and number helpers. Tango's Go internal ABI has no
callee-save registers, so live parser state must be preserved around surviving
calls. Inlining can remove that obligation, as the short-string experiment
demonstrates; this is not a language-level lower bound on parser performance.

The state representations also differ. The generated C parser temporarily
stores a parent link in the container node's `val` and its child count in
`typ` bits 8–31, then rewrites these fields on close. Control continues through
labels and the parent node type. The Go parser instead maintains a separate
`goFrame` slice containing node index, count, and state, with indexed updates
and append/growth machinery. The old parser also loads and stores parser
fields; it is incorrect to describe all its state as register-resident.

These representation and remaining call differences are concrete sources of
extra work. The exact trace above accounts for the additional instructions by
operation class, but individual semantic regions' net cycle costs have not all
been isolated. The
experiments establish a useful short-string boundary improvement, a smaller
whitespace-wrapper effect, and a modest bounds-check effect. They do not
justify attributing the entire unexplained balance to the compiler, adding
their separate gains, or declaring the Go framework at its performance limit.

### Go source structure versus compiler behavior

The instruction delta is not evidence that an equivalent C algorithm was
translated literally and then made slower entirely by the Go compiler. The
implementations make several different source-level choices:

| Concern | Generated C source | Current Go source | Attribution |
| --- | --- | --- | --- |
| Continuations | `obj_key`, `obj_val`, `obj_cont`, `arr_val`, and `arr_cont` labels directly encode the expected next operation | Known transitions write/select states and return through a common `scan` / `switch state` | Additional redispatch is an implementation choice; Go supports labels too |
| Container bookkeeping | Parent link and temporary count occupy unfinished tape-node fields | Separate `goFrame` slice stores node index, count, and state | Different representation, with extra metadata and indexing; not a Go requirement |
| Small operations | `always_inline` readers, visitors, and scanners specialize into the native body | Normal Go helpers and ABI0 kernels retain calls where Go inlining does not apply | Source boundaries, inlining policy, and ABI interact |
| Input bounds | Reader assumes 64 bytes of sentinel padding | Parser uses only logical bytes, with bounded scanners | Different safety contract; checks cannot simply be deleted |
| Number result | Kind plus an eight-byte union payload | Kind plus three separate payload fields in a 32-byte result | Source/API representation difference, not a language requirement |

The relevant C control flow is `native/parse_with_padding.c:1144–1334`; the Go
dispatch and transitions are `internal/decoder/optdec/parse_exec_go.go:101–151`
and `:299–331`. For example, after a colon Go stores ObjectValue, re-enters the
common scan/dispatch, then stores ObjectAfter before parsing the value. C goes
directly to `obj_val`. Every suspended Go parent is already in an After state,
so several transient frame-state writes are redundant for continuation.

However, backing-array writes to `goFrames` are **not** the SP-addressed stack
traffic counted above. A compiler SSA dump establishes the immediate cause of
the twelve hot stack stores: none exists before register allocation, and all
appear as `StoreReg` operations afterward. Their values are:

- At the scan gateway: node pointer/length/capacity, frame
  pointer/length/capacity, emitted-node count, and current token kind.
- At the ready/dispatch gateway: input position and whitespace-mask
  base/width/bits.

These are values carried around the common loop and through its joins. The
local Tango compiler's `makeSpill` creates one spill for an original SSA value
and records all reload sites. `placeSpills` chooses a location that dominates
all those reloads, considering loop depth and a bounded search; it does not
clone the store onto each path that needs it. Here those common locations also
execute for punctuation that makes no helper call. ARM64 Go calls clobber the
allocatable caller registers, making preservation necessary across the calls
that remain. This establishes the compiler mechanism, not a proof that all
twelve stores can be safely sunk or eliminated.

Exact compiler references, copied with line numbers into the evidence, are
Tango `src/cmd/compile/internal/ssa/regalloc.go:27–82`, `:532–549`, and
`:2244–2344`, plus `ssa/_gen/ARM64Ops.go:146,531`. The general `tighten` pass
precedes register allocation in `ssa/compile.go:507–519`; the subsequent
layout passes do not provide a general path-specific spill-sinking pass.

#### A small proven source issue: retaining the previous token kind

The numeric-result switch at `parse_exec_go.go:226` handles Signed, Unsigned,
and Float, but has no default. Successful `ParsePrefix` returns only those
kinds, but that contract is not visible through the non-inlined call. Go
semantics require an unrecognized kind to leave the previous `kind` unchanged.
Consequently, the compiler carries `kind` around the parser loop and spills it
on every scan, although this cross-token dependency is unnecessary.

A temporary variant adds an impossible-case panic to express the contract.
The loop PHI for `kind` disappears and the scan stores fall from eight to seven;
the four ready stores remain. All 7,416 differential records match, and
optdec/number tests pass. Five matched alternating 200 ms measurements give:

| Workload, ns | Control | Exhaustive kind handling | Change |
| --- | ---: | ---: | ---: |
| Twitter typed prepare + parse | 12974 | 12837 | -1.1% |
| Compact Twitter typed prepare + parse | 9395 | 9265 | -1.4% |
| Whole typed decode | 29713 | 29582 | -0.4% |
| Whole generic decode | 54040 | 53777 | -0.5% |

Three alternating 100,000-iteration PMU pairs give 177,539 to 175,357
normalized instructions (-1.2%) and 40,351 to 39,887 cycles (-1.1%). These
include the previously described process/benchmark overhead. Other allocation
decisions also change, so this is the net result of the source edit, not exactly
990 stores multiplied by a fixed cost. Whole-decoder sample ranges overlap;
the result is a small parser improvement, not a large end-to-end claim.

#### Direct continuations alone do not reproduce C's machine code

A second temporary variant gives known states their own bounded scan and
continuation labels, retaining the same byte kernels, frame representation,
value parsing, error positions, and exit commit. It removes the general state
switch and transient frame-state writes; the After state still selects a
continuation after the shared value handler. This is a partial control-flow
experiment, not a complete port of C's duplicated value handlers, padding,
inlined scanners, and packed container representation.

It passes optdec/decoder/number tests in default and optimized modes and all
7,416 differential records. Nevertheless, Twitter prepare-and-parse regresses
from 12.960 to 13.996 us (+8.0%); compact input regresses 11.5%, and whole typed
decoding 3.6%. All measured allocations remain unchanged.

An exact warmed-invocation trace explains the conflicting instruction effects:

| Property | Control | Direct continuations | Difference |
| --- | ---: | ---: | ---: |
| Comparisons and branches | 56487 | 49879 | -6608 |
| Stack loads/stores | 37568 | 45839 | +8271 |
| Total instructions | 174775 | 171347 | -3428 |
| Parser frame, bytes | 384 | 560 | +176 |
| Parser code, bytes | 3536 | 5152 | +1616 |

Every string, whitespace, and number helper has exactly the same entry and
instruction counts in both traces; all differences are in `parseImpl`. PMU
also reports fewer instructions and branch misses, but more cycles. The
changed source removes actual control work while producing substantially more
stack traffic and larger code. Those facts explain why a source-level branch
reduction is insufficient; they do not isolate every cause of the latency
regression or prove that a fuller C-like implementation would fail.

The responsibility is therefore specific: redundant state dispatch, unnecessary
value lifetime, and representation/API choices are source issues; spill
placement, register allocation, and inlining determine their generated cost.
There is no evidence of an inherent Go-language requirement for this overhead.
Lexically narrowing a variable's braces alone is not a demonstration of shorter
SSA lifetime. Further changes should reduce the actual values carried across
joins and calls, then verify the resulting assembly and measured behavior.

A literal C transplant also requires care with existing contracts: Go emits a
key before checking its colon, counts a value before parsing it, and updates
maximum depth when opening a container. C uses different intermediate ordering.
Its temporary node count is 24 bits, while Go's frame count is 32 bits. Preserve
current partial-error tape, cursor, depth, and source ownership when changing
the representation. Both experiments remain isolated; production code is
unchanged.

### Evidence retained for reproduction

- `output/arm-native-analysis/go-residual/`: exact-binary CPU profiles,
  disassembly-independent PMU totals, raw runs, commands, and binary hashes.
- `output/arm-native-analysis/string-gap-attribution/`: unchanged, manually
  inlined, and prefix-reuse snapshots/diffs, disassembly, fixture replay counts,
  correctness/differential results, and five-round measurements.
- `output/arm-native-final-gap/bce/`: matched control, package-scoped `-B`, and
  safe node-store diagnostic builds, compiler BCE output, disassembly,
  differential results, raw timing samples, and `medians.csv`.
- `output/arm-native-analysis/remaining-wrapper/`: matched alias control,
  direct-leaf wrapper variant, and numeric guard, with source diffs, exact
  scanner path analysis, tests, differential hashes, and timing samples.
- `output/arm-native-analysis/instruction-attribution/`: exact GDB block and
  edge traces, binary/input hashes, per-PC and per-function counts, disjoint
  operation classifications, independent path replays, sample-skew evidence,
  and reproduction scripts. `accounting-summary.json` contains the table above;
  `run-trace.py` and `trace.py` reproduce the debugger counting method.
- `output/arm-native-analysis/instruction-attribution/scan/kind-default/`:
  matched source, before/after-regalloc SSA, missing-default diagnostic, and
  differential results; adjacent `compiler-spill-policy.txt` preserves compiler
  source excerpts and `compiler-source-findings.json` records the mapping.
- `output/arm-native-analysis/source-comparison/`: direct-continuation diff,
  both experiments' timing/counter runs, exact continuation trace, code/frame
  sizes, tests, differential results, and `source-experiment-conclusions.json`.
