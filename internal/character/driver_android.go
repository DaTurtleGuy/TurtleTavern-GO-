//go:build android

package character

import _ "github.com/mattn/go-sqlite3"

// Android's seccomp policy blocks the raw stat/lstat/fstat syscalls that
// modernc.org/libc issues on amd64, so Android uses the real C SQLite library.
const sqliteDriver = "sqlite3"
