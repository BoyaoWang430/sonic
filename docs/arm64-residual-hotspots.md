# ARM64 residual hotspots: source-level optimization and rejected boundaries

This report preserves measurements from before Runs became the normal ARM64
backend. Its build tags and decoder-selection descriptions identify the
measured versions. See [current integration and validation](arm64-native-go.md#current-retained-version)
for the present architecture selection; no custom tags are now required.


This follow-up starts from the version retained in
[the preceding hotspot report](arm64-hotspot-optimization.md).
The decoder comparison uses `sonic_cparity,sonic_cparity_runs`; AST and formatter
changes are shared with the ordinary build. Backend selection is unchanged.
The original C reference remains revision
`a7193312dec5165ec6686a9793eb308e2e642903`.

## Retained results

Times are median microseconds. Percentages are median same-round ns/op
ratios; negative is faster. Before means the immediately preceding retained
Go/assembly version. The AST float-array row validates syntax without
converting each token to float64.

| Scenario | C | Before | Retained | vs before | vs C |
| --- | ---: | ---: | ---: | ---: | ---: |
| AST complete scan, float array | 18.986 | 22.622 | 18.991 | -16.08% | +0.13% |
| AST skip a 4096-byte numeric candidate | 0.528 | 0.624 | 0.459 | -26.44% | -13.02% |
| AST dense escaped strings, compact, last index | 1.487 | 1.919 | 1.884 | -1.77% | +26.66% |
| AST dense escaped strings, compact, missing index | 1.435 | 1.869 | 1.837 | -1.77% | +28.06% |
| AST dense escaped strings, spaced, last index | 1.610 | 2.214 | 2.154 | -2.71% | +33.73% |
| AST dense escaped strings, spaced, missing index | 1.557 | 2.157 | 2.095 | -2.81% | +34.53% |
| AST mixed array, spaced, last index | 1.149 | 1.341 | 1.332 | -0.67% | +15.93% |
| AST short escaped fields | 0.375 | 0.420 | 0.418 | -0.25% | +11.66% |
| AST skip a 128-byte key | 0.126 | 0.146 | 0.144 | -0.89% | +13.59% |
| Marshal mixed floating-point numbers | 18.482 | 22.341 | 22.180 | -0.66% | +20.25% |
| Unmarshal mixed floating-point numbers | 12.402 | 14.457 | 14.476 | +0.15% | +16.77% |

The main gains are the float-array validation and long-number boundary scan.
Dense escaped-string traversal still trails C by 26.66–34.53%; mixed-float
encoding and decoding still trail by 20.25% and 16.77%. Spaced mixed arrays
retain a 14.47–15.93% gap. These residual gaps are not considered solved.

ConfigDefault public Marshal binding and Unmarshal generic/binding are
within 0.2% of C in this paired measurement; generic Marshal is 10.23% faster.
All four change by less than 0.5% versus the preceding retained source.
ConfigStd remains faster than C. These figures use the retained Runs build;
the untagged decoder is still a separate implementation and was not switched.

[All 72 scenarios](arm64-retained-results/table.md) and
[timing ranges, medians, and allocations](arm64-retained-results/table.csv)
include the smaller residual gaps and the new numeric-transition guards.
The [committed provenance](arm64-retained-results/provenance.json) identifies
the measured binaries and archived source. Measurements precede commit
curation; the table is an exact copy of the retained result, not a new run.

## Retained changes and their reasons

### AST numeric validation skips an empty digit scan

The FloatArray whole-document benchmark validates number spelling; it does
not convert each number to float64. For `-12.345e+67`, the previous strict
scanner separately dispatched the exponent marker and its sign, probing for
digits between them. Consuming the sign with its exponent removes that empty
probe. The remaining integer, fraction, and exponent digit runs still use the
same bounded Go-word/SIMD scanner. Duplicate-marker precedence and error
cursors are unchanged and tested explicitly.

### Relaxed numeric boundaries use a smaller SIMD classifier

The existing ARM64 `skipNumber` still recognizes exactly HT, LF, CR, space,
comma, `]`, and `}`. Low- and high-nibble lookup tables replace the previous
whitespace classifier plus three delimiter comparisons. Classification drops
from nine to five NEON instructions per 16-byte block. A matching lane contains
1, 2, 4, or 8, so its 16-byte sum cannot wrap to zero. Only a matching block
expands its lanes to all-ones bytes for the existing exact position packing.
The new readonly table is 32 bytes. Loads, tail handling, and grammar ownership
remain unchanged.

### AST array traversal has one Go operation

A focused `pathBlock.arrayIndex` function owns index advancement and delimiter
checking. It calls the existing generic value skipper, retaining relaxed
semantics for unselected values. This reduces the traversal frame used inside
the array loop without introducing a second token probe. It also makes the
array boundary easier to review independently of reflective object lookup.

The path-string kernel directly packs its 32-byte block before checking for
an event, avoiding a separate vector reduction when the caller has already
found an escape. It retains the first-32/then-64 scan policy and raw suffix
cache. The path scanner shrinks from 832 to 816 bytes; the ordinary string
scanner remains byte-identical. This is a small improvement, not a removal of
the per-string ABI0 boundary.

### Float formatting removes wrapper overhead only

The shared formatter dispatches precision once, directly returns the fixed
format result, and trims scientific exponents through one bounded tail slice.
Tango's formatter wrapper shrinks from 544 to 432 bytes. The integer fast path,
float32 rounding, notation thresholds, signed zero, and nonfinite handling are
preserved. `math.Trunc` was rejected because it would introduce an extra
assembly call on RISC-V without Zfa.

The remaining difference from C is larger than this wrapper. C uses a dedicated
Schubfach conversion with inline digit output. The supplied Tango library uses
Dragonbox through `genericFtoa`, conversion helpers, `formatDigits`, and
`fmtF`/`fmtE`, with an intermediate digit buffer and general formatting logic.
These are algorithm, library organization, and call-boundary differences;
the evidence does not assign a measured fraction to each one.

One toolchain/library opportunity is recorded, not changed: Tango's
`internal/strconv/ftoadbox.go` implements `umul128Upper64` with four 32-bit
partial products. Its generated leaf has 19 emitted instructions including three NOPs and return,
with three MULs and one MADD plus shifts/adds. The high half can instead be
expressed through `bits.Mul64` and lowered to UMULH. This is a library-source
opportunity that the compiler did not recognize automatically, not evidence
that all remaining formatting cost is a compiler problem.

## Experiments rejected on broader coverage

The remaining mixed-float fixture does not enter the slow strconv fallback.
Two of its four token shapes use exact multiplication/division; the other two
use Eisel-Lemire, including the original truncation-ambiguity check. C inlines
these operations, while the retained Go parser calls a combined conversion
helper. This identifies a code/call-organization difference, not a quantitative
attribution of the whole time gap.

Splitting an inlineable exact conversion was also tested: Tango reported cost
70 and actually inlined it in token parsing. Extended exponents, Eisel-Lemire,
truncation, and arithmetic order stayed unchanged. Despite removing the common
helper call, the isolated mixed-float API was 1.39% slower; the six public guard
cases ranged from -0.97% to +2.57%. That source split was rejected as well.
The earlier counter-to-pointer loop rewrite produced fewer static loop
instructions without a meaningful timing gain and was likewise reverted.
Both `parse_cparity_number_arm64.go` and `parse_cparity_runs_arm64.go` match
the pre-turn source byte-for-byte. The retained mixed-float Unmarshal result
therefore remains effectively unchanged. A smaller percentage against C than
in the preceding report is not credited as a decoding improvement: those
percentages came from different measurement sessions.

The numeric batch experiments initially improved mixed-float decoding, but
moving integer transitions into Go regressed alternating float/integer inputs.
A bounded scalar lookahead gave up much of the gain; a word-based dispatch hint
still regressed alternating 8- and 19-digit integers. Moving the batch loop
between the dispatcher and number finisher did not resolve the tradeoff.
Fresh-destination public Unmarshal benchmarks include copying, allocation, and
binding, so these costs are not just parser microbenchmark effects. None of
these grammar/batching changes is retained. The original Runs control source
is byte-identical to the pre-turn snapshot. A diagnostic counter build of the
pre-turn parser observed 258 outer assembly entries for 256 mixed numeric
tokens; no reduction of that count is claimed for the retained implementation.

A Go loop consuming every cached quote/backslash event regressed dense strings
by roughly 52–66%. A separate direct-string probe in array traversal repeated
work on other values, regressing empty-container and mixed arrays. Starting
string scans at 64 bytes instead of 32 also lost on the dense fixture. These
variants remain only in the experiment artifacts.

The dense escaped-string array fixture contains roughly 80-byte strings. C's
inline `skip_string_fast` only locates a boundary. The current shared Go path
also obtains the first escape, publishes reusable raw suffix masks, and crosses
an ABI0 boundary per scanned string. Scalar cache walking was more expensive
than SIMD scanning here. Eliminating the residual gap would require a further
justified change in operation responsibilities; this work does not introduce
array grammar into assembly or additional continuation/cache fields.

### Direct relaxed-string dispatch was also rejected

Calling the existing relaxed assembly entry directly removed an unnecessary
Go dispatch frame. An isolated AST screen improved short escaped-field queries
by 3.82% and dense arrays by about 0.3–1%. However, the complete API comparison
showed collateral regressions. A separate six-round, three-version public
check reproduced +2.90% generic Default Unmarshal, +6.04% dense-Unicode Marshal,
and +7.32% long-plain-string Marshal versus the retained implementation.
This candidate and its RVV adapter were reverted.

The build flags and loaded data sections were identical. The path wrapper
shrunk from 160 to 144 bytes, moving 17 adjacent kernel functions, including
Quote and Unquote, by 16 bytes. All other 6,239 text symbol addresses were
unchanged. This is a concrete layout difference, not proof of the regressions'
cause. No padding, alignment directive, or compiler workaround was added.
The audit is in `direct-path-binary-audit.json`; the broader candidate results
are `final-v2-*`, and the independent check is `direct-path-public-isolated/`.
These are rejected-candidate measurements, not the final retained results.

The rejected RVV adapter exposed a separate compiler organization detail:
a direct six-result tuple return had inline cost 88; named-result assignment
and return reduced it to 70. Both Tango and stock Go then emitted a direct
call to the existing assembly scanner. That source change was reverted with
the candidate and is recorded only as an observation.

## Results and verification

The production delta is 26 Go lines and 23 assembly lines, including comments
and blank lines. No assembly entry, ABI parameter, parser field, or cache field
is added. Six new public and parser-stage guard benchmarks share identical
fixtures; their timing scopes remain distinct.

Final paired results, allocation comparisons, retained source/binary hashes,
exact source diff, compiler output, and rejected experiments are stored in
[`output/arm-residual-optimization/`](../output/arm-residual-optimization/).
That directory contains local, ignored artifacts. The complete retained
tables and compact provenance linked above are committed with the source;
binaries and raw logs are not.
The canonical retained measurements are `final-*`; `selection.json` explicitly
distinguishes them from the rejected direct-call `final-v2-*` series. The final
source hashes exactly match the source used to build the retained binaries.
Final measurements use identical fixtures,
Tango 1.0.0.1153 / Go 1.26.4, ARM64 v8.0, CGO disabled, CPU 16, GOMAXPROCS=1,
normal GC, SONIC_NO_ASYNC_GC=1, and a fixed argv[0]. No build, test, debugger, or
other benchmark job overlaps a timed group. Percentages are median within-round
ns/op ratios; negative is faster. No traffic-weighted overall gain is inferred. Core, shapes, numeric/whitespace,
and AST use six balanced 200 ms rounds; the six new numeric-transition guards
use eight 200 ms rounds. There are 72 unique scenarios and 210
variant/scenario rows. Allocation counts match in every row; two B/op medians
differ by one byte. The six new guard cases compare only before/retained.
Their paired changes range from +0.07% to +0.41%.


Validation includes the relevant public, encoder, decoder, native, kernel,
number, and AST packages under default and explicit optdec/FastMap/encoder-VM
environments; stock Go 1.26.1 and Go 1.20.14 checks; Tango GOGC=1 checks; the
retained C-parity suite; the frozen preceding-Go AST oracle; and nibble-byte,
alignment, guard-page, formatting-boundary, and numeric-marker tests.
RISC-V rva22u64 compilation is checked separately, not executed on this host.

The full tagged optdec suite has the same 15 historical top-level failures.
Before/retained failure names and normalized failure blocks match exactly.
The previously documented 2,181 AST/C semantic differences are not addressed
by this performance work. Valid-input performance and equivalence to the
preceding Go backend do not establish complete C compatibility.
