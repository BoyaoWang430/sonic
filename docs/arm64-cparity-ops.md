# ARM64 operation-oriented parser boundary

The `sonic_cparity,sonic_cparity_ops` experiment gives Go the complete JSON
grammar and uses assembly for independent, complete scanning operations. It
does not change the default backend. Unlike the runs experiment, assembly
cannot inspect `Parser`, the tape, the current parent, or the grammar phase.
Unlike the batch lexer, there is no intermediate token buffer or second token
dispatch.

## Ownership

| Responsibility | Owner |
| --- | --- |
| Grammar, punctuation, literals, container stack and depth | Compact Go FSM |
| Tape writes, statistics, error ordering and growth recovery | Go |
| Integer/float conversion and raw-number validation | Existing C-aligned Go helpers |
| Common nonspace byte probe | Inlined Go helper |
| Short whitespace probes and cached bitmap lookup | Go |
| Complete padded string scan and in-place decoding | Existing fused assembly operation |
| Refill whitespace bitmap, including consecutive all-space blocks | Existing fused assembly operation |
| Classify one 16-byte raw-number block | Existing SIMD leaf; still a small boundary |

The string operation takes an input address and options and returns consumed
bytes, decoded length/error, and escape status. The whitespace operation takes
an input address and an operation-local bitmap cache. Neither can publish a
JSON node or advance a container. Their padded allocation/lifetime contracts
are documented beside the Go declarations. All experiments share allocation
and restart logic in `parse_cparity_framework_arm64.go`.

SIMD is useful for string-byte classification, contiguous copies, and long
whitespace classification. Ordinary integer conversion, literals, punctuation,
container bookkeeping and Unicode conversion do not require SIMD. Keeping
scalar escape/Unicode handling inside the complete string operation is a
deliberate cohesion/performance tradeoff: splitting every escape into a Go
callback would interrupt the same operation repeatedly. This is distinct from
moving unrelated grammar into assembly.

## Remaining limitations

* The complete string operation still contains handwritten Unicode policy.
  Its independent boundary is easier to test than a parser-wide state machine,
  but does not make that policy free to maintain. The existing offset,
  surrogate, overlap and error-priority tests are part of its contract.
* `internal/native/kernels` already has bounded string/space/number operations.
  Their contracts differ from this padded C-parity experiment, particularly
  malformed-input cursor/error priority. Reusing them requires an explicit
  padded entry point or a conscious semantic change. Sharing classification
  and mask-packing code is a lower-risk first consolidation step.
* Raw-number validation still calls a SIMD mask leaf for each 16-byte block.
  A complete candidate-number scan could amortize this boundary if a separate
  `UseNumber` benchmark justifies it. The binding benchmarks do not exercise
  this option and cannot establish its benefit.
* Short plain strings still always enter the complete assembly operation.
  A conservative Go/SWAR fast path for short keys is a useful next experiment:
  only accept an unescaped closing quote in a small prefix, otherwise fall
  back without changing the input. This could avoid SIMD setup and ABI work
  where the input is too short to benefit. It has not been implemented or
  measured here, and must retain C's control/error ordering.
* The padded functions are private operations, not arbitrary-slice APIs. Their
  address-only arguments require owned sentinel padding and caller lifetime
  management. A long string operation remains non-preemptible until return.
* Experimental backends remain for controlled comparisons; their total file
  count is not the intended final production layout. Consolidation should
  follow an explicit boundary choice and preserve the frozen evidence.

No compiler changes or mechanical source expansion are used in this backend.

## Source changes and instruction evidence

The first compact implementation had an out-of-line nonspace probe and an
extra Go wrapper around each string assembly call. A small normally inlined
probe (cost 80 under Tango) and direct use of the string operation's offset
results removed those layers. The two grammar sites still independently
implement the required key/value error publication order; no parser source
expansion is used.

The remaining major source adjustment was to stop caching `start`, `end`,
`options` and derived buffer addresses across every scanner call. Reading
them from the owned `Parser` where needed shortens live ranges. The last
cleanup snapshots the tape write cursor before writing through `unsafe`,
avoiding conservative reloads after the node store.

| Matched Twitter trace | Dynamic instructions | Explicit SP/FP loads/stores |
| --- | ---: | ---: |
| Generated C reference | 78,780 | 101 |
| Whole assembly | 68,541 | 674 |
| Runs | 78,602 | 2,373 |
| Ops before shortening live ranges | 130,624 | 35,170 |
| Ops final | 121,779 | 22,995 |

The final trace contains 414 complete-string assembly calls and 161 whitespace
refill calls. Their instruction counts are 23,925 and 8,533 respectively.
Go whitespace scanning still accounts for 26,060 instructions in 709 calls;
the Go FSM itself accounts for 55,256. These function totals are disjoint, but
do not isolate cycles or prove that every stack access is a compiler spill.
The whole Go whitespace helper exceeds the normal inline budget, so only the
common nonspace probe is inlined. Compiler inlining/register-allocation work
is recorded as a limitation rather than addressed with compiler patches or
duplicating the scanner into every grammar continuation.

A cursor-by-value wrapper experiment also passed the oracle but made the
Twitter screening benchmark about 12% slower than its matched control. It was
not retained. The tape-cursor cleanup reduced only 134 net instructions and
did not establish a meaningful timing gain; it is primarily a clearer value
flow. Intermediate binaries, sources and screening samples are retained under
`output/arm-native-go-cparity-ops/`.

The core Go files contain 487 physical lines (328 FSM/helpers, 83 framework,
76 operation wrappers/contracts); the two complete assembly operations share
391 lines. The shared leaf assembly file adds 138 lines, including the
raw-number mask operation. Shared scan/number/decimal code adds 789 Go lines,
leaf declarations add 25, and numeric tables add another 820. Thus the
relevant-file total excluding tables is 1,301 Go / 529 assembly lines. Counts
include comments/blanks, exclude tests, and do not count the rest of the decoder
or unused experimental backends. Runs' dedicated assembly was 739 lines;
the reduction to 391 removes grammar and tape coupling, not just source lines.

## Validation

The final candidate matches 8,892 C-oracle records (digest
`d8618b2bf9b49d485dfad6956f340edc0eee73e8588c2e5b4053261d7161a004`) and
1,464 boundary/growth records (digest
`ed8388bc61447786e59c10496eb4c947cd0a27c8133b2d4bc0d6930856d8c247`).
The 170 numeric error/restart cases, independent fused string/space tests,
and mixed cold-path/growth checks pass. Decoder tests pass in the normal
environment and with `GOGC=1`, optimized decoder, fastmap and VM encoder.
C-parity tests also pass with stock Go 1.26.1 and Go 1.20.14. Whole, runs,
batch and fused C-parity suites pass after the build-tag changes, and expanded
reference files reproduce byte-for-byte with their generator.

Instruction tracing uses Tango 1.0.1153 / Go 1.26.4, normal compilation,
`CGO_ENABLED=0`, `-ldflags=-checklinkname=0`, and the same warmed 13,035-byte
Twitter binding invocation as the previous comparisons. The input hash is
`ddce57c7d74998f87f4901f1d471999ac5fcaf9f00337b5498b993d520ace970`.
Debugger elapsed time is never used as a performance measurement.

## Paired timing and decision

The main results use eight balanced serial rounds at 300 ms, CPU 16,
`GOMAXPROCS=1`, `SONIC_NO_ASYNC_GC=1`, normal GC and no concurrent builds or
debugger. Times are median microseconds; percentages are medians of per-round
paired ratios, so they need not equal ratios of the displayed medians. These
are fixtures on the measured ARM64 machine, not traffic-weighted estimates.

| Benchmark | C | Whole | Runs | Ops | Ops vs runs |
| --- | ---: | ---: | ---: | ---: | ---: |
| Twitter parser | 8.9935 | 8.6780 | 10.1720 | 14.5025 | +42.62% |
| Compact parser | 6.2775 | 6.6165 | 8.0735 | 10.6380 | +31.79% |
| Twitter prepare + parse | 9.2170 | 8.9185 | 10.3765 | 14.7315 | +42.01% |
| Compact prepare + parse | 6.4130 | 6.7515 | 8.2385 | 10.7370 | +30.37% |
| Full binding decoder | 25.9495 | 25.3890 | 26.7495 | 32.7050 | +22.24% |
| Full generic decoder | 48.8905 | 48.6830 | 50.1010 | 58.4530 | +16.69% |

The boundary fixtures use six balanced serial rounds at 200 ms:

| Parser fixture | Whole | Runs | Ops | Ops vs runs |
| --- | ---: | ---: | ---: | ---: |
| NumericArray | 2.6605 | 2.6645 | 4.2245 | +58.55% |
| UnicodeString | 5.1600 | 9.1880 | 5.1385 | -44.09% |
| ContainerArray | 10.4950 | 26.4670 | 16.5485 | -37.44% |

Parser-only results retain zero allocations. Stage and full decoder timings
are not additive. Raw samples, ranges, environment and binary hashes are in
`output/arm-native-go-cparity-ops/timing-{stages,decoder,boundaries}/`.

This boundary meets the independent-operation maintenance objective much
better, and removes the Unicode/container event round-trip penalties visible
in runs. It does not achieve an overall speed improvement: ordinary numbers
and the Twitter/compact parsers regress. It remains an opt-in experimental
backend. The next useful work is short-input handling and reducing Go scanner
call/state overhead, with reuse of the public kernel code only after reconciling
its contracts. Expanding assembly back into JSON grammar would undo the
maintenance objective this experiment is testing.
