#!/usr/bin/env bash
# Record and compare public API behavior across RISC-V backends.
set -euo pipefail

usage() {
    cat <<'EOF'
Usage: scripts/test-riscv.sh --mode baseline|candidate --output DIR [--suite default|optimized] [--corpus FILE]
       scripts/test-riscv.sh --mode compare --output DIR --baseline REPORT --candidate REPORT [--allow-same-backend]

SONIC_RISCV_GO selects the Go executable (default: go on PATH).
GORISCV64 selects the candidate profile (default: rva23u64).
SONIC_RISCV_BASELINE_PROFILE selects fallback (default: rva20u64).
RVA22 selects scalar native code; RVA23 selects RVV. Optional profile suffixes are preserved.
For scalar qualification on a V-capable host, set GODEBUG=cpu.v=off.
CGO_ENABLED=0 and GOTOOLCHAIN=local are enforced.
Candidate mode requires native dispatch; baseline mode requires fallback.
The comparison includes compiler target differences as well as backend differences.
Output directories must not already contain metadata.json or results.json.
EOF
}

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
mode= suite=default out= baseline= candidate= allow_same=false
corpus="$root/internal/testutil/riscv/testdata/common.json"
while (($#)); do
    case "$1" in
        --mode|--suite|--output|--corpus|--baseline|--candidate)
            if (($# < 2)); then usage >&2; exit 2; fi
            case "$1" in
                --mode) mode=$2 ;;
                --suite) suite=$2 ;;
                --output) out=$2 ;;
                --corpus) corpus=$2 ;;
                --baseline) baseline=$2 ;;
                --candidate) candidate=$2 ;;
            esac
            shift 2 ;;
        --allow-same-backend) allow_same=true; shift ;;
        --help|-h) usage; exit 0 ;;
        *) usage >&2; exit 2 ;;
    esac
done
if [[ -z "$out" || ! "$mode" =~ ^(baseline|candidate|compare)$ || ! "$suite" =~ ^(default|optimized)$ ]]; then
    usage >&2
    exit 2
fi

go_bin=$(command -v "${SONIC_RISCV_GO:-go}")
export PATH="$(dirname "$go_bin"):$PATH"
export GORISCV64="${GORISCV64:-rva23u64}" CGO_ENABLED=0 GOTOOLCHAIN=local
if [[ "$mode" == baseline ]]; then
    export GORISCV64="${SONIC_RISCV_BASELINE_PROFILE:-rva20u64}"
fi
if [[ "$suite" == optimized ]]; then
    export SONIC_USE_OPTDEC=1 SONIC_USE_FASTMAP=1 SONIC_ENCODER_USE_VM=1
else
    unset SONIC_USE_OPTDEC SONIC_USE_FASTMAP SONIC_ENCODER_USE_VM
fi
mkdir -p "$out"
out=$(cd "$out" && pwd)
if [[ -e "$out/metadata.json" || -e "$out/results.json" ]]; then
    echo "Refusing to overwrite an existing run: $out" >&2
    exit 2
fi
cd "$root"
printf 'mode=%s suite=%s profile=%s CGO_ENABLED=%s go=%s output=%s\n' "$mode" "$suite" "$GORISCV64" "$CGO_ENABLED" "$go_bin" "$out"
printf 'SONIC_USE_OPTDEC=%s SONIC_USE_FASTMAP=%s SONIC_ENCODER_USE_VM=%s\n' "${SONIC_USE_OPTDEC:-unset}" "${SONIC_USE_FASTMAP:-unset}" "${SONIC_ENCODER_USE_VM:-unset}"

# Isolate go_flags.sh tracing in a subshell.
linkname_flag=$(
    exec 2>"$out/go-flags.log"
    _GO_FLAGS_LOADED=
    source "$root/scripts/go_flags.sh" >/dev/null
    get_go_linkname_flag
)
flags=()
if [[ -n "$linkname_flag" ]]; then flags+=("$linkname_flag"); fi

if [[ "$mode" == compare ]]; then
    if [[ -z "$baseline" || -z "$candidate" ]]; then usage >&2; exit 2; fi
    "$go_bin" build "${flags[@]}" -o "$out/compare" ./internal/testutil/riscv/cmd/compare 2>&1 | tee "$out/build.log"
    args=(-baseline "$baseline" -candidate "$candidate")
    if [[ "$allow_same" == true ]]; then args+=(-allow-same-backend); fi
    "$out/compare" "${args[@]}" 2>&1 | tee "$out/compare.log"
    exit 0
fi

"$go_bin" env -json GOVERSION GOOS GOARCH GORISCV64 CGO_ENABLED GOEXPERIMENT GOFLAGS GOTOOLCHAIN GOROOT GOPATH GOMODCACHE GOCACHE GOMOD GOWORK > "$out/go-env.json"
"$go_bin" list -f '{{join .GoFiles "\n"}}' . > "$out/selected-files.txt"
python3 - "$root" "$out" "$go_bin" "$mode" "$suite" "$linkname_flag" <<'PY'
import json
import os
from pathlib import Path
import subprocess
import sys

root, out, go, mode, suite, linkname = sys.argv[1:]
out = Path(out)
go_env = json.loads((out / "go-env.json").read_text())
metadata = {
    "revision": subprocess.check_output(["git", "rev-parse", "HEAD"], text=True).strip(),
    "dirty": bool(subprocess.check_output(["git", "status", "--porcelain"], text=True).strip()),
    "working_dir": root,
    "go_executable": go,
    "go_version": go_env["GOVERSION"],
    "go_env": go_env,
    "mode": mode,
    "suite": suite,
    "build_tags": "",
    "build_flags": [linkname] if linkname else [],
    "toggles": {k: os.environ.get(k, "<unset>") for k in
                ("SONIC_USE_OPTDEC", "SONIC_USE_FASTMAP", "SONIC_ENCODER_USE_VM", "GODEBUG")},
    "selected_root_files": (out / "selected-files.txt").read_text().splitlines(),
}
(out / "metadata.json").write_text(json.dumps(metadata, indent=2) + "\n")
PY
"$go_bin" build "${flags[@]}" -o "$out/probe" ./internal/testutil/riscv/cmd/probe 2>&1 | tee "$out/build.log"
trap 'rm -f -- "$out/results.json.tmp"' EXIT
if "$out/probe" -metadata "$out/metadata.json" -corpus "$corpus" > "$out/results.json.tmp" 2> "$out/probe.log"; then
    mv "$out/results.json.tmp" "$out/results.json"
    cat "$out/probe.log"
    printf 'Results: %s/results.json\n' "$out"
else
    status=$?
    cat "$out/probe.log" >&2
    exit "$status"
fi
