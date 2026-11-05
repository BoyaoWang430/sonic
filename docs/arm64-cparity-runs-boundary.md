# Clearer operation boundaries in the ARM64 runs parser

This experiment refines the Go/assembly contract of
`sonic_cparity,sonic_cparity_runs`, starting from the
[tuned runs backend](arm64-cparity-runs-tuned.md). The retained change completes
cold scalar operations in Go and reduces shared continuation state. The public
SIMD kernels, compiler and default backend are unchanged.

## Retained ownership changes

Complex numbers previously followed assembly → Go decode → assembly node
submission through a dedicated `NumberResume` phase. Go now owns the complete
fallback operation: decode, publish the partial node and statistics, then apply
the decode-error-before-capacity ordering. Successful nested values return to
the ordinary `ParentContinue` phase; root completion and errors return directly.
Common integers retain their existing assembly path.

An invalid `true`, `false` or `null` previously returned to assembly after Go
located its first mismatch. Go now consumes that mismatch, writes the partial
node's type without changing its unspecified payload, and returns the error.
There is no `LiteralResume` phase or capacity check overriding this error.
Valid literals stay within the assembly run.

These are ownership changes, not just moved decoder calls: each Go fallback
finishes its node/error operation. Hot and cold paths still share the tape
format, and both contain node-publication logic.

Token positions now consistently identify the first token byte, including the
opening quote of object keys. Unicode resumption reconstructs the string's
content start from that position. This removes `stringStart`; deriving a
number's sign from its first byte also removes `neg` and its hot-path store.
The remaining `stringErr` and `count` fields each have one purpose. Shared state
falls from 10 fields / 88 bytes to 8 fields / 72 bytes; resume phases fall from
eight to six. Key position normalization adds two scalar instructions per key.

`num` and `hasEsc` deliberately persist across tokens: C's malformed-input
partial tape can depend on their previous values. Clearing them on each event
would break the C contract. Actual tape growth starts a new padded invocation
and resets them, as before.

## Event contract

Every assembly return publishes the input and tape cursors. Fields not listed
for an event are not its payload; in particular, close events must not read
`pos` or `context`.

| Event | Assembly hands over | Go completes |
| --- | --- | --- |
| Open object/array | Opening delimiter consumed; `pos` at token start; no pending node written | Container node, parent/depth and capacity; resume ObjectStart/ArrayStart |
| Close object/array | Closing delimiter consumed; `count` includes the completed child | Length/span, statistics, parent/depth and adjacent matching closes; resume ParentContinue or finish |
| Number | `pos` includes any minus; `cur` at the body after that minus; no pending node written | Decode, partial node, statistics, error/capacity ordering; resume ParentContinue or finish |
| Invalid literal | Fixed probe failed; `cur` after t/f/n; no pending node written | First mismatch, type-only partial node and final error |
| Unicode | `pos` at opening quote; `cur` at the backslash; `stringDst` at the output position; input may already be modified | Adjacent Unicode escapes, cursor/output/error; resume StringResume |

Unicode remains the only continuation through a partially decoded scalar.
Its Go handler performs no allocation or tape growth. Assembly still owns
plain scanning, simple escapes, copying, string-result submission and their
key/value error ordering. This exception retains the existing fast copy loop.
Flat punctuation/grammar and node emission also remain in assembly, so this
backend is not a set of independent SIMD-only operators.

## Rejected whole escaped-string handoff

A separate prototype yielded before the first escape changed input. Go then
completed the entire escaped string using the existing exact Go scanner;
assembly only committed its result. It removed the partial-string cursor and
Unicode continuation, including the assembly escape/copy loops.

The prototype passed all C-parity tests, but its short screening runs showed
about 5–6% slower Twitter/compact parsing and 288% more time on ASCII
escape-dense input. Unicode-dense input improved by about 6%. The Go scanner
rescans the untouched prefix and uses small SIMD/move calls in its loop; the
measurement does not isolate their individual contributions. This prototype
is preserved under `source-escaped-go/` with its binary and results, and is
not retained in the source tree.

A future string-boundary experiment would need to preserve bulk scanning and
copying efficiency while giving the string operation an explicit contract.
Simply replacing the current loop with the available Go scanner does not meet
the small-regression requirement.

## Correctness and scope

The 8,892 C-oracle records and 1,464 boundary/growth records retain their fixed
digests. Numeric partial-state/error tests, Unicode/key/growth tests and the
prior short-string/close-chain tests pass. The new 18-case literal test covers
root/array/object contexts, first/last suffix mismatches and exactly full tape:
the first bad byte is consumed, the payload is retained, growth does not occur,
and the unfinished child does not increment its parent count. The same new
test passes in an isolated copy of the pre-change source.

C-parity and full decoder tests pass with Tango in normal and optimized/VM
environments, including `GOGC=1`. C-parity tests also pass with stock Go 1.26.1
and Go 1.20.14. Untagged optdec and decoder package tests pass in both normal
and optimized/VM environments.

The complete optdec test binary with the experimental C-parity tags still
fails 15 native-backend contract tests. The frozen baseline and candidate
produce identical failure logs, including partial-tape, frame reuse, offset,
Unicode-option and error-position expectations. These existing differences
are not fixed by this boundary experiment; the experimental tag must not be
treated as having passed that whole package suite.

Source snapshots, binaries, test logs and paired timing samples are retained
in `output/arm-native-go-cparity-runs-boundary/`. Earlier measurements and
source snapshots remain unchanged.

## Dynamic instructions and result lifetime

A matched warmed Twitter binding invocation executes 77,170 instructions,
versus 76,613 for the tuned baseline: +557 (+0.73%). Assembly entries remain
135. This uses the same 13,035-byte fixture, options and third-invocation
trace convention as the previous report. The retained baseline binary's hash
matches the previous trace exactly; the candidate is traced separately.
Debugger durations are not timing results.

| Metric | Tuned baseline | Retained boundary cleanup |
| --- | ---: | ---: |
| Total instructions | 76,613 | 77,170 |
| Assembly instructions | 69,396 | 69,926 |
| Go dispatch instructions | 4,080 | 4,083 |
| Go container-close instructions | 2,924 | 2,924 |
| Cold numeric wrapper instructions | 29 | 53 |
| Assembly entries | 135 | 135 |
| Explicit SP/FP loads and stores | 2,372 | 2,372 |

An initial implementation assigned the numeric result to the loop's final
error variable before checking it. Tango kept that variable live across later
iterations, introducing extra saves/restores even for other events. Scoping
`numberCode` to the numeric branch, and assigning the final error only when
exiting, preserves the same operation boundary and removes 537 dynamic
instructions. The Go dispatcher falls from 4,620 to 4,083 instructions.
No compiler changes or source expansion are involved. The first candidate's
77,707-instruction trace and timings remain available separately.

The remaining net assembly increase reflects token-position normalization
alongside removal of sign stores and dedicated resume dispatch. The trace
contains only one cold numeric operation, so the separate dense numeric
fixture is needed to assess that ownership change. Instructions are not
cycles, and explicit stack accesses are not all register spills.

## Paired performance

Measurements use Tango 1.0.1153 / Go 1.26.4, eight balanced serial rounds,
CPU 16 (Neoverse-N2), `GOMAXPROCS=1`, `SONIC_NO_ASYNC_GC=1` and normal GC.
Main and cold-number samples use 300 ms; boundary/scan samples use 200 ms.
No builds or GDB run concurrently with timing. Times are median microseconds;
percentages are medians of per-round ratios, not ratios of displayed medians.
Raw ranges and samples are retained; no statistical significance is claimed.

The baseline is the prior tuned runs binary. The new cold-number fixture uses
an isolated rebuild of that exact source with the same additional test and
benchmark file as the candidate. It contains 256 mixed fractions, exponents
and integers beyond uint64 (3,073 bytes), and validates the decoded tape
against `encoding/json`. It uses CopyString and ordinary numeric decoding,
not UseNumber. Its SHA256 is
`a16b76e3a8dc6d90a9203b59b12385fbf6203eb7363fdf7818b8893281c99e41`.

| Benchmark | Tuned baseline | Retained cleanup | Paired change |
| --- | ---: | ---: | ---: |
| Compact parser | 7.2040 | 7.1675 | -0.35% |
| Compact prepare + parse | 7.3365 | 7.3350 | +0.05% |
| Twitter parser | 9.7750 | 9.7575 | -0.17% |
| Twitter prepare + parse | 9.9610 | 10.0290 | +0.64% |
| Full binding decoder | 26.6535 | 26.4720 | -0.64% |
| Full generic decoder | 50.6995 | 50.0665 | -1.30% |
| ContainerArray | 22.0030 | 22.1490 | +0.67% |
| NumericArray | 2.7035 | 2.7025 | -0.04% |
| UnicodeString | 9.2240 | 9.0450 | -1.96% |
| EscapedString | 3.1385 | 3.1350 | -0.13% |
| LongString | 1.0670 | 1.0745 | +0.05% |
| LongWhitespace | 0.2334 | 0.2329 | -0.45% |
| ColdNumbers | 11.2190 | 11.4315 | +1.79% |

Main parser-only medians are effectively unchanged. The complex-number
fixture costs 1.79% and the container fixture costs 0.67% in this measurement.
Full decoder medians improve slightly; this is not evidence of a universal
decoder speedup. The small cost is retained for clearer fallback ownership and
less continuation state. Parser-only fixtures retain zero allocations. Stage
and full decoder results are not additive or traffic-weighted.

Using the previous counting convention, Go parser/framework source grows from
326 to 370 lines (287 runs + 83 framework), while assembly falls from 761 to
748 lines (345 entry + 88 core + 315 scanner). Counts include comments and
blank lines, and exclude tests and shared numeric helpers. Total source lines
increase by 31; the maintenance benefit is fewer shared fields and continuation
phases, not a smaller total source listing.

The retained source is `source-scoped/`, with binaries `scoped.test` and
`scoped-decoder.test`, trace `instruction-attribution/scoped-*` and timing
groups `timing-scoped-*`. The `source-final/`, `final.test` and plain `timing-*`
artifacts record the first boundary candidate before the result-lifetime fix.
The rejected whole escaped-string experiment has its own source, binary and
`screen-escaped-*` groups. None of these earlier artifacts were overwritten.
