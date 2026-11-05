#!/usr/bin/env bash
# Validate the ARM64 Go parser and ABI0 kernel backend.
set -euo pipefail

usage() {
    cat <<'EOF'
Usage: scripts/test-arm-native.sh [--go GO_EXECUTABLE] [--build-only]

Run ARM-relevant main-module and regression suites with default and optimized settings.
Requires an ARM64 host unless --build-only is used. In build-only mode test
binaries are linked but not executed; this also supports older Go toolchains.
The selection checks require the Runs parser and shared kernels without tags,
and verify that public APIs do not import native/neon.
SONIC_ARM_GO supplies the default Go executable. GOFLAGS are cleared.
EOF
}

go_override= build_only=0
while (($#)); do
    case "$1" in
        --go) if (($# < 2)); then usage >&2; exit 2; fi; go_override=$2; shift 2 ;;
        --build-only) build_only=1; shift ;;
        --help|-h) usage; exit 0 ;;
        *) usage >&2; exit 2 ;;
    esac
done
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
go_bin=$(command -v "${go_override:-${SONIC_ARM_GO:-go}}")
go_bin=$(cd "$(dirname "$go_bin")" && pwd)/$(basename "$go_bin")
if (( !build_only )) && [[ $("$go_bin" env GOHOSTARCH) != arm64 ]]; then
    echo 'Execution requires ARM64; use --build-only to cross-compile.' >&2
    exit 2
fi
export GOARCH=arm64 GOFLAGS= GOTOOLCHAIN=local GOMAXPROCS=${GOMAXPROCS:-4}
if (( build_only )); then export CGO_ENABLED=0; fi
unset SONIC_USE_OPTDEC SONIC_USE_FASTMAP SONIC_ENCODER_USE_VM
cd "$root"
"$go_bin" version
version=$("$go_bin" env GOVERSION)
link_flags=()
if [[ $version =~ go1\.([0-9]+) ]] && (( BASH_REMATCH[1] >= 23 )); then
    link_flags=(-ldflags=-checklinkname=0)
fi
exec_flags=()
if (( build_only )); then exec_flags=(-exec=true); fi
deps=$("$go_bin" list -deps . ./ast ./encoder ./decoder ./utf8 ./unquote)
if [[ $deps == *github.com/bytedance/sonic/internal/native/neon* ]]; then
    echo 'Public ARM64 APIs still depend on internal/native/neon.' >&2
    exit 1
fi
if [[ $deps != *github.com/bytedance/sonic/internal/native/kernels* ]]; then
    echo 'Public ARM64 APIs did not select the shared native kernels.' >&2
    exit 1
fi
echo 'Dependency check: shared kernels selected; generated NEON package absent.'
parser_sources=$'\n'"$("$go_bin" list -f '{{join .GoFiles "\n"}}{{"\n"}}{{join .SFiles "\n"}}' ./internal/decoder/optdec)"$'\n'
for source in parse_cparity_framework.go parse_cparity_runs.go parse_cparity_runs_arm64.s; do
    if [[ $parser_sources != *$'\n'"$source"$'\n'* ]]; then
        printf 'ARM64 did not select the default Runs source: %s\n' "$source" >&2
        exit 1
    fi
done
for source in parse_exec_native.go; do
    if [[ $parser_sources == *$'\n'"$source"$'\n'* ]]; then
        printf 'ARM64 selected an alternate parser source: %s\n' "$source" >&2
        exit 1
    fi
done
echo 'Parser selection check: ARM64 Runs selected without custom build tags.'
package_lines=$("$go_bin" list ./...)
packages=()
while IFS= read -r pkg; do
    case "$pkg" in
        github.com/bytedance/sonic/internal/jit|github.com/bytedance/sonic/internal/jit/*|\
        github.com/bytedance/sonic/internal/decoder/jitdec|github.com/bytedance/sonic/internal/decoder/jitdec/*|\
        github.com/bytedance/sonic/internal/encoder/x86|github.com/bytedance/sonic/internal/encoder/x86/*|\
        github.com/bytedance/sonic/internal/native/avx2|github.com/bytedance/sonic/internal/native/avx2/*|\
        github.com/bytedance/sonic/internal/native/sse|github.com/bytedance/sonic/internal/native/sse/*)
            printf 'Exclude x86-only implementation package: %s\n' "$pkg" ;;
        *) packages+=("$pkg") ;;
    esac
done <<< "$package_lines"
"$go_bin" test "${link_flags[@]}" "${exec_flags[@]}" "${packages[@]}"
SONIC_USE_OPTDEC=1 SONIC_USE_FASTMAP=1 SONIC_ENCODER_USE_VM=1 \
    "$go_bin" test "${link_flags[@]}" "${exec_flags[@]}" "${packages[@]}"
# Older toolchains may not support newer workspace modules. In build-only mode,
# callers can use GOWORK=off to validate the main module's minimum Go version.
if [[ ${GOWORK:-} != off ]]; then
    "$go_bin" test "${link_flags[@]}" "${exec_flags[@]}" ./issue_test ./generic_test
    SONIC_USE_OPTDEC=1 SONIC_USE_FASTMAP=1 SONIC_ENCODER_USE_VM=1 \
        "$go_bin" test "${link_flags[@]}" "${exec_flags[@]}" ./issue_test ./generic_test
fi
