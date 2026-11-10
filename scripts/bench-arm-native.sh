#!/usr/bin/env bash
# Compare the same ARM64 fixtures with the generated and Go/ABI0 backends.
set -euo pipefail
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
exec python3 - "$root" "$@" <<'PY'
import argparse
import csv
import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import statistics
import subprocess
import sys

root = Path(sys.argv[1])
p = argparse.ArgumentParser(description="Compare ARM64 native backends with identical fixtures. "
    "Keep compilation and other tests idle during measurement. Results are medians, not significance tests.")
baseline = p.add_mutually_exclusive_group()
baseline.add_argument("--baseline", help="Git revision to archive as the baseline")
baseline.add_argument("--baseline-dir", type=Path, help="Existing baseline checkout (fixture is copied into it)")
p.add_argument("--output", type=Path, required=True, help="New result directory, or prepared directory with --measure-only")
p.add_argument("--go", default=os.environ.get("SONIC_ARM_GO", "go"))
p.add_argument("--cpu", type=int, help="Pin measurement processes to one CPU using taskset")
p.add_argument("--count", type=int, default=5, help="Independent alternating samples (minimum 5)")
p.add_argument("--benchtime", default="200ms")
stage = p.add_mutually_exclusive_group()
stage.add_argument("--build-only", action="store_true", help="Prepare binaries and smoke checks, without timing")
stage.add_argument("--measure-only", action="store_true", help="Time binaries already prepared in --output")
a = p.parse_args(sys.argv[2:])
if a.count < 5 or a.cpu is not None and a.cpu < 0:
    p.error("count must be at least 5 and cpu must be nonnegative")
go_path = shutil.which(a.go)
if go_path is None:
    p.error("Go executable not found: " + a.go)
a.go = str(Path(go_path).resolve())
out = a.output.resolve()
env = dict(os.environ, GOARCH="arm64", CGO_ENABLED="0", GOTOOLCHAIN="local", GOFLAGS="",
           GOMAXPROCS="1", SONIC_NO_ASYNC_GC="1")
for key in ("SONIC_USE_OPTDEC", "SONIC_USE_FASTMAP", "SONIC_ENCODER_USE_VM", "SONIC_SYNC_GC"):
    env.pop(key, None)
# An absolute workspace from the caller must not mix the two source trees.
# GOWORK=off is preserved for toolchains that cannot load newer workspace modules.
if env.get("GOWORK") != "off":
    env.pop("GOWORK", None)
pin = [] if a.cpu is None else ["taskset", "-c", str(a.cpu)]
if pin:
    subprocess.run(pin + ["true"], check=True)

def capture(cmd, cwd=root):
    return subprocess.check_output(list(map(str, cmd)), cwd=cwd, env=env, text=True).strip()

def run(label, cmd, cwd=root):
    cmd = list(map(str, cmd))
    with (out / "commands.jsonl").open("a") as f:
        f.write(json.dumps(dict(label=label, cwd=str(cwd), argv=cmd)) + "\n")
    print(label, flush=True)
    with (out / (label + ".log")).open("w") as f:
        result = subprocess.run(cmd, cwd=cwd, env=env, stdout=f, stderr=subprocess.STDOUT)
    (out / (label + ".exit-code.txt")).write_text(str(result.returncode) + "\n")
    if result.returncode:
        raise RuntimeError(f"{label} failed: {out / (label + '.log')}")
    return (out / (label + ".log")).read_text()

if capture([a.go, "env", "GOHOSTARCH"]) != "arm64":
    p.error("benchmark execution requires an ARM64 host")
env["GOOS"] = capture([a.go, "env", "GOHOSTOS"])
groups = {
    "coverage": (".", "^BenchmarkARMNative(Coverage|Kernels)$", 2, "^$", 0),
    "decoder": ("./decoder", "^BenchmarkDecoder_(Generic|Binding)_Sonic$", 2, "^TestDecoder_(Generic|Binding)$", 2),
    "encoder": ("./encoder", "^BenchmarkEncoder_(Generic|Binding)_Sonic$", 2, "^TestEncoder_(Generic|Binding)$", 2),
    "ast": ("./ast", "^Benchmark(GetOne|GetFull|ParseSeven|SetOne)_Sonic$", 4, "^TestSearcher_GetByPath$", 1),
}
fixture = "arm64_framework_bench_test.go"
if a.measure_only:
    metadata = json.loads((out / "metadata.json").read_text())
    if not metadata.get("prepared") or metadata.get("measurements_complete"):
        p.error("--measure-only requires a prepared, unmeasured output directory")
    if metadata["fixture_sha256"] != hashlib.sha256((root / fixture).read_bytes()).hexdigest():
        p.error("benchmark fixture changed since preparation")
    if metadata["go_version"] != capture([a.go, "version"]):
        p.error("Go toolchain changed since preparation")
    jobs = metadata["jobs"]
    for job in jobs:
        if hashlib.sha256(Path(job["binary"]).read_bytes()).hexdigest() != job["sha256"]:
            p.error("prepared benchmark binary was modified: " + job["binary"])
else:
    if not a.baseline and not a.baseline_dir:
        p.error("provide --baseline REV or --baseline-dir DIR")
    out.mkdir(parents=True, exist_ok=False)
    if a.baseline:
        old = out / "baseline"
        old.mkdir()
        revision = capture(["git", "rev-parse", a.baseline + "^{commit}"])
        archive = subprocess.check_output(["git", "archive", revision], cwd=root)
        subprocess.run(["tar", "-xf", "-", "-C", str(old)], input=archive, check=True)
    else:
        old = a.baseline_dir.resolve()
        if old == root:
            p.error("baseline and candidate must use different directories")
        revision = None
        if (old / ".git").exists():
            revision = capture(["git", "rev-parse", "HEAD"], old)
    source = (root / fixture).read_bytes()
    target = old / fixture
    if target.exists() and target.read_bytes() != source:
        p.error("baseline contains a different benchmark fixture; refusing to overwrite it")
    target.write_bytes(source)
    for name in ("decoder/decoder_test.go", "encoder/encoder_test.go", "ast/search_test.go", "ast/parser_test.go"):
        if (old / name).read_bytes() != (root / name).read_bytes():
            p.error("baseline and candidate benchmark sources differ: " + name)
    metadata = dict(started_utc=datetime.datetime.now(datetime.timezone.utc).isoformat(),
        go_version=capture([a.go, "version"]), go_executable=a.go,
        go_env=json.loads(capture([a.go, "env", "-json"])),
        baseline=dict(path=str(old), revision=revision),
        candidate=dict(path=str(root), revision=capture(["git", "rev-parse", "HEAD"]),
                       status=capture(["git", "status", "--porcelain"])),
        fixture_sha256=hashlib.sha256(source).hexdigest(),
        prepared=False, measurements_complete=False)
    (out / "system.txt").write_text(capture(["uname", "-a"]) + "\n")
    if Path("/proc/cpuinfo").exists():
        shutil.copyfile("/proc/cpuinfo", out / "cpuinfo.txt")
    if shutil.which("lscpu"):
        (out / "lscpu.txt").write_text(capture(["lscpu"]) + "\n")
    if shutil.which("taskset"):
        (out / "inherited-affinity.txt").write_text(capture(["taskset", "-pc", str(os.getpid())]) + "\n")
    (out / "candidate.diff").write_text(capture(["git", "diff", "--no-ext-diff", "HEAD"]) + "\n")
    sources = capture(["git", "ls-files", "-co", "--exclude-standard", "-z"]).split("\0")
    manifest = {}
    for name in sorted(set(sources)):
        path = root / name
        if path.is_file() and path.suffix in (".go", ".s", ".h", ".mod", ".sum"):
            manifest[name] = hashlib.sha256(path.read_bytes()).hexdigest()
    (out / "candidate.source-sha256.json").write_text(json.dumps(manifest, indent=2) + "\n")
    selected = capture([a.go, "list", "-deps", ".", "./ast", "./decoder", "./encoder", "./utf8", "./unquote"])
    (out / "candidate.dependencies.txt").write_text(selected + "\n")
    if "github.com/bytedance/sonic/internal/native/neon" in selected.splitlines():
        raise RuntimeError("candidate public APIs still depend on generated native/neon")
    if "github.com/bytedance/sonic/internal/native/kernels" not in selected.splitlines():
        raise RuntimeError("candidate did not select shared kernels")
    jobs = []
    ldflags = []
    for group, (pkg, regex, expected, smoke, expected_tests) in groups.items():
        for variant, path in (("baseline", old), ("candidate", root)):
            name = variant + "-" + group
            binary = out / (name + ".test")
            run(name + ".build", [a.go, "test", "-c", *ldflags, "-o", binary, pkg], path)
            found = run(name + ".list", [binary, "-test.list=" + regex + "|" + smoke], path)
            if len(re.findall(r"^Benchmark", found, re.M)) != expected:
                raise RuntimeError(name + ": unexpected benchmark selection")
            if len(re.findall(r"^Test", found, re.M)) != expected_tests:
                raise RuntimeError(name + ": unexpected smoke test selection")
            run(name + ".smoke", pin + [binary, "-test.run=" + smoke, "-test.bench=" + regex,
                "-test.benchtime=1x", "-test.count=1", "-test.benchmem"], path)
            jobs.append(dict(name=name, binary=str(binary), regex=regex, cwd=str(path),
                sha256=hashlib.sha256(binary.read_bytes()).hexdigest()))
    metadata.update(prepared=True, jobs=jobs)
    (out / "metadata.json").write_text(json.dumps(metadata, indent=2) + "\n")
if a.build_only:
    print("Prepared binaries; keep other compilation/tests idle before running --measure-only.")
    sys.exit(0)

metadata.update(cpu=a.cpu, count=a.count, benchtime=a.benchtime,
    run_env={k: env.get(k, "<unset>") for k in ("GOMAXPROCS", "GOGC", "GOMEMLIMIT", "GODEBUG",
        "SONIC_NO_ASYNC_GC", "SONIC_USE_OPTDEC", "SONIC_USE_FASTMAP", "SONIC_ENCODER_USE_VM")})
(out / "metadata.json").write_text(json.dumps(metadata, indent=2) + "\n")
for repeat in range(a.count):
    # Alternate old/new order between repeats, and run all processes serially.
    ordered = jobs if repeat % 2 == 0 else [job for i in range(0, len(jobs), 2) for job in jobs[i:i+2][::-1]]
    for job in ordered:
        run(f"{job['name']}.bench-{repeat}", pin + [job["binary"], "-test.run=^$",
            "-test.bench=" + job["regex"], "-test.benchtime=" + a.benchtime,
            "-test.count=1", "-test.benchmem"], Path(job["cwd"]))

values = {}
pattern = re.compile(r"^(Benchmark\S+)\s+\d+\s+([\d.]+) ns/op(?:\s+[\d.]+ MB/s)?\s+(\d+) B/op\s+(\d+) allocs/op$", re.M)
for job in jobs:
    variant, group = job["name"].split("-", 1)
    for repeat in range(a.count):
        samples = pattern.findall((out / f"{job['name']}.bench-{repeat}.log").read_text())
        if not samples:
            raise RuntimeError(f"No benchmark samples for {job['name']}, repeat {repeat}")
        for bench, ns, size, allocs in samples:
            bench = re.sub(r"-\d+$", "", bench)
            values.setdefault((group, bench, variant), []).append((float(ns), int(size), int(allocs)))
rows = []
for group, bench in sorted({(g, b) for g, b, v in values}):
    row = dict(group=group, benchmark=bench)
    for variant in ("baseline", "candidate"):
        samples = values.get((group, bench, variant), [])
        if len(samples) != a.count:
            raise RuntimeError(f"Incomplete results: {group}, {bench}, {variant}")
        for i, metric in enumerate(("ns", "bytes", "allocs")):
            row[variant + "_" + metric] = statistics.median(x[i] for x in samples)
    row["candidate_vs_baseline_percent"] = (row["candidate_ns"] / row["baseline_ns"] - 1) * 100
    rows.append(row)
with (out / "medians.csv").open("w") as f:
    writer = csv.DictWriter(f, fieldnames=["group", "benchmark"] +
        [v + "_" + m for v in ("baseline", "candidate") for m in ("ns", "bytes", "allocs")] +
        ["candidate_vs_baseline_percent"])
    writer.writeheader()
    writer.writerows(rows)
metadata.update(measurements_complete=True, finished_utc=datetime.datetime.now(datetime.timezone.utc).isoformat())
(out / "metadata.json").write_text(json.dumps(metadata, indent=2) + "\n")
print("Results:", out / "medians.csv")
PY
