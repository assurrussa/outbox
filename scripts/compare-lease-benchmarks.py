#!/usr/bin/env python3
"""Compare immutable snapshots with one identical, current lease benchmark."""

import argparse
import hashlib
import io
import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import tarfile
from datetime import datetime, timezone


def command(args, cwd, env=None):
    return subprocess.check_output(args, cwd=cwd, env=env, text=True).strip()


def checksums(root):
    return {
        path.relative_to(root).as_posix(): hashlib.sha256(path.read_bytes()).hexdigest()
        for path in sorted(root.rglob("*")) if path.is_file()
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base", required=True)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    repo = Path(__file__).resolve().parents[1]
    output = args.output.resolve()
    if output.is_relative_to(repo):
        parser.error("output must be outside the source repository")
    benchstat = shutil.which("benchstat")
    if benchstat is None:
        parser.error("benchstat must be installed and available on PATH")
    base = command(["git", "rev-parse", "--verify", args.base + "^{commit}"], repo)
    head = command(["git", "rev-parse", "HEAD"], repo)
    output.mkdir(parents=True, exist_ok=False)
    before, after = output / "before", output / "after"
    before.mkdir()
    after.mkdir()
    archive = subprocess.check_output(["git", "archive", base], cwd=repo)
    with tarfile.open(fileobj=io.BytesIO(archive)) as source:
        source.extractall(before, filter="data")
    paths = subprocess.check_output(
        ["git", "ls-files", "--cached", "--others", "--exclude-standard", "-z"], cwd=repo
    ).split(b"\0")
    for entry in set(paths) - {b""}:
        relative = Path(os.fsdecode(entry))
        source = repo / relative
        if source.is_file():
            destination = after / relative
            destination.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(source, destination)

    harness = Path("outbox/service_execution_batch_benchmark_test.go")
    shutil.copyfile(after / harness, before / harness)
    assert (before / harness).read_bytes() == (after / harness).read_bytes()
    source_hashes = {"before": checksums(before), "after": checksums(after)}
    (output / "source-checksums.json").write_text(json.dumps(source_hashes, indent=2) + "\n")
    tracked_diff = subprocess.check_output(["git", "diff", "--binary", base], cwd=repo)
    (output / "tracked-changes.patch").write_bytes(tracked_diff)
    manifest = {
        "base": base, "head": head,
        "working_tree_dirty": bool(command(["git", "status", "--porcelain"], repo)),
        "source_tree_sha256": {
            name: hashlib.sha256(json.dumps(files, sort_keys=True).encode()).hexdigest()
            for name, files in source_hashes.items()
        },
        "harness_sha256": source_hashes["after"][harness.as_posix()],
        "tracked_diff_sha256": hashlib.sha256(tracked_diff).hexdigest(),
        "measured_sources_sha256": {
            name: hashlib.sha256(json.dumps({
                path: sha for path, sha in files.items()
                if path.endswith((".go", ".sql")) or Path(path).name in ("go.mod", "go.sum", "go.work", "go.work.sum")
            }, sort_keys=True).encode()).hexdigest()
            for name, files in source_hashes.items()
        },
        "go": command(["go", "version"], repo),
        "benchstat_build": command(["go", "version", "-m", benchstat], repo).replace(benchstat, "benchstat"),
        "host": {"system": platform.system(), "architecture": platform.machine()},
        "settings": {"GOMAXPROCS": "2", "GOGC": "100", "GOMEMLIMIT": "off", "GOFLAGS": ""},
        "go_experiment": command(["go", "env", "GOEXPERIMENT"], repo),
        "benchtime": "300ms", "pairs": 10, "order": "AB then BA, alternating",
        "jobs_per_operation": 1000, "scope": "in-memory core; no database or broker",
        "started_at": datetime.now(timezone.utc).isoformat(),
    }
    manifest_file = output / "manifest.json"
    manifest_file.write_text(json.dumps(manifest, indent=2) + "\n")
    env = dict(os.environ, **manifest["settings"])
    env["GOCACHE"] = str(repo / ".cache/go-build")
    env["GOMODCACHE"] = str(repo / ".cache/go-mod")
    for name, source in (("before", before), ("after", after)):
        build_env = dict(env, GOWORK=str(source / "go.work"))
        subprocess.run(
            ["go", "test", "-c", "-o", str(output / (name + ".test")), "./outbox"],
            cwd=source, env=build_env, check=True,
        )
    bench_args = ["-test.run=^$", "-test.bench=^BenchmarkExecutionPaths$", "-test.benchmem",
                  "-test.benchtime=300ms", "-test.count=1", "-test.cpu=2"]
    with (output / "before.txt").open("w") as a, (output / "after.txt").open("w") as b:
        outputs = {"before": a, "after": b}
        for pair in range(10):
            order = ("before", "after") if pair % 2 == 0 else ("after", "before")
            for name in order:
                result = subprocess.run(
                    [str(output / (name + ".test")), *bench_args], cwd=output / name,
                    env=env, text=True, capture_output=True, check=True,
                )
                outputs[name].write(result.stdout)
                outputs[name].flush()
                (output / f"{pair + 1:02d}-{name}.txt").write_text(result.stdout + result.stderr)
                for line in result.stdout.splitlines():
                    if line.startswith("cpu: "):
                        manifest["host"]["cpu"] = line.removeprefix("cpu: ")
            print(f"pair {pair + 1}/10 completed", flush=True)
    comparison = command([benchstat, "before.txt", "after.txt"], output)
    (output / "comparison.txt").write_text(comparison + "\n")
    manifest["completed_at"] = datetime.now(timezone.utc).isoformat()
    manifest_file.write_text(json.dumps(manifest, indent=2) + "\n")
    print(f"comparison saved to {output / 'comparison.txt'}", flush=True)


if __name__ == "__main__":
    main()
