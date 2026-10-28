#!/usr/bin/env bash
# Compare profile-selected RISC-V backends using the same public fixtures.
set -euo pipefail

usage() {
    cat <<'EOF'
Usage: scripts/bench-riscv.sh --output DIR [--go GO_EXECUTABLE]

DIR must not already exist. --go overrides SONIC_RISCV_GO (default: go on PATH).
Use a Go toolchain supporting the selected profiles, e.g.:
  scripts/bench-riscv.sh --output /tmp/sonic-bench --go /path/to/go/bin/go

Builds eight test binaries: fallback and native decoder/encoder/AST,
plus candidate native kernels and optdec stages. All fixture smoke checks complete
before serial measurements start. Public defaults: 200ms x 5; internal: 100ms x 3.
BENCHTIME and BENCHCOUNT override both groups (e.g. BENCHTIME=1x BENCHCOUNT=1).
SONIC_RISCV_CPU optionally selects one CPU with taskset; no CPU is pinned by default.

GORISCV64 selects the candidate profile (default: rva23u64).
SONIC_RISCV_BASELINE_PROFILE selects fallback (default: rva20u64).
RVA22 selects scalar native code; RVA23 selects RVV. Optional profile suffixes are preserved.
For scalar measurements on a V-capable host, set GODEBUG=cpu.v=off.
Each build records its profile. Results include compiler target differences as
well as backend differences. Uses CGO_ENABLED=0, GOMAXPROCS=1,
SONIC_NO_ASYNC_GC=1, and no optimization toggles. Inherited GOFLAGS are cleared.
Keep other compilation/tests idle during measurement. Raw results are retained.
EOF
}

out= go_override=
while (($#)); do
    case "$1" in
        --output|--go)
            if (($# < 2)); then usage >&2; exit 2; fi
            if [[ "$1" == --output ]]; then out=$2; else go_override=$2; fi
            shift 2 ;;
        --help|-h) usage; exit 0 ;;
        *) usage >&2; exit 2 ;;
    esac
done
if [[ -z "$out" ]]; then usage >&2; exit 2; fi
if [[ -e "$out" || -L "$out" ]]; then
    echo "Refusing to overwrite an existing output path: $out" >&2
    exit 2
fi
if [[ -n "${BENCHCOUNT:-}" && ! "$BENCHCOUNT" =~ ^[1-9][0-9]*$ ]]; then
    echo "BENCHCOUNT must be a positive integer" >&2
    exit 2
fi
cpu=${SONIC_RISCV_CPU:-}
if [[ -n "$cpu" && ! "$cpu" =~ ^[0-9]+$ ]]; then
    echo "SONIC_RISCV_CPU must select one nonnegative CPU number" >&2
    exit 2
fi

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
go_bin=$(command -v "${go_override:-${SONIC_RISCV_GO:-go}}")
go_bin=$(cd "$(dirname "$go_bin")" && pwd)/$(basename "$go_bin")
inherited_goflags=${GOFLAGS:-}
candidate_profile=${GORISCV64:-rva23u64}
baseline_profile=${SONIC_RISCV_BASELINE_PROFILE:-rva20u64}
export GORISCV64="$candidate_profile" CGO_ENABLED=0 GOTOOLCHAIN=local GOFLAGS=
export GOMAXPROCS=1 SONIC_NO_ASYNC_GC=1
unset SONIC_USE_OPTDEC SONIC_USE_FASTMAP SONIC_ENCODER_USE_VM SONIC_SYNC_GC
pin=()
if [[ -n "$cpu" ]]; then
    command -v taskset >/dev/null
    taskset -c "$cpu" true
    pin=(taskset -c "$cpu")
fi
source_sha=$(git -C "$root" rev-parse HEAD)
source_status=$(git -C "$root" status --porcelain)
mkdir -p "$(dirname "$out")"
mkdir "$out"
out=$(cd "$out" && pwd)
cd "$root"
trap 'status=$?; printf "%s\n" "$status" > "$out/exit-code.txt"' EXIT

run_logged() {
    local label=$1 status
    shift
    { printf '%s: ' "$label"; printf '%q ' "$@"; printf '\n'; } | tee -a "$out/commands.log"
    if "$@" 2>&1 | tee "$out/$label.log"; then status=0; else status=$?; fi
    printf '%s\n' "$status" > "$out/$label.exit-code.txt"
    return "$status"
}

# Pair public baseline/candidate measurements, then measure candidate internals.
runs=(baseline-decoder candidate-decoder baseline-encoder candidate-encoder
      baseline-ast candidate-ast candidate-kernel candidate-optdec)
configure_run() {
    local variant=${1%%-*} group=${1#*-}
    profile=$candidate_profile
    if [[ "$variant" == baseline ]]; then profile=$baseline_profile; fi
    bench_time=${BENCHTIME:-200ms}
    bench_count=${BENCHCOUNT:-5}
    case "$group" in
        decoder)
            pkg=./decoder
            bench='^BenchmarkDecoder_(Generic|Binding)_Sonic$'
            smoke='^TestDecoder_(Generic|Binding)$'
            expected_benchmarks=2 expected_tests=2 ;;
        encoder)
            pkg=./encoder
            bench='^BenchmarkEncoder_(Generic|Binding)_Sonic$'
            smoke='^TestEncoder_(Generic|Binding)$'
            expected_benchmarks=2 expected_tests=2 ;;
        ast)
            pkg=./ast
            bench='^Benchmark(GetOne|GetFull|ParseSeven|SetOne)_Sonic$'
            smoke='^TestSearcher_GetByPath$'
            expected_benchmarks=4 expected_tests=1 ;;
        kernel|optdec)
            bench='^BenchmarkRVV'
            bench_time=${BENCHTIME:-100ms}
            bench_count=${BENCHCOUNT:-3}
            if [[ "$group" == kernel ]]; then
                pkg=./internal/native/riscv smoke='^$' bench='^BenchmarkNative'
                expected_benchmarks=15 expected_tests=0
                if [[ "${profile%%,*}" == rva22u64 ]]; then expected_benchmarks=17; fi
            else
                pkg=./internal/decoder/optdec smoke='^$'
                expected_benchmarks=2 expected_tests=0
            fi ;;
    esac
}

"$go_bin" version > "$out/go-version.txt"
"$go_bin" env -json GOVERSION GOOS GOARCH GORISCV64 CGO_ENABLED GOEXPERIMENT GOFLAGS GOTOOLCHAIN GOROOT GOPATH GOMODCACHE GOCACHE GOMOD GOWORK > "$out/go-env.json"
printf '%s\n' "$source_status" > "$out/source-status.txt"
uname -a > "$out/system.txt"
if [[ -r /proc/cpuinfo ]]; then cp /proc/cpuinfo "$out/cpuinfo.txt"; fi
if command -v lscpu >/dev/null; then lscpu > "$out/lscpu.txt"; fi
if command -v taskset >/dev/null; then taskset -pc $$ > "$out/inherited-affinity.txt"; fi
for run in "${runs[@]}"; do
    configure_run "$run"
    printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$run" "$pkg" "$profile" "$smoke" "$bench" "$bench_time" "$bench_count" >> "$out/plan.tsv"
done
python3 - "$out" "$root" "$source_sha" "$go_bin" "$inherited_goflags" "$cpu" <<'PY'
import csv, datetime, json, os, pathlib, sys
out, root, sha, go, goflags, cpu = sys.argv[1:]
out = pathlib.Path(out)
metadata = {
    "source_sha": sha, "source_worktree": root,
    "dirty": bool((out / "source-status.txt").read_text().strip()),
    "go_executable": go, "go_version": (out / "go-version.txt").read_text().strip(),
    "go_env": json.loads((out / "go-env.json").read_text()),
    "inherited_goflags_cleared": goflags, "baseline": "Go fallback selected by a lower target profile",
    "comparison_scope": "Includes compiler target differences as well as backend differences",
    "build_args": ["test", "-c", "-ldflags=-checklinkname=0"],
    "run_env": {k: os.environ.get(k, "<unset>") for k in (
        "GOMAXPROCS", "GOGC", "GOMEMLIMIT", "GODEBUG", "SONIC_NO_ASYNC_GC", "SONIC_SYNC_GC",
        "SONIC_USE_OPTDEC", "SONIC_USE_FASTMAP", "SONIC_ENCODER_USE_VM")},
    "cpu_pin": int(cpu) if cpu else None,
    "smoke_benchtime": "1x", "smoke_count": 1, "measurements_complete": False,
    "started_utc": datetime.datetime.now(datetime.timezone.utc).isoformat(),
    "plan": list(csv.DictReader((out / "plan.tsv").open(), delimiter="\t", fieldnames=(
        "name", "package", "GORISCV64", "smoke_regex", "bench_regex", "benchtime", "count"))),
}
(out / "metadata.json").write_text(json.dumps(metadata, indent=2) + "\n")
PY

for variant in baseline candidate; do
    profile=$candidate_profile
    expected=sonic.go
    if [[ "$variant" == baseline ]]; then profile=$baseline_profile; expected=compat.go; fi
    GORISCV64="$profile" "$go_bin" list -f '{{join .GoFiles "\n"}}' . > "$out/$variant.selected-files.txt"
    if ! grep -Fxq "$expected" "$out/$variant.selected-files.txt"; then
        echo "$variant: GORISCV64=$profile does not select $expected" >&2
        exit 1
    fi
done

for run in "${runs[@]}"; do
    configure_run "$run"
    run_logged "$run.build" env GORISCV64="$profile" "$go_bin" test -c -ldflags=-checklinkname=0 -o "$out/$run.test" "$pkg"
    "$go_bin" version -m "$out/$run.test" > "$out/$run.build-info.txt"
done
for run in "${runs[@]}"; do
    configure_run "$run"
    run_logged "$run.list" "${pin[@]}" "$out/$run.test" "-test.list=($smoke)|($bench)"
    listed_tests=$(awk '/^Test/ {n++} END {print n+0}' "$out/$run.list.log")
    listed_benchmarks=$(awk '/^Benchmark/ {n++} END {print n+0}' "$out/$run.list.log")
    if [[ "$listed_tests" != "$expected_tests" || "$listed_benchmarks" != "$expected_benchmarks" ]]; then
        echo "$run: unexpected fixture/benchmark matches ($listed_tests/$listed_benchmarks)" >&2
        exit 1
    fi
    run_logged "$run.smoke" "${pin[@]}" "$out/$run.test" "-test.run=$smoke" "-test.bench=$bench" -test.benchtime=1x -test.count=1 -test.benchmem -test.v
done
for run in "${runs[@]}"; do
    configure_run "$run"
    run_logged "$run.bench" "${pin[@]}" "$out/$run.test" '-test.run=^$' "-test.bench=$bench" "-test.benchtime=$bench_time" "-test.count=$bench_count" -test.benchmem
done
python3 - "$out" <<'PY'
import datetime, json, pathlib, sys
out = pathlib.Path(sys.argv[1])
metadata = json.loads((out / "metadata.json").read_text())
metadata.update(measurements_complete=True,
                finished_utc=datetime.datetime.now(datetime.timezone.utc).isoformat())
(out / "metadata.json").write_text(json.dumps(metadata, indent=2) + "\n")
PY
printf 'Completed eight benchmark groups. Results: %s\n' "$out"
