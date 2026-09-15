package handlers

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/TurtleTavern/turtletavern/internal/auth"
	"github.com/TurtleTavern/turtletavern/internal/character"
	"github.com/TurtleTavern/turtletavern/internal/config"
	"github.com/TurtleTavern/turtletavern/internal/maintenance"
	"github.com/TurtleTavern/turtletavern/internal/util"
	"github.com/go-chi/chi/v5"
)

const (
	backupFormat        = "turtletavern-backup"
	backupFormatVersion = 1

	backupManifestEntry  = "manifest.json"
	backupChecksumsEntry = "checksums.sha256"

	secretsEntry = "secrets.json"
	backupsEntry = "backups"

	restoreMultipartField = "file"

	// restoreMaxUncompressedBytes is a decompression-bomb ceiling for a single
	// restore. It bounds what an archive can write to disk regardless of the
	// compressed size the client sent.
	restoreMaxUncompressedBytes = int64(1) << 40

	// restoreMaxChecksumBytes bounds the in-memory checksum table.
	restoreMaxChecksumBytes = 96 << 20
)

type backupManifest struct {
	Format         string `json:"format"`
	FormatVersion  int    `json:"formatVersion"`
	AppVersion     string `json:"appVersion"`
	ExportedAt     string `json:"exportedAt"`
	Handle         string `json:"handle"`
	IncludeKeys    bool   `json:"includeKeys"`
	IncludeBackups bool   `json:"includeBackups"`
	FileCount      int    `json:"fileCount"`
	TotalBytes     int64  `json:"totalBytes"`
}

type UserDataHandler struct {
	Cfg   *config.Config
	Index *character.Index
	prog  *restoreProgress
}

func NewUserDataHandler(cfg *config.Config, idx *character.Index) *UserDataHandler {
	return &UserDataHandler{Cfg: cfg, Index: idx, prog: &restoreProgress{}}
}

func (h *UserDataHandler) RegisterRoutes(r chi.Router) {
	r.Get("/api/users/backup", h.Export)
	r.Get("/api/users/backup/capabilities", h.Capabilities)
	r.Post("/api/users/restore", h.Restore)
	r.Get("/api/users/restore/status", h.RestoreStatus)
}

func (h *UserDataHandler) Capabilities(w http.ResponseWriter, r *http.Request) {
	if userCtx(r) == nil {
		writeUserDataError(w, http.StatusForbidden, "Forbidden")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{
		"allowFullDataBackup": h.Cfg.Backups.AllowFullDataBackup,
		"allowKeysExposure":   h.Cfg.AllowKeysExposure,
	})
}

// Export streams the current user's data as a zip directly to the response.
// Nothing is buffered server-side: files are copied straight into the archive
// writer, so memory stays flat regardless of profile size.
//
// It is a GET so the browser (or the Android native layer) can perform a native
// streaming download instead of buffering the whole archive in memory.
func (h *UserDataHandler) Export(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		writeUserDataError(w, http.StatusForbidden, "Forbidden")
		return
	}
	if !h.Cfg.Backups.AllowFullDataBackup {
		writeUserDataError(w, http.StatusForbidden, "Full data backup is disabled")
		return
	}

	includeKeys := truthyQuery(r, "includeKeys")
	includeBackups := truthyQuery(r, "includeBackups")
	if includeKeys && !h.Cfg.AllowKeysExposure {
		writeUserDataError(w, http.StatusBadRequest, "API key export is disabled on this server")
		return
	}

	handle := uc.Profile.Handle
	if requested := strings.TrimSpace(r.URL.Query().Get("handle")); requested != "" && requested != handle {
		if !uc.Profile.Admin || !validUserHandle(requested) {
			writeUserDataError(w, http.StatusForbidden, "Forbidden")
			return
		}
		handle = requested
	}
	root := auth.UserRoot(h.Cfg.DataDir(), handle)

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-%s.zip"`, handle, time.Now().Format("20060102-150405")))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")

	zw := zip.NewWriter(w)

	var (
		checksums  strings.Builder
		fileCount  int
		totalBytes int64
	)

	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel == backupManifestEntry || rel == backupChecksumsEntry {
			return nil
		}
		if rel == secretsEntry && !includeKeys {
			return nil
		}
		if !includeBackups && (rel == backupsEntry || strings.HasPrefix(rel, backupsEntry+"/")) {
			return nil
		}

		f, err := os.Open(p)
		if err != nil {
			return nil
		}
		info, err := f.Stat()
		if err != nil {
			f.Close()
			return nil
		}
		fw, err := zw.CreateHeader(&zip.FileHeader{
			Name:     rel,
			Method:   methodForPath(rel),
			Modified: info.ModTime(),
		})
		if err != nil {
			f.Close()
			return err
		}
		hsh := sha256.New()
		n, err := io.Copy(io.MultiWriter(fw, hsh), f)
		f.Close()
		if err != nil {
			return err
		}

		fileCount++
		totalBytes += n
		checksums.WriteString(hex.EncodeToString(hsh.Sum(nil)))
		checksums.WriteString("  ")
		checksums.WriteString(rel)
		checksums.WriteByte('\n')
		return nil
	})
	if walkErr != nil {
		_ = zw.Close()
		return
	}

	if cw, err := zw.CreateHeader(&zip.FileHeader{Name: backupChecksumsEntry, Method: zip.Deflate}); err == nil {
		_, _ = io.WriteString(cw, checksums.String())
	}
	manifest := backupManifest{
		Format:         backupFormat,
		FormatVersion:  backupFormatVersion,
		AppVersion:     buildPkgVersion,
		ExportedAt:     time.Now().UTC().Format(time.RFC3339),
		Handle:         handle,
		IncludeKeys:    includeKeys,
		IncludeBackups: includeBackups,
		FileCount:      fileCount,
		TotalBytes:     totalBytes,
	}
	if mw, err := zw.CreateHeader(&zip.FileHeader{Name: backupManifestEntry, Method: zip.Deflate}); err == nil {
		_ = json.NewEncoder(mw).Encode(manifest)
	}
	_ = zw.Close()
}

// Restore accepts a multipart upload, spools it to a temp file, validates it,
// extracts it into a staging directory and atomically swaps it in for the live
// user folder. The upload and extraction are both streamed.
func (h *UserDataHandler) Restore(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		writeUserDataError(w, http.StatusForbidden, "Forbidden")
		return
	}
	if !h.Cfg.Backups.AllowFullDataBackup {
		writeUserDataError(w, http.StatusForbidden, "Full data backup is disabled")
		return
	}
	if !h.prog.begin() {
		writeUserDataError(w, http.StatusConflict, "A restore is already in progress")
		return
	}
	defer h.prog.end()

	mr, err := r.MultipartReader()
	if err != nil {
		writeUserDataError(w, http.StatusBadRequest, "Expected a multipart upload")
		return
	}
	zipPath, err := spoolUpload(mr)
	if err != nil {
		writeUserDataError(w, http.StatusBadRequest, err.Error())
		return
	}
	defer os.Remove(zipPath)

	root := uc.Directories.Root
	dataRoot := filepath.Dir(root)

	h.prog.setPhase("validating", 0, 0)
	staging, err := os.MkdirTemp(dataRoot, "."+uc.Profile.Handle+".restore-")
	if err != nil {
		writeUserDataError(w, http.StatusInternalServerError, "Cannot create staging directory")
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(staging)
		}
	}()

	stats, err := h.extractArchive(zipPath, staging)
	if err != nil {
		writeUserDataError(w, http.StatusBadRequest, err.Error())
		return
	}

	h.prog.setPhase("swapping", int64(stats.Files), int64(stats.Files))
	maintenance.Begin()
	err = swapUserRoot(root, staging)
	maintenance.End()
	if err != nil {
		writeUserDataError(w, http.StatusInternalServerError, err.Error())
		return
	}
	committed = true

	auth.EnsureUserDirs(dataRoot, uc.Profile.Handle)
	if h.Index != nil {
		h.Index.ClearUserIndex(filepath.Join(root, "characters"))
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":    true,
		"files": stats.Files,
		"bytes": stats.Bytes,
	})
}

func (h *UserDataHandler) RestoreStatus(w http.ResponseWriter, r *http.Request) {
	if userCtx(r) == nil {
		writeUserDataError(w, http.StatusForbidden, "Forbidden")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(h.prog.snapshot())
}

type extractStats struct {
	Files int
	Bytes int64
}

func (h *UserDataHandler) extractArchive(zipPath, destRoot string) (extractStats, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return extractStats{}, fmt.Errorf("the uploaded file is not a valid zip archive")
	}
	defer zr.Close()

	manifest, err := readManifest(zr)
	if err != nil {
		return extractStats{}, err
	}
	if manifest.FormatVersion > backupFormatVersion {
		return extractStats{}, fmt.Errorf("this backup was made by a newer version (format %d)", manifest.FormatVersion)
	}
	checksums, err := readChecksums(zr)
	if err != nil {
		return extractStats{}, err
	}

	h.prog.setPhase("restoring", int64(manifest.FileCount), 0)

	var stats extractStats
	var total int64
	renamed := make(map[string]string)
	for _, f := range zr.File {
		if f.Name == backupManifestEntry || f.Name == backupChecksumsEntry || f.FileInfo().IsDir() {
			continue
		}
		rel, err := safeRelativePath(f.Name)
		if err != nil {
			return extractStats{}, err
		}
		want, ok := checksums[filepath.ToSlash(rel)]
		if !ok {
			return extractStats{}, fmt.Errorf("archive contains an unexpected file: %s", f.Name)
		}
		if runtime.GOOS == "windows" {
			// Archive entries written on Linux/Android may contain characters
			// that are illegal in Windows file names; sanitize each segment.
			// Content hashes are unaffected by file names.
			rel = sanitizeExtractPath(rel)
		}
		dest := filepath.Join(destRoot, rel)
		if existing, seen := renamed[dest]; seen {
			dest = existing
		} else if _, err := os.Lstat(dest); err == nil {
			// Fresh staging dir, so only sanitized renames can collide —
			// never overwrite an already-written file.
			dest, ok = uniqueExtractPath(dest)
			if !ok {
				continue
			}
		} else {
			renamed[dest] = dest
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return extractStats{}, fmt.Errorf("cannot create directory for %s", f.Name)
		}
		rc, err := f.Open()
		if err != nil {
			return extractStats{}, fmt.Errorf("cannot read %s from the archive", f.Name)
		}
		out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			rc.Close()
			return extractStats{}, fmt.Errorf("cannot write %s", f.Name)
		}
		hsh := sha256.New()
		remaining := restoreMaxUncompressedBytes - total
		n, err := io.Copy(io.MultiWriter(out, hsh), io.LimitReader(rc, remaining+1))
		rc.Close()
		out.Close()
		if err != nil {
			return extractStats{}, fmt.Errorf("failed while extracting %s", f.Name)
		}
		if n > remaining {
			return extractStats{}, fmt.Errorf("archive exceeds the maximum uncompressed size")
		}
		total += n
		if !strings.EqualFold(want, hex.EncodeToString(hsh.Sum(nil))) {
			return extractStats{}, fmt.Errorf("integrity check failed for %s", f.Name)
		}
		if !f.Modified.IsZero() {
			_ = os.Chtimes(dest, f.Modified, f.Modified)
		}
		stats.Files++
		stats.Bytes += n
		h.prog.setFiles(int64(stats.Files), total)
	}

	if manifest.FileCount != 0 && stats.Files != manifest.FileCount {
		return extractStats{}, fmt.Errorf("archive is incomplete: expected %d files, found %d", manifest.FileCount, stats.Files)
	}
	return stats, nil
}

func readManifest(zr *zip.ReadCloser) (backupManifest, error) {
	for _, f := range zr.File {
		if f.Name != backupManifestEntry {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return backupManifest{}, fmt.Errorf("cannot read the backup manifest")
		}
		defer rc.Close()
		var m backupManifest
		if err := json.NewDecoder(io.LimitReader(rc, 1<<20)).Decode(&m); err != nil {
			return backupManifest{}, fmt.Errorf("the backup manifest is corrupt")
		}
		if m.Format != backupFormat {
			return backupManifest{}, fmt.Errorf("this zip is not a TurtleTavern backup")
		}
		return m, nil
	}
	return backupManifest{}, fmt.Errorf("this zip is not a TurtleTavern backup (no manifest)")
}

func readChecksums(zr *zip.ReadCloser) (map[string]string, error) {
	for _, f := range zr.File {
		if f.Name != backupChecksumsEntry {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("cannot read the checksum table")
		}
		defer rc.Close()
		data, err := io.ReadAll(io.LimitReader(rc, restoreMaxChecksumBytes+1))
		if err != nil {
			return nil, fmt.Errorf("cannot read the checksum table")
		}
		if int64(len(data)) > restoreMaxChecksumBytes {
			return nil, fmt.Errorf("the checksum table is too large")
		}
		out := make(map[string]string)
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimRight(line, "\r")
			if line == "" {
				continue
			}
			parts := strings.SplitN(line, "  ", 2)
			if len(parts) != 2 || len(parts[0]) != 64 {
				return nil, fmt.Errorf("the checksum table is malformed")
			}
			out[parts[1]] = parts[0]
		}
		return out, nil
	}
	return nil, fmt.Errorf("the backup has no checksum table")
}

// safeRelativePath rejects absolute paths, drive letters and any traversal so
// a crafted archive can never write outside the extraction root.
func safeRelativePath(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("archive contains an empty file name")
	}
	slashed := strings.ReplaceAll(name, "\\", "/")
	if strings.HasPrefix(slashed, "/") {
		return "", fmt.Errorf("archive contains an absolute path: %s", name)
	}
	clean := path.Clean(slashed)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("archive contains a path traversal: %s", name)
	}
	rel := filepath.FromSlash(clean)
	if filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" {
		return "", fmt.Errorf("archive contains an invalid path: %s", name)
	}
	return rel, nil
}

// sanitizeExtractPath replaces illegal Windows filename characters in every
// segment of a path extracted from a backup archive; separators and hash
// verification are unaffected. Linux-written names like "Love <3.jsonl"
// become "Love _3.jsonl".
func sanitizeExtractPath(rel string) string {
	segs := strings.Split(rel, string(filepath.Separator))
	for i, seg := range segs {
		if seg == "" || seg == "." || seg == ".." {
			continue
		}
		if s := util.SanitizeFileName(seg); s != seg {
			segs[i] = s
		}
	}
	return strings.Join(segs, string(filepath.Separator))
}

// uniqueExtractPath returns a non-existing sibling path with a " (n)" suffix.
func uniqueExtractPath(dest string) (string, bool) {
	ext := filepath.Ext(dest)
	base := strings.TrimSuffix(dest, ext)
	for i := 1; i < 10000; i++ {
		candidate := fmt.Sprintf("%s (%d)%s", base, i, ext)
		if _, err := os.Lstat(candidate); os.IsNotExist(err) {
			return candidate, true
		}
	}
	return dest, false
}

func swapUserRoot(root, staging string) error {
	old := root + ".restore-old-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	hadLive := false
	if _, err := os.Stat(root); err == nil {
		if err := os.Rename(root, old); err != nil {
			return fmt.Errorf("cannot move the current data aside: %w", err)
		}
		hadLive = true
	}
	if err := os.Rename(staging, root); err != nil {
		if hadLive {
			_ = os.Rename(old, root)
		}
		return fmt.Errorf("cannot activate the restored data: %w", err)
	}
	if hadLive {
		_ = os.RemoveAll(old)
	}
	return nil
}

func spoolUpload(mr *multipart.Reader) (string, error) {
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("the upload was interrupted")
		}
		if part.FormName() != restoreMultipartField {
			part.Close()
			continue
		}
		tmp, err := os.CreateTemp("", "tt-restore-*.zip")
		if err != nil {
			part.Close()
			return "", fmt.Errorf("cannot buffer the upload on the server")
		}
		_, err = io.Copy(tmp, part)
		tmp.Close()
		part.Close()
		if err != nil {
			os.Remove(tmp.Name())
			return "", fmt.Errorf("the upload was interrupted")
		}
		return tmp.Name(), nil
	}
	return "", fmt.Errorf("the upload did not contain a file")
}

func methodForPath(name string) uint16 {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".avif", ".bmp", ".ico",
		".zip", ".gz", ".bz2", ".xz", ".7z", ".rar",
		".mp3", ".mp4", ".m4a", ".ogg", ".oga", ".opus", ".flac", ".wav", ".webm", ".mkv",
		".woff", ".woff2", ".ttf", ".otf", ".pdf":
		return zip.Store
	default:
		return zip.Deflate
	}
}

func truthyQuery(r *http.Request, key string) bool {
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get(key))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func validUserHandle(handle string) bool {
	if handle == "" || handle == "." || handle == ".." {
		return false
	}
	if strings.ContainsAny(handle, `/\`) {
		return false
	}
	return filepath.Base(handle) == handle
}

func writeUserDataError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

type restoreProgress struct {
	mu      sync.Mutex
	active  bool
	phase   string
	files   int64
	total   int64
	bytes   int64
	started time.Time
}

func (p *restoreProgress) begin() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.active {
		return false
	}
	p.active = true
	p.phase = "receiving"
	p.files = 0
	p.total = 0
	p.bytes = 0
	p.started = time.Now()
	return true
}

func (p *restoreProgress) end() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.active = false
	p.phase = "idle"
}

func (p *restoreProgress) setPhase(phase string, total, files int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.phase = phase
	p.total = total
	p.files = files
}

func (p *restoreProgress) setFiles(files, bytes int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.files = files
	p.bytes = bytes
}

func (p *restoreProgress) snapshot() map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	return map[string]any{
		"active":  p.active,
		"phase":   p.phase,
		"files":   p.files,
		"total":   p.total,
		"bytes":   p.bytes,
		"elapsed": time.Since(p.started).Milliseconds(),
	}
}
