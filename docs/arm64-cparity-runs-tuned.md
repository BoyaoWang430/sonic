# Small refinements to the ARM64 runs parser

This change keeps the `sonic_cparity,sonic_cparity_runs` boundary: assembly
handles flat key/scalar runs, while Go owns container bookkeeping, Unicode,
complex numbers, allocation and growth. It does not select a different default
backend, change the compiler, or expand the assembly grammar.

Subsequent changes to operation ownership and continuation state are measured
separately in [the boundary refinement](arm64-cparity-runs-boundary.md).

## Short string probe

`runs_string` first classifies 16 bytes with one combined event mask. If the
first event is the closing quote, it completes that plain string immediately.
The probe includes controls when validation is enabled. All other outcomes
fall through to the original 32-byte scanner without moving the cursor or
writing input. In particular, encountering a control or backslash does not
let the probe decide C's unusual malformed-input priority.

The probe runs only on the first entry, not each long-string block or Unicode
continuation. It uses existing scratch registers and adds no state fields or
register preservation requirements. Long strings and initially escaped strings
pay an extra probe before the existing scanner; those cases are measured
separately. String copying and Unicode policy remain unchanged.

## Adjacent container closes

The existing Go `cParityRunsClose` operation now processes a chain of directly
adjacent, matching parent terminators. After closing the current container and
performing its depth/root checks, it can consume the parent's closing byte,
count the completed child, and close the parent in the same Go invocation.
For example, the adjacent terminators in `{"a":[{"b":1}]}` no longer each
require another assembly round trip.

Whitespace, separators and mismatched terminators return to the existing
assembly continuation without consumption. This preserves its whitespace
cache behavior and mismatch cursor/count ordering. The depth-overflow check
still occurs before decrementing active depth. Tape growth cannot occur during
this operation because closing containers allocates no new nodes. It adds
no helper layer and keeps container processing in Go.
After a chain, `ParentContinue` reconstructs the current parent from the tape;
it does not depend on the earlier close event's position, context or length. Numeric
and escape state continue to survive the return exactly as before.

The production change is confined to two files: Go grows from 210 to 243 lines,
and the scanner header grows from 293 to 315. Including the existing shared
framework, the parser core is now 326 Go / 761 assembly lines, counting
comments and blank lines but excluding tests and shared numeric helpers.

## Correctness

The existing 8,892 C-oracle records and 1,464 boundary/growth records retain
their exact digests:

* `d8618b2bf9b49d485dfad6956f340edc0eee73e8588c2e5b4053261d7161a004`
* `ed8388bc61447786e59c10496eb4c947cd0a27c8133b2d4bc0d6930856d8c247`

The added focused tests compare the new probe against the existing 32-byte Go
scanner at all 32 input alignments, with short/boundary/long inputs, validation
on/off, stale escape state, Unicode, control errors and overlapping copies.
They also cover matching/mismatched close chains, whitespace lengths around
64 bytes, capacities 1–24, decoded values/statistics against `encoding/json`,
and depths 4096/4097. The same four new tests pass on a separate copy of the
original runs backend; expectations are not snapshots of the candidate.

All C-parity tests and decoder tests pass under Tango in both the normal
environment and `GOGC=1` with optimized decoder/fastmap/VM options. C-parity
tests also pass with stock Go 1.26.1 and Go 1.20.14.

Original binaries and source snapshots remain untouched. Isolated candidates,
short screening runs, final sources, test logs and measurement metadata are
retained under `output/arm-native-go-cparity-runs-tuned/`.

## Dynamic instructions

The same warmed Twitter binding invocation is traced with Tango 1.0.1153 /
Go 1.26.4, normal compilation, `CGO_ENABLED=0` and
`-ldflags=-checklinkname=0`. The original runs count is reproduced before
tracing the candidate. The input is 13,035 bytes with SHA256
`ddce57c7d74998f87f4901f1d471999ac5fcaf9f00337b5498b993d520ace970`.

| Metric | Original runs | Tuned runs |
| --- | ---: | ---: |
| Total instructions | 78,602 | 76,613 |
| Assembly instructions | 72,046 | 69,396 |
| Go close-operation instructions | 2,261 | 2,924 |
| Assembly entries | 135 | 135 |
| Explicit SP/FP loads/stores | 2,373 | 2,372 |

The net reduction is 1,989 instructions (2.53%). This formatted fixture has
whitespace between its container terminators, so it does not benefit from
close batching: the assembly entry count is unchanged, and the extra Go
lookahead costs 663 instructions. Short-string handling saves 2,650 assembly
instructions, more than offsetting that cost. This makes the tradeoff explicit:
the probe benefits ordinary short keys, while closing chains target compact
and container-dense inputs. Instruction counts do not imply equal cycle costs,
and explicit stack accesses are not all register spills.

## Paired performance

Final timing uses six balanced serial rounds, pinned to CPU 16, with
`GOMAXPROCS=1`, `SONIC_NO_ASYNC_GC=1` and normal GC. Builds and GDB do not run
concurrently with timing. Main benchmarks use 300 ms per sample; boundary and
scan fixtures use 200 ms. Times below are median microseconds. Percentages
are medians of paired per-round ratios, not ratios of displayed medians.

### Main benchmarks

| Benchmark | Whole | Original runs | Tuned runs | Tuned vs original |
| --- | ---: | ---: | ---: | ---: |
| Twitter parser | 8.6715 | 10.1730 | 9.7340 | -3.62% |
| Compact parser | 6.5990 | 8.0760 | 7.1960 | -10.87% |
| Twitter prepare + parse | 8.9195 | 10.3735 | 10.0220 | -3.19% |
| Compact prepare + parse | 6.7250 | 8.2580 | 7.3490 | -11.08% |
| Full binding decoder | 25.3295 | 26.7975 | 26.4990 | -1.10% |
| Full generic decoder | 48.6940 | 49.9985 | 50.0565 | +0.16% |

### Boundary and scan fixtures

| Benchmark | Whole | Original runs | Tuned runs | Tuned vs original |
| --- | ---: | ---: | ---: | ---: |
| NumericArray | 2.6530 | 2.6685 | 2.6830 | +0.58% |
| UnicodeString | 5.1605 | 9.1935 | 9.2125 | +0.24% |
| ContainerArray | 10.4160 | 26.4290 | 21.9725 | -16.86% |
| LongString | 1.0840 | 1.0610 | 1.0740 | +0.85% |
| EscapedString | 3.1640 | 3.2145 | 3.1450 | -2.24% |
| LongWhitespace | 0.2209 | 0.2331 | 0.2343 | +0.64% |

The clearest gains are compact parsing and container-heavy input. The full
generic decoder is effectively unchanged in this measurement, while binding
improves modestly. Numeric, Unicode, long-string and long-whitespace medians
shift by less than 1%; raw samples and ranges are retained without a statistical
significance claim. The escaped-string fixture improves, but its copy loop was
not changed, so this result is not attributed to a new escape algorithm.

Parser-only benchmarks retain zero allocations. The original Unicode return
boundary is still expensive on Unicode-heavy input, and the container fixture
remains substantially slower than whole assembly despite this improvement.
Stage and full decoder times are not additive, and these fixtures are not
traffic weights. The two small changes are retained because they improve the
target parsing cases while preserving the existing ownership split.
