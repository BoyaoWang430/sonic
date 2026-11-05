#!/usr/bin/env python3
"""Run existing Go benchmark binaries serially with balanced, paired repeats.

This script does not compile binaries, change CPU policy, or collect debugger
timings. Use a separate output directory for each benchmark group/measurement.
"""

import argparse
import datetime
import hashlib
import json
import math
import os
from pathlib import Path
import re
import statistics
import subprocess
import sys
import time


ROOT = Path(__file__).resolve().parents[2]
CONTROLLED_ENV = (
    "GOMAXPROCS", "SONIC_NO_ASYNC_GC", "GOGC", "GOMEMLIMIT", "GODEBUG",
    "SONIC_USE_OPTDEC", "SONIC_USE_FASTMAP", "SONIC_ENCODER_USE_VM", "SONIC_SYNC_GC",
)


def utc_now():
    return datetime.datetime.now(datetime.timezone.utc).isoformat()


def sha256(path):
    with path.open("rb") as handle:
        return hashlib.file_digest(handle, "sha256").hexdigest() if hasattr(hashlib, "file_digest") else hashlib.sha256(handle.read()).hexdigest()


def write_json(path, value):
    temporary = path.with_suffix(path.suffix + ".tmp")
    temporary.write_text(json.dumps(value, indent=2) + "\n")
    temporary.replace(path)


def read_optional(path):
    try:
        return Path(path).read_text().strip()
    except OSError:
        return None


def parse_rows(output):
    rows = []
    for line in output.splitlines():
        match = re.match(r"^(Benchmark\S+)\s+(\d+)\s+(.+)$", line)
        if not match:
            continue
        fields = match[3].split()
        if len(fields) % 2:
            raise ValueError("Unrecognized benchmark metrics: " + line)
        metrics = {}
        for index in range(0, len(fields), 2):
            value, unit = float(fields[index]), fields[index + 1]
            if not math.isfinite(value) or unit in metrics:
                raise ValueError("Invalid benchmark metrics: " + line)
            metrics[unit] = value
        if metrics.get("ns/op", 0) <= 0 or int(match[2]) <= 0:
            raise ValueError("Missing positive ns/op or iteration count: " + line)
        rows.append({
            "benchmark": re.sub(r"-\d+$", "", match[1]),
            "reported_name": match[1], "iterations": int(match[2]),
            "metrics": metrics,
        })
    names = [row["benchmark"] for row in rows]
    if not rows or len(names) != len(set(names)):
        raise ValueError("Expected nonempty, unique benchmark results")
    if re.search(r"^(?:FAIL(?:\s|$)|--- FAIL:|panic:|fatal error:)", output, re.M):
        raise ValueError("Benchmark process reported a failure")
    return rows


def aggregate(values):
    median = statistics.median(values)
    return {
        "median": median, "min": min(values), "max": max(values),
        "median_absolute_deviation": statistics.median(abs(v - median) for v in values),
        "samples": values,
    }


def summarize(runs, variants, baseline, rounds):
    benchmarks = sorted({row["benchmark"] for row in runs})
    result = []
    for benchmark in benchmarks:
        row = {"benchmark": benchmark, "variants": {}, "paired_comparisons": {}}
        by_variant = {}
        for variant in variants:
            samples = sorted(
                (r for r in runs if r["benchmark"] == benchmark and r["variant"] == variant),
                key=lambda r: r["round"],
            )
            if [s["round"] for s in samples] != list(range(rounds)):
                raise ValueError("Missing paired samples for " + variant + ": " + benchmark)
            by_variant[variant] = samples
            common_metrics = set.intersection(*(set(s["metrics"]) for s in samples))
            row["variants"][variant] = {
                metric: aggregate([s["metrics"][metric] for s in samples])
                for metric in sorted(common_metrics)
            }
        # Keep every ordered pair so a C reference and the old Go backend can
        # both be used without changing or repeating the measurements.
        for reference in variants:
            row["paired_comparisons"][reference] = {}
            reference_ns = row["variants"][reference]["ns/op"]["median"]
            for variant in variants:
                if variant == reference:
                    continue
                percentages = [
                    (candidate["metrics"]["ns/op"] / control["metrics"]["ns/op"] - 1) * 100
                    for candidate, control in zip(by_variant[variant], by_variant[reference])
                ]
                comparison = aggregate(percentages)
                comparison["ratio_of_medians_percent"] = (
                    row["variants"][variant]["ns/op"]["median"] / reference_ns - 1
                ) * 100
                row["paired_comparisons"][reference][variant] = comparison
        result.append(row)
    return {
        "baseline": baseline, "rounds": rounds,
        "comparison": "Percent ns/op change; negative is faster. Median paired ratios use the same round. No significance claim or confidence interval is inferred.",
        "benchmarks": result,
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--variant", action="append", required=True, metavar="NAME=PATH")
    parser.add_argument("--baseline", help="Name used in the text summary; default: first variant")
    parser.add_argument("--bench", required=True, help="Go benchmark regexp, including slash-separated subbenchmarks")
    parser.add_argument("--cpu", type=int, default=16, help="Pin all benchmark threads to this logical CPU")
    parser.add_argument("--rounds", type=int, default=8, help="Use a multiple of 2 * variants for balanced order")
    parser.add_argument("--benchtime", default="300ms")
    parser.add_argument("--expect", type=int, help="Expected number of benchmark rows per invocation")
    parser.add_argument("--timeout", type=float, default=300, help="Maximum wall seconds per process")
    parser.add_argument("--cwd", type=Path, default=ROOT)
    parser.add_argument("--out", type=Path, required=True, help="New output directory; existing directories are rejected")
    parser.add_argument("--env", action="append", default=[], metavar="KEY=VALUE", help="Explicit override after benchmark environment normalization")
    parser.add_argument("--note", default="", help="Record fixture/options/build details without interpreting them")
    parser.add_argument("--dry-run", action="store_true", help="Write the execution plan without launching binaries")
    args = parser.parse_args()
    if args.rounds < 1 or args.timeout <= 0 or args.cpu < 0 or (args.expect is not None and args.expect < 1):
        parser.error("rounds, timeout and expect must be positive; cpu must be nonnegative")
    variants = {}
    for specification in args.variant:
        name, separator, path = specification.partition("=")
        if not separator or not re.fullmatch(r"[A-Za-z0-9_-]+", name) or name in variants:
            parser.error("Use unique NAME=PATH variants with simple names")
        binary = Path(path).resolve()
        if not binary.is_file() or not os.access(binary, os.X_OK):
            parser.error("Missing executable: " + str(binary))
        variants[name] = binary
    baseline = args.baseline or next(iter(variants))
    if baseline not in variants:
        parser.error("baseline must name a variant")
    allowed_cpus = sorted(os.sched_getaffinity(0)) if hasattr(os, "sched_getaffinity") else None
    if allowed_cpus is not None and args.cpu not in allowed_cpus:
        parser.error("Requested CPU is outside this process's allowed affinity")
    env = dict(os.environ)
    for key in CONTROLLED_ENV:
        env.pop(key, None)
    env.update(GOMAXPROCS="1", SONIC_NO_ASYNC_GC="1")
    for specification in args.env:
        key, separator, value = specification.partition("=")
        if not separator or not key or "=" in key or "\0" in key + value:
            parser.error("Environment overrides must use KEY=VALUE")
        env[key] = value
    cwd, out = args.cwd.resolve(), args.out.resolve()
    if not cwd.is_dir():
        parser.error("cwd must exist")
    out.mkdir(parents=True, exist_ok=False)
    names = list(variants)
    plan = []
    for repeat in range(args.rounds):
        rotation = repeat % len(names)
        order = names[rotation:] + names[:rotation]
        if (repeat // len(names)) % 2:
            order.reverse()
        for position, name in enumerate(order):
            plan.append({
                "round": repeat, "position": position, "variant": name,
                "command": ["taskset", "-c", str(args.cpu), str(variants[name]),
                            "-test.run=^$", "-test.bench=" + args.bench,
                            "-test.benchtime=" + args.benchtime, "-test.count=1", "-test.benchmem"],
                "log": f"round-{repeat:02d}-{position:02d}-{name}.log",
            })
    hashes = {name: sha256(binary) for name, binary in variants.items()}
    metadata = {
        "started_utc": utc_now(), "complete": False, "dry_run": args.dry_run,
        "cwd": str(cwd), "cpu": args.cpu, "allowed_cpus": allowed_cpus,
        "cpu_siblings": read_optional(f"/sys/devices/system/cpu/cpu{args.cpu}/topology/thread_siblings_list"),
        "cpu_governor": read_optional(f"/sys/devices/system/cpu/cpu{args.cpu}/cpufreq/scaling_governor"),
        "kernel_isolated_cpus": read_optional("/sys/devices/system/cpu/isolated"),
        "affinity_note": "CPU pinning is not exclusive host isolation; other processes and interrupts may still run on this CPU.",
        "uname": list(os.uname()), "rounds": args.rounds, "benchtime": args.benchtime,
        "benchmark": args.bench, "expected_rows": args.expect, "baseline": baseline,
        "binaries": {name: {"path": str(binary), "sha256": hashes[name]} for name, binary in variants.items()},
        "environment": {key: env.get(key) for key in sorted(set(CONTROLLED_ENV) | {s.split("=", 1)[0] for s in args.env})},
        "note": args.note, "plan": plan,
        "order": "Rotate variants each round; reverse rotations in the next block. Fully balanced at multiples of twice the variant count.",
        "runner_sha256": sha256(Path(__file__)),
    }
    write_json(out / "metadata.json", metadata)
    if args.dry_run:
        print("Wrote execution plan to", out / "metadata.json")
        return 0
    runs, expected_names = [], None
    try:
        for job in plan:
            name = job["variant"]
            if sha256(variants[name]) != hashes[name]:
                raise RuntimeError("Binary changed during comparison: " + name)
            began = time.monotonic()
            with (out / job["log"]).open("w") as log:
                process = subprocess.run(job["command"], cwd=cwd, env=env, stdout=log,
                                         stderr=subprocess.STDOUT, timeout=args.timeout, check=False)
            output = (out / job["log"]).read_text(errors="replace")
            if process.returncode:
                raise RuntimeError(f"{name} exited {process.returncode}; see {job['log']}")
            rows = parse_rows(output)
            benchmark_names = sorted(row["benchmark"] for row in rows)
            if args.expect is not None and len(rows) != args.expect:
                raise RuntimeError(f"Expected {args.expect} rows, found {len(rows)} in {job['log']}")
            if expected_names is None:
                expected_names = benchmark_names
            if benchmark_names != expected_names:
                raise RuntimeError("Benchmark names differ between runs: " + job["log"])
            for row in rows:
                row.update(variant=name, round=job["round"], position=job["position"],
                           log=job["log"], process_wall_seconds=time.monotonic() - began)
                runs.append(row)
            write_json(out / "runs.json", runs)
            print(f"round {job['round'] + 1}/{args.rounds}: {name}, {len(rows)} results", flush=True)
        if any(sha256(binary) != hashes[name] for name, binary in variants.items()):
            raise RuntimeError("At least one binary changed during the comparison")
        summary = summarize(runs, names, baseline, args.rounds)
        write_json(out / "summary.json", summary)
        lines = [summary["comparison"], "Reference: " + baseline, ""]
        for row in summary["benchmarks"]:
            lines.append(row["benchmark"])
            for name in names:
                ns = row["variants"][name]["ns/op"]["median"]
                change = "reference" if name == baseline else f"{row['paired_comparisons'][baseline][name]['median']:+.2f}% paired median"
                lines.append(f"  {name}: {ns:.3f} ns/op; {change}")
        (out / "summary.txt").write_text("\n".join(lines) + "\n")
        metadata["complete"] = True
        print("\n".join(lines), flush=True)
    except BaseException as error:
        metadata["error"] = repr(error)
        raise
    finally:
        metadata["finished_utc"] = utc_now()
        write_json(out / "metadata.json", metadata)
    return 0


if __name__ == "__main__":
    sys.exit(main())
