#!/usr/bin/env bash
# Static architectural, dependency, and build-tag matrix gate.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

echo "[1/6] Checking zero //go:linkname directives..."
if grep -rn '//go:linkname' --include='*.go' --exclude-dir=skills --exclude-dir=.git .; then
    echo "FAIL: forbidden //go:linkname directive found" >&2
    exit 1
fi

echo "[2/6] Checking zero JIT / executable mmap / private runtime header mirrors..."
if grep -rnE 'PROT_EXEC|bytedance/sonic/loader|\btype[[:space:]]+rawSliceHeader\b' --include='*.go' --exclude-dir=skills --exclude-dir=.git .; then
    echo "FAIL: forbidden JIT, executable mmap, or rawSliceHeader mirror found" >&2
    exit 1
fi

echo "[3/6] Checking package dependency boundaries..."
optdec_asm="$(find ./internal/decoder/optdec -maxdepth 1 -name '*.s' -o -name '*.h')"
if [ -n "${optdec_asm}" ]; then
    echo "FAIL: internal/decoder/optdec must not contain standalone .s/.h parser kernels (G4.4c):" >&2
    echo "${optdec_asm}" >&2
    exit 1
fi
if grep -rnE 'cParityParse(Runs|Number)' --include='*.go' ./internal/decoder/optdec; then
    echo "FAIL: legacy cParityParseRuns / cParityParseNumber symbols still present in optdec (G4.4c)" >&2
    exit 1
fi
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
if echo "${kernels_deps}" | grep -qE '^github\.com/bytedance/sonic/(ast|decoder|encoder|internal/(decoder|encoder|resolver|optcaching|caching|jsonscan))'; then
    echo "FAIL: internal/native/kernels must not depend on parser, decoder, encoder, AST, or resolver packages" >&2
    echo "${kernels_deps}" | grep -E '^github\.com/bytedance/sonic/' >&2
    exit 1
fi

jsonscan_imports="$(go list -f '{{join .Imports "\n"}}' ./internal/jsonscan)"
if echo "${jsonscan_imports}" | grep -qE '^reflect$'; then
    echo "FAIL: internal/jsonscan must not import reflect" >&2
    echo "${jsonscan_imports}" >&2
    exit 1
fi

jsonscan_deps="$(go list -f '{{join .Deps "\n"}}' ./internal/jsonscan)"
if echo "${jsonscan_deps}" | grep -qE '^github\.com/bytedance/sonic/(ast|decoder|encoder|internal/(decoder|encoder|resolver|optcaching|caching))'; then
    echo "FAIL: internal/jsonscan must remain neutral and must not depend on optdec, encoder/vm, AST, or resolver" >&2
    echo "${jsonscan_deps}" | grep -E '^github\.com/bytedance/sonic/' >&2
    exit 1
fi

echo "[4/6] Checking pure-Go scalar build-tag matrix (-tags=sonic_scalar -> sonic.go, zero .s files)..."
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

echo "[5/6] Checking explicit stdlib adapter matrix (-tags=sonic_compat and uncertified 32-bit/big-endian/rva20u64)..."
for target in "linux/amd64/" "linux/arm64/" "linux/riscv64/rva20u64" "linux/riscv64/rva22u64"; do
    IFS=/ read -r goos goarch goriscv <<<"${target}"
    if [ "${goarch}" = "riscv64" ] && [ "${goriscv}" = "rva22u64" ]; then
        if ! GORISCV64=rva22u64 go env GORISCV64 >/dev/null 2>&1; then
            continue
        fi
    fi
    compat_root="$(GOOS="${goos}" GOARCH="${goarch}" GORISCV64="${goriscv}" CGO_ENABLED=0 go list -tags=sonic_compat -f '{{.GoFiles}}' .)"
    if [[ "${compat_root}" != *"compat.go"* ]] || [[ "${compat_root}" == *"sonic.go"* ]]; then
        echo "FAIL: -tags=sonic_compat on ${target} selected ${compat_root} instead of compat.go" >&2
        exit 1
    fi
    compat_asm="$(GOOS="${goos}" GOARCH="${goarch}" GORISCV64="${goriscv}" CGO_ENABLED=0 go list -tags=sonic_compat -f '{{if .SFiles}}{{.ImportPath}}:{{.SFiles}}{{end}}' ./...)"
    if [ -n "${compat_asm}" ]; then
        echo "FAIL: -tags=sonic_compat on ${target} compiled unexpected .s assembly files:" >&2
        echo "${compat_asm}" >&2
        exit 1
    fi
done

for fallback in "linux/386/" "linux/arm/" "linux/ppc64le/" "linux/s390x/" "js/wasm/" "linux/riscv64/rva20u64"; do
    IFS=/ read -r goos goarch goriscv <<<"${fallback}"
    fb_root="$(GOOS="${goos}" GOARCH="${goarch}" GORISCV64="${goriscv}" CGO_ENABLED=0 go list -f '{{.GoFiles}}' .)"
    if [[ "${fb_root}" != *"compat.go"* ]] || [[ "${fb_root}" == *"sonic.go"* ]]; then
        echo "FAIL: fallback target ${fallback} selected ${fb_root} instead of compat.go" >&2
        exit 1
    fi
done

echo "[6/6] Checking compile-time witness/diag build-tag isolation matrix..."
for target in "linux/amd64" "linux/arm64"; do
    IFS=/ read -r goos goarch <<<"${target}"
    def_k="$(GOOS="${goos}" GOARCH="${goarch}" CGO_ENABLED=0 go list -f '{{.GoFiles}}' ./internal/native/kernels)"
    if [[ "${def_k}" != *"lex_witness_off.go"* ]] || [[ "${def_k}" == *"lex_witness_on.go"* ]]; then
        echo "FAIL: default build on ${target} selected wrong kernels witness file: ${def_k}" >&2
        exit 1
    fi
    def_j="$(GOOS="${goos}" GOARCH="${goarch}" CGO_ENABLED=0 go list -f '{{.GoFiles}}' ./internal/jsonscan)"
    if [[ "${def_j}" != *"witness_off.go"* ]] || [[ "${def_j}" == *"witness_on.go"* ]] || [[ "${def_j}" == *"kernels_native_witness.go"* ]]; then
        echo "FAIL: default build on ${target} selected wrong jsonscan witness files: ${def_j}" >&2
        exit 1
    fi
    for wtag in "sonic_witness" "sonic_diag" "sonic_witness,sonic_diag"; do
        wit_k="$(GOOS="${goos}" GOARCH="${goarch}" CGO_ENABLED=0 go list -tags="${wtag}" -f '{{.GoFiles}}' ./internal/native/kernels)"
        if [[ "${wit_k}" != *"lex_witness_on.go"* ]] || [[ "${wit_k}" == *"lex_witness_off.go"* ]]; then
            echo "FAIL: -tags=${wtag} on ${target} selected wrong kernels witness file: ${wit_k}" >&2
            exit 1
        fi
        wit_j="$(GOOS="${goos}" GOARCH="${goarch}" CGO_ENABLED=0 go list -tags="${wtag}" -f '{{.GoFiles}}' ./internal/jsonscan)"
        if [[ "${wit_j}" != *"witness_on.go"* ]] || [[ "${wit_j}" != *"kernels_native_witness.go"* ]] || [[ "${wit_j}" == *"witness_off.go"* ]]; then
            echo "FAIL: -tags=${wtag} on ${target} selected wrong jsonscan witness files: ${wit_j}" >&2
            exit 1
        fi
    done
done

echo "PASS: all architectural, dependency, witness-isolation, and backend build-tag gates verified."
