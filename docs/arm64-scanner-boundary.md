# ARM64 complete string and number scanners

ARM64 now keeps each string or numeric token scan inside one assembly
invocation, following the RISC-V scanner boundary. Several scanning and AST
workloads improve, but this boundary does not improve every caller.

## Operation ownership

Go retains AST traversal, container grammar, ownership, allocation, number
conversion, and translation of scanner-relative offsets. The assembly owns:

- `string_scan_strict_arm64.s/.h`: bounded NEON scanning, escape checks,
  cross-block continuation, and exact error positions. The ordinary and suffix
  metadata entries share one body. Relaxed string scanning is unchanged.
- `number_scan_arm64.s`: all three existing numeric modes (prefix, strict
  candidate, relaxed boundary), short words, NEON digit runs, and marker state.
  An integer that has not encountered a marker needs no final marker checks.

Numeric Go code only declares `numberScan`. A small Go mode dispatcher was not
inlined by the tested compiler and added a call/frame; it was removed rather
than retaining another boundary before the complete operation.

These functions perform one bounded token operation. They do not allocate,
convert numbers, decode Unicode, or traverse the AST. No readable padding,
custom build tag, Clang, SIMDe, or assembly generator is required. RISC-V
production sources and scalar/RVV selection are unchanged.

Strict strings occupy 222 physical assembly source lines, including the two
entry points and shared header. Numbers occupy 349, including the existing
relaxed scanner and table. The affected Go declarations/dispatch shrink from
259 to 52 lines. Removing old helpers and moving the relaxed number scanner
removes 275 assembly lines: the net production change is **-207 Go lines and
+296 assembly lines**. Counts include comments/blanks and describe this change,
not the whole backend or compiled instruction counts.

## Timing comparisons

The baseline is retained commit `8e580e0459c76716d4f019b2951cc9d8e9471d17`,
with identical benchmark fixtures. Measurements use Neoverse-N2, Tango
1.0.0.1153 / Go 1.26.4, CPU 16, GOMAXPROCS=1, normal GC, fixed argv[0], and
eight balanced before/after rounds at 150 ms per benchmark. Compilation and
tests were idle. Negative changes mean less time per operation. This compares
with the preceding Go/assembly backend, not the original generated C backend.

| Workload | Before ns/op | After ns/op | Paired change |
| --- | ---: | ---: | ---: |
| Strict mixed-escape string kernel | 2919.00 | 2253.00 | -22.72% |
| Strict early escape + long plain suffix | 984.50 | 473.70 | -51.89% |
| Strict plain 4096-byte string | 465.35 | 476.90 | +2.48% |
| Strict short integer kernel | 12.93 | 8.41 | -34.93% |
| Strict short float kernel | 18.34 | 16.62 | -9.34% |
| AST search: small integer array | 6107.50 | 5441.50 | -10.83% |
| AST search: mixed float array | 7976.50 | 7664.50 | -3.91% |
| AST search: long decimal array | 4842.50 | 4179.00 | -13.65% |
| Native FSM: escaped object array | 22129.00 | 14693.00 | -33.59% |
| Native FSM: small integer array | 7203.00 | 8598.50 | +19.32% |
| Native FSM: mixed float array | 9246.50 | 10530.00 | +13.91% |

`Native FSM` explicitly enables `F_VALIDATE_STRING`. Default
`Searcher.ValidateJSON` controls structural validation without enabling that
flag, and uses a different traversal path. Compare each row before/after;
the difference between these APIs is not the cost of string validation alone.
The strict-string kernel gains do not imply corresponding default
Marshal/Unmarshal gains.

The full **121-case matrix**, including public Default/Std Marshal/Unmarshal,
AST queries and materialization, is in [timings.csv](arm64-scanner-boundary/timings.csv).
Small changes and improvements in unrelated Marshal operations should not be
attributed solely to scanner logic: code layout and measurement variability
also affect these binaries. CPU affinity is not exclusive host isolation.
The medians/ranges are not significance tests or confidence intervals.
Allocation counts are unchanged in all 121 cases. Two public benchmarks differ
by one reported byte/op; the CSV retains those measurements.

The explicit native FSM's short integer and float arrays remain slower despite
lower retired instruction counts. The complete assembly boundary is therefore
not a universal latency win for small tokens. Ordinary AST paths improve;
the residual FSM cases are recorded rather than adding a second Go grammar or
a larger combined container parser. A caller disassembly comparison of the
first candidate found identical FSM spills and instruction structure; it did
not support blaming extra caller spills. This is not proof of a compiler bug
or a complete explanation of the remaining latency difference.

## Dynamic instructions

User-mode hardware PMU counters estimate instructions per operation. Three
repetitions subtract the process count at N iterations from that at 2N, then
divide by N. This mostly removes startup/setup and includes benchmark loops
and Go wrappers. Array fixtures measure a document or token batch per operation,
not one number. Timings above were collected separately, without perf.

| Fixture | Before instructions/op | After instructions/op | Change |
| --- | ---: | ---: | ---: |
| `string_dense` | 39100.8 | 26135.4 | -33.16% |
| `string_early_escape` | 12030.2 | 5026.9 | -58.21% |
| `string_plain7` | 192.0 | 160.0 | -16.67% |
| `number_short_float` | 243.0 | 197.0 | -18.93% |
| `number_strict_float` | 312.0 | 230.0 | -26.29% |
| `number_sequential` | 148460.9 | 112631.5 | -24.13% |
| `native_strict_strings` | 140625.2 | 89812.7 | -36.13% |
| `native_strict_integers` | 118423.5 | 99060.8 | -16.35% |
| `native_strict_floats` | 145809.2 | 116470.0 | -20.12% |
| `ast_mixed_floats` | 136298.2 | 106969.6 | -21.52% |

Estimates and repetitions are in [instructions.json](arm64-scanner-boundary/instructions.json).
Binary/source hashes, fixtures and measurement settings are in
[provenance.json](arm64-scanner-boundary/provenance.json). Raw logs, binaries
and experiment snapshots remain local under `output/arm-scanner-boundary/`.

## Validation

- Tango: kernel, native traversal/conversion, AST, optdec, decoder and root
  tests pass in default and optimized/VM configurations. The latter also
  includes encoder and UTF-8 packages.
- Stock Go 1.26.1 and Go 1.20.14: kernel, native and AST suites pass.
- Guard-page, logical-tail, dense escape, cross-block Unicode, marker priority
  and suffix-mask tests pass. Added coverage exhausts every escape byte and
  Unicode hex position, compares 12,000 generated numeric candidates against
  an independent byte reference, and tests the production numeric entry
  instead of the removed digit helper.
- `FuzzScanStringBlock` completed 848,000 inputs in 15 seconds. The final numeric
  state change passed 195,541 fuzz inputs in 5 seconds, in addition to earlier
  numeric runs. No failing inputs were found.
- All five RISC-V build-selection configurations pass, and kernel tests
  cross-link for RVA22 and RVA23. These are not target execution tests or
  RISC-V performance measurements.

To repeat the timing comparison, build kernel, root and AST test binaries from
both revisions with `go test -c -ldflags=-checklinkname=0`, copy the two added
benchmark fixtures into the baseline, and use
`scripts/cparity/compare_backends.py` with the regexes/settings in the
provenance file. Omit the linker flag with Go 1.20. Existing benchmark sources
must also be identical on both sides.
