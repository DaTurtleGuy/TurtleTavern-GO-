package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/TurtleTavern/turtletavern/internal/config"
	"github.com/TurtleTavern/turtletavern/internal/llm"
	"github.com/TurtleTavern/turtletavern/internal/server"
	"github.com/TurtleTavern/turtletavern/internal/util"
)

var debugDefault = ""

func main() {
	if debugDefault != "" || os.Getenv("GOTAVERN_DEBUG") != "" {
		llm.Debug = true
		log.Println("[TT-DEBUG] verbose AI-provider logging enabled")
	}

	cliOpts := config.ParseCLI()
	configPath := resolveConfigPathWithFlag(cliOpts.ConfigPath)

	cfg, err := config.Load(configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	config.ApplyCLI(cfg, cliOpts)

	if err := util.EnsureDir(cfg.DataDir()); err != nil {
		log.Fatalf("Failed to create data directory: %v", err)
	}

	publicDir := resolvePublicDir()

	inst, err := server.Build(cfg, publicDir)
	if err != nil {
		log.Fatalf("Failed to build server: %v", err)
	}

	var addrs []string
	if cfg.Protocol.IPv6Enabled() {
		host := loopbackOrListen(cfg.Listen, cfg.ListenAddress.IPv6, "[::1]", "[::]")
		addrs = append(addrs, host+":"+strconv.Itoa(cfg.Port))
	}
	if cfg.Protocol.IPv4Enabled() {
		host := loopbackOrListen(cfg.Listen, cfg.ListenAddress.IPv4, "127.0.0.1", "0.0.0.0")
		addrs = append(addrs, host+":"+strconv.Itoa(cfg.Port))
	}
	if len(addrs) == 0 {
		log.Fatal("Both IPv6 and IPv4 are disabled or not detected")
	}
	var servers []*http.Server
	for _, addr := range addrs {
		servers = append(servers, &http.Server{
			Addr:              addr,
			Handler:           inst.Mux,
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       0,
			WriteTimeout:      0,
			IdleTimeout:       120 * time.Second,
		})
	}

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGTERM)

	hbCancel := context.CancelFunc(func() {})
	if cfg.HeartbeatInterval > 0 {
		var hbCtx context.Context
		hbCtx, hbCancel = context.WithCancel(context.Background())
		startHeartbeat(hbCtx, cfg.DataDir(), cfg.HeartbeatInterval)
	}

	for _, srv := range servers {
		go func(s *http.Server) {
			fmt.Printf("TurtleTavern (Go) listening on %s\n", s.Addr)
			if err := s.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Printf("Server error on %s: %v", s.Addr, err)
			}
		}(srv)
	}
	fmt.Printf("Config loaded from: %s\n", configPath)
	fmt.Printf("Data root: %s\n", cfg.DataDir())

	<-done
	fmt.Println("\nShutting down...")
	hbCancel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for _, srv := range servers {
		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("Shutdown error on %s: %v", srv.Addr, err)
		}
	}
	inst.CharIndex.Close()
	fmt.Println("Server stopped.")
}

func loopbackOrListen(listen bool, configured, loopback, wildcard string) string {
	if !listen {
		return loopback
	}
	if configured == "" {
		return wildcard
	}
	trimmed := strings.Trim(configured, "[]")
	if net.ParseIP(trimmed) == nil {
		return wildcard
	}
	return configured
}

func resolveConfigPathWithFlag(cliPath string) string {
	if cliPath != "" {
		return cliPath
	}
	if envPath := os.Getenv("SILLYTAVERN_CONFIG"); envPath != "" {
		return envPath
	}

	if p := util.ResolveAppPath("config.yaml"); util.FileExists(p) {
		return p
	}

	if _, err := os.Stat("config.yaml"); err == nil {
		return "config.yaml"
	}

	return "config.yaml"
}

func startHeartbeat(ctx context.Context, dataRoot string, intervalSec int) {
	beat := func() {
		path := filepath.Join(dataRoot, "heartbeat.json")
		_ = os.WriteFile(path, []byte(`{"timestamp":`+strconv.FormatInt(time.Now().UnixMilli(), 10)+`}`), 0o644)
	}
	beat()
	go func() {
		ticker := time.NewTicker(time.Duration(intervalSec) * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				beat()
			}
		}
	}()
}

func resolvePublicDir() string {
	return util.ResolveAppPath("public")
}
