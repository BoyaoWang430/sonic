# Further ownership refinements for the ARM64 runs parser

This experiment starts from the retained version in
[the previous boundary report](arm64-cparity-runs-boundary.md), with the
`sonic_cparity,sonic_cparity_runs` build tags. It keeps the default backend,
public SIMD kernels and compiler unchanged.

## Container state has one owner

The unfinished parent tape node already records the container kind and its
completed child count. Before this change, assembly also extracted that count
into shared state and returned distinct object/array close events. Go then
read the parent node to finish the operation.

There is now one CloseContainer event with no state payload. Go reads both
kind and count from the parent node, updates statistics, span and parent/depth,
and continues any adjacent matching closes. This removes the shared `count`
field and the redundant extraction/store. Empty containers naturally have a
zero count. The count is still extracted as `uint32(parent.typ) >> TypeBits`,
preserving the original 24-bit behavior. Assembly still counts the last child
before validating a closing delimiter, including malformed-input ordering.

Close events publish input/tape cursors but no longer save unused token
position or string context. Other non-string continuations also stop restoring
those fields on assembly entry. Only Unicode resumption reads the saved
string context; other paths establish their own token position and context
before using them.

Shared state falls from eight fields / 72 bytes to seven fields / 64 bytes.
There are six event kinds instead of seven; the six resume phases are unchanged.

## Numeric success contract

The Go numeric fallback still owns decoding, node publication and error/capacity
ordering. Every successful `cParityParseNumber` return assigns a valid integer
or real type. The extra type check now runs only on errors, where C may retain
the previous number or an initially invalid type. UseNumber keeps its raw-number
path. This removes redundant option/type checks from valid complex numbers
without expanding the decoder or changing the compiler.

## Private assembly operations

Four routines now have separate static `TEXT` symbols and explicit register
contracts. They are `NOSPLIT|NOFRAME` leaf functions, called directly by the
outer assembly parser. Go still enters only the outer parser through ABI0;
these private calls have no Go argument marshaling or extra stack frames.

| Operation | Responsibility | State visible to it |
| --- | --- | --- |
| `runs_string` | Scan a plain string or find its first escape | Input cursor, options, SIMD constants |
| `runs_string_continue` | Decode ASCII escapes and copy blocks until completion, Unicode or error | Input/output cursors, content start, options |
| `runs_space` | Consume first nonspace and maintain the whitespace bitmap | Input cursor and explicit cache pointer |
| `runs_number` | Parse common integer cases or request Go fallback | Numeric body cursor and sign |

The plain scanner tail-jumps to the shared escaped-string routine, preserving
the caller's return address. Unicode resumption calls the same routine; there
is one copy of its scan/copy loop. On Unicode, the output cursor is returned in
a register and saved by the outer parser. None of these routine bodies knows
the Parser, tape or continuation layout. The cache pointer is reconstructed
once per outer assembly entry and preserved by all private routines.

Go still owns containers, Unicode policy, complex numbers, malformed literals
and growth. The outer assembly parser still owns flat grammar and hot node
submission. Unicode remains a partial-string handoff, so the implementation
does not claim to eliminate every cross-language continuation.

The source count is 371 Go lines (288 runs + 83 framework) and 761 assembly
lines (346 entry + 88 core header + 327 scanner header), versus 370 and 748
before this experiment. Counts include comments and blank lines and exclude
tests and shared numeric helpers. The extra source describes the operation
contracts; the shared state and event protocol are smaller.

## Return instruction control experiment

The former helpers were labels inside the outer framed function and returned
with `JMP (R30)`, emitted as `BR X30`. The new leaf functions use ordinary
`RET`. This change explains most of the observed speedup.

An isolated control starts from the smaller ownership/numeric changes, keeping
the old single-TEXT organization. It replaces only the five helper return sites
with raw `RET X30` instructions. Using normal assembler `RET` in the old framed
function could generate its outer epilogue, so this raw encoding is confined
to the control artifact. Production uses the independent leaf functions.

Machine-code comparison confirms that the 2,640-byte outer routine has the
same address and size, and differs at exactly five instruction words:
`d61f03c0` becomes `d65f03c0`. The control passes the C-parity tests.
In six balanced paired rounds, this return-only control improves Twitter by
9.94% and Compact by 14.61% relative to the unchanged-return numeric candidate.
The complete private-function version improves them by 10.19% and 14.62%
in the same measurement. This isolates the return-instruction choice as the
main cause; branch-mispredict or cycle counters were not measured.

The final matched Twitter trace executes 76,806 instructions versus 77,170
for the previous retained binary: -364 (-0.47%). The baseline binary hash
matches the previous trace. Go-to-assembly outer entries remain 135. The
newly named helper entries were already internal calls in the old assembly;
they are not added Go/ABI0 crossings. Classification changes from branch to
return for old `BR X30` sites likewise do not indicate extra calls.

The matched trace attributes 28,822 instructions to the outer assembly parser,
20,775 to whitespace, 16,000 to plain strings, 1,145 to the escaped tail and
2,761 to common integers. The Go dispatcher uses 3,881, container closing
3,192 and the cold numeric wrapper 46. All callees are included in the total.
This is the same warmed 13,035-byte Twitter binding fixture as the earlier
reports; debugger elapsed time is not a performance measurement.

## Escaped-string experiment

A separate prototype moved completed escaped-string coordination into Go,
using a large assembly operation for consecutive ASCII escapes and scan/copy
blocks. The assembly helper had no Parser or tape state and returned only at
a quote, Unicode escape or error. Go retained Unicode policy and batched
adjacent Unicode sequences. The initial plain prefix was never rescanned.

This addressed the previous prototype's long-string problem: the long ASCII
escape fixture was essentially unchanged, and Unicode-heavy input improved
about 19% in screening. However, 256 short escaped values cost 83% more and
256 escaped keys cost 43% more. Crossing Go/assembly per short escaped token
is still too expensive. The prototype is preserved as `source-bulk-string/`
and `bulk-string.test`, and is not retained in the source tree.

The two new short-token benchmarks remain useful regression fixtures. Four
new string-priority cases also cover adjacent versus separated invalid escapes,
controls after an invalid escape, malformed surrogate retry, and decoded
quote/backslash/NUL followed by a long overlapping copy. Their expectations
come from the existing exact scanner, and the same cases pass on the frozen
baseline in a separate checkout.

Artifacts for this experiment are under
`output/arm-native-go-cparity-runs-ownership/`. Earlier reports and measurements
remain unchanged.

## Final paired performance

Final timing uses eight balanced serial rounds on Neoverse-N2 CPU 16,
Tango 1.0.1153 / Go 1.26.4, `GOMAXPROCS=1`, `SONIC_NO_ASYNC_GC=1` and normal
GC. Main and cold-number samples use 300 ms; other fixtures use 200 ms.
Builds and GDB are excluded from the timing interval. Values are median
microseconds; changes are medians of per-round ratios, not ratios of the
shown medians. Raw samples and ranges are retained without significance claims.

The baseline is the previous retained boundary version. Only the new short
escaped-token fixtures use its isolated rebuild with the same new benchmark
file. Parser-only fixtures reuse warmed storage and exclude restoration time;
the full decoder measurements include the public decoding workflow. These
numbers are not additive or traffic weights.

| Benchmark | Previous boundary | Private operations | Paired change |
| --- | ---: | ---: | ---: |
| Compact parser | 7.1460 | 6.2320 | -12.75% |
| Compact prepare + parse | 7.3045 | 6.3765 | -13.01% |
| Twitter parser | 9.7430 | 8.8920 | -8.82% |
| Twitter prepare + parse | 9.9875 | 9.1175 | -8.70% |
| Full binding decoder | 26.4070 | 25.5660 | -3.19% |
| Full generic decoder | 49.9095 | 49.1070 | -1.62% |
| ContainerArray | 22.1230 | 13.6625 | -38.25% |
| NumericArray | 2.7055 | 2.2770 | -15.82% |
| UnicodeString | 9.0575 | 7.8975 | -12.80% |
| EscapedString | 3.1360 | 3.1090 | -0.83% |
| LongString | 1.0745 | 1.0815 | +1.17% |
| LongWhitespace | 0.2334 | 0.2259 | -3.29% |
| ColdNumbers | 11.3895 | 10.3045 | -9.50% |
| EscapedKeys | 6.3940 | 6.3440 | -0.82% |
| ShortEscapedValues | 3.8965 | 3.8920 | -0.23% |

The main parsing cases improve by about 9–13%, and full decoding by about
1.6–3.2%. Container-heavy input improves substantially; both numeric fixtures
also improve. Short escaped tokens retain their previous performance instead
of paying the rejected prototype's extra crossings. LongString shows a small
1.17% increase in these samples. All parser-only cases remain allocation-free.

## Correctness and retained artifacts

The fixed 8,892-record C oracle and 1,464-record boundary oracle pass, along
with numeric partial-state/error tests, container growth/depth tests and the
expanded string priority/overlap tests. Tango C-parity and full decoder tests
pass both normally and with optimized decoder/fastmap/VM options plus `GOGC=1`.
C-parity tests also pass under stock Go 1.26.1 and Go 1.20.14. The RET-only
control passes both C-oracle digests.

The complete experimental optdec test binary still reports the same 15 native
internal-contract failures as the previous version. Its full failure log is
byte-identical to that baseline; these preexisting differences are not claimed
as fixed or as passing tests.

Retained sources are frozen under `source-private/`; the measured binaries are
`private.test` and `private-decoder.test`. `metadata.json` records hashes,
compiler settings, tests and result directories. The matched trace is under
`instruction-attribution/private-*`. The isolated RET experiment retains its
source headers, machine-code comparison, test logs and `timing-return-control/`.
Rejected and intermediate candidates remain separate from the retained source.
