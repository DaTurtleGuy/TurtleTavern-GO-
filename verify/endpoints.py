#!/usr/bin/env python3
"""Endpoint verification for GoTavern. Replaces verify/endpoints.ps1.

Builds the server + mock LLM, boots both on 127.0.0.1:18080/:18081 with a
fresh temp data dir, runs the check matrix, prints PASS/FAIL, tears down.
Unlike the ps1 version, server logs are captured and readiness is polled
instead of a fixed sleep, so boot failures are diagnosable.

Stdlib only. Exit code 1 if any check fails.
"""
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

BASE = "http://127.0.0.1:18080"
MOCK = "http://127.0.0.1:18081"
GOTAVERN_DIR = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
EXE = ".exe" if os.name == "nt" else ""
SERVER_BIN = os.path.join(GOTAVERN_DIR, "verify-server" + EXE)
MOCK_BIN = os.path.join(GOTAVERN_DIR, "verify-mock" + EXE)
PUBLIC_DIR = os.path.join(GOTAVERN_DIR, "public")
LIB_FIXTURE_DIR = os.path.join(PUBLIC_DIR, "webpack", "verify-fixture")


class Client:
    def __init__(self, base):
        self.base = base
        self.jar = http.cookiejar.CookieJar()
        self.token = None
        self.opener = urllib.request.build_opener(
            urllib.request.HTTPCookieProcessor(self.jar))
        # Opener that does NOT follow redirects (for the 308 check).
        self.no_redirect = urllib.request.build_opener(
            urllib.request.HTTPCookieProcessor(self.jar),
            urllib.request.HTTPHandler(),
            urllib.request.HTTPSHandler(),
            urllib.request.HTTPErrorProcessor(),
        )

    def req(self, method, path, body="__none__", follow=True):
        data = None
        headers = {}
        if self.token:
            headers["X-CSRF-Token"] = self.token
        if body != "__none__":
            if isinstance(body, (dict, list)):
                data = json.dumps(body).encode()
                headers["Content-Type"] = "application/json"
            elif isinstance(body, str):
                data = body.encode()
                headers["Content-Type"] = "application/json"
            elif isinstance(body, bytes):
                data = body
            # None body with explicit POST: empty body, no content-type
        r = urllib.request.Request(self.base + path, data=data,
                                   headers=headers, method=method)
        opener = self.opener if follow else self.no_redirect
        try:
            with opener.open(r, timeout=20) as resp:
                code = resp.status
                text = resp.read().decode("utf-8", "replace")
        except urllib.error.HTTPError as e:
            code = e.code
            try:
                text = e.read().decode("utf-8", "replace")
            except Exception:
                text = ""
        return code, text

    def req_raw(self, method, path):
        headers = {}
        if self.token:
            headers["X-CSRF-Token"] = self.token
        r = urllib.request.Request(self.base + path, headers=headers,
                                   method=method)
        try:
            with self.opener.open(r, timeout=20) as resp:
                return resp.status, resp.read()
        except urllib.error.HTTPError as e:
            try:
                return e.code, e.read()
            except Exception:
                return e.code, b""
        except Exception:
            return -1, b""

    def req_multipart(self, method, path, fields):
        boundary = "----verifyboundary1234"
        parts = []
        for k, v in fields.items():
            parts += ["--" + boundary,
                      'Content-Disposition: form-data; name="%s"' % k,
                      "", str(v)]
        parts += ["--" + boundary + "--", ""]
        data = "\r\n".join(parts).encode()
        headers = {"Content-Type":
                   "multipart/form-data; boundary=" + boundary}
        if self.token:
            headers["X-CSRF-Token"] = self.token
        r = urllib.request.Request(self.base + path, data=data,
                                   headers=headers, method=method)
        try:
            with self.opener.open(r, timeout=20) as resp:
                return (resp.status,
                        resp.read().decode("utf-8", "replace"))
        except urllib.error.HTTPError as e:
            try:
                text = e.read().decode("utf-8", "replace")
            except Exception:
                text = ""
            return (e.code, text)


def run(cmd, **kw):
    p = subprocess.run(cmd, cwd=GOTAVERN_DIR, capture_output=True,
                       text=True, **kw)
    return p


def teardown(server_proc, mock_proc, data_dir, cleanup_bins):
    for p in (server_proc, mock_proc):
        if p is not None:
            try:
                p.terminate()
            except Exception:
                pass
    for p in (server_proc, mock_proc):
        if p is not None:
            try:
                p.wait(timeout=5)
            except Exception:
                try:
                    p.kill()
                except Exception:
                    pass
    if data_dir:
        shutil.rmtree(data_dir, ignore_errors=True)
    shutil.rmtree(LIB_FIXTURE_DIR, ignore_errors=True)
    if cleanup_bins:
        for b in (SERVER_BIN, MOCK_BIN):
            try:
                os.remove(b)
            except OSError:
                pass


def main():
    skip_server = "--skip-server" in sys.argv
    base = BASE
    for a in sys.argv[1:]:
        if a.startswith("--base="):
            base = a.split("=", 1)[1]

    server_proc = mock_proc = None
    data_dir = None
    log_dir = tempfile.mkdtemp(prefix="tt-verify-logs-")
    server_log = os.path.join(log_dir, "server.log")
    mock_log = os.path.join(log_dir, "mock.log")

    if not skip_server:
        print("Building server and mock...")
        p = run(["go", "build", "-o", SERVER_BIN, "./cmd/server"])
        if p.returncode != 0:
            print("BUILD FAILED\n" + p.stderr)
            return 1
        p = run(["go", "build", "-o", MOCK_BIN, "./verify/mock"])
        if p.returncode != 0:
            print("MOCK BUILD FAILED\n" + p.stderr)
            return 1

        data_dir = tempfile.mkdtemp(prefix="tt-verify-data-")
        # Clear orphans from previously crashed runs holding the ports.
        if os.name == "nt":
            for img in ("verify-mock.exe", "verify-server.exe"):
                subprocess.run(["taskkill", "/F", "/IM", img],
                               capture_output=True)
            time.sleep(0.5)
        # Seed a fake webpack bundle so /lib.js serves 200 like a real install.
        # Newest mtime wins, so this fixture shadows the real public/webpack bundle.
        bundle_dir = os.path.join(LIB_FIXTURE_DIR, "output")
        os.makedirs(bundle_dir, exist_ok=True)
        with open(os.path.join(bundle_dir, "lib.js"), "w") as f:
            f.write("/* verify fixture */\n")

        slog = open(server_log, "w")
        mlog = open(mock_log, "w")
        mock_proc = subprocess.Popen([MOCK_BIN, "-port", "18081"],
                                     stdout=mlog, stderr=subprocess.STDOUT)
        server_proc = subprocess.Popen(
            [SERVER_BIN, "--disableCsrf", "-port", "18080",
             "-dataRoot", data_dir],
            cwd=GOTAVERN_DIR, stdout=slog, stderr=subprocess.STDOUT)

        # Poll for readiness instead of a blind sleep.
        c = Client(base)
        ready = False
        for _ in range(60):
            if server_proc.poll() is not None:
                break
            try:
                code, _ = c.req("GET", "/version")
                if code == 200:
                    ready = True
                    break
            except Exception:
                pass
            time.sleep(0.5)
        if not ready:
            print("SERVER FAILED TO BOOT")
            for path in (server_log, mock_log):
                print(f"--- {os.path.basename(path)} ---")
                try:
                    with open(path) as f:
                        print(f.read()[-4000:])
                except OSError:
                    print("(no log)")
            teardown(server_proc, mock_proc, data_dir, cleanup_bins=True)
            return 1
        print("Server up.")

    c = Client(base)
    results = []

    def check(name, fn):
        try:
            ok = fn() is True
        except Exception as e:
            print(f"  ! {name} raised {e!r}")
            ok = False
        results.append((name, ok))
        print(("PASS  " if ok else "FAIL  ") + name)

    def jget(code_body, *keys):
        code, text = code_body
        try:
            j = json.loads(text)
        except Exception:
            return None
        for k in keys:
            j = j.get(k) if isinstance(j, dict) else None
        return j

    check("GET /version shape", lambda: (
        (lambda r: r[0] == 200 and '"pkgVersion"' in r[1] and '"agent"' in r[1])
        (c.req("GET", "/version"))))

    def get_token():
        code, text = c.req("GET", "/csrf-token")
        if code != 200:
            return False
        try:
            tok = json.loads(text)["token"]
        except Exception:
            return False
        if tok == "disabled":
            c.token = None
            return True
        c.token = tok
        return len(tok) > 10
    check("GET /csrf-token", get_token)

    check("POST /api/ping -> 204",
          lambda: c.req("POST", "/api/ping")[0] == 204)
    check("POST /api/users/list", lambda: (
        (lambda r: r[0] == 200 and "default-user" in r[1])
        (c.req("POST", "/api/users/list", {}))))
    check("GET /api/users/me", lambda: (
        (lambda r: r[0] == 200 and "default-user" in r[1])
        (c.req("GET", "/api/users/me"))))

    def admin_delete_rejects_traversal():
        code, _ = c.req("POST", "/api/users/delete",
                        {"handle": "../../evil", "purge": True})
        return code == 400
    check("POST /api/users/delete rejects traversal handle",
          admin_delete_rejects_traversal)
    def settings_cycle():
        code, _ = c.req("POST", "/api/settings/save", {"a": 1})
        if code != 200:
            return False
        code, text = c.req("POST", "/api/settings/get", {})
        return code == 200 and '"settings"' in text
    check("POST /api/settings/save then get", settings_cycle)
    check("POST /api/settings/save + reload", lambda: (
        (lambda r: r[0] == 200 and "ok" in r[1])
        (c.req("POST", "/api/settings/save", {"a": 1}))))
    check("POST /api/secrets/read",
          lambda: c.req("POST", "/api/secrets/read", {})[0] == 200)

    def secrets_cycle():
        code, text = c.req("POST", "/api/secrets/write",
                           {"key": "api_key_openai", "value": "sk-test",
                            "label": "t"})
        if code != 200:
            return False
        try:
            sid = json.loads(text)["id"]
        except Exception:
            return False
        code, _ = c.req("POST", "/api/secrets/delete",
                        {"key": "api_key_openai", "id": sid})
        return code == 204
    check("POST /api/secrets/write+find+delete", secrets_cycle)

    check("POST /api/characters/all (empty)",
          lambda: c.req("POST", "/api/characters/all", {})[0] == 200)

    def char_cycle():
        fields = {"ch_name": "TestChar", "description": "d",
                  "personality": "p", "scenario": "s", "first_mes": "hi",
                  "mes_example": "", "creator_notes": "",
                  "system_prompt": "", "post_history_instructions": "",
                  "tags": "[]", "creator": "t", "character_version": "",
                  "alternate_greetings": "[]", "extensions": "{}"}
        code, text = c.req_multipart("POST", "/api/characters/create",
                                     fields)
        if code not in (200, 201) or "TestChar" not in text:
            return False
        avatar = text.strip()
        code, _ = c.req("POST", "/api/characters/get",
                        {"avatar_url": avatar})
        if code != 200:
            return False
        code, _ = c.req("POST", "/api/characters/delete",
                        {"avatar_url": avatar})
        return code == 200
    check("POST /api/characters/create+get+delete", char_cycle)

    check("POST /api/chats/recent",
          lambda: c.req("POST", "/api/chats/recent", {})[0] == 200)

    def chat_cycle():
        chat = [{"name": "User", "is_user": True, "mes": "hello",
                 "send_date": 1}]
        code, _ = c.req("POST", "/api/chats/save",
                        {"avatar_url": "TestChar.png", "chat_name": "t",
                         "chat": chat})
        if code not in (200, 204):
            return False
        code, _ = c.req("POST", "/api/chats/get",
                        {"avatar_url": "TestChar.png",
                         "chat_name": "t.jsonl"})
        return code == 200
    check("POST /api/chats/save+get+delete", chat_cycle)

    def group_cycle():
        code, _ = c.req("POST", "/api/groups/all", {})
        if code != 200:
            return False
        code, text = c.req("POST", "/api/groups/create", {"name": "g1"})
        if code != 200:
            return False
        try:
            gid = json.loads(text)["id"]
        except Exception:
            return False
        code, _ = c.req("POST", "/api/groups/delete", {"id": gid})
        return code == 200
    check("POST /api/groups/all+create+delete", group_cycle)

    def wi_cycle():
        code, _ = c.req("POST", "/api/worldinfo/edit",
                        {"name": "w1", "data": {"entries": {}}})
        if code != 200:
            return False
        code, _ = c.req("POST", "/api/worldinfo/get", {"name": "w1"})
        if code != 200:
            return False
        code, _ = c.req("POST", "/api/worldinfo/delete", {"name": "w1"})
        return code == 200
    check("POST /api/worldinfo/list+edit+get+delete", wi_cycle)

    def preset_cycle():
        code, _ = c.req("POST", "/api/presets/save",
                        {"apiId": "openai", "name": "p1",
                         "preset": {"a": 1}})
        if code != 200:
            return False
        code, _ = c.req("POST", "/api/presets/delete",
                        {"apiId": "openai", "name": "p1"})
        return code == 200
    check("POST /api/presets/save+delete", preset_cycle)

    def theme_cycle():
        code, _ = c.req("POST", "/api/themes/save", {"name": "t1"})
        if code != 200:
            return False
        code, _ = c.req("POST", "/api/themes/delete", {"name": "t1"})
        return code == 200
    check("POST /api/themes/save+delete", theme_cycle)

    def moving_qr():
        a = c.req("POST", "/api/moving-ui/save", {"name": "m1"})[0]
        b = c.req("POST", "/api/quick-replies/save", {"name": "q1"})[0]
        d = c.req("POST", "/api/quick-replies/delete", {"name": "q1"})[0]
        return a == 200 and b == 200 and d == 200
    check("POST /api/moving-ui/save + quick-replies", moving_qr)

    def stats_cycle():
        g = c.req("POST", "/api/stats/get", {})[0]
        u = c.req("POST", "/api/stats/update", {"x": 1})[0]
        return g == 200 and u == 200
    check("POST /api/stats/get+update", stats_cycle)

    check("POST /api/backgrounds/all", lambda: (
        (lambda r: r[0] == 200 and '"images"' in r[1])
        (c.req("POST", "/api/backgrounds/all", {}))))
    check("POST /api/avatars/get",
          lambda: c.req("POST", "/api/avatars/get", {})[0] == 200)

    def folders_files():
        a = c.req("POST", "/api/images/folders", {})[0]
        b = c.req("POST", "/api/files/verify", {"urls": []})[0]
        return a == 200 and b == 200
    check("POST /api/images/folders + files/verify", folders_files)

    check("POST /api/files/sanitize-filename", lambda: (
        (lambda r: r[0] == 200 and "fileName" in r[1])
        (c.req("POST", "/api/files/sanitize-filename",
               {"fileName": "a:b"}))))

    def assets_cycle():
        a = c.req("POST", "/api/assets/get", {})[0]
        b = c.req("POST", "/api/assets/character?name=x&category=bgm",
                  {})[0]
        return a == 200 and b == 200
    check("POST /api/assets/get + character", assets_cycle)

    check("GET /api/sprites/get",
          lambda: c.req("GET", "/api/sprites/get?name=x")[0] == 200)
    check("GET /thumbnail 404 for missing",
          lambda: c.req("GET",
                        "/thumbnail/?file=nope.png&type=bg")[0] == 404)

    def thumbnail_has_image_content():
        code, data = c.req_raw("GET",
                               "/thumbnail?type=avatar"
                               "&file=default_Seraphina.png")
        if code != 200:
            return False
        is_png = data[:8] == b"\x89PNG\r\n\x1a\n"
        is_jpg = data[:2] == b"\xff\xd8"
        if not (is_png or is_jpg):
            return False
        # Blank/uniform output compresses to a few hundred bytes whether
        # PNG or JPEG; real 96x144 artwork lands well above this.
        return len(data) > 2000
    check("GET /thumbnail scales real image (not blank)",
          thumbnail_has_image_content)

    def meta_cycle():
        a = c.req("POST", "/api/image-metadata/all", {})[0]
        b = c.req("POST", "/api/image-metadata/cleanup", {})[0]
        return a == 200 and b == 200
    check("POST /api/image-metadata/all + cleanup", meta_cycle)

    def meta_folder():
        code, text = c.req("POST", "/api/image-metadata/folders/create",
                           {"name": "f1"})
        if code != 200:
            return False
        try:
            fid = json.loads(text)["id"]
        except Exception:
            return False
        code, _ = c.req("POST", "/api/image-metadata/folders/delete",
                        {"id": fid})
        return code == 200
    check("POST /api/image-metadata/folders/get+create+delete",
          meta_folder)

    check("GET /api/extensions/discover",
          lambda: c.req("GET", "/api/extensions/discover")[0] == 200)

    def vector_cycle():
        a = c.req("POST", "/api/vector/purge-all", {})[0]
        b = c.req("POST", "/api/vector/list",
                  {"collectionId": "c1"})[0]
        return a == 200 and b == 200
    check("POST /api/vector/purge-all + list", vector_cycle)

    check("POST /api/tokenizers/remote 400s",
          lambda: c.req("POST", "/api/tokenizers/remote/kobold/count",
                        None)[0] == 400)
    check("POST /api/translate 400s",
          lambda: c.req("POST", "/api/translate/deepl",
                        {"text": "", "lang": ""})[0] == 400)

    def search_400():
        a = c.req("POST", "/api/search/serpapi", {})[0]
        b = c.req("POST", "/api/search/visit", {"url": "ftp://x"})[0]
        return a == 400 and b == 400
    check("POST /api/search 400s", search_400)

    check("Deprecated redirect /savechat -> 308",
          lambda: c.req("POST", "/savechat", {},
                        follow=False)[0] == 308)
    check("Stub 501 /api/sd/generate",
          lambda: c.req("POST", "/api/sd/generate", {})[0] == 501)
    check("Stub 501 /api/speech/synthesize",
          lambda: c.req("POST", "/api/speech/synthesize",
                        {})[0] == 501)
    check("GET /lib.js bundle", lambda: (
        (lambda r: r[0] == 200 and "verify fixture" in r[1])
        (c.req("GET", "/lib.js"))))

    if not skip_server:
        mock = MOCK

        def llm_nonstream():
            code, text = c.req(
                "POST", "/api/backends/chat-completions/generate",
                {"chat_completion_source": "openai",
                 "reverse_proxy": mock, "model": "mock-model",
                 "messages": [{"role": "user", "content": "hi"}],
                 "stream": False})
            return code == 200 and '"Hi"' in text
        check("LLM chat generate (mock, non-stream)", llm_nonstream)

        def llm_stream():
            code, text = c.req(
                "POST", "/api/backends/chat-completions/generate",
                {"chat_completion_source": "openai",
                 "reverse_proxy": mock, "model": "mock-model",
                 "messages": [{"role": "user", "content": "hi"}],
                 "stream": True})
            return code == 200 and "Hi" in text and "[DONE]" in text
        check("LLM chat generate (mock, stream)", llm_stream)

        def llm_status():
            code, text = c.req(
                "POST", "/api/backends/chat-completions/status",
                {"chat_completion_source": "openai",
                 "reverse_proxy": mock})
            return code == 200 and "mock-model" in text
        check("LLM chat status (mock)", llm_status)

        def llm_text():
            code, text = c.req(
                "POST", "/api/backends/text-completions/generate",
                {"api_type": "generic", "api_server": mock,
                 "prompt": "hi", "stream": False, "model": "mock"})
            return code == 200 and "Hi" in text
        check("LLM text generate generic (mock)", llm_text)

        check("LLM kobold status (unreachable -> shape)", lambda: (
            (lambda r: r[0] == 200 and "no_connection" in r[1])
            (c.req("POST", "/api/backends/kobold/status",
                   {"api_server": "http://127.0.0.1:19"}))))

        check("LLM bias passthrough", lambda: (
            (lambda r: r[0] == 200 and '"1"' in r[1])
            (c.req("POST",
                   "/api/backends/chat-completions/bias?model=gpt",
                   [{"text": "[1,2]", "value": 5}]))))

        check("LLM process strict", lambda: (
            (lambda r: r[0] == 200 and '"messages"' in r[1])
            (c.req("POST", "/api/backends/chat-completions/process",
                   {"messages": [{"role": "user", "content": "hi"}],
                    "type": "strict"}))))

    passed = sum(1 for _, ok in results if ok)
    total = len(results)
    print(f"\n{passed}/{total} passed")
    if passed != total:
        print("Server log tail (last 60 lines):")
        try:
            with open(server_log) as f:
                lines = f.read().splitlines()
            print("\n".join(lines[-60:]))
        except OSError:
            print("(no server log)")

    if not skip_server:
        teardown(server_proc, mock_proc, data_dir, cleanup_bins=True)
    try:
        shutil.rmtree(log_dir, ignore_errors=True)
    except OSError:
        pass
    return 0 if passed == total else 1


if __name__ == "__main__":
    sys.exit(main())
