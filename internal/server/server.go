package server

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/TurtleTavern/turtletavern/internal/auth"
	"github.com/TurtleTavern/turtletavern/internal/character"
	"github.com/TurtleTavern/turtletavern/internal/config"
	"github.com/TurtleTavern/turtletavern/internal/content"
	"github.com/TurtleTavern/turtletavern/internal/handlers"
	"github.com/TurtleTavern/turtletavern/internal/maintenance"
	"github.com/TurtleTavern/turtletavern/internal/middleware"
	"github.com/TurtleTavern/turtletavern/internal/util"
	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
)

// Instance is a fully wired server: an HTTP handler plus the resources it owns.
type Instance struct {
	Mux       *chi.Mux
	CharIndex *character.Index
}

// Build wires the router, session and character index for cfg. The caller owns
// the listener and must Close CharIndex when finished.
func Build(cfg *config.Config, publicDir string) (*Instance, error) {
	session, err := auth.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("initialize session: %w", err)
	}

	r := chi.NewRouter()
	r.Use(chimw.Recoverer)
	middleware.SetupMiddleware(r, cfg)
	r.Use(maintenance.Middleware)

	if cfg.Listen && cfg.BasicAuthMode {
		r.Use(session.BasicAuth(publicDir))
	}
	if cfg.WhitelistMode {
		whitelist := auth.NewWhitelist(cfg.Whitelist, cfg.EnableForwardedWhitelist, cfg.WhitelistDockerHosts, publicDir)
		r.Use(whitelist.Middleware())
	}
	hostGuard := auth.NewHostGuard(cfg.HostWhitelist.Enabled, cfg.HostWhitelist.Scan, cfg.HostWhitelist.Hosts, publicDir)
	r.Use(hostGuard.Middleware())

	r.Use(session.CookieMiddleware)
	r.Use(session.SetUserData)

	if !cfg.DisableCsrfProtection {
		r.Use(csrfMiddleware(session))
	}

	r.Get("/version", handlers.VersionHandler().ServeHTTP)
	r.Get("/csrf-token", session.CSRFTokenHandler().ServeHTTP)
	r.Get("/lib.js", func(w http.ResponseWriter, req *http.Request) {
		if bundle := findWebpackBundle(publicDir); bundle != "" {
			w.Header().Set("Content-Type", "application/javascript")
			w.Header().Set("Cache-Control", "no-cache")
			http.ServeFile(w, req, bundle)
			return
		}
		log.Println("WARNING: webpack lib.js bundle not found under " + filepath.Join(publicDir, "webpack") + "; run the Node.js server once to build it")
		http.NotFound(w, req)
	})
	session.RegisterPublicUserRoutes(r)
	r.Get("/callback", callbackHandler)
	r.Get("/callback/{source}", callbackHandler)
	r.Get("/login", func(w http.ResponseWriter, req *http.Request) {
		if !cfg.EnableUserAccounts {
			http.Redirect(w, req, "/", http.StatusFound)
			return
		}
		if session.TryAutoLogin(w, req) {
			http.Redirect(w, req, "/", http.StatusFound)
			return
		}
		http.ServeFile(w, req, filepath.Join(publicDir, "login.html"))
	})
	r.Get("/", func(w http.ResponseWriter, req *http.Request) {
		if cfg.EnableUserAccounts && auth.UserFromRequest(req) == nil {
			target := "/login"
			if req.URL.RawQuery != "" {
				target += "?" + req.URL.RawQuery
			}
			http.Redirect(w, req, target, http.StatusFound)
			return
		}
		index := filepath.Join(publicDir, "index.html")
		setStaticCachePolicy(w, index)
		http.ServeFile(w, req, index)
	})

	charIndex, err := character.OpenIndex(filepath.Join(cfg.DataDir(), "_cache", "character-index"))
	if err != nil {
		return nil, fmt.Errorf("open character index: %w", err)
	}

	r.Group(func(r chi.Router) {
		r.Use(session.RequireLogin)
		r.Post("/api/ping", handlers.PingHandler().ServeHTTP)
		session.RegisterPrivateUserRoutes(r)
		session.RegisterAdminUserRoutes(r)
		session.RegisterUserFileRoutes(r)

		handlers.NewCharacterHandler(charIndex).RegisterRoutes(r)
		handlers.NewChatHandler(cfg).RegisterRoutes(r)
		handlers.NewGroupHandler().RegisterRoutes(r)
		handlers.NewSettingsHandler(cfg).RegisterRoutes(r)
		handlers.NewSecretsHandler(cfg).RegisterRoutes(r)
		handlers.RegisterLLMRoutes(r, cfg)
		handlers.NewTranslateHandler(cfg).RegisterRoutes(r)
		handlers.NewSearchHandler(cfg).RegisterRoutes(r)
		handlers.NewVectorsHandler(cfg).RegisterRoutes(r)
		handlers.NewTokenizersHandler(cfg).RegisterRoutes(r)
		handlers.NewMediaHandler(cfg).RegisterRoutes(r)
		handlers.NewContentHandler(cfg, publicDir).RegisterRoutes(r)
		handlers.NewImportHandler(cfg, publicDir).RegisterRoutes(r)
		handlers.RegisterDeprecatedRedirects(r)
		handlers.NewProxyHandler(cfg).RegisterRoutes(r)
		handlers.NewUserDataHandler(cfg, charIndex).RegisterRoutes(r)

		content.CheckForNewContent(
			userDataRoots(cfg.DataDir()),
			nil,
			cfg.SkipContentCheck,
		)
		handlers.MigrateGroupMetadata(cfg.DataDir(), userHandles(cfg.DataDir()))

		r.Get("/*", func(w http.ResponseWriter, req *http.Request) {
			p := filepath.Join(publicDir, filepath.FromSlash(req.URL.Path))
			if st, err := os.Stat(p); err == nil {
				if st.IsDir() {
					if index := filepath.Join(p, "index.html"); util.FileExists(index) {
						setStaticCachePolicy(w, index)
						http.ServeFile(w, req, index)
						return
					}
				} else {
					setStaticCachePolicy(w, p)
					http.ServeFile(w, req, p)
					return
				}
			}
			notFound := filepath.Join(publicDir, "error", "url-not-found.html")
			if st, err := os.Stat(notFound); err == nil && !st.IsDir() {
				if data, err := os.ReadFile(notFound); err == nil {
					w.Header().Set("Content-Type", "text/html; charset=utf-8")
					w.WriteHeader(http.StatusNotFound)
					_, _ = w.Write(data)
					return
				}
			}
			http.NotFound(w, req)
		})
	})

	return &Instance{Mux: r, CharIndex: charIndex}, nil
}

func csrfMiddleware(session *auth.Session) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
				next.ServeHTTP(w, r)
				return
			}
			if len(r.URL.Path) < 4 || r.URL.Path[:4] != "/api" {
				next.ServeHTTP(w, r)
				return
			}
			if !session.ValidCSRF(r) {
				http.Error(w, `{"error":"Invalid CSRF token"}`, http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func callbackHandler(w http.ResponseWriter, r *http.Request) {
	source := chi.URLParam(r, "source")
	values := url.Values{}
	if source != "" {
		values.Set("source", source)
	}
	if r.URL.RawQuery != "" {
		values.Set("query", r.URL.RawQuery)
	}
	target := "/"
	if encoded := values.Encode(); encoded != "" {
		target += "?" + encoded
	}
	http.Redirect(w, r, target, http.StatusTemporaryRedirect)
}

func userDataRoots(dataRoot string) []string {
	roots := []string{}
	for _, h := range userHandles(dataRoot) {
		roots = append(roots, filepath.Join(dataRoot, h))
	}
	if len(roots) == 0 {
		roots = append(roots, filepath.Join(dataRoot, "default-user"))
	}
	return roots
}

func userHandles(dataRoot string) []string {
	entries, err := os.ReadDir(dataRoot)
	if err != nil {
		return []string{"default-user"}
	}
	var handles []string
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), "_") {
			continue
		}
		handles = append(handles, e.Name())
	}
	if len(handles) == 0 {
		handles = append(handles, "default-user")
	}
	return handles
}

func findWebpackBundle(publicDir string) string {
	root := filepath.Join(publicDir, "webpack")
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	best := ""
	var bestMod time.Time
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		candidate := filepath.Join(root, e.Name(), "output", "lib.js")
		st, err := os.Stat(candidate)
		if err != nil || st.IsDir() {
			continue
		}
		if best == "" || st.ModTime().After(bestMod) {
			best, bestMod = candidate, st.ModTime()
		}
	}
	return best
}

func setStaticCachePolicy(w http.ResponseWriter, p string) {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".js", ".mjs", ".html", ".css", ".json":
		w.Header().Set("Cache-Control", "no-cache")
	}
}
