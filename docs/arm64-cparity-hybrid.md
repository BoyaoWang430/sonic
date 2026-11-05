# ARM64 Go/assembly boundary experiments

The purpose of this experiment is to move policy and complex control back into
Go while retaining most of the whole-parser assembly backend's performance.
It does not change the default backend or modify the Go compiler.

For the stricter requirement that each assembly entry solve an independent
operation, runs remains too coupled to grammar and tape state. The subsequent
[operation-oriented experiment](arm64-cparity-ops.md) moves those responsibilities
entirely to Go and reuses complete string/space operations. It documents the
remaining maintenance issues and measures the cost of that narrower boundary;
the runs figures below remain the results of this earlier compromise.

Subsequent [small runs refinements](arm64-cparity-runs-tuned.md) retain this
ownership split while adding a short-string probe and batching adjacent Go
container closes. That document contains their separately measured results;
the original runs evidence below remains unchanged.

## Implemented boundaries

The closer maintenance/performance compromise on the Twitter fixture is
`sonic_cparity,sonic_cparity_runs`. Dense-container and Unicode-heavy workloads
still regress substantially, so this remains an opt-in candidate:

| Responsibility | Owner |
| --- | --- |
| Input ownership, allocation, tape growth and restart | Shared Go wrapper |
| Container entry/closure, parent restoration, depth and container statistics | Go |
| Unicode validation, surrogate handling and replacement | Existing exact Go helper |
| Floating-point, long integers, UseNumber and invalid literal recovery | Go |
| Flat key/value runs, separators and scalar tape writes | Assembly |
| SIMD whitespace/string scans, copies and simple ASCII escapes | Assembly |
| Common integer conversion | Small assembly helper |

The assembly runs directly on the input and final tape until it reaches a
container boundary or a complex decoding event. It does not materialize an
intermediate token stream. This still retains some scalar grammar and node
emission in assembly; it is deliberately broader than a collection of SIMD
intrinsics.

`parse_cparity_framework_arm64.go` now provides the common constructor,
allocation/growth wrapper and lifetime management for whole, batch and runs.
The three copies of that code were removed. `parse_cparity_runs_arm64.go` owns
the orchestration and C-compatible recovery rules. Events, phases and contexts
are defined in Go and exported to assembly by `go_asm.h`.

Unicode returns preserve the source and contracted output pointers inside the
owned padded allocation. Go handles a run of adjacent Unicode sequences, including surrogate retries;
assembly then resumes its string scan. Numeric/escape state
survives these returns, but resets after actual tape growth, as in C. Existing
scalar tape writes still precede value errors; key errors precede key writes.

The private assembly convention is documented in `parse_cparity_runs_arm64.h`.
Only raw addresses into heap storage owned by `Parser` cross a return; the Go
wrapper keeps those owners alive. There are no Go calls within an active
assembly frame. A very long flat run remains non-preemptible until it returns,
as with the previous native parser.

Counting the relevant `.s` and `.h` source files, including comments/macros,
whole has 933 lines and runs has 739: a 20.8% reduction. The reduction is modest
in lines, but removes container/depth and Unicode policy from hand assembly.
Line counts do not measure maintenance cost by themselves.

## Alternatives tested

`sonic_cparity,sonic_cparity_hybrid` selects a read-only batched lexer with the
entire grammar and tape ownership in Go. It produces up to 32 tokens per call,
stopping at escapes, complex numbers or unknown bytes. Go commits only accepted
tokens; a rejected token advances just its first byte, regardless of lookahead.
This is a clearer split, but the intermediate token writes/reads and repeated
dispatch cost too much for the current implementation.

The first batch implementation executed 157,167 instructions on Twitter.
Separating the infrequent refill from the normally inlined token reader, and
using a byte-class table instead of a long punctuation dispatch chain, reduced
this to 119,816. No inline-budget change, compiler patch or mechanical Go source
expansion was used. The improved batch implementation remains a comparison
backend, rather than the recommended balance.

Moving all ordinary integers back into Go was also tried in the runs backend.
The short paired screening run added about 7.7% to Twitter parse time and 10.2%
to compact parse time. The retained integer helper is small and straightforward;
removing it would introduce a Go/assembly round trip for every numeric value.
That rejected source and binary are retained under the measurement directory.

## Correctness and instructions

The runs and batch candidates match the original 8,892 C-oracle records. The
digest remains
`d8618b2bf9b49d485dfad6956f340edc0eee73e8588c2e5b4053261d7161a004`.
An additional C-derived corpus covers 1,464 lookahead/error/growth records,
digest `ed8388bc61447786e59c10496eb4c947cd0a27c8133b2d4bc0d6930856d8c247`.
The existing 170 numeric-error records and mixed cold-return/growth tests also
pass, including a new string that alternates ASCII and Unicode escapes across
multiple returns. Batch has a separate scalar-reference test for its read-only
lexer contract and random unaligned inputs.

Final runs passes default and optimized/VM decoder tests, C-parity tests under
`GOGC=1`, and C-parity tests with stock Go 1.26.1 and Go 1.20.14. Whole and batch
also pass after extracting the shared wrapper. These are finite tests, not a
proof for every input.

The instruction trace uses the supplied Tango 1.0.1153 / Go 1.26.4, normal
compiler settings, `CGO_ENABLED=0` and `-ldflags=-checklinkname=0`. It follows the
same warmed Twitter binding invocation as the earlier experiments: 13,035
bytes, CopyString enabled, ValidateString disabled. The input SHA256 is
`ddce57c7d74998f87f4901f1d471999ac5fcaf9f00337b5498b993d520ace970`.

| Implementation | Dynamic instructions | Explicit SP/FP loads/stores |
| --- | ---: | ---: |
| Generated C | 78,780 | 101 |
| Previous fused Go parser | 103,626 | 18,031 |
| Whole assembly | 68,541 | 674 |
| Batch lexer + Go grammar | 119,816 | 12,261 |
| Go hierarchy + scalar runs | 78,602 | 2,373 |

Runs enters assembly 135 times on this document, compared with whole's two
entries. It executes 10,061 more instructions than whole, but stays close to C's
instruction count. The final cleanup reproduces the runs count exactly. These
counts do not imply equal cycle counts, and explicit stack accesses are not all
register spills. Local register-based calls inside assembly are separate from
Go ABI crossings.

## Paired timing

Timing is separate from builds and GDB, pinned to CPU 16 with `GOMAXPROCS=1`,
`SONIC_NO_ASYNC_GC=1` and normal GC. The final candidate (with adjacent Unicode
batching) uses six serial, balanced rounds at 300 ms per benchmark. Times below
are medians in microseconds; changes are medians of paired per-round ratios.
Affinity does not reserve the CPU; raw ranges and samples are retained.

| Benchmark | Generated C | Whole | Final runs | Runs vs whole | Runs vs C |
| --- | ---: | ---: | ---: | ---: | ---: |
| Twitter parser | 8.9880 | 8.6785 | 10.1675 | +17.36% | +13.12% |
| Compact parser | 6.2775 | 6.5955 | 8.0385 | +21.97% | +28.18% |
| Twitter prepare + parse | 9.2150 | 8.9000 | 10.4110 | +17.01% | +12.91% |
| Full binding decoder | 25.9210 | 25.3350 | 26.7860 | +5.59% | +3.12% |
| Full generic decoder | 48.8485 | 48.7810 | 50.0130 | +2.66% | +2.32% |

The selected runs boundary trades about 17% of Twitter's parser-only speed for
moving container and Unicode policy into Go. The full decoder costs about 5.6%
in binding mode and 2.7% in generic mode relative to whole in this final run.
Compact JSON has a larger parser-only cost, about 22% against whole and 28%
against C. Stage times are not additive. Parser-only measurements have zero
allocations. This is a maintenance/performance tradeoff, not an unconditional
performance improvement over whole assembly.

The earlier eight-round comparison also measured batch/Go: Twitter parse was
13.383 microseconds and compact parse 10.9425 microseconds. Its full binding and
generic times were 30.044 and 53.624 microseconds. Those are a separate paired
comparison, retained under `timing-stages/` and `timing-decoder/`.

## Workload-dependent costs

The following fixtures measure the boundary itself, not production traffic
weights. The final Unicode change batches adjacent `\u` sequences in Go instead
of returning to assembly after every sequence. Its six-round paired comparison
at 200 ms per benchmark gives:

| Fixture | Whole (microseconds) | Final runs | Runs vs whole |
| --- | ---: | ---: | ---: |
| ContainerArray | 10.4625 | 26.4420 | +152.25% |
| NumericArray | 2.6585 | 2.6705 | +0.19% |
| UnicodeString | 5.1615 | 9.2085 | +78.51% |

Batching adjacent Unicode escapes reduces this fixture's runs time by 25.61% compared with the first runs version. It still trails whole assembly substantially.

The mixed integer array is essentially tied with whole when its ordinary
integer loop stays in assembly. Moving every number to Go costs about 144% on
that same fixture in the earlier eight-round screening. That is why the final
candidate retains the small integer helper.

The container-heavy fixture is about 2.52 times whole's time. Go container
handling means frequent returns and re-entry initialization on this input.
Likewise the mixed ASCII/Unicode fixture remains about 79% slower, even after
batching adjacent Unicode sequences. These are material regressions: the runs
candidate should not replace whole universally or become the default based on
Twitter alone. Keeping the full state machine in Go avoids these particular
per-container crossings, but its token buffer and extra dispatch make it slower
on Twitter; the measured batch result remains in the evidence.

The maintenance benefit is therefore selective: complicated recovery, Unicode
and hierarchy policy is ordinary Go, while a narrow but still substantial
assembly state machine retains scalar sequencing and direct tape writes.
This experiment does not establish that assembly can be reduced to SIMD
intrinsics alone at the same performance.

## Reproduction and retained evidence

Build the final candidate with a normal Go compiler; the measured compiler was
Tango 1.0.1153:

```sh
env GOMODCACHE=/tmp/sonic-mod-cache GOCACHE=/tmp/sonic-tango-cache \
  GOWORK=off CGO_ENABLED=0 GOTOOLCHAIN=local GOFLAGS= \
  /tmp/sonic-arm-framework/tango-compiler/bin/go test -c \
  -tags=sonic_cparity,sonic_cparity_runs -ldflags=-checklinkname=0 \
  -o /tmp/sonic-runs.test ./internal/decoder/optdec
```

Use `sonic_cparity,sonic_cparity_hybrid` for the batch comparison, or
`sonic_cparity,sonic_cparity_whole` for the whole-parser control. Do not combine
backend tags. The default backend remains unchanged.

Evidence is under `output/arm-native-go-cparity-hybrid/`:

- `runs-unicode-batch.test` and `runs-unicode-decoder.test`: final measured binaries.
- `timing-stages-final/`, `timing-decoder-final/`, `timing-unicode-final/`:
  final balanced measurements, raw logs, hashes, commands and environments.
- `timing-stages/`, `timing-decoder/`, `timing-boundaries/`: broader boundary
  comparisons before adjacent Unicode batching; `screen-*` are shorter design
  screening runs and are not the final results.
- `instruction-attribution/`: exact traces, executed disassembly and category
  counts for initial batch, optimized batch, runs and final runs. Final Twitter
  remains exactly 78,602 instructions after Unicode batching.
- `source-unicode-final/`: final experimental source and matched test fixtures.
  Earlier `source-*` folders preserve the rejected/intermediate implementations.
- `runs-oracle.jsonl`, `runs-lookahead.jsonl`, `hybrid-boundary-{c,go}.jsonl`:
  original C comparison records; test logs cover the final builds separately.
- `metadata.json`: source/binary hashes and the complete source snapshot path.

No compiler-level change is included, and the historical control binaries and
measurements were not overwritten. Only comments clarifying adjacent Unicode
handling changed after the final measured builds; executable source is fixed.
