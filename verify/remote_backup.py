"""Remote backup (TurtleTavern Backupper) end-to-end verification.

Brings up a throwaway TurtleTavern instance and a throwaway Backupper, wires
them together through the same per-user secret path the UI uses, then exercises
sync / incremental sync / snapshot listing / restore / type-to-confirm.

Sandbox only: everything lives in temp dirs. Never point this at a live
instance, because the restore step replaces the whole user data root.

Run from the repo root:  python verify/remote_backup.py
"""

import hashlib
import json
import os
import shutil
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
GOTAVERN_DIR = os.path.dirname(HERE)
WORKSPACE = os.path.dirname(GOTAVERN_DIR)
BACKUPPER_DIR = os.path.join(WORKSPACE, "TurtleTavern Backupper")

TT_PORT = 18093
BK_PORT = 18094
TT_BASE = f"http://127.0.0.1:{TT_PORT}"
BK_BASE = f"http://127.0.0.1:{BK_PORT}"
ADMIN_KEY = "e2e-backupper-admin"

HANDLE = "default-user"
failures = []


def check(name, ok, detail=""):
    print(f"[{'PASS' if ok else 'FAIL'}] {name}" + (f" -- {detail}" if detail else ""))
    if not ok:
        failures.append(name)


def http(method, url, body=None, token=None, timeout=60):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, method=method)
    if body is not None:
        req.add_header("Content-Type", "application/json")
    if token:
        req.add_header("Authorization", "Bearer " + token)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            raw = r.read()
            return r.status, (json.loads(raw) if raw else None)
    except urllib.error.HTTPError as e:
        raw = e.read()
        try:
            return e.code, json.loads(raw)
        except Exception:
            return e.code, {"error": raw.decode(errors="replace")}


def wait_http(url, timeout=60):
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            with urllib.request.urlopen(url, timeout=2) as r:
                if r.status == 200:
                    return True
        except Exception:
            time.sleep(0.25)
    return False


def sha256_file(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 16), b""):
            h.update(chunk)
    return h.hexdigest()


def write_secret(key, value):
    return http("POST", TT_BASE + "/api/secrets/write", {"key": key, "value": value})


def status():
    st, body = http("GET", TT_BASE + "/api/remote/status")
    if st != 200:
        raise SystemExit(f"status failed: {st} {body}")
    return body


def wait_idle(timeout=240):
    deadline = time.time() + timeout
    while time.time() < deadline:
        body = status()
        if not body.get("busy"):
            return body
        time.sleep(0.3)
    raise SystemExit("a remote operation never finished")


def user_root(tt_data):
    return os.path.join(tt_data, HANDLE)


def seed(tt_data, rel, data):
    full = os.path.join(user_root(tt_data), *rel.split("/"))
    os.makedirs(os.path.dirname(full), exist_ok=True)
    with open(full, "wb") as f:
        f.write(data if isinstance(data, bytes) else data.encode("utf-8"))


def files_view(tt_data, skip_exact=(), skip_prefix=()):
    """Map of relative path -> sha256 under the user root."""
    root = user_root(tt_data)
    out = {}
    for dirpath, _dirs, files in os.walk(root):
        for name in files:
            full = os.path.join(dirpath, name)
            rel = os.path.relpath(full, root).replace(os.sep, "/")
            if rel in skip_exact:
                continue
            if any(rel.startswith(p) for p in skip_prefix):
                continue
            out[rel] = sha256_file(full)
    return out


# Mirrors remotebackup.ScanDir: these never go into a snapshot.
SYNC_SKIP_EXACT = ("secrets.json", "content.log")
SYNC_SKIP_PREFIX = ("backups/",)


def synced_view(tt_data):
    return files_view(tt_data, SYNC_SKIP_EXACT, SYNC_SKIP_PREFIX)


def copy_tree(src, dst):
    shutil.rmtree(dst, ignore_errors=True)
    shutil.copytree(src, dst)


def main():
    work = os.path.join(tempfile.gettempdir(), "opencode", "tt-remote-e2e")
    shutil.rmtree(work, ignore_errors=True)
    os.makedirs(work)
    bindir = os.path.join(work, "bin")
    os.makedirs(bindir)

    tt_data = os.path.join(work, "tt-data")
    bk_data = os.path.join(work, "bk-data")
    os.makedirs(tt_data)
    os.makedirs(bk_data)

    print("building go server and backupper...")
    tt_bin = os.path.join(bindir, "gotavern.exe")
    bk_bin = os.path.join(bindir, "backupper.exe")
    for out, cwd, target in (
        (tt_bin, GOTAVERN_DIR, "./cmd/server"),
        (bk_bin, BACKUPPER_DIR, "./cmd/backupper"),
    ):
        p = subprocess.run(["go", "build", "-o", out, target], cwd=cwd, capture_output=True, text=True)
        if p.returncode != 0:
            print(p.stdout, p.stderr)
            raise SystemExit(f"build failed: {target}")

    bk_config = os.path.join(work, "bk-config.yaml")
    with open(bk_config, "w", encoding="utf-8") as f:
        f.write(f'listen: "127.0.0.1:{BK_PORT}"\n')
        f.write(f'dataDir: "{bk_data.replace(os.sep, "/")}"\n')
        f.write(f'adminKey: "{ADMIN_KEY}"\n')
        f.write('compression: "zstd"\n')
        f.write('logLevel: "warn"\n')

    tt_log = open(os.path.join(work, "tt.log"), "w", encoding="utf-8")
    bk_log = open(os.path.join(work, "bk.log"), "w", encoding="utf-8")
    tt_proc = bk_proc = None
    try:
        bk_proc = subprocess.Popen([bk_bin, "-config", bk_config], cwd=BACKUPPER_DIR,
                                   stdout=bk_log, stderr=subprocess.STDOUT)
        check("backupper is up", wait_http(BK_BASE + "/api/v1/health"), BK_BASE)

        st, keyresp = http("POST", BK_BASE + "/api/v1/keys",
                           {"name": "tt-e2e", "scopes": ["push", "read"]}, token=ADMIN_KEY)
        device_key = (keyresp or {}).get("key", "")
        check("device key created on the backupper", st == 201 and len(device_key) == 64, f"{st}")

        tt_proc = subprocess.Popen(
            [tt_bin, "--disableCsrf", "-port", str(TT_PORT), "-dataRoot", tt_data],
            cwd=GOTAVERN_DIR, stdout=tt_log, stderr=subprocess.STDOUT)
        check("tavern is up", wait_http(TT_BASE + "/version"), TT_BASE)

        seed(tt_data, "characters/Ada.png", b"\x89PNG\r\n\x1a\n" + os.urandom(150 * 1024))
        seed(tt_data, "chats/ada/2026-09-01.jsonl", "".join(
            json.dumps({"name": "User", "mes": f"line {i}", "send_date": 1756000000000 + i}) + "\n"
            for i in range(300)))
        seed(tt_data, "settings.json", json.dumps({"theme": "dark"}))
        seed(tt_data, "secrets.json", json.dumps({"api_key_openai": "sk-should-not-be-uploaded"}))
        seed(tt_data, "backups/old-chat.jsonl", '{"mes":"excluded by default"}\n')
        seed(tt_data, "empty.txt", b"")

        st, _ = write_secret("backupper_url", BK_BASE)
        check("backupper_url secret accepted", st == 200, f"{st}")
        st, _ = write_secret("api_key_backupper", device_key)
        check("device key stored as a secret", st == 200, f"{st}")

        body = status()
        check("status reports configured", body.get("configured") is True, json.dumps(body)[:200])
        check("status reports reachable", body.get("reachable") is True, json.dumps(body)[:200])

        st, listing = http("GET", TT_BASE + "/api/remote/snapshots")
        check("no snapshots before the first sync", st == 200 and len(listing["snapshots"]) == 0)

        st, _ = http("POST", TT_BASE + "/api/remote/sync", {"label": "e2e-first"})
        check("sync accepted", st == 202, f"{st}")
        wait_idle()
        body = status()
        check("sync finished without error", body.get("lastError") == "", str(body.get("lastError")))

        expected = synced_view(tt_data)
        st, listing = http("GET", TT_BASE + "/api/remote/snapshots")
        snaps = listing["snapshots"]
        check("one snapshot exists", len(snaps) == 1, f"{len(snaps)}")
        check("snapshot file count matches the synced subset",
              snaps[0]["fileCount"] == len(expected), f"{snaps[0]['fileCount']} vs {len(expected)}")
        check("sync skipped secrets.json and backups/",
              body["lastSync"]["scanned"] == len(expected), f"scanned {body['lastSync']['scanned']}")
        check("first sync uploaded every file",
              body["lastSync"]["uploaded"] == len(expected), f"{body['lastSync']}")

        st, _ = http("POST", TT_BASE + "/api/remote/sync", {})
        check("second sync accepted", st == 202)
        wait_idle()
        body = status()
        check("unchanged sync recognises a duplicate",
              body["lastSync"]["duplicate"] is True, f"{body['lastSync']}")
        check("unchanged sync uploaded nothing",
              body["lastSync"]["uploaded"] == 0 and body["lastSync"]["uploadedBytes"] == 0,
              f"{body['lastSync']}")

        seed(tt_data, "chats/ada/2026-09-02.jsonl", "brand new chat\n")
        seed(tt_data, "settings.json", json.dumps({"theme": "light", "changed": True}))
        st, _ = http("POST", TT_BASE + "/api/remote/sync", {"label": "e2e-second"})
        check("incremental sync accepted", st == 202)
        wait_idle()
        body = status()
        check("incremental sync uploaded exactly the two changed files",
              body["lastSync"]["uploaded"] == 2, f"{body['lastSync']}")
        check("incremental sync sent only those bytes",
              body["lastSync"]["uploadedBytes"] < 4096, f"{body['lastSync']['uploadedBytes']}")

        st, listing = http("GET", TT_BASE + "/api/remote/snapshots")
        snaps = listing["snapshots"]
        check("two snapshots now exist", len(snaps) == 2, f"{len(snaps)}")
        newest = snaps[0]["id"]
        expected = synced_view(tt_data)
        reference = os.path.join(work, "reference")
        copy_tree(user_root(tt_data), reference)

        st, _ = http("POST", TT_BASE + "/api/remote/restore", {"id": newest, "confirm": "wrong"})
        check("restore refuses a mismatched confirmation", st == 400, f"{st}")

        os.remove(os.path.join(user_root(tt_data), "chats", "ada", "2026-09-02.jsonl"))
        seed(tt_data, "settings.json", json.dumps({"theme": "corrupted"}))
        seed(tt_data, "added-after-snapshot.txt", "should disappear")

        st, _ = http("POST", TT_BASE + "/api/remote/restore", {"id": newest, "confirm": newest})
        check("restore accepted", st == 202, f"{st} {newest}")
        wait_idle()
        body = status()
        check("restore finished without error", body.get("lastError") == "", str(body.get("lastError")))

        restored = files_view(tt_data)
        missing = sorted(set(expected) - set(restored))
        extra = sorted(set(restored) - set(expected) - {"secrets.json", "content.log"})
        differing = sorted(p for p in set(expected) & set(restored) if expected[p] != restored[p])
        check("restored file set matches the snapshot", not missing and not extra,
              f"missing={missing} extra={extra}")
        check("restored bytes are identical", not differing, f"differing={differing}")
        check("restore replaced rather than merged",
              not os.path.exists(os.path.join(user_root(tt_data), "added-after-snapshot.txt")))

        st, listing = http("GET", TT_BASE + "/api/remote/snapshots")
        check("remote backup still configured after the restore",
              st == 200 and status().get("configured") is True, f"HTTP {st}")
        labels = [s.get("label", "") for s in listing["snapshots"]]
        check("a pre-restore safety snapshot was taken", "pre-restore" in labels, f"{labels}")

        st, _ = write_secret("api_key_backupper", "")
        body = status()
        check("clearing the key marks it unconfigured",
              body.get("configured") is False, json.dumps(body)[:160])
        st, _ = http("POST", TT_BASE + "/api/remote/sync", {})
        check("sync is refused when unconfigured", st == 400, f"{st}")

    finally:
        for p in (tt_proc, bk_proc):
            if p and p.poll() is None:
                p.terminate()
                try:
                    p.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    p.kill()
        tt_log.close()
        bk_log.close()

    print()
    if failures:
        print(f"{len(failures)} check(s) FAILED: {failures}")
        print(f"logs: {work}")
        return 1
    print("all remote-backup checks passed")
    print(f"workdir: {work}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
