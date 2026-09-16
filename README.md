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

I pointed all three servers at the same generated library and measured boot,
memory, list latency, per-card latency, and concurrency, one server at a time:
upstream SillyTavern 1.19, TurtleTavern (Node), and this one. The library is 5,000
generated cards (~2 MB each, mostly a 150-entry lorebook) plus a ~20 MB monster
every 50th card, tested at 100 / 1,000 / 5,000 cards.

Fair warning: the harness was vibe-coded in an afternoon, one run per cell,
synthetic cards, and upstream is 1.19 while TurtleTavern (Node) is 1.17-based, so some of
the TurtleTavern (Node)-vs-upstream gap is version drift. Don't quote these numbers at anyone.
The shapes are real, though:

| | 100 cards | 1,000 cards | 5,000 cards |
|---|---|---|---|
| Boot (first → warm avg) | ST 13.7→3.6s, fork 11.5→3.1s, **Go 0.6→0.6s** | same shape | same shape |
| Cold list | ST 13.8s, fork 12.4s, **Go 2.0s** | ST 💥 500, fork 124s, **Go 25.5s** | ST 💥 dead, fork 647s, **Go 145s** |
| Warm list | ST 1.78s, fork 0.026s, **Go 0.016s** | ST 💥, fork 0.079s, **Go 0.045s** | ST 💥, fork 0.317s, **Go 0.157s** |
| Peak RSS | ST ~3–4GB, fork 1.5GB, **Go 0.2GB** | ST 4.4GB, fork 1.9GB, **Go 0.3GB** | ST 💥, fork 1.9GB, **Go 0.3GB** |
| Per-card fetch | ~30ms on all three — it's the 1.5MB payload, not the parser | | |

The 💥 is the interesting part: upstream serialises every card's full JSON into
the list response, so around ~1,000 cards `JSON.stringify` exceeds V8's ~512MB
string ceiling (`RangeError`, HTTP 500 at `characters.js:1472`); at 5,000 cards
the process dies with a heap OOM. TurtleTavern (Node) dodges it with the SQLite shallow
index. Go does the same thing, about 4–5x faster to build and ~6x leaner to
hold.
