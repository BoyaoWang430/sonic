#!/usr/bin/env python3
"""Compare identical fixtures at two native revisions and a lower-profile fallback.

GORISCV64 selects the old/new target (default: rva23u64).
SONIC_RISCV_BASELINE_PROFILE selects fallback (default: rva20u64).
RVA22 selects scalar native code; RVA23 selects RVV. Profile suffixes are preserved.
For scalar measurements on a V-capable host, set GODEBUG=cpu.v=off.
Fallback comparisons include compiler target differences as well as backend differences.
"""
import argparse
import csv
import datetime
import json
import os
from pathlib import Path
import re
import statistics
import subprocess

p = argparse.ArgumentParser(description=__doc__)
p.add_argument("--baseline", type=Path, required=True, help="old checkout with profile-selected native code and the same coverage benchmark")
p.add_argument("--output", type=Path, required=True, help="new output directory")
p.add_argument("--go", default=os.environ.get("SONIC_RISCV_GO") or "go",
               help="Go executable (default: SONIC_RISCV_GO or go on PATH)")
p.add_argument("--cpu", type=int, help="pin measurements to this CPU (default: no affinity change)")
p.add_argument("--count", type=int, default=5)
p.add_argument("--benchtime", default="200ms")
p.add_argument("--public-only", action="store_true", help="time only public workloads; kernel fixtures still get smoke checked")
a = p.parse_args()
if a.count < 1 or a.cpu is not None and a.cpu < 0:
    p.error("count must be positive and cpu nonnegative")
pin = []
if a.cpu is not None:
    pin = ["taskset", "-c", str(a.cpu)]
    subprocess.run(pin + ["true"], check=True)
root = Path(__file__).resolve().parent.parent
old = a.baseline.resolve()
fixture = "riscv64_coverage_bench_test.go"
if (root / fixture).read_bytes() != (old / fixture).read_bytes():
    p.error("baseline and candidate coverage benchmark sources differ")
a.output.mkdir(parents=True, exist_ok=False)
out = a.output.resolve()
candidate_profile = os.environ.get("GORISCV64") or "rva23u64"
fallback_profile = os.environ.get("SONIC_RISCV_BASELINE_PROFILE") or "rva20u64"
env = dict(os.environ, GORISCV64=candidate_profile, CGO_ENABLED="0", GOTOOLCHAIN="local",
           GOFLAGS="", GOMAXPROCS="1", SONIC_NO_ASYNC_GC="1")
for key in ("SONIC_USE_OPTDEC", "SONIC_USE_FASTMAP", "SONIC_ENCODER_USE_VM", "SONIC_SYNC_GC"):
    env.pop(key, None)

def capture(cmd, cwd=root, profile=candidate_profile):
    return subprocess.check_output(cmd, cwd=cwd, env=dict(env, GORISCV64=profile), text=True).strip()

def run(label, cmd, cwd=root, profile=candidate_profile):
    with (out / "commands.jsonl").open("a") as f:
        f.write(json.dumps(dict(label=label, cwd=str(cwd), argv=list(map(str, cmd)), GORISCV64=profile)) + "\n")
    print(label, flush=True)
    with (out / (label + ".log")).open("w") as f:
        result = subprocess.run(list(map(str, cmd)), cwd=cwd, env=dict(env, GORISCV64=profile), stdout=f, stderr=subprocess.STDOUT)
    (out / (label + ".exit-code.txt")).write_text(str(result.returncode) + "\n")
    if result.returncode:
        raise RuntimeError(f"{label} failed: see {out / (label + '.log')}")
    return (out / (label + ".log")).read_text()

metadata = dict(started_utc=datetime.datetime.now(datetime.timezone.utc).isoformat(),
    go_executable=a.go, go_version=capture([a.go, "version"]),
    go_env=json.loads(capture([a.go, "env", "-json"])),
    inherited_goflags=os.environ.get("GOFLAGS", ""),
    run_env={k: env.get(k, "<unset>") for k in ("GOMAXPROCS", "GOGC", "GOMEMLIMIT", "GODEBUG", "SONIC_NO_ASYNC_GC", "SONIC_USE_OPTDEC", "SONIC_USE_FASTMAP", "SONIC_ENCODER_USE_VM")},
    cpu=a.cpu, count=a.count, benchtime=a.benchtime, public_only=a.public_only, measurements_complete=False,
    source={name:dict(path=str(path), sha=capture(["git", "rev-parse", "HEAD"], path),
                     status=capture(["git", "status", "--porcelain"], path))
            for name, path in (("old", old), ("new", root))})
(out / "metadata.json").write_text(json.dumps(metadata, indent=2) + "\n")
(out / "cpuinfo.txt").write_text(Path("/proc/cpuinfo").read_text())
(out / "system.txt").write_text(capture(["uname", "-a"]) + "\n")
groups = {
 "coverage": (".", "^BenchmarkRVVCoverage$", 1),
 "decoder": ("./decoder", "^BenchmarkDecoder_(Generic|Binding)_Sonic$", 2),
 "encoder": ("./encoder", "^BenchmarkEncoder_(Generic|Binding)_Sonic$", 2),
 "ast": ("./ast", "^Benchmark(GetOne|GetFull|ParseSeven|SetOne)_Sonic$", 4),
 "kernel": ("./internal/native/riscv", "^BenchmarkNative(SkipSpace|ScanString|Unquote|ParseString|Quote|HTMLEscape|ValidUTF8|InvalidUTF8)$", 8),
}
variants = {"old": (old, candidate_profile), "new": (root, candidate_profile), "fallback": (root, fallback_profile)}
metadata["target_profiles"] = {name: profile for name, (_, profile) in variants.items()}
metadata["comparison_scope"] = "Old/new share a compiler target; fallback comparisons include compiler target differences"
metadata["selected_root_files"] = {}
for variant, (path, profile) in variants.items():
    selected = capture([a.go, "list", "-f", '{{join .GoFiles "\\n"}}', "."], path, profile).splitlines()
    expected = "compat.go" if variant == "fallback" else "sonic.go"
    if expected not in selected:
        raise RuntimeError(f"{variant}: GORISCV64={profile} does not select {expected}; check the profile and checkout's backend selection")
    metadata["selected_root_files"][variant] = selected
(out / "metadata.json").write_text(json.dumps(metadata, indent=2) + "\n")
jobs = []
metadata["kernel_packages"] = {}
for group, (pkg, regex, expected) in groups.items():
    for variant, (path, profile) in variants.items():
        if group == "kernel" and variant == "fallback":
            continue
        selected_pkg, selected_regex = pkg, regex
        if group == "kernel":
            # Older checkouts kept these eight fixtures in the RVV package.
            if not (path / "internal/native/riscv/bench_riscv64_test.go").is_file():
                selected_pkg = "./internal/native/rvv"
                selected_regex = regex.replace("BenchmarkNative", "BenchmarkRVV")
            metadata["kernel_packages"][variant] = selected_pkg
        name = variant + "-" + group
        binary = out / (name + ".test")
        run(name + ".build", [a.go, "test", "-c", "-ldflags=-checklinkname=0", "-o", binary, selected_pkg], path, profile)
        jobs.append((name, binary, selected_regex, expected, profile))
(out / "metadata.json").write_text(json.dumps(metadata, indent=2) + "\n")
# Discover fixtures and run each once before any timed measurements.
for name, binary, regex, expected, profile in jobs:
    found = run(name + ".list", pin + [binary, "-test.list=" + regex], profile=profile)
    if len(re.findall(r"^Benchmark", found, re.M)) != expected:
        raise RuntimeError(name + ": unexpected benchmark selection")
    run(name + ".smoke", pin + [binary, "-test.run=^$", "-test.bench=" + regex,
                          "-test.benchtime=1x", "-test.count=1", "-test.benchmem"], profile=profile)
# Alternate variants for each repeat, avoiding one long old run followed by new.
measure_jobs = [job for job in jobs if not a.public_only or not job[0].endswith("-kernel")]
for repeat in range(a.count):
    for name, binary, regex, expected, profile in measure_jobs:
        run(f"{name}.bench-{repeat}", pin + [binary, "-test.run=^$", "-test.bench=" + regex,
                "-test.benchtime=" + a.benchtime, "-test.count=1", "-test.benchmem"], profile=profile)
values = {}
pattern = re.compile(r"^(Benchmark\S+)\s+\d+(?:\s+([\d.]+) ns/op)?(?:\s+[\d.]+ MB/s)?\s+(\d+) B/op\s+(\d+) allocs/op$", re.M)
for name, *_ in measure_jobs:
    variant, group = name.split("-", 1)
    for repeat in range(a.count):
        samples = pattern.findall((out / f"{name}.bench-{repeat}.log").read_text())
        if not samples:
            raise RuntimeError(f"no benchmark results parsed: {name} repeat {repeat}")
        for bench, ns, size, allocs in samples:
            bench = re.sub(r"-\d+$", "", bench)
            if group == "kernel":
                bench = re.sub(r"^BenchmarkRVV", "BenchmarkNative", bench)
            values.setdefault((group, bench, variant), []).append((float(ns or 0), int(size), int(allocs)))
rows = []
for group, bench in sorted({(g, b) for g, b, v in values}):
    row = dict(group=group, benchmark=bench)
    for variant in (("old", "new") if group == "kernel" else variants):
        samples = values.get((group, bench, variant), [])
        if len(samples) != a.count:
            raise RuntimeError(f"incomplete measurements: {group} {bench} {variant}")
        for i, metric in enumerate(("ns", "bytes", "allocs")):
            row[variant + "_" + metric] = statistics.median(x[i] for x in samples)
    row["new_vs_old_percent"] = (row["new_ns"] / row["old_ns"] - 1) * 100 if row["old_ns"] else None
    rows.append(row)
if not rows:
    raise RuntimeError("no benchmark results parsed")
with (out / "medians.csv").open("w") as f:
    writer = csv.DictWriter(f, fieldnames=["group", "benchmark"] + [v + "_" + m for v in variants for m in ("ns", "bytes", "allocs")] + ["new_vs_old_percent"])
    writer.writeheader()
    writer.writerows(rows)
metadata.update(measurements_complete=True, finished_utc=datetime.datetime.now(datetime.timezone.utc).isoformat())
(out / "metadata.json").write_text(json.dumps(metadata, indent=2) + "\n")
print("Results:", out / "medians.csv")
