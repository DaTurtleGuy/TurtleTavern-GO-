package handlers

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/TurtleTavern/turtletavern/internal/config"
	"github.com/TurtleTavern/turtletavern/internal/models"
	"github.com/TurtleTavern/turtletavern/internal/util"
	"github.com/go-chi/chi/v5"
)

func readAllAndClose(rc io.ReadCloser) ([]byte, error) {
	defer rc.Close()
	return io.ReadAll(rc)
}

type ContentHandler struct {
	Cfg       *config.Config
	PublicDir string

	statsMu   sync.Mutex
	stats     map[string]map[string]any
	statsSave map[string]int64

	maidMu     sync.Mutex
	maidTokens map[string]maidToken
}

type maidToken struct {
	handle string
	paths  []maidPath
}

type maidPath struct {
	path string
	hash string
}

func NewContentHandler(cfg *config.Config, publicDir string) *ContentHandler {
	return &ContentHandler{
		Cfg: cfg, PublicDir: publicDir,
		stats: map[string]map[string]any{}, statsSave: map[string]int64{},
		maidTokens: map[string]maidToken{},
	}
}

func (h *ContentHandler) RegisterRoutes(r chi.Router) {
	r.Route("/api/worldinfo", func(r chi.Router) {
		r.Post("/list", h.WorldList)
		r.Post("/get", h.WorldGet)
		r.Post("/delete", h.WorldDelete)
		r.Post("/import", h.WorldImport)
		r.Post("/edit", h.WorldEdit)
	})
	r.Route("/api/extensions", func(r chi.Router) {
		r.Post("/install", h.ExtInstall)
		r.Post("/update", h.ExtUpdate)
		r.Post("/branches", h.ExtBranches)
		r.Post("/switch", h.ExtSwitch)
		r.Post("/move", h.ExtMove)
		r.Post("/version", h.ExtVersion)
		r.Post("/delete", h.ExtDelete)
		r.Get("/discover", h.ExtDiscover)
	})
	r.Route("/api/stats", func(r chi.Router) {
		r.Post("/get", h.StatsGet)
		r.Post("/recreate", h.StatsRecreate)
		r.Post("/update", h.StatsUpdate)
	})
	r.Route("/api/backups", func(r chi.Router) {
		r.Post("/chat/get", h.BackupChatGet)
		r.Post("/chat/delete", h.BackupChatDelete)
		r.Post("/chat/download", h.BackupChatDownload)
	})
	r.Route("/api/data-maid", func(r chi.Router) {
		r.Post("/report", h.MaidReport)
		r.Post("/finalize", h.MaidFinalize)
		r.Get("/view", h.MaidView)
		r.Post("/delete", h.MaidDelete)
	})
}

func readWorldInfoFile(dirs models.UserDirectories, name string, allowDummy bool) any {
	if name == "" {
		if allowDummy {
			return map[string]any{"entries": map[string]any{}}
		}
		return nil
	}
	p := filepath.Join(dirs.Worlds, util.SanitizeFileName(name+".json"))
	data, err := os.ReadFile(p)
	if err != nil {
		return map[string]any{"entries": map[string]any{}}
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return map[string]any{"entries": map[string]any{}}
	}
	return v
}

func (h *ContentHandler) WorldList(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	entries, err := os.ReadDir(uc.Directories.Worlds)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	type item struct {
		FileID     string         `json:"file_id"`
		Name       string         `json:"name"`
		Extensions map[string]any `json:"extensions"`
	}
	var out []item
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(uc.Directories.Worlds, e.Name()))
		if err != nil {
			continue
		}
		var parsed map[string]any
		if err := json.Unmarshal(data, &parsed); err != nil {
			continue
		}
		base := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		name, _ := parsed["name"].(string)
		if name == "" {
			name = base
		}
		exts, _ := parsed["extensions"].(map[string]any)
		if exts == nil {
			exts = map[string]any{}
		}
		out = append(out, item{FileID: base, Name: name, Extensions: exts})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FileID < out[j].FileID })
	if out == nil {
		out = []item{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (h *ContentHandler) WorldGet(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body := translateBody(r)
	name, _ := body["name"].(string)
	if name == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(readWorldInfoFile(uc.Directories, name, true))
}

func (h *ContentHandler) WorldDelete(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body := translateBody(r)
	name, _ := body["name"].(string)
	if name == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	p := filepath.Join(uc.Directories.Worlds, util.SanitizeFileName(name+".json"))
	if _, err := os.Stat(p); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	_ = os.Remove(p)
	w.WriteHeader(http.StatusOK)
}

func (h *ContentHandler) WorldImport(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	var fileContents string
	if err := r.ParseMultipartForm(500 << 20); err == nil && r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
		if vals := r.MultipartForm.Value["convertedData"]; len(vals) > 0 {
			fileContents = vals[0]
		}
		if fileContents == "" && len(r.MultipartForm.File["avatar"]) > 0 {
			fh := r.MultipartForm.File["avatar"][0]
			originalName := fh.Filename
			f, err := fh.Open()
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			data, err := readAllAndClose(f)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			fileContents = string(data)
			var parsed map[string]any
			if err := json.Unmarshal([]byte(fileContents), &parsed); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte("Is not a valid world info file"))
				return
			}
			if _, ok := parsed["entries"]; !ok {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte("Is not a valid world info file"))
				return
			}
			base := strings.TrimSuffix(originalName, filepath.Ext(originalName))
			worldName := util.SanitizeFileName(base)
			if worldName == "" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte("World file must have a name"))
				return
			}
			_ = util.AtomicWrite(filepath.Join(uc.Directories.Worlds, worldName+".json"), []byte(fileContents))
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"name": worldName})
			return
		}
		if fileContents == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
	} else {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(fileContents), &parsed); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("Is not a valid world info file"))
		return
	}
	if _, ok := parsed["entries"]; !ok {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("Is not a valid world info file"))
		return
	}
	name, _ := parsed["name"].(string)
	if name == "" {
		name = "imported"
	}
	worldName := util.SanitizeFileName(name)
	_ = util.AtomicWrite(filepath.Join(uc.Directories.Worlds, worldName+".json"), []byte(fileContents))
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"name": worldName})
}

func (h *ContentHandler) WorldEdit(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body := translateBody(r)
	name, _ := body["name"].(string)
	if name == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("World file must have a name"))
		return
	}
	data, ok := body["data"].(map[string]any)
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("Is not a valid world info file"))
		return
	}
	if _, ok := data["entries"]; !ok {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("Is not a valid world info file"))
		return
	}
	encoded, _ := json.MarshalIndent(data, "", "    ")
	_ = util.AtomicWrite(filepath.Join(uc.Directories.Worlds, util.SanitizeFileName(name+".json")), encoded)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func (h *ContentHandler) globalExtensionsDir() string {
	return filepath.Join(h.PublicDir, "scripts", "extensions", "third-party")
}

func (h *ContentHandler) extBasePath(uc *models.UserContext, global bool) string {
	if global {
		return h.globalExtensionsDir()
	}
	return uc.Directories.Extensions
}

func gitRun(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func isValidGitURL(rawURL string) bool {
	if strings.HasPrefix(rawURL, "https://") || strings.HasPrefix(rawURL, "http://") {
		return true
	}
	if strings.HasPrefix(rawURL, "git@") {
		return strings.Contains(rawURL, ":")
	}
	return false
}

func (h *ContentHandler) ExtInstall(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body := translateBody(r)
	rawURL, _ := body["url"].(string)
	if rawURL == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("Bad Request: URL is required in the request body."))
		return
	}
	if !isValidGitURL(rawURL) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("Bad Request: The provided URL is not a valid git repository URL."))
		return
	}
	global, _ := body["global"].(bool)
	branch, _ := body["branch"].(string)
	if global && !uc.Profile.Admin {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("Forbidden: No permission to install global extensions."))
		return
	}
	basePath := h.extBasePath(uc, global)
	_ = os.MkdirAll(uc.Directories.Extensions, 0o755)
	_ = os.MkdirAll(h.globalExtensionsDir(), 0o755)
	base := strings.TrimSuffix(rawURL, ".git")
	parts := strings.Split(strings.TrimSuffix(base, "/"), "/")
	extPath := filepath.Join(basePath, util.SanitizeFileName(parts[len(parts)-1]))
	if _, err := os.Stat(extPath); err == nil {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte("Directory already exists"))
		return
	}
	args := []string{"clone", "--depth", "1"}
	if branch != "" {
		args = append(args, "--branch", branch)
	}
	args = append(args, rawURL, extPath)
	if out, err := gitRun("", args...); err != nil {
		log.Printf("git clone failed for %s: %s", rawURL, out)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("Server Error: failed to clone extension"))
		return
	}
	manifest := map[string]any{}
	if data, err := os.ReadFile(filepath.Join(extPath, "manifest.json")); err == nil {
		_ = json.Unmarshal(data, &manifest)
	}
	version, _ := manifest["version"].(string)
	author, _ := manifest["author"].(string)
	displayName, _ := manifest["display_name"].(string)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"version": version, "author": author, "display_name": displayName,
		"extensionPath": extPath, "folderName": filepath.Base(extPath),
	})
}

func gitIsRepo(dir string) bool {
	out, err := gitRun(dir, "rev-parse", "--is-inside-work-tree")
	return err == nil && out == "true"
}

func (h *ContentHandler) ExtUpdate(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body := translateBody(r)
	extName, _ := body["extensionName"].(string)
	if extName == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("Bad Request: extensionName is required in the request body."))
		return
	}
	global, _ := body["global"].(bool)
	if global && !uc.Profile.Admin {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("Forbidden: No permission to update global extensions."))
		return
	}
	extPath := filepath.Join(h.extBasePath(uc, global), util.SanitizeFileName(extName))
	if _, err := os.Stat(extPath); err != nil {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("Directory does not exist"))
		return
	}
	if !gitIsRepo(extPath) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("Directory is not a Git repository"))
		return
	}
	headBefore, _ := gitRun(extPath, "rev-parse", "HEAD")
	branchOut, _ := gitRun(extPath, "branch", "--show-current")
	_, _ = gitRun(extPath, "fetch", "origin")
	aheadOut, _ := gitRun(extPath, "rev-list", "--count", "HEAD..@{u}")
	remoteOut, _ := gitRun(extPath, "remote", "get-url", "origin")
	isUpToDate := aheadOut == "0"
	if !isUpToDate {
		if _, err := gitRun(extPath, "pull", "origin", branchOut); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("Internal Server Error. Check the server logs for more details."))
			return
		}
	}
	_, _ = gitRun(extPath, "fetch", "origin")
	headAfter, _ := gitRun(extPath, "rev-parse", "HEAD")
	_ = headBefore
	short := headAfter
	if len(short) > 7 {
		short = short[:7]
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"shortCommitHash": short, "extensionPath": extPath,
		"isUpToDate": isUpToDate, "remoteUrl": remoteOut,
	})
}

func (h *ContentHandler) ExtBranches(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body := translateBody(r)
	extName, _ := body["extensionName"].(string)
	if extName == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("Bad Request: extensionName is required in the request body."))
		return
	}
	global, _ := body["global"].(bool)
	if global && !uc.Profile.Admin {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("Forbidden: No permission to list branches of global extensions."))
		return
	}
	extPath := filepath.Join(h.extBasePath(uc, global), util.SanitizeFileName(extName))
	if _, err := os.Stat(extPath); err != nil {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("Directory does not exist"))
		return
	}
	if out, _ := gitRun(extPath, "rev-parse", "--is-shallow-repository"); out == "true" {
		_, _ = gitRun(extPath, "fetch", "origin", "--unshallow")
	}
	_, _ = gitRun(extPath, "remote", "set-branches", "origin", "*")
	_, _ = gitRun(extPath, "fetch", "origin")
	currentOut, _ := gitRun(extPath, "branch", "--show-current")
	localOut, _ := gitRun(extPath, "branch", "--format=%(refname:short)|%(objectname:short)|%(HEAD)")
	remoteOut, _ := gitRun(extPath, "branch", "-r", "--format=%(refname:short)|%(objectname:short)|%(HEAD)")
	type branchInfo struct {
		Current bool   `json:"current"`
		Commit  string `json:"commit"`
		Name    string `json:"name"`
		Label   string `json:"label"`
	}
	var result []branchInfo
	parseBranches := func(out string) {
		for _, line := range strings.Split(out, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			parts := strings.Split(line, "|")
			name := strings.TrimSpace(strings.TrimPrefix(parts[0], "* "))
			current := strings.HasPrefix(parts[0], "*") || (currentOut != "" && name == currentOut)
			commit := ""
			if len(parts) > 1 {
				commit = strings.TrimSpace(parts[1])
			}
			result = append(result, branchInfo{Current: current, Commit: commit, Name: name, Label: name})
		}
	}
	parseBranches(localOut)
	parseBranches(remoteOut)
	if result == nil {
		result = []branchInfo{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func (h *ContentHandler) ExtSwitch(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body := translateBody(r)
	extName, _ := body["extensionName"].(string)
	branch, _ := body["branch"].(string)
	if extName == "" || branch == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("Bad Request: extensionName and branch are required in the request body."))
		return
	}
	global, _ := body["global"].(bool)
	if global && !uc.Profile.Admin {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("Forbidden: No permission to switch branches of global extensions."))
		return
	}
	extPath := filepath.Join(h.extBasePath(uc, global), util.SanitizeFileName(extName))
	if _, err := os.Stat(extPath); err != nil {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("Directory does not exist"))
		return
	}
	if strings.HasPrefix(branch, "origin/") {
		local := strings.TrimPrefix(branch, "origin/")
		localOut, _ := gitRun(extPath, "branch", "--list", local)
		if strings.TrimSpace(localOut) != "" {
			_, _ = gitRun(extPath, "checkout", local)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if _, err := gitRun(extPath, "checkout", "-b", local, branch); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("Internal Server Error. Check the server logs for more details."))
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	localOut, _ := gitRun(extPath, "branch", "--list", branch)
	if strings.TrimSpace(localOut) == "" {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("Branch " + branch + " does not exist locally"))
		return
	}
	currentOut, _ := gitRun(extPath, "branch", "--show-current")
	if currentOut == branch {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if _, err := gitRun(extPath, "checkout", branch); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("Internal Server Error. Check the server logs for more details."))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *ContentHandler) ExtMove(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body := translateBody(r)
	extName, _ := body["extensionName"].(string)
	source, _ := body["source"].(string)
	destination, _ := body["destination"].(string)
	if extName == "" || source == "" || destination == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("Bad Request. Not all required parameters are provided."))
		return
	}
	if !uc.Profile.Admin {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("Forbidden: No permission to move extensions."))
		return
	}
	srcBase := uc.Directories.Extensions
	if source == "global" {
		srcBase = h.globalExtensionsDir()
	}
	dstBase := uc.Directories.Extensions
	if destination == "global" {
		dstBase = h.globalExtensionsDir()
	}
	srcPath := filepath.Join(srcBase, util.SanitizeFileName(extName))
	dstPath := filepath.Join(dstBase, util.SanitizeFileName(extName))
	if st, err := os.Stat(srcPath); err != nil || !st.IsDir() {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("Source directory does not exist."))
		return
	}
	if _, err := os.Stat(dstPath); err == nil {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte("Destination directory already exists."))
		return
	}
	if source == destination {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte("Source and destination directories are the same."))
		return
	}
	if err := copyDir(srcPath, dstPath); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("Internal Server Error. Check the server logs for more details."))
		return
	}
	_ = os.RemoveAll(srcPath)
	w.WriteHeader(http.StatusNoContent)
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return util.AtomicWrite(target, data)
	})
}

func (h *ContentHandler) ExtVersion(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body := translateBody(r)
	extName, _ := body["extensionName"].(string)
	if extName == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("Bad Request: extensionName is required in the request body."))
		return
	}
	global, _ := body["global"].(bool)
	extPath := filepath.Join(h.extBasePath(uc, global), util.SanitizeFileName(extName))
	if _, err := os.Stat(extPath); err != nil {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("Directory does not exist"))
		return
	}
	empty := map[string]any{"currentBranchName": "", "currentCommitHash": "", "isUpToDate": true, "remoteUrl": ""}
	if !gitIsRepo(extPath) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(empty)
		return
	}
	head, err := gitRun(extPath, "rev-parse", "HEAD")
	if err != nil || head == "" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(empty)
		return
	}
	branch, _ := gitRun(extPath, "branch", "--show-current")
	_, _ = gitRun(extPath, "fetch", "origin")
	aheadOut, _ := gitRun(extPath, "rev-list", "--count", "HEAD..@{u}")
	remoteOut, _ := gitRun(extPath, "remote", "get-url", "origin")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"currentBranchName": branch, "currentCommitHash": head,
		"isUpToDate": aheadOut == "0", "remoteUrl": remoteOut,
	})
}

func (h *ContentHandler) ExtDelete(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body := translateBody(r)
	extName, _ := body["extensionName"].(string)
	if extName == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("Bad Request: extensionName is required in the request body."))
		return
	}
	global, _ := body["global"].(bool)
	if global && !uc.Profile.Admin {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("Forbidden: No permission to delete global extensions."))
		return
	}
	extPath := filepath.Join(h.extBasePath(uc, global), util.SanitizeFileName(extName))
	if _, err := os.Stat(extPath); err != nil {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("Directory does not exist"))
		return
	}
	_ = os.RemoveAll(extPath)
	_, _ = w.Write([]byte("Extension has been deleted"))
}

func (h *ContentHandler) ExtDiscover(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	_ = os.MkdirAll(uc.Directories.Extensions, 0o755)
	_ = os.MkdirAll(h.globalExtensionsDir(), 0o755)
	type ext struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	var all []ext
	builtInRoot := filepath.Join(h.PublicDir, "scripts", "extensions")
	if entries, err := os.ReadDir(builtInRoot); err == nil {
		for _, e := range entries {
			if e.IsDir() && e.Name() != "third-party" {
				all = append(all, ext{Type: "system", Name: e.Name()})
			}
		}
	}
	userNames := map[string]bool{}
	if entries, err := os.ReadDir(uc.Directories.Extensions); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				all = append(all, ext{Type: "local", Name: "third-party/" + e.Name()})
				userNames["third-party/"+e.Name()] = true
			}
		}
	}
	if entries, err := os.ReadDir(h.globalExtensionsDir()); err == nil {
		for _, e := range entries {
			if e.IsDir() && !userNames["third-party/"+e.Name()] {
				all = append(all, ext{Type: "global", Name: "third-party/" + e.Name()})
			}
		}
	}
	if all == nil {
		all = []ext{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(all)
}

var monthNames = []string{
	"January", "February", "March", "April", "May", "June",
	"July", "August", "September", "October", "November", "December",
}

var meridiemRe = regexp.MustCompile(`(\w+)\s(\d{1,2}),\s(\d{4})\s(\d{1,2}):(\d{1,2})(am|pm)`)
var humanizedRe1 = regexp.MustCompile(`(\d{4})-(\d{1,2})-(\d{1,2})@(\d{1,2})h(\d{1,2})m(\d{1,2})s(\d{1,3})ms`)
var humanizedRe2 = regexp.MustCompile(`(\d{4})-(\d{1,2})-(\d{1,2})@(\d{1,2})h(\d{1,2})m(\d{1,2})s`)
var humanizedRe3 = regexp.MustCompile(`(\d{4})-(\d{1,2})-(\d{1,2}) @(\d{1,2})h (\d{1,2})m (\d{1,2})s (\d{1,3})ms`)
var digitsRe = regexp.MustCompile(`^\d+$`)
var wordRe = regexp.MustCompile(`\b\w+\b`)

func parseStatsTimestamp(v any) int64 {
	switch t := v.(type) {
	case nil:
		return 0
	case float64:
		if t < 0 {
			return 0
		}
		return int64(t)
	case string:
		if t == "" {
			return 0
		}
		if digitsRe.MatchString(t) {
			var n int64
			if _, err := fmt.Sscanf(t, "%d", &n); err == nil && n >= 0 {
				return n
			}
			return 0
		}
		if matched, _ := regexp.MatchString(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z$`, t); matched {
			if tm, err := time.Parse(time.RFC3339, t); err == nil {
				return tm.UnixMilli()
			}
			return 0
		}
		if m := meridiemRe.FindStringSubmatch(t); m != nil {
			monthNum := 0
			for i, name := range monthNames {
				if strings.EqualFold(name, m[1]) {
					monthNum = i + 1
					break
				}
			}
			if monthNum == 0 {
				return 0
			}
			hour := parseNum(m[4])
			min := parseNum(m[5])
			if strings.ToLower(m[6]) == "pm" {
				hour = hour%12 + 12
			} else {
				hour = hour % 12
			}
			tm := time.Date(parseNum(m[3]), time.Month(monthNum), parseNum(m[2]), hour, min, 0, 0, time.UTC)
			return tm.UnixMilli()
		}
		for _, re := range []*regexp.Regexp{humanizedRe1, humanizedRe3} {
			if m := re.FindStringSubmatch(t); m != nil {
				ms := 0
				if len(m) > 7 {
					ms = parseNum(m[7])
				}
				tm := time.Date(padYear(m[1]), time.Month(parseNum(m[2])), parseNum(m[3]),
					parseNum(m[4]), parseNum(m[5]), parseNum(m[6]), ms*1000000, time.UTC)
				return tm.UnixMilli()
			}
		}
		if m := humanizedRe2.FindStringSubmatch(t); m != nil {
			tm := time.Date(padYear(m[1]), time.Month(parseNum(m[2])), parseNum(m[3]),
				parseNum(m[4]), parseNum(m[5]), parseNum(m[6]), 0, time.UTC)
			return tm.UnixMilli()
		}
		return 0
	default:
		return 0
	}
}

func parseNum(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func padYear(s string) int {
	for len(s) < 4 {
		s = "0" + s
	}
	return parseNum(s)
}

func countStatsWords(s string) int {
	return len(wordRe.FindAllString(s, -1))
}

func (h *ContentHandler) statsFilePath(handle, dataRoot string) string {
	return filepath.Join(dataRoot, handle, "stats.json")
}

func (h *ContentHandler) loadStats(handle, dataRoot string) map[string]any {
	h.statsMu.Lock()
	defer h.statsMu.Unlock()
	if s, ok := h.stats[handle]; ok {
		return s
	}
	data, err := os.ReadFile(h.statsFilePath(handle, dataRoot))
	if err != nil {
		return map[string]any{}
	}
	var v map[string]any
	if err := json.Unmarshal(data, &v); err != nil {
		return map[string]any{}
	}
	h.stats[handle] = v
	return v
}

func (h *ContentHandler) saveStats(handle, dataRoot string, stats map[string]any) {
	h.statsMu.Lock()
	defer h.statsMu.Unlock()
	stats["timestamp"] = float64(time.Now().UnixMilli())
	h.stats[handle] = stats
	if data, err := json.Marshal(stats); err == nil {
		_ = util.AtomicWrite(h.statsFilePath(handle, dataRoot), data)
		h.statsSave[handle] = time.Now().UnixMilli()
	}
}

func (h *ContentHandler) StatsGet(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	stats := h.loadStats(uc.Profile.Handle, h.dataRoot())
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(stats)
}

func (h *ContentHandler) dataRoot() string {
	return h.Cfg.DataDir()
}

func (h *ContentHandler) StatsRecreate(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	stats := collectChatStats(uc.Directories.Chats, uc.Directories.Characters)
	h.saveStats(uc.Profile.Handle, h.dataRoot(), stats)
	w.WriteHeader(http.StatusOK)
}

func (h *ContentHandler) StatsUpdate(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body := map[string]any{}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err != io.EOF {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if body == nil {
		body = map[string]any{}
	}
	h.saveStats(uc.Profile.Handle, h.dataRoot(), body)
	w.WriteHeader(http.StatusOK)
}

func collectChatStats(chatsPath, charactersPath string) map[string]any {
	final := map[string]any{}
	entries, err := os.ReadDir(charactersPath)
	if err != nil {
		final["timestamp"] = float64(time.Now().UnixMilli())
		return final
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".png") {
			continue
		}
		stats := map[string]any{
			"total_gen_time": 0.0, "user_word_count": 0.0, "non_user_word_count": 0.0,
			"user_msg_count": 0.0, "non_user_msg_count": 0.0, "total_swipe_count": 0.0,
			"chat_size": 0.0, "date_last_chat": 0.0, "date_first_chat": float64(time.Date(9999, 12, 31, 23, 59, 59, 999000000, time.UTC).UnixMilli()),
		}
		seen := map[string]bool{}
		chatDir := filepath.Join(chatsPath, strings.TrimSuffix(e.Name(), ".png"))
		chatEntries, err := os.ReadDir(chatDir)
		if err != nil {
			final[e.Name()] = stats
			continue
		}
		for _, ce := range chatEntries {
			if ce.IsDir() {
				continue
			}
			res := accumulateChatFile(filepath.Join(chatDir, ce.Name()), seen)
			for k, v := range res.nums {
				stats[k] = stats[k].(float64) + v
			}
			if st, err := os.Stat(filepath.Join(chatDir, ce.Name())); err == nil {
				stats["chat_size"] = stats["chat_size"].(float64) + float64(st.Size())
				if mtime := float64(st.ModTime().UnixMilli()); mtime > stats["date_last_chat"].(float64) {
					stats["date_last_chat"] = mtime
				}
			}
			if res.firstChat < stats["date_first_chat"].(float64) {
				stats["date_first_chat"] = res.firstChat
			}
		}
		final[e.Name()] = stats
	}
	final["timestamp"] = float64(time.Now().UnixMilli())
	return final
}

type chatAccum struct {
	nums      map[string]float64
	firstChat float64
}

func accumulateChatFile(path string, seen map[string]bool) chatAccum {
	acc := chatAccum{
		nums:      map[string]float64{},
		firstChat: float64(time.Date(9999, 12, 31, 23, 59, 59, 999000000, time.UTC).UnixMilli()),
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return acc
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var msg map[string]any
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		if mes, ok := msg["mes"].(string); ok && mes != "" {
			sum := sha256.Sum256([]byte(mes))
			key := fmt.Sprintf("%x", sum)
			if seen[key] {
				continue
			}
			seen[key] = true
		}
		if gs, ok := msg["gen_started"]; ok && msg["gen_finished"] != nil {
			if gf, ok := msg["gen_finished"]; ok && gf != nil {
				acc.nums["total_gen_time"] += float64(parseStatsTimestamp(gf) - parseStatsTimestamp(gs))
			}
			if swipes, ok := msg["swipes"].([]any); ok && msg["swipe_info"] == nil {
				acc.nums["total_gen_time"] += float64(parseStatsTimestamp(msg["gen_finished"])-parseStatsTimestamp(msg["gen_started"])) * float64(len(swipes))
			}
		}
		if mes, ok := msg["mes"].(string); ok && mes != "" {
			wc := float64(countStatsWords(mes))
			if isUser, _ := msg["is_user"].(bool); isUser {
				acc.nums["user_word_count"] += wc
				acc.nums["user_msg_count"]++
			} else {
				acc.nums["non_user_word_count"] += wc
				acc.nums["non_user_msg_count"]++
			}
		}
		if swipes, ok := msg["swipes"].([]any); ok && len(swipes) > 1 {
			acc.nums["total_swipe_count"] += float64(len(swipes) - 1)
			for i := 1; i < len(swipes); i++ {
				if text, ok := swipes[i].(string); ok {
					wc := float64(countStatsWords(text))
					if isUser, _ := msg["is_user"].(bool); isUser {
						acc.nums["user_word_count"] += wc
						acc.nums["user_msg_count"]++
					} else {
						acc.nums["non_user_word_count"] += wc
						acc.nums["non_user_msg_count"]++
					}
				}
			}
		}
		if infos, ok := msg["swipe_info"].([]any); ok && len(infos) > 1 {
			for i := 1; i < len(infos); i++ {
				if info, ok := infos[i].(map[string]any); ok {
					if gs, ok := info["gen_started"]; ok && info["gen_finished"] != nil {
						if gf, ok := info["gen_finished"]; ok && gf != nil {
							acc.nums["total_gen_time"] += float64(parseStatsTimestamp(gf) - parseStatsTimestamp(gs))
						}
					}
				}
			}
		}
		if isUser, _ := msg["is_user"].(bool); isUser {
			if ts := float64(parseStatsTimestamp(msg["send_date"])); ts < acc.firstChat {
				acc.firstChat = ts
			}
		}
	}
	return acc
}

func (h *ContentHandler) BackupChatGet(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	entries, err := os.ReadDir(uc.Directories.Backups)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	var out []models.ChatInfo
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".jsonl" || !strings.HasPrefix(e.Name(), "chat_") {
			continue
		}
		info := getChatFileInfo(filepath.Join(uc.Directories.Backups, e.Name()))
		if info.FileName == "" {
			continue
		}
		out = append(out, info)
	}
	if out == nil {
		out = []models.ChatInfo{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (h *ContentHandler) BackupChatDelete(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body := translateBody(r)
	name, _ := body["name"].(string)
	p := filepath.Join(uc.Directories.Backups, util.SanitizeFileName(name))
	if !strings.HasPrefix(filepath.Base(p), "chat_") {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if _, err := os.Stat(p); err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	_ = os.Remove(p)
	w.WriteHeader(http.StatusOK)
}

func (h *ContentHandler) BackupChatDownload(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body := translateBody(r)
	name, _ := body["name"].(string)
	p := filepath.Join(uc.Directories.Backups, util.SanitizeFileName(name))
	if !strings.HasPrefix(filepath.Base(p), "chat_") {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if _, err := os.Stat(p); err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+filepath.Base(p)+`"`)
	http.ServeFile(w, r, p)
}

func randRead(b []byte) (int, error) {
	return rand.Read(b)
}

func mimeForPath(p string) string {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".json", ".jsonl":
		return "application/json"
	case ".mp3":
		return "audio/mpeg"
	case ".mp4":
		return "video/mp4"
	default:
		return "text/plain"
	}
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", sum)
}

type maidReport struct {
	Images               []string
	Files                []string
	Chats                []string
	GroupChats           []string
	AvatarThumbnails     []string
	BackgroundThumbnails []string
	PersonaThumbnails    []string
	ChatBackups          []string
	SettingsBackups      []string
}

type maidRecord struct {
	Name   string   `json:"name"`
	Hash   string   `json:"hash"`
	Parent string   `json:"parent,omitempty"`
	Size   *int64   `json:"size,omitempty"`
	Mtime  *float64 `json:"mtime,omitempty"`
}

func sanitizeMaidRecord(path string, withParent bool) maidRecord {
	rec := maidRecord{Name: filepath.Base(path), Hash: sha256Hex(path)}
	if withParent {
		rec.Parent = filepath.Base(filepath.Dir(path))
	}
	if st, err := os.Stat(path); err == nil {
		size := st.Size()
		mtime := float64(st.ModTime().UnixMilli())
		rec.Size = &size
		rec.Mtime = &mtime
	}
	return rec
}

func parseChatMessagesFile(path string) []map[string]any {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []map[string]any
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var msg map[string]any
		if err := json.Unmarshal([]byte(line), &msg); err == nil {
			out = append(out, msg)
		}
	}
	return out
}

func allChatMessages(dirs models.UserDirectories, filter func(map[string]any) bool) []map[string]any {
	var out []map[string]any
	collect := func(path string) {
		for _, m := range parseChatMessagesFile(path) {
			if filter(m) {
				out = append(out, m)
			}
		}
	}
	if entries, err := os.ReadDir(filepath.Join(dirs.Root, "group chats")); err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".jsonl") {
				collect(filepath.Join(dirs.Root, "group chats", e.Name()))
			}
		}
	}
	if entries, err := os.ReadDir(dirs.Chats); err == nil {
		for _, d := range entries {
			if !d.IsDir() {
				continue
			}
			sub, err := os.ReadDir(filepath.Join(dirs.Chats, d.Name()))
			if err != nil {
				continue
			}
			for _, f := range sub {
				if !f.IsDir() && strings.EqualFold(filepath.Ext(f.Name()), ".jsonl") {
					collect(filepath.Join(dirs.Chats, d.Name(), f.Name()))
				}
			}
		}
	}
	return out
}

func allChatMetadata(dirs models.UserDirectories, filter func(map[string]any) bool) []map[string]any {
	var out []map[string]any
	if entries, err := os.ReadDir(dirs.Groups); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".json") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dirs.Groups, e.Name()))
			if err != nil {
				continue
			}
			var group map[string]any
			if err := json.Unmarshal(data, &group); err != nil {
				continue
			}
			if meta, ok := group["chat_metadata"].(map[string]any); ok && filter(meta) {
				out = append(out, meta)
			}
			if past, ok := group["past_metadata"].(map[string]any); ok {
				for _, v := range past {
					if meta, ok := v.(map[string]any); ok && filter(meta) {
						out = append(out, meta)
					}
				}
			}
		}
	}
	collectMeta := func(dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		var files []string
		for _, e := range entries {
			if e.IsDir() {
				sub, err := os.ReadDir(filepath.Join(dir, e.Name()))
				if err != nil {
					continue
				}
				for _, f := range sub {
					if !f.IsDir() && strings.EqualFold(filepath.Ext(f.Name()), ".jsonl") {
						files = append(files, filepath.Join(dir, e.Name(), f.Name()))
					}
				}
			} else if strings.EqualFold(filepath.Ext(e.Name()), ".jsonl") {
				files = append(files, filepath.Join(dir, e.Name()))
			}
		}
		for _, f := range files {
			msgs := parseChatMessagesFile(f)
			if len(msgs) > 0 {
				if meta, ok := msgs[0]["chat_metadata"].(map[string]any); ok && filter(meta) {
					out = append(out, meta)
				}
			}
		}
	}
	collectMeta(filepath.Join(dirs.Root, "group chats"))
	collectMeta(dirs.Chats)
	return out
}

func (h *ContentHandler) maidCollectImages(dirs models.UserDirectories) []string {
	var result []string
	hasMedia := func(m map[string]any) bool {
		extra, ok := m["extra"].(map[string]any)
		if !ok {
			return false
		}
		if _, ok := extra["image"].(string); ok {
			return true
		}
		if _, ok := extra["video"].(string); ok {
			return true
		}
		if arr, ok := extra["image_swipes"].([]any); ok && len(arr) > 0 {
			return true
		}
		if arr, ok := extra["media"].([]any); ok && len(arr) > 0 {
			return true
		}
		return false
	}
	known := map[string]bool{}
	for _, m := range allChatMessages(dirs, hasMedia) {
		extra, _ := m["extra"].(map[string]any)
		if s, ok := extra["image"].(string); ok {
			known[s] = true
		}
		if s, ok := extra["video"].(string); ok {
			known[s] = true
		}
		if arr, ok := extra["image_swipes"].([]any); ok {
			for _, s := range arr {
				if str, ok := s.(string); ok {
					known[str] = true
				}
			}
		}
		if arr, ok := extra["media"].([]any); ok {
			for _, item := range arr {
				if mm, ok := item.(map[string]any); ok {
					if u, ok := mm["url"].(string); ok {
						known[u] = true
					}
				}
			}
		}
	}
	for _, meta := range allChatMetadata(dirs, func(m map[string]any) bool {
		if arr, ok := m["chat_backgrounds"].([]any); ok {
			return len(arr) > 0
		}
		return false
	}) {
		if arr, ok := meta["chat_backgrounds"].([]any); ok {
			for _, b := range arr {
				if s, ok := b.(string); ok && s != "" {
					known[s] = true
				}
			}
		}
	}
	knownPaths := map[string]bool{}
	for img := range known {
		if strings.HasPrefix(img, "http") || strings.HasPrefix(img, "data:") {
			continue
		}
		knownPaths[filepath.Join(dirs.Root, filepath.FromSlash(img))] = true
	}
	entries, err := os.ReadDir(dirs.UserImages)
	if err != nil {
		return result
	}
	for _, e := range entries {
		p := filepath.Join(dirs.UserImages, e.Name())
		if e.IsDir() {
			sub, err := os.ReadDir(p)
			if err != nil {
				continue
			}
			for _, f := range sub {
				fp := filepath.Join(p, f.Name())
				if !f.IsDir() && !knownPaths[fp] {
					result = append(result, fp)
				}
			}
			continue
		}
		if !knownPaths[p] {
			result = append(result, p)
		}
	}
	return result
}

func (h *ContentHandler) maidCollectFiles(dirs models.UserDirectories) []string {
	var result []string
	hasFile := func(m map[string]any) bool {
		extra, ok := m["extra"].(map[string]any)
		if !ok {
			return false
		}
		if f, ok := extra["file"].(map[string]any); ok {
			if _, ok := f["url"].(string); ok {
				return true
			}
		}
		if arr, ok := extra["files"].([]any); ok && len(arr) > 0 {
			return true
		}
		return false
	}
	known := map[string]bool{}
	for _, m := range allChatMessages(dirs, hasFile) {
		extra, _ := m["extra"].(map[string]any)
		if f, ok := extra["file"].(map[string]any); ok {
			if u, ok := f["url"].(string); ok {
				known[u] = true
			}
		}
		if arr, ok := extra["files"].([]any); ok {
			for _, item := range arr {
				if mm, ok := item.(map[string]any); ok {
					if u, ok := mm["url"].(string); ok {
						known[u] = true
					}
				}
			}
		}
	}
	for _, meta := range allChatMetadata(dirs, func(m map[string]any) bool {
		if arr, ok := m["attachments"].([]any); ok {
			return len(arr) > 0
		}
		return false
	}) {
		if arr, ok := meta["attachments"].([]any); ok {
			for _, a := range arr {
				if mm, ok := a.(map[string]any); ok {
					if u, ok := mm["url"].(string); ok {
						known[u] = true
					}
				}
			}
		}
	}
	if data, err := os.ReadFile(filepath.Join(dirs.Root, "settings.json")); err == nil {
		var settings map[string]any
		if json.Unmarshal(data, &settings) == nil {
			if es, ok := settings["extension_settings"].(map[string]any); ok {
				if arr, ok := es["attachments"].([]any); ok {
					for _, item := range arr {
						if mm, ok := item.(map[string]any); ok {
							if u, ok := mm["url"].(string); ok {
								known[u] = true
							}
						}
					}
				}
				if ca, ok := es["character_attachments"].(map[string]any); ok {
					for _, v := range ca {
						if arr, ok := v.([]any); ok {
							for _, item := range arr {
								if mm, ok := item.(map[string]any); ok {
									if u, ok := mm["url"].(string); ok {
										known[u] = true
									}
								}
							}
						}
					}
				}
			}
		}
	}
	knownPaths := map[string]bool{}
	for f := range known {
		knownPaths[filepath.Join(dirs.Root, filepath.FromSlash(f))] = true
	}
	entries, err := os.ReadDir(dirs.Files)
	if err != nil {
		return result
	}
	for _, e := range entries {
		p := filepath.Join(dirs.Files, e.Name())
		if !e.IsDir() && !knownPaths[p] {
			result = append(result, p)
		}
	}
	return result
}

func (h *ContentHandler) maidCollectChats(dirs models.UserDirectories) []string {
	var result []string
	known := map[string]bool{}
	if entries, err := os.ReadDir(dirs.Characters); err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".png") {
				known[strings.TrimSuffix(e.Name(), ".png")] = true
			}
		}
	}
	if entries, err := os.ReadDir(dirs.Chats); err == nil {
		for _, d := range entries {
			if !d.IsDir() || known[d.Name()] {
				continue
			}
			sub, err := os.ReadDir(filepath.Join(dirs.Chats, d.Name()))
			if err != nil {
				continue
			}
			for _, f := range sub {
				if !f.IsDir() && strings.EqualFold(filepath.Ext(f.Name()), ".jsonl") {
					result = append(result, filepath.Join(dirs.Chats, d.Name(), f.Name()))
				}
			}
		}
	}
	return result
}

func (h *ContentHandler) maidCollectGroupChats(dirs models.UserDirectories) []string {
	var result []string
	known := map[string]bool{}
	if entries, err := os.ReadDir(dirs.Groups); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".json") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dirs.Groups, e.Name()))
			if err != nil {
				continue
			}
			var group map[string]any
			if err := json.Unmarshal(data, &group); err != nil {
				continue
			}
			if id, ok := group["chat_id"].(string); ok {
				known[id] = true
			}
			if arr, ok := group["chats"].([]any); ok {
				for _, c := range arr {
					if s, ok := c.(string); ok {
						known[s] = true
					}
				}
			}
		}
	}
	groupChatsDir := filepath.Join(dirs.Root, "group chats")
	if entries, err := os.ReadDir(groupChatsDir); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".jsonl") {
				continue
			}
			if !known[strings.TrimSuffix(e.Name(), ".jsonl")] {
				result = append(result, filepath.Join(groupChatsDir, e.Name()))
			}
		}
	}
	return result
}

func maidCollectOrphans(sourceDir, thumbDir string) []string {
	var result []string
	known := map[string]bool{}
	if entries, err := os.ReadDir(sourceDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				known[e.Name()] = true
			}
		}
	}
	if entries, err := os.ReadDir(thumbDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() && !known[e.Name()] {
				result = append(result, filepath.Join(thumbDir, e.Name()))
			}
		}
	}
	return result
}

func maidCollectBackups(backupsDir, prefix string) []string {
	var result []string
	entries, err := os.ReadDir(backupsDir)
	if err != nil {
		return result
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), prefix) {
			result = append(result, filepath.Join(backupsDir, e.Name()))
		}
	}
	return result
}

func (h *ContentHandler) maidGenerateReport(uc *models.UserContext) maidReport {
	thumbs := filepath.Join(uc.Directories.Root, "thumbnails")
	return maidReport{
		Images:               h.maidCollectImages(uc.Directories),
		Files:                h.maidCollectFiles(uc.Directories),
		Chats:                h.maidCollectChats(uc.Directories),
		GroupChats:           h.maidCollectGroupChats(uc.Directories),
		AvatarThumbnails:     maidCollectOrphans(uc.Directories.Characters, filepath.Join(thumbs, "avatar")),
		BackgroundThumbnails: maidCollectOrphans(uc.Directories.Backgrounds, filepath.Join(thumbs, "bg")),
		PersonaThumbnails:    maidCollectOrphans(uc.Directories.Avatars, filepath.Join(thumbs, "persona")),
		ChatBackups:          maidCollectBackups(uc.Directories.Backups, "chat_"),
		SettingsBackups:      maidCollectBackups(uc.Directories.Backups, "settings_"+uc.Profile.Handle+"_"),
	}
}

func maidSanitizeList(paths []string, withParent bool) []maidRecord {
	out := make([]maidRecord, 0, len(paths))
	for _, p := range paths {
		out = append(out, sanitizeMaidRecord(p, withParent))
	}
	return out
}

func (h *ContentHandler) MaidReport(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	raw := h.maidGenerateReport(uc)
	report := map[string]any{
		"images":               maidSanitizeList(raw.Images, true),
		"files":                maidSanitizeList(raw.Files, false),
		"chats":                maidSanitizeList(raw.Chats, true),
		"groupChats":           maidSanitizeList(raw.GroupChats, false),
		"avatarThumbnails":     maidSanitizeList(raw.AvatarThumbnails, false),
		"backgroundThumbnails": maidSanitizeList(raw.BackgroundThumbnails, false),
		"personaThumbnails":    maidSanitizeList(raw.PersonaThumbnails, false),
		"chatBackups":          maidSanitizeList(raw.ChatBackups, false),
		"settingsBackups":      maidSanitizeList(raw.SettingsBackups, false),
	}
	all := append([]string{}, raw.Images...)
	all = append(all, raw.Files...)
	all = append(all, raw.Chats...)
	all = append(all, raw.GroupChats...)
	all = append(all, raw.AvatarThumbnails...)
	all = append(all, raw.BackgroundThumbnails...)
	all = append(all, raw.PersonaThumbnails...)
	all = append(all, raw.ChatBackups...)
	all = append(all, raw.SettingsBackups...)
	token := h.maidMintToken(uc.Profile.Handle, all)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"report": report, "token": token})
}

func (h *ContentHandler) maidMintToken(handle string, paths []string) string {
	h.maidMu.Lock()
	defer h.maidMu.Unlock()
	for token, entry := range h.maidTokens {
		if entry.handle == handle {
			delete(h.maidTokens, token)
		}
	}
	var b [32]byte
	_, _ = randRead(b[:])
	token := fmt.Sprintf("%x", b)
	var list []maidPath
	for _, p := range paths {
		list = append(list, maidPath{path: p, hash: sha256Hex(p)})
	}
	h.maidTokens[token] = maidToken{handle: handle, paths: list}
	return token
}

func (h *ContentHandler) maidLookup(r *http.Request, token string) (maidToken, bool) {
	uc := userCtx(r)
	if uc == nil {
		return maidToken{}, false
	}
	h.maidMu.Lock()
	defer h.maidMu.Unlock()
	entry, ok := h.maidTokens[token]
	if !ok || entry.handle != uc.Profile.Handle {
		return maidToken{}, false
	}
	return entry, true
}

func (h *ContentHandler) MaidFinalize(w http.ResponseWriter, r *http.Request) {
	if userCtx(r) == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body := translateBody(r)
	token, _ := body["token"].(string)
	if token == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	h.maidMu.Lock()
	entry, ok := h.maidTokens[token]
	if !ok {
		h.maidMu.Unlock()
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if uc := userCtx(r); uc == nil || entry.handle != uc.Profile.Handle {
		h.maidMu.Unlock()
		w.WriteHeader(http.StatusForbidden)
		return
	}
	delete(h.maidTokens, token)
	h.maidMu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (h *ContentHandler) MaidView(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	q := r.URL.Query()
	token, hash := q.Get("token"), q.Get("hash")
	if token == "" || hash == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	entry, ok := h.maidLookup(r, token)
	if !ok {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	var target string
	for _, p := range entry.paths {
		if p.hash == hash {
			target = p.path
			break
		}
	}
	if target == "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	absBase, _ := filepath.Abs(uc.Directories.Root)
	absTarget, _ := filepath.Abs(target)
	if absTarget != absBase && !strings.HasPrefix(absTarget, absBase+string(filepath.Separator)) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if _, err := os.Stat(absTarget); err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	data, err := os.ReadFile(absTarget)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", mimeForPath(absTarget))
	_, _ = w.Write(data)
}

func (h *ContentHandler) MaidDelete(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body := translateBody(r)
	token, _ := body["token"].(string)
	rawHashes, _ := body["hashes"].([]any)
	if token == "" || len(rawHashes) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	entry, ok := h.maidLookup(r, token)
	if !ok {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	absBase, _ := filepath.Abs(uc.Directories.Root)
	for _, rh := range rawHashes {
		hash, _ := rh.(string)
		for _, p := range entry.paths {
			if p.hash != hash {
				continue
			}
			absTarget, _ := filepath.Abs(p.path)
			if absTarget != absBase && !strings.HasPrefix(absTarget, absBase+string(filepath.Separator)) {
				continue
			}
			_ = os.Remove(absTarget)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
