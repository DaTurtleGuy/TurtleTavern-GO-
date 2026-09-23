package handlers

import (
	"encoding/json"
	"net/http"
	"path/filepath"

	"github.com/TurtleTavern/turtletavern/internal/models"
)

var (
	buildPkgVersion  = "0.1.5 (BETA)"
	buildGitRevision = ""
	buildGitBranch   = ""
	buildCommitDate  = ""
)

// IndexHandler serves the main index page.
func IndexHandler(publicDir string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join(publicDir, "index.html"))
	})
}

// LoginHandler serves the login page.
func LoginHandler(publicDir string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join(publicDir, "login.html"))
	})
}

// VersionHandler returns the server version as JSON.
func VersionHandler() http.Handler {
	info := resolveVersionInfo()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(info)
	})
}

func resolveVersionInfo() models.VersionInfo {
	return models.VersionInfo{
		PkgVersion:  buildPkgVersion,
		IsLatest:    true,
		GitRevision: buildGitRevision,
		GitBranch:   buildGitBranch,
		CommitDate:  buildCommitDate,
		Agent:       "TurtleTavern:0.1.5:BETA",
	}
}

// PingHandler returns 204 No Content.
func PingHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
}
