# TurtleTavern (GO)

A pure-Go backend for SillyTavern-style web frontends: it serves the unchanged
SillyTavern frontend and reimplements the server pieces the Node version used
(settings, chats, characters, secrets, per-user backups and restore).

Origin: the Node-based TurtleTavern fork — https://github.com/DaTurtleGuy/TurtleTavern

## How this was built

Genuinely vibe-coded. Built with OpenCode — orchestrating multiple models as my
whims dictated: GLM 5.3 Flash, DeepSeek V4.1 Flash, Mimo 2.5, among others.
Expect asymmetric polish: some corners are hardened by regression suites,
others survived by being touched fewer times. `AGENTS.md` is the scar-tissue
knowledge base — when something breaks, check there first.



## Layout

- `internal/`  — server internals (router/handlers, character index, LLM proxy…)
- `public/`    — the web frontend (unmodified at runtime)
- `default/`   — default settings seed
- `tools/`     — packaging helpers (mobile asset packing)
- `verify/`    — Python regression suites
- `config.yaml` — server settings

## Building

```bash
# Windows x64 (pure Go)
go build -ldflags "-s -w" -o gotavern.exe ./cmd/server

# Termux / Android arm64 — CGO is REQUIRED (DNS + TLS resolution on Android)
CGO_ENABLED=1 GOOS=android GOARCH=arm64 \
  CC="<ndk>/toolchains/llvm/prebuilt/windows-x86_64/bin/aarch64-linux-android21-clang.cmd" \
  go build -trimpath -ldflags="-s -w" -o gotavern ./cmd/server
```

## Android app

The mobile app wraps this server in-process (gomobile AAR) — see the
TurtleTavern Android repo for the app shell and releases.

## Migrating from the Node version

See `public/MIGRATION.md` — export a user-data backup in the Node server and
restore it in the Go server; archive formats are wire-compatible in both
directions. After the first restore, the settings, chats, characters and
extensions live under `data/<handle>/`.

## Benchmarks (grain of salt included)

I pointed all three servers at the same generated character library and measured boot,
memory, list latency, per-character latency, and concurrency, one server at a time:
upstream SillyTavern 1.19, TurtleTavern (Node), and this one. The character library is 5,000
generated characters (~2 MB each, mostly a 150-entry lorebook) plus a ~20 MB monster
every 50th character, tested at 100 / 1,000 / 5,000 characters.

Fair warning: the harness was vibe-coded in an afternoon, one run per cell,
synthetic characters, and upstream is 1.19 while TurtleTavern (Node) is 1.17-based, so some of
the TurtleTavern (Node)-vs-upstream gap is version drift. Don't quote these numbers at anyone.
The shapes are real, though:

"Cold list" is the first list request against a fresh character library — the server has
to read and parse every character to build its index, so this is the price of
opening a big character library for the first time. "Warm list" is every request after
that, served straight from the built index. Same split for boot: first boot
vs. the average of five restarts on the same data.

| 100 characters | Upstream 1.19 | TurtleTavern (Node) | Go |
|---|---|---|---|
| Boot (first → warm avg) | 13.7s → 3.6s | 11.5s → 3.1s | **0.6s → 0.6s** |
| Cold list | 13.8s | 12.4s | **2.0s** |
| Warm list | 1.78s | 0.026s | **0.016s** |
| Peak RSS | ~3–4GB | 1.5GB | **0.2GB** |

| 1,000 characters | Upstream 1.19 | TurtleTavern (Node) | Go |
|---|---|---|---|
| Boot (first → warm avg) | 14.7s → 4.1s | 12.0s → 3.2s | **0.6s → 0.6s** |
| Cold list | HTTP 500 after 111s | 124s | **25.5s** |
| Warm list | HTTP 500, always | 0.079s | **0.045s** |
| Peak RSS | 4.4GB | 1.9GB | **0.3GB** |

| 5,000 characters | Upstream 1.19 | TurtleTavern (Node) | Go |
|---|---|---|---|
| Boot (first → warm avg) | — (see below) | 16.2s → 3.6s | **0.6s → 0.6s** |
| Cold list | process died | 647s | **145s** |
| Warm list | process died | 0.317s | **0.157s** |
| Peak RSS | process died | 1.9GB | **0.3GB** |

Per-character fetch is ~30ms on all three at every tier — it's the 1.5MB payload,
not the parser.

The upstream failures are the interesting part: it serialises every character's full
JSON into the list response, so around ~1,000 characters `JSON.stringify` exceeds
V8's ~512MB string ceiling (`RangeError`, HTTP 500 at `characters.js:1472`);
at 5,000 characters the process dies with a heap OOM before it even gets there.
TurtleTavern (Node) dodges it with the SQLite shallow index. Go does the same
thing, about 4–5x faster to build and ~6x leaner to hold.
