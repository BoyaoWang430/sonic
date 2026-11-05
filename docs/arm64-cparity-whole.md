# ARM64 whole-parser assembly experiment

This backend moves the optdec JSON-to-node-tape hot loop into one standard Go
ARM64 assembly function. It is selected with
`-tags=sonic_cparity,sonic_cparity_whole`; the default backend is unchanged.
The experiment follows `native/parse_with_padding.c` and uses the retained
C-generated assembly as a reference. It does not require SIMDE, ASM2ARM, a C
compiler, or a modified Go assembler.

## Boundary and implementation

The current source shares its Go allocation/growth wrapper with the later
[hybrid boundary experiments](arm64-cparity-hybrid.md). The original measured
binaries and source snapshot below remain frozen.

`internal/decoder/optdec/parse_cparity_whole_arm64.s` contains the root/object/array
state machine, literal parsing, tape visitors, packed parent/count state, and
error/growth continuations. Included headers provide string/Unicode decoding,
whitespace scanning and ordinary integers of up to 19 digits.

The parser pointer, options, input cursor, tape cursor, parent node, input start
and end stay in registers. Token position and scalar continuation also survive
scanner calls. SIMD constants are initialized once per entry. Internal helpers
use labels and a private register convention inside the same `TEXT`; they have
local branch-and-link instructions but no Go ABI argument/result transfers.
They are not public SIMD kernels.

The Go wrapper retains allocation, buffer growth, input ownership, UTF-8
correction and exact numeric fallbacks. Floats, integers longer than 19 digits,
and `UseNumber` parsing currently yield to the existing C-aligned Go numeric
implementation, then resume the assembly state machine. A cold numeric return
preserves the previous number and escape flag; a new invocation after tape
growth resets them, matching C's error behavior. `FromDom`, which creates Go
values from the tape, is unchanged.

The assembly is `NOSPLIT` and has no Go calls or allocations while active. Its
input/tape addresses refer to heap storage owned by `Parser`; the Go caller keeps
that owner alive. Like the old native parser, a long uninterrupted assembly
invocation offers no Go scheduler safepoint. This experimental backend is not a
claim that all parser modes are now entirely implemented in assembly.

## Correctness

The retained C oracle's 8,892 records / 1,452 inputs match exactly, including
cursor, errors, partial tape, statistics, Unicode mutations and depth behavior.
The digest remains
`d8618b2bf9b49d485dfad6956f340edc0eee73e8588c2e5b4053261d7161a004`.
Only unspecified null/bool payload words are normalized.

Additional tests mix numeric cold returns with escaped keys, nested containers
and tape capacities 1 through 24. Another 170 malformed-input scenarios use
explicit expected states captured from C, covering stale numeric values,
growth-induced resets and both `UseNumber` modes. Default and optimized/VM
decoder tests pass, as do C-parity tests with `GOGC=1`, stock Go 1.26.1 and
Go 1.20.14. Default-backend optdec/decoder suites also pass in both configurations.
These finite tests do not prove equivalence for every input.

## Dynamic instructions

The compiler used for measurement is the supplied Tango 1.0.1153 / Go 1.26.4,
with `CGO_ENABLED=0`, compiler defaults and `-ldflags=-checklinkname=0` on ARM64
Neoverse-N2. GDB counts original executed instructions in the same third warmed
`parseImpl` invocation used by the previous comparisons. Debugger elapsed time
is not used for performance measurement.

The Twitter input is 13,035 bytes, SHA256
`ddce57c7d74998f87f4901f1d471999ac5fcaf9f00337b5498b993d520ace970`,
in binding mode with CopyString enabled and ValidateString disabled.

| Backend | Instructions | Explicit SP/FP loads and stores |
| --- | ---: | ---: |
| Retained generated C | 78,780 | 101 |
| Previous blocks | 103,515 | 19,720 |
| Previous fused | 103,626 | 18,031 |
| Whole parser | 68,541 | 674 |

Whole reduces instructions by 33.86% against fused and 13.00% against C on this
invocation. The 575 fused string/space ABI0 calls become two entries into the
whole-parser assembly: the initial entry and one resume after a floating-point
fallback. Local assembly helper calls still exist. There are 307 key-length
stack saves and reloads, so most remaining explicit stack traffic is deliberate.
A paired load/store counts as one instruction; stack traffic is not synonymous
with register spilling.

| Synthetic input | Generated C | Fused | Whole |
| --- | ---: | ---: | ---: |
| LongString (8,194 bytes) | 9,128 | 9,141 | 9,120 |
| EscapedString (5,122 bytes) | 53,393 | 49,285 | 49,264 |
| LongWhitespace (2,053 bytes) | 1,545 | 1,705 | 1,528 |

These synthetic inputs expose individual scans; they are not production traffic
weights. Whole adds little over fused for a single long string because fused
already keeps that entire string loop inside assembly. Twitter benefits from
retaining state across its many short keys and values.

## Timing

Timing is separate from GDB and compilation, pinned to CPU 16 with
`GOMAXPROCS=1`, `SONIC_NO_ASYNC_GC=1` and normal GC. Each group uses six serial
rounds, balanced across the three variants, with 300 ms per benchmark. Affinity
does not reserve the CPU. Values below are medians in microseconds; percentage
changes are medians of paired per-round ratios, not ratios of the table medians.

| Benchmark | Generated C | Fused | Whole | Whole vs fused | Whole vs C |
| --- | ---: | ---: | ---: | ---: | ---: |
| Twitter parser | 8.9880 | 13.8590 | 8.7035 | -37.25% | -3.26% |
| Compact parser | 6.2730 | 10.7805 | 6.5905 | -38.97% | +5.11% |
| Twitter prepare + parse | 9.2145 | 13.9940 | 8.9005 | -36.55% | -3.44% |
| Full binding decoder | 25.9050 | 30.4515 | 25.3400 | -16.79% | -2.18% |
| Full generic decoder | 49.0110 | 54.0740 | 48.7140 | -9.93% | -0.55% |

`ParseBatch` restores input and parser state outside the timer; prepare + parse
includes input preparation. Binding and generic stage results are consistent;
all raw samples and ranges are retained. The full generic result is effectively
at C's level: a 0.55% paired difference is too small to treat as an established
win. These are results for these fixtures on this machine, not a universal
speedup claim.

The previously large gap is removed on Twitter, but compact JSON still takes
about 5% longer than C. Fewer total instructions alone do not predict cycles:
whole still has local helper calls and more control-transfer instructions than
C. The present measurements do not isolate which part accounts for compact
JSON's residual timing difference; this needs a separate cycle/profile study.

The largest measured instruction reduction against fused is explicit stack
traffic: 17,357 fewer instructions out of 35,085 saved overall. Arithmetic,
constant materialization and other loads also decrease when the parser state
and SIMD constants persist across tokens. The reductions demonstrate the value
of changing the assembly boundary and register lifetime; they do not show that
all earlier differences were compiler bugs or that pure Go generates the same
machine code.

Synthetic timings (nanoseconds per parse):

| Input | Generated C | Fused | Whole |
| --- | ---: | ---: | ---: |
| EscapedString | 3549.0 | 3143.5 | 3152.5 |
| LongString | 1057.0 | 1077.0 | 1072.5 |
| LongWhitespace | 220.7 | 237.8 | 221.0 |

## Reproduction and evidence

Build the candidate using the supplied compiler and the existing dependency
cache:

```sh
env GOMODCACHE=/tmp/sonic-mod-cache GOCACHE=/tmp/sonic-tango-cache \
  GOWORK=off CGO_ENABLED=0 GOTOOLCHAIN=local GOFLAGS= \
  /tmp/sonic-arm-framework/tango-compiler/bin/go test -c \
  -tags=sonic_cparity,sonic_cparity_whole -ldflags=-checklinkname=0 \
  -o /tmp/whole-rebuilt.test ./internal/decoder/optdec
```

Use `scripts/cparity/compare_backends.py` to compare existing binaries serially;
the exact commands, environments and hashes for this run are recorded under
`output/arm-native-go-cparity-whole/`:

- `metadata.json`: source and binary hashes, compiler/options, C-oracle digest.
- `source/`: the whole-parser source and matched benchmark fixtures.
- `instruction-attribution/`: exact traces, executed disassembly, per-function
  and per-instruction counts, category rules, and reusable trace scripts.
- `instruction-comparison.json`: disjoint category totals and changes.
- `timing-{stages,decoder,workloads}/`: six-round raw logs, ranges and paired
  comparisons, with binary hashes and full invocation plans.
- `oracle.jsonl` and `{c,whole}-numeric-errors.jsonl`: C comparison records.
- `runtime-frame-audit.txt`: static source/binary verification of stack metadata
  for helpers located after the core's return instruction.
- `test-*.log` and `decoder-*.log`: validation output.

The measured binaries remain frozen. The additional numeric-error test was
added afterward and run separately; it did not change parser executable code.
The prior C, blocks, fused and pure Go binaries and evidence were preserved.
