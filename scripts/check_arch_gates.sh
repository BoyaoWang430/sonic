#!/usr/bin/env bash
# Static architectural, dependency, and build-tag matrix gate (Task G4).
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

echo "[1/5] Checking zero //go:linkname directives..."
if grep -rn '//go:linkname' --include='*.go' --exclude-dir=skills --exclude-dir=.git .; then
    echo "FAIL: forbidden //go:linkname directive found" >&2
    exit 1
fi

echo "[2/5] Checking zero JIT / executable mmap / private runtime header mirrors..."
if grep -rnE 'PROT_EXEC|bytedance/sonic/loader|\btype[[:space:]]+rawSliceHeader\b' --include='*.go' --exclude-dir=skills --exclude-dir=.git .; then
    echo "FAIL: forbidden JIT, executable mmap, or rawSliceHeader mirror found" >&2
    exit 1
fi

echo "[3/5] Checking package dependency boundaries..."
resolver_deps="$(go list -f '{{join .Deps "\n"}}' ./internal/resolver)"
if echo "${resolver_deps}" | grep -qE '^github\.com/bytedance/sonic/internal/(encoder|decoder)'; then
    echo "FAIL: internal/resolver must not depend on internal/encoder or internal/decoder" >&2
    echo "${resolver_deps}" | grep -E '^github\.com/bytedance/sonic/' >&2
    exit 1
fi

kernels_imports="$(go list -f '{{join .Imports "\n"}}' ./internal/native/kernels)"
if echo "${kernels_imports}" | grep -qE '^reflect$'; then
    echo "FAIL: internal/native/kernels must not import reflect" >&2
    echo "${kernels_imports}" >&2
    exit 1
fi

kernels_deps="$(go list -f '{{join .Deps "\n"}}' ./internal/native/kernels)"
if echo "${kernels_deps}" | grep -qE '^github\.com/bytedance/sonic/(ast|decoder|encoder|internal/(decoder|encoder|resolver|optcaching|caching))'; then
    echo "FAIL: internal/native/kernels must not depend on parser, decoder, encoder, AST, or resolver packages" >&2
    echo "${kernels_deps}" | grep -E '^github\.com/bytedance/sonic/' >&2
    exit 1
fi

echo "[4/5] Checking pure-Go scalar build-tag matrix (-tags=sonic_scalar -> sonic.go, zero .s files)..."
for target in "linux/amd64/" "linux/arm64/" "linux/riscv64/rva20u64" "linux/riscv64/rva22u64"; do
    IFS=/ read -r goos goarch goriscv <<<"${target}"
    if [ "${goarch}" = "riscv64" ] && [ "${goriscv}" = "rva22u64" ]; then
        if ! GORISCV64=rva22u64 go env GORISCV64 >/dev/null 2>&1; then
            continue
        fi
    fi
    root_files="$(GOOS="${goos}" GOARCH="${goarch}" GORISCV64="${goriscv}" CGO_ENABLED=0 go list -tags=sonic_scalar -f '{{.GoFiles}}' .)"
    if [[ "${root_files}" != *"sonic.go"* ]] || [[ "${root_files}" == *"compat.go"* ]]; then
        echo "FAIL: -tags=sonic_scalar on ${target} selected ${root_files} instead of sonic.go" >&2
        exit 1
    fi
    asm_files="$(GOOS="${goos}" GOARCH="${goarch}" GORISCV64="${goriscv}" CGO_ENABLED=0 go list -tags=sonic_scalar -f '{{if .SFiles}}{{.ImportPath}}:{{.SFiles}}{{end}}' ./...)"
    if [ -n "${asm_files}" ]; then
        echo "FAIL: -tags=sonic_scalar on ${target} compiled unexpected .s assembly files:" >&2
        echo "${asm_files}" >&2
        exit 1
    fi
done

echo "[5/5] Checking explicit stdlib adapter matrix (-tags=sonic_compat and uncertified 32-bit/big-endian/rva20u64)..."
compat_root="$(CGO_ENABLED=0 go list -tags=sonic_compat -f '{{.GoFiles}}' .)"
if [[ "${compat_root}" != *"compat.go"* ]] || [[ "${compat_root}" == *"sonic.go"* ]]; then
    echo "FAIL: -tags=sonic_compat selected ${compat_root} instead of compat.go" >&2
    exit 1
fi

for fallback in "linux/386/" "linux/arm/" "linux/ppc64le/" "linux/s390x/" "js/wasm/" "linux/riscv64/rva20u64"; do
    IFS=/ read -r goos goarch goriscv <<<"${fallback}"
    fb_root="$(GOOS="${goos}" GOARCH="${goarch}" GORISCV64="${goriscv}" CGO_ENABLED=0 go list -f '{{.GoFiles}}' .)"
    if [[ "${fb_root}" != *"compat.go"* ]] || [[ "${fb_root}" == *"sonic.go"* ]]; then
        echo "FAIL: fallback target ${fallback} selected ${fb_root} instead of compat.go" >&2
        exit 1
    fi
done

echo "PASS: all architectural, dependency, and backend build-tag gates verified."
