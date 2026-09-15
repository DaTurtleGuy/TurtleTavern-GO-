#!/usr/bin/env python3
"""Backup export / restore round-trip verification for GoTavern.

Builds the server, boots it on loopback with a temp data root, then exercises
GET /api/users/backup and POST /api/users/restore: option gating, manifest and
checksum integrity, exclusion rules, tamper rejection and zip-slip rejection.
Stdlib only. Exit code 1 if any check fails.
"""
import hashlib
import io
import json
import os
import shutil
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
import zipfile

GOTAVERN_DIR = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
EXE = ".exe" if os.name == "nt" else ""
SERVER_BIN = os.path.join(GOTAVERN_DIR, "verify-server-backup" + EXE)
PORT = 18091
BASE = f"http://127.0.0.1:{PORT}"


def http_get(path):
    try:
        with urllib.request.urlopen(BASE + path, timeout=120) as r:
            return r.status, r.read()
    except urllib.error.HTTPError as e:
        return e.code, e.read()


def http_post_file(path, filename, data):
    boundary = "----ttboundary7f3a"
    body = io.BytesIO()
    body.write(f"--{boundary}\r\n".encode())
    body.write(f'Content-Disposition: form-data; name="file"; filename="{filename}"\r\n'.encode())
    body.write(b"Content-Type: application/zip\r\n\r\n")
    body.write(data)
    body.write(f"\r\n--{boundary}--\r\n".encode())
    req = urllib.request.Request(
        BASE + path, data=body.getvalue(), method="POST",
        headers={"Content-Type": f"multipart/form-data; boundary={boundary}"})
    try:
        with urllib.request.urlopen(req, timeout=300) as r:
            return r.status, r.read()
    except urllib.error.HTTPError as e:
        return e.code, e.read()


def main():
    results = []

    def check(name, ok):
        results.append((name, bool(ok)))
        print(("PASS  " if ok else "FAIL  ") + name)

    build = subprocess.run(["go", "build", "-o", SERVER_BIN, "./cmd/server"],
                           cwd=GOTAVERN_DIR, capture_output=True, text=True)
    if build.returncode != 0:
        print("BUILD FAILED\n" + build.stderr)
        return 1

    data_dir = tempfile.mkdtemp(prefix="tt-backup-")
    log = open(os.path.join(data_dir, "server.log"), "w")
    proc = subprocess.Popen(
        [SERVER_BIN, "--disableCsrf", "-port", str(PORT), "-dataRoot", data_dir],
        cwd=GOTAVERN_DIR, stdout=log, stderr=subprocess.STDOUT)
    try:
        ready = False
        for _ in range(60):
            if proc.poll() is not None:
                break
            try:
                if http_get("/version")[0] == 200:
                    ready = True
                    break
            except Exception:
                pass
            time.sleep(0.5)
        if not ready:
            print("server failed to boot")
            return 1

        root = os.path.join(data_dir, "default-user")

        def seed(rel, data=b"x"):
            fp = os.path.join(root, rel)
            os.makedirs(os.path.dirname(fp), exist_ok=True)
            with open(fp, "wb") as f:
                f.write(data)

        seed("characters/TestChar.png", b"\x89PNG-fake")
        seed("chats/default_TestChar/chat.jsonl", b'{"a":1}\n')
        seed("settings.json", b'{"theme":"dark"}')
        seed("backgrounds/bg.jpg", b"JPEGDATA" * 100)
        seed("secrets.json", b'{"api_key":[{"value":"secret","active":true}]}')
        seed("backups/chat_x_20260101.jsonl", b'{"old":1}\n')
        time.sleep(0.3)

        code, body = http_get("/api/users/backup")
        check("GET /api/users/backup -> 200 zip", code == 200 and body[:2] == b"PK")
        zf = zipfile.ZipFile(io.BytesIO(body))
        names = set(zf.namelist())
        check("manifest present", "manifest.json" in names)
        check("checksums present", "checksums.sha256" in names)
        check("character file included", "characters/TestChar.png" in names)
        check("settings included", "settings.json" in names)
        check("backups excluded by default", not any(n.startswith("backups/") for n in names))
        check("secrets excluded by default", "secrets.json" not in names)

        manifest = json.loads(zf.read("manifest.json"))
        check("manifest format tag", manifest.get("format") == "turtletavern-backup")

        cks = {}
        for line in zf.read("checksums.sha256").decode().splitlines():
            digest, rel = line.split("  ", 1)
            cks[rel] = digest
        matches = all(
            cks.get(n) == hashlib.sha256(zf.read(n)).hexdigest()
            for n in names if n not in ("manifest.json", "checksums.sha256"))
        check("checksum table matches payload", matches)
        check("manifest fileCount matches checksums", manifest.get("fileCount") == len(cks))

        check("includeKeys=1 blocked when exposure off -> 400",
              http_get("/api/users/backup?includeKeys=1")[0] == 400)
        check("traversal handle rejected -> 403",
              http_get("/api/users/backup?handle=..%2Fevil")[0] == 403)

        _, with_backups = http_get("/api/users/backup?includeBackups=1")
        check("includeBackups=1 includes backups/",
              any(n.startswith("backups/") for n in zipfile.ZipFile(io.BytesIO(with_backups)).namelist()))

        shutil.rmtree(os.path.join(root, "characters"), ignore_errors=True)
        with open(os.path.join(root, "settings.json"), "wb") as f:
            f.write(b'{"theme":"GONE"}')
        code, _ = http_post_file("/api/users/restore", "backup.zip", body)
        check("POST /api/users/restore -> 200", code == 200)
        check("characters restored", os.path.exists(os.path.join(root, "characters", "TestChar.png")))
        with open(os.path.join(root, "settings.json"), "rb") as f:
            check("settings restored", f.read() == b'{"theme":"dark"}')
        check("staging dirs cleaned",
              not any(n.startswith(".default-user.restore") for n in os.listdir(data_dir)))

        tampered = io.BytesIO()
        with zipfile.ZipFile(tampered, "w", zipfile.ZIP_DEFLATED) as z:
            for n in names:
                z.writestr(n, b"\x89PNG-TAMPERED" if n == "characters/TestChar.png" else zf.read(n))
        check("tampered archive rejected (400)",
              http_post_file("/api/users/restore", "tampered.zip", tampered.getvalue())[0] == 400)

        slip = io.BytesIO()
        with zipfile.ZipFile(slip, "w") as z:
            z.writestr("manifest.json", json.dumps({"format": "turtletavern-backup", "formatVersion": 1, "fileCount": 0}))
            z.writestr("checksums.sha256", "")
            z.writestr("../evil.txt", "x")
        check("zip-slip rejected (400)",
              http_post_file("/api/users/restore", "slip.zip", slip.getvalue())[0] == 400)

        junk = io.BytesIO()
        with zipfile.ZipFile(junk, "w") as z:
            z.writestr("hello.txt", "hi")
        check("non-backup zip rejected (400)",
              http_post_file("/api/users/restore", "junk.zip", junk.getvalue())[0] == 400)

        with open(os.path.join(root, "settings.json"), "rb") as f:
            check("live data intact after rejections", f.read() == b'{"theme":"dark"}')
    finally:
        try:
            proc.terminate()
            proc.wait(timeout=5)
        except Exception:
            proc.kill()
        log.close()
        shutil.rmtree(data_dir, ignore_errors=True)
        try:
            os.remove(SERVER_BIN)
        except OSError:
            pass

    passed = sum(1 for _, ok in results if ok)
    print(f"\n{passed}/{len(results)} passed")
    return 0 if passed == len(results) else 1


if __name__ == "__main__":
    sys.exit(main())
