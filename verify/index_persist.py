#!/usr/bin/env python3
"""Proves the character index persists across restarts (it used to be
closed at startup, forcing a full rebuild every boot)."""
import os
import shutil
import subprocess
import sys
import tempfile
import time
import urllib.request

GO_DIR = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
BIN = os.path.join(GO_DIR, "index-test.exe")
PORT = "18098"
BASE = "http://127.0.0.1:" + PORT


def boot(data_dir, log_path):
    f = open(log_path, "w")
    p = subprocess.Popen([BIN, "--disableCsrf", "-port", PORT,
                          "-dataRoot", data_dir], cwd=GO_DIR,
                         stdout=f, stderr=subprocess.STDOUT)
    t0 = time.time()
    while time.time() - t0 < 60:
        try:
            with urllib.request.urlopen(BASE + "/version", timeout=5) as r:
                if r.status == 200:
                    return p
        except Exception:
            pass
        time.sleep(0.2)
    p.kill()
    raise RuntimeError("server never ready")


def create_char():
    boundary = "----idx"
    fields = {"ch_name": "IndexProbe", "description": "d"}
    parts = []
    for k, v in fields.items():
        parts += ["--" + boundary,
                  'Content-Disposition: form-data; name="%s"' % k, "", v]
    parts += ["--" + boundary + "--", ""]
    data = "\r\n".join(parts).encode()
    req = urllib.request.Request(
        BASE + "/api/characters/create", data=data,
        headers={"Content-Type": "multipart/form-data; boundary=" + boundary},
        method="POST")
    with urllib.request.urlopen(req, timeout=15) as r:
        return r.status


def main():
    print("Building...")
    p = subprocess.run(["go", "build", "-o", BIN, "./cmd/server"], cwd=GO_DIR,
                       capture_output=True, text=True)
    if p.returncode != 0:
        print("BUILD FAILED\n" + p.stderr)
        return 1
    data_dir = tempfile.mkdtemp(prefix="tt-idx-")
    log1 = os.path.join(tempfile.gettempdir(), "tt-idx-1.log")
    log2 = os.path.join(tempfile.gettempdir(), "tt-idx-2.log")
    proc = None
    try:
        proc = boot(data_dir, log1)
        print("create status:", create_char())
        proc.kill()
        proc.wait()
        time.sleep(0.5)

        proc = boot(data_dir, log2)
        time.sleep(2)
        proc.kill()
        proc.wait()

        first = open(log1).read()
        second = open(log2).read()
        rebuilt_1 = "Rebuilding character index" in first
        rebuilt_2 = "Rebuilding character index" in second
        print("boot1 rebuilt:", rebuilt_1)
        print("boot2 rebuilt:", rebuilt_2)
        if rebuilt_2:
            print("FAIL: index did not persist; still rebuilding on boot 2")
            return 1
        print("PASS: index persisted (no rebuild on second boot)")
        return 0
    finally:
        if proc is not None:
            try:
                proc.kill()
            except Exception:
                pass
        shutil.rmtree(data_dir, ignore_errors=True)
        for f in (BIN, log1, log2):
            try:
                os.remove(f)
            except OSError:
                pass


if __name__ == "__main__":
    sys.exit(main())
