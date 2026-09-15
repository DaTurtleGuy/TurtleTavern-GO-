package auth

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/TurtleTavern/turtletavern/internal/models"
	"github.com/go-chi/chi/v5"
)

func (s *Session) serveUserFile(dirFn func(dirs models.UserDirectories) string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uc := UserFromRequest(r)
		if uc == nil {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		raw := chi.URLParam(r, "*")
		decoded, err := url.PathUnescape(raw)
		if err != nil {
			decoded = raw
		}
		base := dirFn(uc.Directories)
		full := filepath.Join(base, filepath.FromSlash(decoded))
		absBase, err := filepath.Abs(base)
		if err != nil {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		absFull, err := filepath.Abs(full)
		if err != nil || (absFull != absBase && !strings.HasPrefix(absFull, absBase+string(os.PathSeparator))) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		if st, err := os.Stat(absFull); err != nil || st.IsDir() {
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}
		http.ServeFile(w, r, absFull)
	}
}

func (s *Session) RegisterUserFileRoutes(r chi.Router) {
	r.Get("/backgrounds/*", s.serveUserFile(func(d models.UserDirectories) string { return d.Backgrounds }))
	r.Get("/characters/*", s.serveUserFile(func(d models.UserDirectories) string { return d.Characters }))
	avatars := s.serveUserFile(func(d models.UserDirectories) string { return d.Avatars })
	r.Get("/User Avatars/*", avatars)
	r.Get("/User%20Avatars/*", avatars)
	r.Get("/assets/*", s.serveUserFile(func(d models.UserDirectories) string { return d.Assets }))
	r.Get("/user/images/*", s.serveUserFile(func(d models.UserDirectories) string { return d.UserImages }))
	r.Get("/user/files/*", s.serveUserFile(func(d models.UserDirectories) string { return d.Files }))
	r.Get("/scripts/extensions/third-party/*", s.serveUserFile(func(d models.UserDirectories) string { return d.Extensions }))
}
