# ARM64 AST scan boundaries

This change applies the optdec Runs design principle to the native operations
used by AST: Go owns paths, grammar, state, errors, and allocation; an assembly
function completes one bounded scanning operation. The retained optdec Runs
sources and backend selection are unchanged.

A subsequent [string-cache handoff refinement](arm64-ast-boundary-refinement.md)
removes repeated Go probing after cached events run out. It adds no assembly
and improves the dense escaped-string array queries by 9.7–17.7% relative to
the version measured below. The tables in this document retain their original
baseline and measurement scope.

## Responsibilities

| Operation | Go responsibility | ARM64 assembly responsibility |
| --- | --- | --- |
| Path lookup | Match keys and indexes, compare escaped keys, decide whether to validate the selected value | Scan strings and skipped containers |
| Relaxed container skip | Identify the opening bracket and translate the returned offset | Find its matching same-kind closing bracket, ignoring brackets inside strings |
| Path string scan | Probe short strings, compare keys, retain classifications for subsequent tokens | Scan the remaining string and return raw quote/backslash events already loaded after its closing quote |
| Relaxed number skip | Choose relaxed traversal and consume the following delimiter | Find the first comma, closing bracket/brace, or JSON whitespace |
| JSON validation and token reading | Retain the existing token traversal, state-machine fallback, numeric conversion, and error reporting | Use the existing bounded scanning contracts |

The three loops previously returned from assembly to Go after each 64-byte
classification block. The container loop also reloaded constants and returned
four masks on every call. Relaxed number skipping calculated string masks that
its caller never used. These loops now stay inside their scanning functions.

The container scanner deliberately does not validate JSON grammar: it counts
only brackets of the opening kind, preserves quote/backslash state across
blocks, and returns the first matching close. Key comparison, Unicode handling,
container validation, and AST node management remain in Go. Each scanner uses
bounded loads, needs no padding, and leaves its input unchanged.

`string_block_arm64.h` is shared by the ordinary relaxed scanner and the path
scanner. The two result shapes are selected at assembly time, so the ordinary
entry has no extra mode branch or cache work. Its expanded instruction sequence
matches the preceding implementation. The obsolete structural-mask entry and
unreachable Go relaxed-string loop were removed.

## Measurement

The original C-generated NEON backend, the preceding Go backend, and this
candidate use byte-identical AST source and benchmark fixtures. Binaries were
built with Tango 1.0.0.1153 / Go 1.26.4, ARM64 v8.0, CGO disabled and
`-ldflags=-checklinkname=0`. Timings use CPU 16, `GOMAXPROCS=1`, asynchronous
Sonic GC disabled, and serial balanced ordering. Debugger runs are separate
from timings.

Results and instruction counts are recorded in
`output/arm-ast-boundaries/`. This directory retains the preceding sources,
changed final sources, binary hashes, commands, raw measurements, profiles,
oracle records, and instruction-counting scripts.

Core results use 12 balanced rounds of 200 ms per benchmark. The table shows
median timings; percentage changes are medians of within-round ratios. Negative
is faster. Allocation counts and bytes per operation are unchanged.

| Benchmark | Original C (µs) | Previous Go (µs) | Current (µs) | vs previous | vs C |
| --- | ---: | ---: | ---: | ---: | ---: |
| GetFull_Sonic | 10.780 | 9.402 | 9.341 | -0.69% | -13.38% |
| GetOneSafe_Sonic | 3.385 | 4.798 | 3.756 | -21.67% | +10.98% |
| GetOne_Sonic | 3.335 | 4.750 | 3.699 | -22.13% | +10.87% |
| GetWithManyCompare_Sonic | 2.502 | 1.462 | 1.462 | -0.03% | -41.62% |
| LoadNode/Interface() | 25.051 | 27.564 | 27.613 | +0.16% | +10.24% |
| LoadNode/InterfaceUseNode() | 7.426 | 8.242 | 7.900 | -4.13% | +6.33% |
| LoadNode/Load() | 6.978 | 7.787 | 7.459 | -4.10% | +6.98% |
| LoadNode/LoadAll() | 6.987 | 7.763 | 7.455 | -3.96% | +6.68% |
| ParseSeven_Sonic | 14.201 | 18.805 | 15.461 | -17.69% | +8.90% |
| Parser_Sonic | 4.378 | 6.064 | 4.576 | -24.58% | +4.55% |
| PathCacheLongQueries/matchKey4096 | 0.862 | 1.091 | 0.592 | -45.69% | -31.21% |
| PathCacheLongQueries/missingAfterLongKey4096 | 0.537 | 0.912 | 0.408 | -55.29% | -24.09% |
| PathCacheLongQueries/skipKey128 | 0.127 | 0.161 | 0.152 | -5.95% | +20.14% |
| PathCacheLongQueries/skipKey4096 | 0.615 | 0.985 | 0.494 | -49.89% | -19.92% |
| PathCacheLongQueries/skipNumber4096 | 0.527 | 1.558 | 0.628 | -59.69% | +19.16% |
| PathCacheLongQueries/skipString4096 | 0.628 | 1.009 | 0.506 | -49.83% | -19.47% |
| PathCacheLongQueries/skipStringDense4096 | 0.746 | 1.129 | 1.019 | -9.74% | +36.66% |
| PathCacheLongQueries/skipStringSparse4096 | 0.648 | 1.010 | 0.541 | -46.42% | -16.48% |

These gains are specific to traversal/scanning. Recursive generic conversion
(`LoadNode/Interface()`) and short-key comparison are essentially unchanged.
Dense escaped strings still cost 36.7% more than C; long relaxed numbers cost
19.2% more, and the Twitter path query costs 10.9% more. The remaining gap is
not attributed wholesale to the compiler: the current Go traversal, bounded
tails, and NEON mask construction still differ from the original C program.

Benchmark names need care:

- `GetFull` validates/scans the document and returns a raw node; it does not
  materialize the AST tree.
- `Parser_Sonic` performs `Parse + LoadAll`. `LoadAll` loads the current layer
  with `loadOnce=true`, preserving child containers as raw nodes.
- `LoadNode/Interface()` includes recursive conversion to Go values.
- `ParseNoLazy_Sonic` measures repeated access to an already loaded node and
  is excluded from the final parser-throughput comparison.

## Workload matrix

A further 54 workloads use six balanced rounds of 100 ms. They cover full
document scans, path shapes, array indexes, and selected containers in both
validation modes. Allocation counts are unchanged in all 54 workloads. The
four dense escaped-string array-index cases regress by 1.5–2.4%; this small
tradeoff is retained to avoid a second short/dense scanning implementation.
The complete results are in `timing-matrix/summary.json`. Selected results:

| Matrix scenario | Current vs previous Go |
| --- | ---: |
| GetFullFlowCoverage/FloatArray | -6.91% |
| GetManyShortEscapedFields | -18.62% |
| GetScanArrayIndex/denseStrings/compact/last | +1.79% |
| GetScanArrayIndex/denseStrings/compact/missing | +2.06% |
| GetScanArrayIndex/denseStrings/spaced/last | +2.39% |
| GetScanArrayIndex/denseStrings/spaced/missing | +1.52% |
| GetScanQueryCoverage/nestedObjects | -27.25% |
| GetScanQueryCoverage/nestedPath | -27.10% |

## Dynamic instruction counts

GDB counts the third warmed `ast.(*Parser).getByPath` invocation and all its
callees, from entry to return. Each scenario verifies identical input hashes
across all three binaries. `getone` is the Twitter query, `string` is the dense
escaped long-string query, and `number` skips a 4,096-byte numeric candidate.
The Twitter AST fixture is 13,023 bytes; it is distinct from the optdec fixture.

| Query | C instructions | Previous Go | Current | Current vs previous | Go-to-kernel calls, previous → current |
| --- | ---: | ---: | ---: | ---: | ---: |
| getone | 31,466 | 48,031 | 36,716 | -23.56% | 155 → 7 |
| string | 7,327 | 13,959 | 10,951 | -21.55% | 72 → 1 |
| number | 4,655 | 14,434 | 5,743 | -60.21% | 64 → 1 |

The call column counts `kernels.*.abi0` entries only; C has its own generated
NEON entry. In the Twitter query, container classification alone changes from
153 block calls to five complete-container calls. Total instructions remain
16.7% above C, compared with 52.6% before this change. The preceding parser
CPU profile attributed about 88% of samples to container scanning and block
classification; this is a sampled profile, not an instruction count.

## Maintenance size

Counting physical lines, including comments and blanks but excluding tests,
the changed Go files shrink from 304 to 252 lines. Assembly `.s` files plus
the shared `.h` grow from 548 to 752 lines: a net change of -52 Go and +204
assembly lines. These are the affected files, not the size of the entire AST
or all shared native kernels. The shared string body is counted once, although
it generates two result-shape variants. No generated C assembly is introduced.

## Validation and compatibility

The independent native API oracle records return codes, cursors, active parser
stacks, and numeric/string state. All 40,946 candidate records match the
preceding Go implementation byte for byte. The retained golden is
`d0d9890199e986afe12deb0cf9168806da295fb43b808794cbb1587a2d3e46b3`.

The old C implementation is also recorded, without normalizing its behavior.
There are 2,181 pre-existing C/Go record differences: 1,711 cursor-only records,
304 token-reader return/cursor records with identical value fields, 14 error
state/cursor records, 142 accept/reject records, and 10 error-code records.
The 142 records are 71 malformed-string mutations tested through both strict
skip and validation: C accepts them and the preceding Go backend rejects them.
The ten error-code differences concern incomplete path inputs. These are
recorded as existing compatibility differences, not attributed to this change.

Validation covers the native, kernel, number, AST, decoder, encoder, UTF-8 and
unquote packages with Tango, both default settings and optimized/VM settings
with `GOGC=1`. Focused native/kernel/AST tests also pass stock Go 1.26.1 and
Go 1.20.14. The retained optdec Runs CParity tests pass. RISC-V kernel tests
cross-compile; RISC-V execution was not performed.

Existing and new scanner tests cover randomized malformed input, every possible
numeric boundary byte, alignment, cross-block escape parity, first-close
ordering, logical-length poison bytes, protected pages, read-only input and
zero-length inputs. A five-second `FuzzScanStringBlock` run completed 74,608
executions without a failure. The new oracle uses the existing `rt.String` helper to
remain compatible with the module's Go 1.18 language setting on Go 1.20.
