# TurtleTavern (GO)

A pure-Go backend for SillyTavern-style web frontends: it serves the unchanged
SillyTavern frontend and reimplements the server pieces the Node version used
(settings, chats, characters, secrets, per-user backups and restore).

Origin: the Node-based TurtleTavern fork — https://github.com/DaTurtleGuy/TurtleTavern

## How this was built

Genuinely vibe-coded. Built with OpenCode — orchestrating multiple models as my
whims dictated: GLM 5.3 Flash, DeepSeek V4.1 Flash, Mimo 2.5, among others.
This is my first *real* project ever and I will tell you right now: I have no
formal clue what I'm doing. Expect asymmetric polish: some corners are hardened
by regression suites, others survived by being touched fewer times.
`AGENTS.md` is the scar-tissue knowledge base — when something breaks, check
there first.



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
