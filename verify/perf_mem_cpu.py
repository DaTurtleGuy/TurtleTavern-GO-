#!/usr/bin/env python3
"""Memory + CPU comparison: GoTavern (Go) vs original SillyTavern (Node).

For each backend, using an isolated fresh data dir and port:
  - boot until ready
  - idle: RSS working set, and CPU seconds consumed over 10s of no traffic
  - load: N mixed API requests; measure wall time, CPU seconds consumed,
    CPU per request, and RSS after load

CPU is total processor seconds (user+kernel) sampled via Get-Process.
Stdlib only. Exit 0 always (report only).
"""
import csv
import http.cookiejar
import json
import os
import shutil
import subprocess
import sys
import tempfile
import time
import urllib.request
import urllib.error

GO_DIR = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
NODE_DIR = os.path.join(os.path.dirname(GO_DIR), "TurtleTavern")
GO_BIN = os.path.join(GO_DIR, "perf2-gotavern.exe")

IDLE_SECONDS = 10
REQUESTS = 400
WARMUP = 20

ENDPOINTS = [
    ("GET", "/version"),
    ("POST", "/api/ping"),
    ("POST", "/api/settings/get"),
    ("POST", "/api/characters/all"),
    ("POST", "/api/chats/recent"),
    ("POST", "/api/backgrounds/all"),
]


def proc_stats(pid):
    try:
        out = subprocess.run(
            ["powershell", "-NoProfile", "-Command",
             "$p=Get-Process -Id %d -ErrorAction Stop; "
             "Write-Output ($p.CPU.ToString([cultureinfo]::InvariantCulture)"
             " + '|' + $p.WorkingSet64)" % pid],
            capture_output=True, text=True, timeout=20).stdout.strip()
        cpu_s, ws = out.split("|")
        return float(cpu_s), float(ws) / 1048576.0
    except Exception:
        return float("nan"), float("nan")


def wait_ready(base, timeout_s):
    t0 = time.time()
    while time.time() - t0 < timeout_s:
        try:
            with urllib.request.urlopen(base + "/version", timeout=5) as r:
                if r.status == 200:
                    return time.time() - t0
        except Exception:
            pass
        time.sleep(0.3)
    return None


class Client:
    def __init__(self, base):
        self.base = base
        jar = http.cookiejar.CookieJar()
        self.opener = urllib.request.build_opener(
            urllib.request.HTTPCookieProcessor(jar))

    def call(self, method, path):
        data = b"{}" if method == "POST" else None
        headers = {"Content-Type": "application/json"} if data else {}
        req = urllib.request.Request(self.base + path, data=data,
                                     headers=headers, method=method)
        try:
            with self.opener.open(req, timeout=30) as r:
                r.read()
                return r.status
        except urllib.error.HTTPError as e:
            return e.code
        except Exception:
            return -1


def run_backend(name, cmd, cwd, port, data_arg):
    base = "http://127.0.0.1:%d" % port
    data_dir = tempfile.mkdtemp(prefix="tt-perf2-%s-" % name)
    res = {"name": name}
    p = subprocess.Popen(cmd + [data_arg, data_dir], cwd=cwd,
                         stdout=subprocess.DEVNULL,
                         stderr=subprocess.DEVNULL)
    try:
        boot = wait_ready(base, 300)
        res["boot_s"] = boot
        if boot is None:
            res["error"] = "never ready"
            return res, data_dir
        time.sleep(3)
        c = Client(base)
        for i in range(WARMUP):
            m, path = ENDPOINTS[i % len(ENDPOINTS)]
            c.call(m, path)

        # idle measurement
        cpu0, rss_idle = proc_stats(p.pid)
        time.sleep(IDLE_SECONDS)
        cpu1, rss_idle2 = proc_stats(p.pid)
        res["rss_idle_mb"] = rss_idle2
        res["idle_cpu_pct"] = (cpu1 - cpu0) / IDLE_SECONDS * 100.0

        # load measurement
        cpu_before, _ = proc_stats(p.pid)
        t0 = time.perf_counter()
        ok = 0
        for i in range(REQUESTS):
            m, path = ENDPOINTS[i % len(ENDPOINTS)]
            if c.call(m, path) in (200, 204):
                ok += 1
        wall = time.perf_counter() - t0
        cpu_after, rss_after = proc_stats(p.pid)
        cpu_used = cpu_after - cpu_before
        res["load_ok"] = ok
        res["load_wall_s"] = wall
        res["load_cpu_s"] = cpu_used
        res["cpu_ms_per_req"] = (cpu_used / REQUESTS) * 1000.0
        res["rps"] = REQUESTS / wall if wall else 0
        res["rss_after_load_mb"] = rss_after
    finally:
        try:
            p.terminate()
            p.wait(timeout=10)
        except Exception:
            try:
                p.kill()
            except Exception:
                pass
    return res, data_dir


def main():
    print("Building Go server...")
    b = subprocess.run(["go", "build", "-o", GO_BIN, "./cmd/server"],
                       cwd=GO_DIR, capture_output=True, text=True)
    if b.returncode != 0:
        print("GO BUILD FAILED\n" + b.stderr)
        return 1
    results = []
    dirs = []
    try:
        for name, cmd, cwd, port, darg in (
                ("go", [GO_BIN, "--disableCsrf", "-port", "18090"],
                 GO_DIR, 18090, "-dataRoot"),
                ("node", ["node", "server.js", "--disableCsrf",
                          "--port", "18092"], NODE_DIR, 18092, "--dataRoot")):
            print("Benchmarking %s (boot may take a while for node)..." % name,
                  flush=True)
            r, d = run_backend(name, cmd, cwd, port, darg)
            results.append(r)
            dirs.append(d)
    finally:
        for d in dirs:
            shutil.rmtree(d, ignore_errors=True)
        try:
            os.remove(GO_BIN)
        except OSError:
            pass

    print("\n================ MEMORY / CPU ================")
    hdr = ["metric", "go", "node"]
    rows = [
        ("boot_s", "boot_s"),
        ("rss_idle_mb", "rss_idle_mb"),
        ("idle_cpu_pct", "idle_cpu_pct"),
        ("rss_after_load_mb", "rss_after_load_mb"),
        ("load_wall_s", "load_wall_s"),
        ("load_cpu_s", "load_cpu_s"),
        ("cpu_ms_per_req", "cpu_ms_per_req"),
        ("rps", "rps"),
        ("load_ok", "load_ok"),
    ]
    byname = {r["name"]: r for r in results}
    print("%-22s %14s %14s" % tuple(hdr))
    for label, key in rows:
        g = byname.get("go", {}).get(key)
        n = byname.get("node", {}).get(key)
        def fmt(v):
            if v is None:
                return "-"
            if isinstance(v, float):
                return "%.2f" % v
            return str(v)
        print("%-22s %14s %14s" % (label, fmt(g), fmt(n)))
    print("\n(raw: %s)" % json.dumps(results))
    return 0


if __name__ == "__main__":
    sys.exit(main())
