#!/usr/bin/env bash
# Check profile dispatch without executing RISC-V binaries.
set -euo pipefail

usage() {
    cat <<'EOF'
Usage: scripts/test-riscv-build.sh [--go GO_EXECUTABLE]

Checks public APIs, internal implementation selection, and dependency graphs for
RVA20, RVA22, RVA23, and profiles with additional extension options.
--go overrides SONIC_RISCV_GO (default: go on PATH).
Requires Python 3 and a Go toolchain supporting these profiles.
No target binaries are executed.
GOOS=linux, GOARCH=riscv64, CGO_ENABLED=0, and GOTOOLCHAIN=local are enforced.
Inherited GOFLAGS are cleared so tags cannot alter the selection matrix.
EOF
}

go_override=
while (($#)); do
    case "$1" in
        --go)
            if (($# < 2)); then usage >&2; exit 2; fi
            go_override=$2
            shift 2 ;;
        --help|-h) usage; exit 0 ;;
        *) usage >&2; exit 2 ;;
    esac
done

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
go_bin=$(command -v "${go_override:-${SONIC_RISCV_GO:-go}}")
export GOOS=linux GOARCH=riscv64 CGO_ENABLED=0 GOTOOLCHAIN=local GOFLAGS=
cd "$root"
python3 - "$go_bin" <<'PY'
from collections import Counter
import json
import os
from pathlib import Path
import re
import subprocess
import sys

go = sys.argv[1]
module = "github.com/bytedance/sonic"
public = [".", "./ast", "./decoder", "./encoder", "./utf8", "./unquote"]
internal = ["./internal/native/number", "./internal/decoder/optdec",
            "./internal/encoder/alg", "./internal/native", "./internal/native/riscv"]

# Native profiles share Go wrappers and select different assembly.
operation_files = {
    "html_escape_riscv64.go", "nonspace_riscv64.go", "number_scan_riscv64.go",
    "path_masks_riscv64.go", "quote_riscv64.go", "skip_string_riscv64.go",
    "space_riscv64.go", "string_decode_riscv64.go", "string_scan_riscv64.go",
    "structural_riscv64.go", "traversal_masks_riscv64.go", "unquote_riscv64.go",
    "utf8_riscv64.go",
}
assembly_entries = {
    "htmlEscape", "nonSpaceMask", "numberScan", "pathMasks", "quote", "space",
    "stringDecode", "scanString", "scanStringBlock", "skipContainer", "traversalMasks", "utf8Scan",
}

# Check selected implementations independently of their build constraints.
pairs = [
    ("", "sonic.go", "compat.go"),
    ("/ast", "api.go", "api_compat.go"),
    ("/decoder", "decoder_native.go", "decoder_compat.go"),
    ("/encoder", "encoder_native.go", "encoder_compat.go"),
    ("/utf8", "utf8.go", "utf8_fallback.go"),
    ("/unquote", "unquote.go", "unquote_fallback.go"),
    ("/internal/native/number", "scan_riscv64.go", "scan_generic.go"),
    ("/internal/decoder/optdec", "parse_exec_riscv64.go", "parse_exec_native.go"),
    ("/internal/encoder/alg", "maporder_riscv64.go", "maporder_legacy.go"),
    ("/internal/encoder/alg", "spec.go", "spec_compat.go"),
]
cases = [
    ("rva20u64", False, False),
    ("rva22u64", True, False),
    ("rva22u64,zicond,zfa,zawrs,zcb", True, False),
    ("rva23u64", True, True),
    ("rva23u64,zacas,zabha", True, True),
]


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def packages(env, targets, deps=False):
    args = [go, "list", "-json"]
    if deps:
        args.append("-deps")
    else:
        # Native-only packages have no files under the fallback profiles.
        args.append("-e")
    raw = subprocess.check_output(args + targets, env=env, text=True)
    decoder = json.JSONDecoder()
    result = {}
    while raw.strip():
        value, end = decoder.raw_decode(raw.lstrip())
        result[value["ImportPath"]] = value
        raw = raw.lstrip()[end:]
    return result


native_sources = None
native_source_profile = None
native_tests = None

print(subprocess.check_output([go, "version"], text=True).strip())
for profile, native, vector in cases:
    env = dict(os.environ, GORISCV64=profile)
    label = f"GORISCV64={profile}"
    selected = packages(env, public + internal)
    for suffix, native_file, fallback_file in pairs:
        pkg = selected[module + suffix]
        require(not pkg.get("Error"), f"{label}: {pkg.get('Error')}")
        files = pkg.get("GoFiles", [])
        wanted, absent = ((native_file, fallback_file) if native
                          else (fallback_file, native_file))
        require(wanted in files and absent not in files,
                f"{label}: {module + suffix} selected {files}; expected {wanted}")

    if native:
        pkg = selected[module + "/internal/native"]
        files = pkg.get("GoFiles", [])
        require("skip_token_riscv64.go" in files and
                not {"skip_scalar_riscv64.go", "skip_rvv_riscv64.go"}.intersection(files),
                f"{label}: expected shared traversal implementation: {files}")
        require("path_string_riscv64.go" in files and
                not {"path_string_scalar_riscv64.go", "path_string_rvv_riscv64.go"}.intersection(files),
                f"{label}: expected shared path string implementation: {files}")
        tests = {kind: sorted(pkg.get(kind, []))
                 for kind in ("TestGoFiles", "XTestGoFiles")}
        require("skip_token_riscv64_test.go" in tests["TestGoFiles"],
                f"{label}: shared traversal parity tests are excluded")
        if native_tests is None:
            native_tests = tests
        else:
            require(tests == native_tests,
                    f"{label}: native traversal test coverage differs across profiles: {tests}")

    for suffix, available in [("/internal/native", native),
                              ("/internal/native/riscv", native)]:
        pkg = selected[module + suffix]
        if available:
            require(not pkg.get("Error") and pkg.get("GoFiles"),
                    f"{label}: expected usable {module + suffix}: {pkg.get('Error')}")
        else:
            error = pkg.get("Error", {}).get("Err", "")
            require(not pkg.get("GoFiles") and "build constraints exclude all Go files" in error,
                    f"{label}: expected excluded {module + suffix}: {error}")
            require(not pkg.get("SFiles"),
                    f"{label}: excluded {module + suffix} selected assembly")

    dependencies = packages(env, public, deps=True)
    for suffix in ["/internal/native", "/internal/native/riscv", "/internal/decoder/optdec"]:
        require((module + suffix in dependencies) == native,
                f"{label}: unexpected public dependency on {module + suffix}")
    for suffix in ["/internal/native/rvv", "/internal/native/scalar"]:
        require(module + suffix not in dependencies,
                f"{label}: unexpected public dependency on {module + suffix}")
    if native:
        # Require shared Go implementations across native profiles.
        sources = {}
        for name, dependency in dependencies.items():
            if name != module and not name.startswith(module + "/"):
                continue
            files = set(dependency.get("GoFiles", [])) | set(dependency.get("CgoFiles", []))
            sources[name] = sorted(files)
        if native_sources is None:
            native_sources, native_source_profile = sources, profile
        else:
            differences = {
                name: {native_source_profile: native_sources.get(name),
                       profile: sources.get(name)}
                for name in sorted(native_sources.keys() | sources.keys())
                if native_sources.get(name) != sources.get(name)
            }
            require(not differences,
                    f"{label}: production Go implementations differ from "
                    f"{native_source_profile}: {differences}")

        pkg = selected[module + "/internal/native/riscv"]
        files = set(pkg["GoFiles"])
        require(files == operation_files,
                f"{label}: shared operation wrappers differ: "
                f"missing={sorted(operation_files - files)}, extra={sorted(files - operation_files)}")
        assembly = pkg.get("SFiles", [])
        suffix = "_rvv_riscv64.s" if vector else "_scalar_riscv64.s"
        require(assembly and all(name.endswith(suffix) for name in assembly),
                f"{label}: unexpected assembly implementation files {assembly}")
        # Each shared ABI entry must have exactly one implementation.
        entries = Counter()
        for name in assembly:
            source = (Path(pkg["Dir"]) / name).read_text()
            entries.update(re.findall(r"^TEXT\s+·(\w+)\(SB\)", source, re.MULTILINE))
        require(all(entries[name] == 1 for name in assembly_entries),
                f"{label}: expected exactly one implementation of each shared "
                f"assembly entry, found {dict(entries)}")
    print(f"PASS {label}: {'RVV' if vector else 'scalar' if native else 'fallback'}")

print(f"PASS {len(cases)} build-selection configurations")
PY
