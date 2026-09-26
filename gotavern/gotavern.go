// Package gotavern is the gomobile-bindable entry point for the TurtleTavern server.
package gotavern

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/TurtleTavern/turtletavern/internal/character"
	"github.com/TurtleTavern/turtletavern/internal/config"
	"github.com/TurtleTavern/turtletavern/internal/server"
	"github.com/TurtleTavern/turtletavern/internal/util"
)

var (
	mu        sync.Mutex
	srv       *http.Server
	idx       *character.Index
	boundPort int32
)

// ringWriter tees log output into a bounded in-memory buffer so the mobile
// shell can display recent server logs without touching logcat.
type ringWriter struct {
	mu  sync.Mutex
	buf []byte
	out io.Writer
}

const logBufCap = 256 * 1024

func (r *ringWriter) Write(p []byte) (int, error) {
	n, err := r.out.Write(p) // forward first; logcat forwarding stays untouched
	r.mu.Lock()
	r.buf = append(r.buf, p...)
	if over := len(r.buf) - logBufCap; over > 0 {
		copy(r.buf, r.buf[over:])
		r.buf = r.buf[:len(r.buf)-over]
	}
	r.mu.Unlock()
	return n, err
}

func (r *ringWriter) Dump() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.buf)
}

var capture *ringWriter

// Start boots the server bound to loopback and returns the port it chose.
// appRoot must hold config.yaml, public/ and default/; user data is created
// under appRoot/data. Pass 0 to let the OS pick a free port.
func Start(appRoot string, port int32) (int32, error) {
	mu.Lock()
	defer mu.Unlock()
	if srv != nil {
		return boundPort, nil
	}

	// Android app sandboxes have no /tmp and do not always export TMPDIR to
	// the gomobile process, so os.CreateTemp("") (restore upload spool,
	// sprite zip spool) fails with ENOENT and restore 400s with
	// "cannot buffer the upload on the server". Pin TMPDIR to a dir we own.
	if tmp := filepath.Join(appRoot, "tmp"); os.MkdirAll(tmp, 0o755) == nil {
		_ = os.Setenv("TMPDIR", tmp)
	}

	// Tee the std log stream (gomobile already forwards it to logcat)
	// so Logs() can hand it to the in-app log viewer.
	if capture == nil && log.Writer() != nil {
		capture = &ringWriter{out: log.Writer()}
		log.SetOutput(capture)
		// gomobile clears the log flags, so server lines reached the in-app
		// viewer with no timestamp and could not be lined up with the app log.
		log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	}

	util.SetAppDir(appRoot)
	publicDir := filepath.Join(appRoot, "public")
	character.ReloadDefaultAvatar(publicDir)

	cfg, err := config.Load(filepath.Join(appRoot, "config.yaml"))
	if err != nil {
		return 0, fmt.Errorf("load config: %w", err)
	}
	cfg.Listen = false
	cfg.BasicAuthMode = false

	if err := util.EnsureDir(cfg.DataDir()); err != nil {
		return 0, fmt.Errorf("create data directory: %w", err)
	}

	inst, err := server.Build(cfg, publicDir)
	if err != nil {
		return 0, err
	}

	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		inst.CharIndex.Close()
		return 0, fmt.Errorf("listen: %w", err)
	}

	httpSrv := &http.Server{
		Handler:           inst.Mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      0,
		IdleTimeout:       120 * time.Second,
	}
	go func() { _ = httpSrv.Serve(listener) }()

	srv = httpSrv
	idx = inst.CharIndex
	boundPort = int32(listener.Addr().(*net.TCPAddr).Port)

	return boundPort, nil
}

// Stop shuts the server down and releases the character index.
func Stop() error {
	mu.Lock()
	defer mu.Unlock()
	if srv == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := srv.Shutdown(ctx)
	if idx != nil {
		idx.Close()
	}
	srv = nil
	idx = nil
	boundPort = 0
	return err
}

// Logs returns the most recent server log output (up to ~256 KiB).
func Logs() string {
	if capture == nil {
		return ""
	}
	return capture.Dump()
}

// Version returns the server version string.
func Version() string {
	return "0.1.6B"
}
