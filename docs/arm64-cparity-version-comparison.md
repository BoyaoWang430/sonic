# ARM64 parser version comparison

This report compares the retained current runs implementation with eight major
historical implementations. It separates elapsed time, source lines and dynamic
instructions. The comparison does not change production source or select a new
default backend.

## Measurement scope

All main measurements were rerun together using the frozen binaries listed in
`output/arm-native-go-cparity-version-comparison/variants.json`. They use Tango
1.0.0.1153 / Go 1.26.4, ARM64 Neoverse-N2 CPU 16, `CGO_ENABLED=0`,
`-ldflags=-checklinkname=0`, `GOMAXPROCS=1`, `SONIC_NO_ASYNC_GC=1` and normal GC.
Ops additionally has the optimizer diagnostic flag `-m=2`; it does not alter
the optimization budget. Backend selection tags differ intentionally.

The nine versions run in 18 balanced serial rounds at 200 ms per sample. No
builds, debugger or other benchmark run concurrently. These are measurements
on this machine and these inputs, not traffic-weighted or universal results.
Raw samples, ranges and paired ratios are retained; small differences are not
claimed as statistically established wins.

Twitter is 13,035 bytes and Compact is 8,914 bytes. The stage harnesses have
identical code after removing comments/blank lines; decoder benchmark and
input sources are byte-identical. ParseBatch restores input outside the timer
and uses warmed storage. It measures parsing into the node tape, not complete
Unmarshal. The two full-decode cases include construction of Go results and
allocations; their times cannot be added to stage times.

## Main timing

Median microseconds per operation; lower is better.

| Version | Twitter parser | Compact parser | Full binding decode | Full generic decode |
| --- | ---: | ---: | ---: | ---: |
| Generated C reference | 8.9900 | 6.2755 | 25.9260 | 48.9830 |
| C-aligned pure Go (expanded) | 14.5030 | 10.2075 | 30.9485 | 54.6035 |
| C-aligned Go + fused scans (expanded) | 13.8835 | 10.7950 | 30.4430 | 54.0565 |
| Batch lexer + Go grammar | 13.4060 | 10.9455 | 30.0005 | 53.6650 |
| Go grammar + independent operations | 14.4825 | 10.6325 | 32.8975 | 58.4970 |
| Whole assembly hot loop | 8.6950 | 6.6010 | 25.3510 | 48.7610 |
| Original runs | 10.1790 | 8.0700 | 26.7950 | 50.1175 |
| Previous boundary runs | 9.7755 | 7.1655 | 26.4485 | 50.0385 |
| Current private-operation runs | 8.8965 | 6.2545 | 25.5390 | 49.0335 |

Percent changes below use medians of within-round ratios, so they need not
match ratios of the displayed medians. Negative means less elapsed time.

| Benchmark | Current vs C | Current vs whole | Current vs original runs | Current vs previous |
| --- | ---: | ---: | ---: | ---: |
| Twitter parser | -0.97% | +2.52% | -12.31% | -8.81% |
| Compact parser | -0.33% | -5.25% | -22.34% | -12.67% |
| Full binding decode | -1.47% | +1.01% | -4.60% | -3.40% |
| Full generic decode | +0.05% | +0.47% | -2.21% | -2.05% |

On these main fixtures, current runs is effectively at the generated C
reference's performance level. Against whole assembly, Twitter parsing and
full decoding are slightly slower, while Compact parsing is faster. Current
runs substantially improves on the older Go-oriented parser variants and the
original runs boundary; this is not evidence that every input now matches
whole assembly.

## Boundary-sensitive fixtures

These use eight balanced serial rounds at 200 ms. The whole stress binary is
`whole-final-workloads.test`, a later rebuild with the extra fixtures, rather
than the original `whole.test`. Its assembly and helpers are identical; the
wrapper functions were moved unchanged into the shared framework. This
relationship, all binary hashes, and identical fixture harnesses are recorded
in `trace-index.json`. The whole binaries are not presented as the same file.

Median microseconds; the last column is a paired change.

| Parser fixture | Whole | Ops | Original runs | Current | Current vs whole |
| --- | ---: | ---: | ---: | ---: | ---: |
| ContainerArray | 10.3635 | 16.5275 | 26.4375 | 13.6655 | +31.90% |
| NumericArray | 2.6440 | 4.2235 | 2.6605 | 2.2755 | -13.77% |
| UnicodeString | 5.1590 | 5.1430 | 9.1980 | 7.9025 | +53.14% |

Unicode remains a deliberate Go/assembly continuation in current runs; whole
and Ops keep its policy within their string assembly operation. Container
bookkeeping remains in Go for current runs. The Unicode/container gaps remain
material even though the main documents are close to C/whole. Ordinary numeric
arrays benefit from the current assembly hot path.

## Source line counts

Physical lines include comments and blanks; tests, unrelated experimental
backends, the rest of Sonic and the Go runtime are excluded. Core Go counts
orchestration and its allocation/growth wrapper. Dedicated ASM counts the
backend's `.s` and included `.h` files. The all-selected columns additionally
include that backend's shared and operation-specific helper files selected by
its actual build tags. Entire selected files are counted even when the linker
can remove unused functions; these totals are not linked machine-code sizes.

| Version | Core Go | Dedicated ASM | All selected Go, excluding tables | Go tables | All selected ASM |
| --- | ---: | ---: | ---: | ---: | ---: |
| C-aligned pure Go (expanded) | 10137 (generated) | 0 | 11304 | 820 | 0 |
| C-aligned Go + fused scans (expanded) | 5183 (generated) | 391 | 6063 | 820 | 529 |
| Batch lexer + Go grammar | 477 | 333 | 1564 | 820 | 471 |
| Go grammar + independent operations | 411 | 391 | 1301 | 820 | 529 |
| Whole assembly hot loop | 137 | 933 | 1224 | 820 | 1071 |
| Original runs | 293 | 739 | 1380 | 820 | 877 |
| Previous boundary runs | 370 | 748 | 1457 | 820 | 886 |
| Current private-operation runs | 371 | 761 | 1458 | 820 | 899 |

Every listed Go backend selects an 820-line numeric table file. Including it,
current runs has 2,278 selected Go lines and 899 ASM lines. Excluding that table
it has 1,458 Go lines. Its core remains 371 Go / 761 ASM, compared with whole's
137 / 933: 234 more core Go lines and 172 fewer dedicated ASM lines (18.44%).
The shared 138-line assembly file is included in all-selected ASM, and may
contain functions unused by the measured fixture.

Pure Go and fused used mechanically expanded generated Go parsers. Their
10,137 / 5,183 core lines are real build-selected source but are not handwritten
maintenance counts. Each has a 637-line authoring parser reference, plus
helpers and a generator, listed separately in `source-counts.json`.
Pure Go means no custom assembly in this parser backend, not that the Sonic
library or Go runtime contains no assembly.

The old C reference separately has a 1,382-line C main file, 14,113 lines of
generated assembly, and 55 lines in its two generated Go wrappers. Generated
assembly includes expanded helper bodies, constants and bridge code and is
not comparable to handwritten assembly maintenance effort. Its 12,676 WORD
directives must not be treated as dynamic instruction counts. The C source
count is for the main file, not all included C/SIMDE dependencies.

`source-counts.json` contains every counted file, path, line count, build
constraint and SHA256. `count_sources.py` reproduces the census without builds.

## Dynamic instruction counts

These count all executed callees within the same warmed Twitter binding
parseImpl invocation: CopyString enabled, ValidateString disabled, input SHA256
`ddce57c7d74998f87f4901f1d471999ac5fcaf9f00337b5498b993d520ace970`.
Saved traces are used only when their binary hashes match the timed parser
binaries. The final batch binary was freshly traced for this comparison,
reproducing 119,816 instructions. Debugger durations are never benchmark times.

| Version | Dynamic instructions | Go-to-ASM entries |
| --- | ---: | ---: |
| Generated C reference | 78,780 | 1 |
| C-aligned pure Go (expanded) | 145,166 | 0 |
| C-aligned Go + fused scans (expanded) | 103,626 | 575 |
| Batch lexer + Go grammar | 119,816 | 57 |
| Go grammar + independent operations | 121,779 | 575 |
| Whole assembly hot loop | 68,541 | 2 |
| Original runs | 78,602 | 135 |
| Previous boundary runs | 77,170 | 135 |
| Current private-operation runs | 76,806 | 135 |

Current runs executes 2.51% fewer instructions than the C reference and 12.06%
more than whole on this invocation. Instructions are not cycles: the current
private leaf functions' use of RET was independently shown to improve elapsed
time with little change in instruction count. Private assembly-internal calls
are excluded from the Go-to-ASM crossing column. Batch's 57 includes 43 lexer,
12 string-mask and 2 copy calls; it is not only the lexer-entry count.

## Evidence

The comparison directory contains raw timings and binary hashes, the source
census, the trace/ABI-call audit and the newly collected batch trace. Existing
artifacts remain untouched. `metadata.json` ties the consolidated result to
these files. This comparison reuses the prior correctness evidence and each
benchmark's decoded-result checks; it does not claim to resolve the previously
documented native-contract test differences of the C-parity experiments.
