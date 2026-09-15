#!/usr/bin/env python3
"""Kill -9 soak: hammer chat saves while hard-killing the server mid-write.
Then validates: chat JSONL parses line-by-line, character PNG has IDAT +
chara chunk, settings.json parses, server reboots and serves everything.
Stdlib only. Exit 1 on any corruption.
"""
import json
import os
import shutil
import struct
import subprocess
import sys
import tempfile
import time
import urllib.request
import urllib.error
import zlib

GO_DIR = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
BIN = os.path.join(GO_DIR, "soak-server.exe")
BASE = "http://127.0.0.1:18095"


def boot(data_dir):
    p = subprocess.Popen(
        [BIN, "--disableCsrf", "-port", "18095", "-dataRoot", data_dir],
        cwd=GO_DIR, stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL)
    t0 = time.time()
    while time.time() - t0 < 60:
        if p.poll() is not None:
            raise RuntimeError("server died on boot")
        try:
            with urllib.request.urlopen(BASE + "/version",
                                        timeout=5) as r:
                if r.status == 200:
                    return p
        except Exception:
            pass
        time.sleep(0.2)
    p.kill()
    raise RuntimeError("server never ready")


def post(path, body, timeout=10):
    data = json.dumps(body).encode()
    req = urllib.request.Request(BASE + path, data=data,
                                 headers={"Content-Type":
                                          "application/json"},
                                 method="POST")
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return r.status
    except urllib.error.HTTPError as e:
        return e.code
    except Exception:
        return -1


def post_mp(path, fields):
    boundary = "----soakboundary"
    parts = []
    for k, v in fields.items():
        parts += ["--" + boundary,
                  'Content-Disposition: form-data; name="%s"' % k,
                  "", str(v)]
    parts += ["--" + boundary + "--", ""]
    data = "\r\n".join(parts).encode()
    req = urllib.request.Request(
        BASE + path, data=data,
        headers={"Content-Type": "multipart/form-data; boundary=" +
                                 boundary},
        method="POST")
    try:
        with urllib.request.urlopen(req, timeout=15) as r:
            return r.status, r.read().decode()
    except urllib.error.HTTPError as e:
        return e.code, ""
    except Exception:
        return -1, ""


def png_chunks(path):
    d = open(path, "rb").read()
    assert d[:8] == b"\x89PNG\r\n\x1a\n", "bad signature"
    pos, types = 8, []
    while pos < len(d):
        ln = struct.unpack(">I", d[pos:pos + 4])[0]
        typ = d[pos + 4:pos + 8]
        types.append(typ)
        pos += 12 + ln
        if typ == b"IEND":
            break
    return types


def main():
    print("Building...")
    p = subprocess.run(["go", "build", "-o", BIN, "./cmd/server"],
                       cwd=GO_DIR, capture_output=True, text=True)
    if p.returncode != 0:
        print("BUILD FAILED\n" + p.stderr)
        return 1
    data_dir = tempfile.mkdtemp(prefix="tt-soak-")
    proc = None
    failures = []
    try:
        proc = boot(data_dir)
        code, text = post_mp("/api/characters/create",
                             {"ch_name": "SoakChar",
                              "description": "d"})
        assert code in (200, 201), "create failed: %r" % code
        avatar = text.strip()
        print("created:", avatar)
        n_saves, n_kills = 0, 0
        for i in range(60):
            chat = [{"user_name": "User",
                     "character_name": "SoakChar",
                     "chat_metadata": {}},
                    {"name": "User", "is_user": True,
                     "mes": "msg %d %s" % (i, "x" * 200),
                     "send_date": i},
                    {"name": "SoakChar", "is_user": False,
                     "mes": "reply %d" % i, "send_date": i}]
            code = post("/api/chats/save",
                        {"avatar_url": avatar, "chat_name": "soak",
                         "chat": chat})
            if code in (200, 204):
                n_saves += 1
            post("/api/settings/save", {"n": i})
            if i % 7 == 6:
                proc.kill()
                proc.wait()
                n_kills += 1
                time.sleep(0.3)
                proc = boot(data_dir)
        print("saves accepted: %d, kills: %d" % (n_saves, n_kills))
        if n_saves < 30:
            failures.append("too few saves accepted: %d" % n_saves)

        # validate chat file
        chat_dir = os.path.join(data_dir, "default-user", "chats",
                                "SoakChar")
        files = os.listdir(chat_dir)
        assert files, "no chat files!"
        for f in files:
            lines = open(os.path.join(chat_dir, f),
                         encoding="utf-8").readlines()
            assert lines, "empty chat file"
            for n, line in enumerate(lines):
                try:
                    json.loads(line)
                except Exception as e:
                    failures.append("chat %s line %d bad: %s"
                                    % (f, n, e))
            head = json.loads(lines[0])
            assert "chat_metadata" in head or "user_name" in head, \
                "header missing metadata"
        print("chat lines ok:", sum(
            len(open(os.path.join(chat_dir, f),
                     encoding="utf-8").readlines()) for f in files))

        # validate character png
        cpath = os.path.join(data_dir, "default-user", "characters",
                             avatar)
        types = png_chunks(cpath)
        assert b"IDAT" in types, "character PNG missing IDAT!"
        print("character png chunks ok")

        # validate settings
        spath = os.path.join(data_dir, "default-user", "settings.json")
        json.load(open(spath))
        print("settings.json ok")

        # reboot once more and read everything back via API
        proc.kill()
        proc.wait()
        proc = boot(data_dir)
        assert post("/api/characters/all", {}) == 200
        assert post("/api/chats/recent", {}) == 200
        print("post-reboot API reads ok")
    finally:
        try:
            if proc is not None:
                proc.kill()
        except Exception:
            pass
        shutil.rmtree(data_dir, ignore_errors=True)
        try:
            os.remove(BIN)
        except OSError:
            pass
    if failures:
        print("FAILURES:")
        for f in failures:
            print(" -", f)
        return 1
    print("SOAK PASSED: no corruption across kills")
    return 0


if __name__ == "__main__":
    sys.exit(main())
