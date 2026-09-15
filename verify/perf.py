#!/usr/bin/env python3
"""Head-to-head perf: GoTavern vs Node SillyTavern. Stdlib only.
Metrics: cold-boot time (spawn -> first 200 on /version), working-set
memory (idle 5s/30s, post-load), per-endpoint latency (30 sequential
POSTs). Fresh temp data dirs for both. Exit 0 always (report only).
"""
import csv
import json
import os
import shutil
import statistics
import subprocess
import sys
import tempfile
import time
import urllib.request
import urllib.error

GO_DIR = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
NODE_DIR = os.path.join(os.path.dirname(GO_DIR), "TurtleTavern")
GO_BIN = os.path.join(GO_DIR, "perf-gotavern.exe")


def rss_mb(pid):
    try:
        out = subprocess.run(["tasklist", "/FI", "PID eq %d" % pid,
                              "/FO", "CSV", "/NH"], capture_output=True,
                             text=True, timeout=10).stdout.strip()
        row = next(csv.reader([out]))
        mem = row[4].replace(",", "").replace(".", "")
        # tasklist prints like 12,344 K
        num = "".join(c for c in row[4] if c.isdigit())
        return float(num) / 1024.0
    except Exception:
        return float("nan")


def wait_ready(base, timeout_s):
    t0 = time.time()
    while time.time() - t0 < timeout_s:
        try:
            with urllib.request.urlopen(base + "/version",
                                        timeout=5) as r:
                if r.status == 200:
                    return time.time() - t0
        except Exception:
            pass
        time.sleep(0.2)
    return None


def post(base, path, body):
    data = json.dumps(body).encode()
    req = urllib.request.Request(base + path, data=data,
                                 headers={"Content-Type":
                                          "application/json"},
                                 method="POST")
    t0 = time.perf_counter()
    try:
        with urllib.request.urlopen(req, timeout=30) as r:
            r.read()
            return (time.perf_counter() - t0) * 1000.0, r.status
    except urllib.error.HTTPError as e:
        return (time.perf_counter() - t0) * 1000.0, e.code
    except Exception:
        return float("nan"), -1


def bench_server(name, cmd, cwd, port, data_dir):
    base = "http://127.0.0.1:%d" % port
    proc = subprocess.Popen(cmd, cwd=cwd,
                            stdout=subprocess.DEVNULL,
                            stderr=subprocess.DEVNULL)
    result = {"name": name}
    try:
        boot = wait_ready(base, 240)
        result["boot_s"] = boot
        if boot is None:
            result["error"] = "never became ready"
            return result
        time.sleep(5)
        result["rss_idle_5s_mb"] = rss_mb(proc.pid)
        # seed + warm up (mirrors first-run frontend flow)
        post(base, "/api/settings/save", {"a": 1})
        post(base, "/api/settings/get", {})
        post(base, "/api/characters/all", {})
        lat = {}
        for path in ("/api/characters/all", "/api/settings/get",
                     "/api/chats/recent"):
            samples = []
            for _ in range(30):
                ms, code = post(base, path, {})
                if code == 200:
                    samples.append(ms)
            lat[path] = {"n": len(samples),
                         "mean_ms": statistics.mean(samples),
                         "p50_ms": statistics.median(samples),
                         "max_ms": max(samples)} if samples else None
        result["latency"] = lat
        time.sleep(25)
        result["rss_idle_30s_mb"] = rss_mb(proc.pid)
        result["rss_postload_mb"] = rss_mb(proc.pid)
    finally:
        try:
            proc.terminate()
            proc.wait(timeout=10)
        except Exception:
            try:
                proc.kill()
            except Exception:
                pass
    return result


def main():
    print("Building Go server...")
    p = subprocess.run(["go", "build", "-o", GO_BIN, "./cmd/server"],
                       cwd=GO_DIR, capture_output=True, text=True)
    if p.returncode != 0:
        print("GO BUILD FAILED\n" + p.stderr)
        return 1
    results = []
    try:
        for name, cmd, cwd, port in (
                ("go", [GO_BIN, "--disableCsrf", "-port", "18090"],
                 GO_DIR, 18090),
                ("node", ["node", "server.js", "--disableCsrf",
                          "--port", "18092"], NODE_DIR, 18092)):
            data_dir = tempfile.mkdtemp(prefix="tt-perf-%s-" % name)
            if name == "go":
                full_cmd = cmd + ["-dataRoot", data_dir]
            else:
                full_cmd = cmd + ["--dataRoot", data_dir]
            print("Benchmarking %s..." % name, flush=True)
            r = bench_server(name, full_cmd, cwd, port, data_dir)
            r["data_dir"] = data_dir
            results.append(r)
            shutil.rmtree(data_dir, ignore_errors=True)
    finally:
        try:
            os.remove(GO_BIN)
        except OSError:
            pass
    print("\n==== RESULTS ====")
    for r in results:
        print("\n[%s]" % r["name"])
        for k in ("boot_s", "rss_idle_5s_mb", "rss_idle_30s_mb",
                  "rss_postload_mb", "error"):
            if k in r and r[k] is not None:
                print("  %s: %s" % (k, r[k]))
        for path, st in (r.get("latency") or {}).items():
            print("  %s: %s" % (path, st))
    return 0


if __name__ == "__main__":
    sys.exit(main())
