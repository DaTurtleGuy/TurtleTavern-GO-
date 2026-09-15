# AGENTS.md — TurtleTavern (Go) — Knowledge Base

This file captures hard-won context so future sessions don't repeat mistakes.

## Project Overview

Go rewrite of SillyTavern's backend. Serves the **unchanged** SillyTavern frontend (HTML/JS/CSS) from `public/`. The frontend is NOT modified at runtime — it's served from disk. `data/` holds user data exclusively.

- Module: `github.com/TurtleTavern/turtletavern`
- Go 1.26, pure Go deps (chi, tiktoken, yaml.v3, modernc.org/sqlite)
- No git repo — local-only

## Node.js (TurtleTavern) Backup/Restore Parity (2026-09-14)

The Node fork has the same feature set, wire-compatible in both directions:
- `TurtleTavern/src/endpoints/userdata.js` — `GET /api/users/backup` (archiver streaming), `/backup/capabilities`, `POST /restore` (multer spool → yauzl validate → staging swap), `/restore/status`.
- Mounted in `server-startup.js`; the global multer in `server-main.js` was widened from `.single('avatar')` to `.fields([{name:'avatar'},{name:'file'}])` so the restore upload's `file` field survives the app-level parser (every upload in this fork flows through that one global multer).
- Frontend files copied verbatim from GoTavern's `public/`: `scripts/user-data.js`, `scripts/templates/userDataBackup.html`, `scripts/templates/userDataRestore.html`, `scripts/templates/userProfile.html`; Node `public/scripts/user.js` imports `user-data.js` (old `backupUserData` POST flow removed).
- Wire format constants (format `turtletavern-backup`, version 1, manifest field names, checksums.sha256 `hash  path` lines) MUST stay identical in both implementations.
- Node export quirk: checksum lines must be pre-computed (`hashFile`) before `archive.append` — pushing them from stream-flush callbacks races archiver's finalize and silently drops tail lines.
- Round-trip verified: Go→Node and Node→Go restores both returned `{ok, files:168, bytes:12558208}` byte-identical.
- Sandbox tests used throwaway instances: node `--dataRoot %TEMP%\...` on port 3999, throwaway Go on 3998 with a port-patched config copy + `default/` + `public`. Do NOT restore test archives into a live server — it atomically replaces the user's data root.
- Desktop `cmd/server` previously had `WriteTimeout: 60s` — killed streaming downloads ~1.5 GB in at ~50 MB/s link speed. Both `ReadTimeout`/`WriteTimeout` are now `0`; keep them 0.

## Android SQLite / Seccomp Pitfall (CONFIRMED 2026-09-14)

Android's **x86_64** app seccomp allowlist blocks the legacy stat syscall family
(`lstat`/`stat`/`fstat` = syscall 6/4/5). Crash signature on x86_64 emulators/AVDs:

```
Fatal signal 31 (SIGSYS), code 1 (SYS_SECCOMP), syscall 6
Cause: seccomp prevented call to disallowed x86_64 system call 6
```

- **arm64 has no `lstat` syscall** (uses `newfstatat`) → unaffected. Proven on a Galaxy A54.
- Switching to `mattn/go-sqlite3` (real C SQLite via CGO) does **NOT** fix x86_64 —
  Bionic still issues `lstat`. Verified by symbol-inspecting `libgojni.so`
  (`sqlite3_open_v2` present, modernc absent — still crashes).
- **Conclusion: the app is arm64-only.** Enforced two ways:
  1. `gomobile bind -target=android/arm64` (no amd64 in the AAR)
  2. `ndk { abiFilters += "arm64-v8a" }` in `TurtleTavern_Mobile/app/build.gradle.kts`
- `MainActivity` shows an "Arch not supported" error if launched on non-arm64.
- **arm64 AVD on an x86_64 Windows host is IMPOSSIBLE**: emulator panics
  (`Avd's CPU Architecture 'arm64' is not supported by the QEMU2 emulator on x86_64 host`).
  x86_64 AVDs boot but the app dies. Only real arm64 devices can test this app.

### SQLite driver split (build tags)

- `internal/character/driver_android.go` (`//go:build android`) → `mattn/go-sqlite3`, `CGO_ENABLED=1`
- `internal/character/driver_desktop.go` (`//go:build !android`) → `modernc.org/sqlite`, `CGO_ENABLED=0`
- **CGO is broken on the Windows host even for desktop builds** (TDM64 gcc produces a
  PE file Windows refuses to run: `WinError 193`). Desktop must stay `CGO_ENABLED=0`.
- Android builds need CGO anyway (DNS + TLS system roots), see the Termux section.

## Android WebView Shell Gotchas (TurtleTavern_Mobile)

- **Edge-to-edge is enforced for targetSdk 35+**. The system bars draw over the app
  unless insets are applied manually.
- **`DrawerLayout` ignores `setPadding()` during layout** — inset padding must go on the
  plain content `FrameLayout` (and the drawer panel), NOT the DrawerLayout root.
  WebView then reports the true `window.innerHeight` to the frontend (verified via
  `window.innerHeight` = padded view height).
- ST's viewport meta uses `viewport-fit=cover`; if the WebView is full-screen under
  system bars, ST's top toolbar scrolls off-screen and the type box hides under the
  nav bar. Padding the content frame fixes both.
- `onPageFinished` never fires reliably (SSE streams keep the page "loading") — do not
  use it for post-load JS injection.
- In-app drawer (swipe from left edge): Config editor (`filesDir/config.yaml`), Logs,
  Restart (`Gotavern.stop()` → `start()`, port may change → reload WebView URL).
  Server logs are teed into a 256 KiB ring buffer via `gotavern.Logs()`
  (`ringWriter` chains to the original `log.Writer()` so gomobile's logcat
  forwarding still works).
- **`android:launchMode="singleTask"` is load-bearing.** Under the default `standard`,
  every tap on the launcher icon created ANOTHER MainActivity instead of resuming the
  existing one — 5 stacked instances, each with its own WebView — so returning to the
  app reloaded the whole page and discarded in-flight streams and JS state. Nothing was
  destroying them: no `onDestroy` in 9 h of logs, `always_finish_activities=null`, no
  `onRenderProcessGone`. Diagnose with `dumpsys activity activities | grep "Task{.*<pkg>"`
  (`sz=` must stay 1) and check that `Logging initialized` / `Server ready on port` do not
  repeat — both only run from `onCreate`. `onNewIntent` logs, which proves the instance
  was reused. The wake lock / foreground service keeps the PROCESS (and the Go server)
  alive; it has no bearing on whether the ACTIVITY is reused.
- A dead WebView (`onRenderProcessGone`) is never reusable, and rebuilding it while the
  activity is hidden reloads the page off-screen; recovery is deferred to `onStart`.

## Background Generation — ROOT CAUSE & LOG CONVENTIONS (2026-09-15)

Symptom: leaving the app mid-generation killed it, and the access log showed
`POST /api/backends/chat-completions/generate -> 502`.

**The wake lock was never actually held.** `MainActivity.onCreate` restored the
switch with `keepAliveSwitch.isChecked = prefs.getBoolean(...)` *before* attaching
`setOnCheckedChangeListener`, so the restored value never fired the listener: the
drawer showed ON, `KeepAliveService` was never started, and no
`PARTIAL_WAKE_LOCK` existed. Proved with `adb shell dumpsys activity services`
(no KeepAliveService) + `dumpsys power` (no `turtletavern::server` lock) while
the pref read `keep_alive = true`. Without a foreground service the app is a
cached process and Android reaps its sockets mid-stream.

- `applyKeepAlive()` is now re-applied in `onStart()` from the pref. **Do not
  delete that line as redundant — it is the fix.** Verified: `applyKeepAlive(true)`
  → `KeepAlive service starting` → `PARTIAL_WAKE_LOCK acquired` → a generation
  logged `-> 200 (29.45s)` that ran *entirely* while the app was backgrounded.
- `webView.setRendererPriorityPolicy(RENDERER_PRIORITY_IMPORTANT, false)` keeps the
  invisible renderer from being waived/frozen. It is an **instance** method, not
  static. `setRendererPriorityPolicy` does not appear in Kotlin completion — check
  the SDK jar with `javap` before trusting memory here.

**A 502 is not automatically a provider error.** Read the `[llm]` line next to it:
- `[llm] ... aborted: the caller went away (context canceled)` + `-> 499`:
  the frontend aborted the fetch itself (swipe/stop). Expected, not a failure.
- `[llm] ... failed: <error>`: real outbound failure (DNS/TCP/TLS).
- `[llm] <host> returned <status>: <body>`: the provider rejected it, body included.

Upstream failures used to be logged only when `Debug` was on, so every 502 was
invisible and the error body was thrown away. Keep `logUpstreamFailure`, the
`writeUpstreamFailure` 499 branch, and the `ForwardStream` >=400 branch.

`gomobile` calls `log.SetFlags(0)`, so server lines reached the in-app viewer with
no timestamp and could not be correlated with the app log. `gotavern.Start`
restores `LstdFlags|Lmicroseconds`.

## Chat dates: last-used and created (2026-09-15)

Two dates drive the character/group lists, and each was wrong in a different way. These
rules are load-bearing; each one was measured against a real 1,050-character restored
library before it was written down.

- **`send_date` has two shapes.** Older SillyTavern wrote a human-readable string
  (`"February 25, 2025 3:46pm"`); newer versions write epoch ms. `parseSendDate` accepts
  both, plus ISO timestamps and epoch seconds. A parser that only accepts numbers matches
  **nothing** in an old library — it matched 0 of 4,227 chat files, so recency silently
  fell back to file mtime. Unit tests in `internal/character/recency_test.go` pin the real
  string formats; do not "simplify" them to numbers.
- **File mtime is never recency or creation evidence.** A restore, import or copy rewrites
  it to the copy time. It fabricated 1,007 identical values in that library and outranked
  genuine dates for 18 more.
- **A card or group cannot predate its own oldest message.** The recompute clamps creation
  dates to the oldest message (`ChatOldestSendDate` / `ChatFileFirstSendDate`), writing the
  card and `date_added.json`. It must stay idempotent: a second run must report 0 changes.
- **`date_last_chat` / `chat_size` must not use `omitempty`.** A 0 was omitted from the
  JSON, the frontend compared `undefined`, and `undefined - number` produced an
  inconsistent comparator — V8 scattered ~1,000 characters into arbitrary order.
- **Group files written by Node-ST are not type-compatible.** Numeric `id`, boolean
  `activation_strategy`, numeric `chats` entries: a strict decode rejects the record and the
  group disappears from the API entirely (43 of 155 were missing). `GroupData.UnmarshalJSON`
  coerces every field leniently — keep it that way.
- **Listings must never write files.** Group creation dates used to be backfilled from the
  group file's mtime *inside the GET handler*, stamping old groups with the restore time.
  Repairs belong to the recompute action only.
- The recompute button asks for confirmation before it starts (it rewrites cards and group
  files), then shows the progress modal. Both are intentional; don't drop the confirm.

## Android / Termux Build (CRITICAL)

### The DNS Pitfall

`CGO_ENABLED=0` + `GOOS=android` → pure Go DNS resolver → **fails on Android** because Android has no `/etc/resolv.conf`. All outbound HTTP calls silently fail DNS.

**Fix:** build with `CGO_ENABLED=1` and the Android NDK clang. This makes Go use Android's Bionic `getaddrinfo` which works.

```bash
# NDK location (Windows):
# C:\Users\<user>\AppData\Local\Android\Sdk\ndk\<version>\toolchains\llvm\prebuilt\windows-x86_64\bin\aarch64-linux-android<api>-clang.cmd

# Correct build command:
CGO_ENABLED=1 GOOS=android GOARCH=arm64 \
  CC="<ndk-path>/bin/aarch64-linux-android21-clang.cmd" \
  go build -trimpath -ldflags="-s -w" -o gotavern-termux-arm64 ./cmd/server
```

- Binary is ~36 MB (vs ~25 MB pure-Go) due to Bionic linkage
- `GOOS=android` not `GOOS=linux` — the NDK targets Android API level, not generic Linux
- The NDK must be installed: `sdkmanager "ndk;30.0.15729638"` or via Android Studio

### TLS Certificates

Pure-Go TLS (`CGO_ENABLED=0`) also fails on Android — no system CA roots. CGO fixes this too (Bionic provides them).

### Termux Transfer

```bash
# adb push to device, then in Termux:
chmod +x gotavern-termux-arm64
./gotavern-termux-arm64
```

Needs `public/` and `config.yaml` alongside the binary.

## Character Image Bug (Fixed 2026-09-14)

**Root cause:** `EditAttribute`, `MergeAttributes`, and `Rename` handlers passed `nil` as the image data to `WriteCharacterDataToFile`. The function falls back to `DefaultAvatarPNG` when `inputImage == nil`:

```go
// process.go:173
func WriteCharacterDataToFile(inputImage []byte, ...) error {
    if inputImage == nil {
        inputImage = DefaultAvatarPNG  // ← replaces character's image!
    }
    ...
}
```

**Fix:** Read the existing PNG bytes before writing:
```go
imgBytes, _ := os.ReadFile(charPath)
character.WriteCharacterDataToFile(imgBytes, string(charJSON), targetFile, uc.Directories)
```

**Lesson:** Any call to `WriteCharacterDataToFile` with `nil` as the first argument will destroy the character's image. Always read the existing file first. The only valid use of `nil` / `DefaultAvatarPNG` is when creating a **new** character from external import (CharX, YAML, BYAF) where no source image exists.

## Frontend Caching (Fixed 2026-09-14)

`index.html` loads JS modules with no cache-busting and the server had no `Cache-Control` headers. Browsers aggressively cache ES modules — users see stale frontend after code changes.

**Fix:** `setStaticCachePolicy` in `cmd/server/main.go` adds `Cache-Control: no-cache` for `.js`, `.mjs`, `.html`, `.css`, `.json` files. Browser revalidates (304 when unchanged), picks up changes immediately.

**User impact:** First load after update requires a hard refresh (Ctrl+Shift+R). After that, changes propagate automatically.

## Backup/Restore Feature

### Export (GET /api/users/backup)

- Streams zip directly to response (constant memory, no temp file)
- Appends `manifest.json` + `checksums.sha256` at the end (single pass)
- `includeBackups` opt-in (skips `backups/` dir by default)
- `includeKeys` → 400 unless `allowKeysExposure` is true in config
- Optional `handle` param for admins backing up other users
- `Cache-Control: no-cache` for download (prevents stale zip)

### Restore (POST /api/users/restore)

- Uses `r.MultipartReader()` (never `ParseMultipartForm`) — streams to temp file
- Validates: manifest format/version, zip-slip rejection, per-file SHA-256, decompression bomb cap (1 TiB)
- Extracts to staging dir, atomic rename swap with rollback
- `maintenance.Begin()`/`End()` gates `/api` with 503 during swap
- Clears character index after restore

### Frontend

- Export: native `<a download>` GET (browser streams to disk, no blob/RAM)
- Restore: `XMLHttpRequest` with `xhr.upload.onprogress` for upload progress
- Progress overlay lives in `document.body` (not inside the popup, which closes)
- File picker uses `<label for="input">` pattern (ST globally hides `input[type=file]`)
- `userDataBackup.html` has "Include chat backups" (opt-in) and "Include API keys" (disabled when `allowKeysExposure` false)

## Routing Gotchas

- Chi returns 405 when a path exists but the method doesn't (not 404)
- Old `POST /api/users/backup` was removed; new route is `GET` — old frontend code POSTing gets 405
- Static catch-all `r.Get("/*", ...)` serves `public/` — specific routes registered earlier take priority
- `#account_button` in the User Settings drawer opens the profile popup (where backup/restore buttons live)

## Build Commands

```bash
# Windows x64
go build -o gotavern.exe ./cmd/server

# Termux ARM64 (CRITICAL: CGO_ENABLED=1)
CGO_ENABLED=1 GOOS=android GOARCH=arm64 \
  CC="<ndk>/bin/aarch64-linux-android21-clang.cmd" \
  go build -trimpath -ldflags="-s -w" -o gotavern-termux-arm64 ./cmd/server

# Verify (always run after changes)
go build ./...
go vet ./...
python verify/endpoints.py        # 46/46
python verify/backup_restore.py   # 21/21
```

## Key Files

| File | Purpose |
|---|---|
| `cmd/server/main.go` | Entry point, router, middleware |
| `internal/handlers/userdata.go` | Backup export/restore (new) |
| `internal/handlers/characters.go` | Character CRUD (edit, rename, avatar) |
| `internal/character/process.go` | `WriteCharacterDataToFile` (image preservation) |
| `internal/character/png.go` | PNG chunk manipulation, `DefaultAvatarPNG` |
| `internal/maintenance/maintenance.go` | 503 gate during restore swap |
| `internal/llm/proxy.go` | `NewHTTPClient` (outbound HTTP) |
| `verify/endpoints.py` | Full endpoint regression suite |
| `verify/backup_restore.py` | Backup/restore round-trip test |
| `public/scripts/user-data.js` | Frontend backup/restore logic |
