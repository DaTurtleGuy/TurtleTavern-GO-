package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/TurtleTavern/turtletavern/internal/auth"
	"github.com/TurtleTavern/turtletavern/internal/character"
	"github.com/TurtleTavern/turtletavern/internal/config"
	"github.com/TurtleTavern/turtletavern/internal/maintenance"
	"github.com/TurtleTavern/turtletavern/internal/remotebackup"
	"github.com/TurtleTavern/turtletavern/internal/secrets"
	"github.com/go-chi/chi/v5"
)

// Secret keys holding the per-user Backupper destination, set from the UI.
const (
	SecretBackupperURL = "backupper_url"
	SecretBackupperKey = "api_key_backupper"
)

// dataOperationMu serialises anything that replaces or reads a whole user data
// root (local restore, remote sync, remote restore) so two swaps can never
// interleave.
var dataOperationMu sync.Mutex

type RemoteBackupHandler struct {
	Cfg   *config.Config
	Index *character.Index

	mu        sync.Mutex
	busy      bool
	phase     string
	files     int64
	total     int64
	bytes     int64
	started   time.Time
	lastError string
	lastSync  time.Time
	lastSnap  string

	lastScanned       int
	lastPresent       int
	lastUploaded      int
	lastUploadedBytes int64
	lastDuplicate     bool

	probeAt   time.Time
	probeOK   bool
	probeVer  string
	probeCaps remotebackup.Capabilities
}

func NewRemoteBackupHandler(cfg *config.Config, idx *character.Index) *RemoteBackupHandler {
	return &RemoteBackupHandler{Cfg: cfg, Index: idx, phase: "idle"}
}

func (h *RemoteBackupHandler) RegisterRoutes(r chi.Router) {
	r.Route("/api/remote", func(r chi.Router) {
		r.Get("/status", h.Status)
		r.Post("/sync", h.Sync)
		r.Get("/snapshots", h.Snapshots)
		r.Post("/restore", h.Restore)
	})
}

// client builds a Backupper client from the user's stored secrets. The URL and
// device key are per-user so several accounts can target different vaults.
func (h *RemoteBackupHandler) client(root string) *remotebackup.Client {
	m := secrets.UserManager(root, h.Cfg.AllowKeysExposure)
	url := m.ReadSecret(SecretBackupperURL, "")
	key := m.ReadSecret(SecretBackupperKey, "")
	if url == "" || key == "" {
		return nil
	}
	return remotebackup.NewClient(url, key, h.Cfg.RemoteBackup.Timeout())
}

func (h *RemoteBackupHandler) deviceName() string {
	if name := strings.TrimSpace(h.Cfg.RemoteBackup.DeviceName); name != "" {
		return name
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "turtletavern"
	}
	return host
}

func (h *RemoteBackupHandler) setPhase(phase string, done, total, bytes int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.phase = phase
	h.files = done
	h.total = total
	h.bytes = bytes
}

func (h *RemoteBackupHandler) finish(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.busy = false
	h.phase = "idle"
	if err != nil {
		h.lastError = err.Error()
	}
}

// probe caches a health/capabilities check so status polling stays cheap.
func (h *RemoteBackupHandler) probe(c *remotebackup.Client) (bool, string, remotebackup.Capabilities) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if time.Since(h.probeAt) < 10*time.Second {
		return h.probeOK, h.probeVer, h.probeCaps
	}
	h.probeAt = time.Now()
	caps, err := c.Capabilities()
	if err != nil {
		h.probeOK, h.probeVer, h.probeCaps = false, "", remotebackup.Capabilities{}
		return false, "", remotebackup.Capabilities{}
	}
	h.probeOK, h.probeVer, h.probeCaps = true, caps.Version, caps
	return true, caps.Version, caps
}

func (h *RemoteBackupHandler) Status(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		writeUserDataError(w, http.StatusForbidden, "Forbidden")
		return
	}

	out := map[string]any{
		"available":  h.Cfg.RemoteBackup.Enabled,
		"configured": false,
		"reachable":  false,
		"device":     h.deviceName(),
		"timeout":    int(h.Cfg.RemoteBackup.Timeout().Seconds()),
	}

	m := secrets.UserManager(uc.Directories.Root, h.Cfg.AllowKeysExposure)
	url := m.ReadSecret(SecretBackupperURL, "")
	key := m.ReadSecret(SecretBackupperKey, "")
	out["url"] = url
	out["configured"] = url != "" && key != ""

	if c := h.client(uc.Directories.Root); c != nil {
		ok, ver, caps := h.probe(c)
		out["reachable"] = ok
		out["serverVersion"] = ver
		out["protocol"] = caps.Protocol
		out["capabilities"] = map[string]any{
			"maxFileBytes":  caps.MaxFileBytes,
			"maxTotalBytes": caps.MaxTotalBytes,
			"storedBytes":   caps.StoredBytes,
			"snapshots":     caps.Snapshots,
		}
	}

	h.mu.Lock()
	out["busy"] = h.busy
	out["phase"] = h.phase
	out["progress"] = map[string]any{"files": h.files, "total": h.total, "bytes": h.bytes}
	out["lastError"] = h.lastError
	if !h.lastSync.IsZero() {
		out["lastSyncAt"] = h.lastSync.UTC().Format(time.RFC3339)
		out["lastSync"] = map[string]any{
			"scanned":       h.lastScanned,
			"present":       h.lastPresent,
			"uploaded":      h.lastUploaded,
			"uploadedBytes": h.lastUploadedBytes,
			"duplicate":     h.lastDuplicate,
			"snapshotId":    h.lastSnap,
		}
	}
	out["lastSnapshotId"] = h.lastSnap
	h.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(out)
}

func (h *RemoteBackupHandler) Snapshots(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		writeUserDataError(w, http.StatusForbidden, "Forbidden")
		return
	}
	c := h.client(uc.Directories.Root)
	if c == nil {
		writeUserDataError(w, http.StatusBadRequest, "Remote backup is not configured yet")
		return
	}
	snaps, err := c.ListSnapshots()
	if err != nil {
		writeUserDataError(w, http.StatusBadGateway, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"snapshots": snaps})
}

func (h *RemoteBackupHandler) Sync(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		writeUserDataError(w, http.StatusForbidden, "Forbidden")
		return
	}
	c := h.client(uc.Directories.Root)
	if c == nil {
		writeUserDataError(w, http.StatusBadRequest, "Remote backup is not configured yet")
		return
	}

	var body struct {
		Label          string `json:"label"`
		IncludeKeys    bool   `json:"includeKeys"`
		IncludeBackups bool   `json:"includeBackups"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body)
	}
	if body.IncludeKeys && !h.Cfg.AllowKeysExposure {
		writeUserDataError(w, http.StatusBadRequest, "API key export is disabled on this server")
		return
	}

	if !dataOperationMu.TryLock() {
		writeUserDataError(w, http.StatusConflict, "Another backup or restore is already running")
		return
	}
	h.mu.Lock()
	if h.busy {
		h.mu.Unlock()
		dataOperationMu.Unlock()
		writeUserDataError(w, http.StatusConflict, "A remote operation is already running")
		return
	}
	h.busy, h.phase, h.lastError = true, "starting", ""
	h.started = time.Now()
	h.mu.Unlock()

	root := uc.Directories.Root
	handle := uc.Profile.Handle
	go func() {
		defer dataOperationMu.Unlock()
		res, err := c.Sync(remotebackup.SyncOptions{
			Root:           root,
			Meta:           remotebackup.Meta{Device: h.deviceName(), Handle: handle, AppVersion: buildPkgVersion, Label: body.Label},
			IncludeKeys:    body.IncludeKeys,
			IncludeBackups: body.IncludeBackups,
			Progress:       h.setPhase,
		})
		h.mu.Lock()
		if err == nil {
			h.lastSync = time.Now()
			h.lastSnap = res.Snapshot.ID
			h.lastScanned = res.Scanned
			h.lastPresent = res.Present
			h.lastUploaded = res.Uploaded
			h.lastUploadedBytes = res.UploadedBytes
			h.lastDuplicate = res.Duplicate
		}
		h.mu.Unlock()
		h.finish(err)
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{"started": true})
}

func (h *RemoteBackupHandler) Restore(w http.ResponseWriter, r *http.Request) {
	uc := userCtx(r)
	if uc == nil {
		writeUserDataError(w, http.StatusForbidden, "Forbidden")
		return
	}
	c := h.client(uc.Directories.Root)
	if c == nil {
		writeUserDataError(w, http.StatusBadRequest, "Remote backup is not configured yet")
		return
	}

	var body struct {
		ID              string `json:"id"`
		Confirm         string `json:"confirm"`
		SkipPreSnapshot bool   `json:"skipPreSnapshot"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeUserDataError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.ID == "" {
		writeUserDataError(w, http.StatusBadRequest, "a snapshot id is required")
		return
	}
	if body.Confirm != body.ID {
		writeUserDataError(w, http.StatusBadRequest, "type the snapshot id to confirm the restore")
		return
	}

	if !dataOperationMu.TryLock() {
		writeUserDataError(w, http.StatusConflict, "Another backup or restore is already running")
		return
	}
	h.mu.Lock()
	if h.busy {
		h.mu.Unlock()
		dataOperationMu.Unlock()
		writeUserDataError(w, http.StatusConflict, "A remote operation is already running")
		return
	}
	h.busy, h.phase, h.lastError = true, "preparing", ""
	h.mu.Unlock()

	root := uc.Directories.Root
	dataRoot := filepath.Dir(root)
	handle := uc.Profile.Handle

	go func() {
		defer dataOperationMu.Unlock()
		h.finish(h.runRestore(c, uc.Directories.Root, dataRoot, handle, body.ID, body.SkipPreSnapshot))
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{"started": true})
}

// runRestore downloads a snapshot into a staging directory and atomically swaps
// it in for the live user root, after taking a safety snapshot of the current
// state.
func (h *RemoteBackupHandler) runRestore(c *remotebackup.Client, root, dataRoot, handle, id string, skipPreSnapshot bool) error {
	snap, err := c.GetSnapshot(id)
	if err != nil {
		return fmt.Errorf("cannot read snapshot %s: %w", id, err)
	}
	if len(snap.Entries) == 0 {
		return fmt.Errorf("snapshot %s has no files", id)
	}

	if !skipPreSnapshot {
		h.setPhase("pre-snapshot", 0, 0, 0)
		if _, err := c.Sync(remotebackup.SyncOptions{
			Root:     root,
			Meta:     remotebackup.Meta{Device: h.deviceName(), Handle: handle, AppVersion: buildPkgVersion, Label: "pre-restore"},
			Progress: h.setPhase,
		}); err != nil {
			return fmt.Errorf("could not record a safety snapshot before restoring: %w", err)
		}
	}

	h.setPhase("staging", 0, int64(len(snap.Entries)), 0)
	staging, err := os.MkdirTemp(dataRoot, "."+handle+".remote-restore-")
	if err != nil {
		return fmt.Errorf("cannot create a staging directory: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(staging)
		}
	}()

	if err := h.stageSnapshot(c, snap.Entries, staging); err != nil {
		return err
	}

	// A snapshot captured without includeKeys contains no secrets.json. Carry
	// the live one across so restoring cannot silently delete the user's API
	// keys - including this feature's own Backupper credentials, which would
	// otherwise disconnect remote backup the moment it is used.
	if !snapshotHasFile(snap.Entries, secretsEntry) {
		if data, err := os.ReadFile(filepath.Join(root, secretsEntry)); err == nil {
			if err := os.WriteFile(filepath.Join(staging, secretsEntry), data, 0o600); err != nil {
				return fmt.Errorf("cannot preserve %s across the restore", secretsEntry)
			}
		}
	}

	h.setPhase("swapping", int64(len(snap.Entries)), int64(len(snap.Entries)), 0)
	maintenance.Begin()
	err = swapUserRoot(root, staging)
	maintenance.End()
	if err != nil {
		return err
	}
	committed = true

	auth.EnsureUserDirs(dataRoot, handle)
	if h.Index != nil {
		h.Index.ClearUserIndex(filepath.Join(root, "characters"))
	}
	return nil
}

// snapshotHasFile reports whether the snapshot carries a given relative path.
func snapshotHasFile(entries []remotebackup.Entry, path string) bool {
	for _, e := range entries {
		if e.Path == path {
			return true
		}
	}
	return false
}

// stageSnapshot writes every entry into destRoot, verifying each file's SHA-256
// and applying the same path hardening the local restore uses.
func (h *RemoteBackupHandler) stageSnapshot(c *remotebackup.Client, entries []remotebackup.Entry, destRoot string) error {
	var done int64
	var total int64
	renamed := make(map[string]string)

	for _, e := range entries {
		rel, err := safeRelativePath(e.Path)
		if err != nil {
			return err
		}
		if runtime.GOOS == "windows" {
			rel = sanitizeExtractPath(rel)
		}
		dest := filepath.Join(destRoot, rel)
		if existing, seen := renamed[dest]; seen {
			dest = existing
		} else if _, err := os.Lstat(dest); err == nil {
			ok := false
			dest, ok = uniqueExtractPath(dest)
			if !ok {
				continue
			}
		} else {
			renamed[dest] = dest
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return fmt.Errorf("cannot create a directory for %s", e.Path)
		}

		rc, _, err := c.OpenFile(e.Hash)
		if err != nil {
			return fmt.Errorf("cannot download %s: %w", e.Path, err)
		}
		tmp := dest + ".part"
		f, err := os.Create(tmp)
		if err != nil {
			rc.Close()
			return fmt.Errorf("cannot write %s", e.Path)
		}
		hsh := sha256.New()
		n, err := io.Copy(io.MultiWriter(f, hsh), rc)
		rc.Close()
		closeErr := f.Close()
		if err != nil {
			os.Remove(tmp)
			return fmt.Errorf("failed while writing %s", e.Path)
		}
		if closeErr != nil {
			os.Remove(tmp)
			return fmt.Errorf("failed while writing %s", e.Path)
		}
		if !strings.EqualFold(e.Hash, hex.EncodeToString(hsh.Sum(nil))) {
			os.Remove(tmp)
			return fmt.Errorf("integrity check failed for %s", e.Path)
		}
		if e.Mode != 0 && runtime.GOOS != "windows" {
			_ = os.Chmod(tmp, os.FileMode(e.Mode))
		}
		if err := os.Rename(tmp, dest); err != nil {
			os.Remove(tmp)
			return fmt.Errorf("cannot finish %s", e.Path)
		}

		total += n
		done++
		h.setPhase("staging", done, int64(len(entries)), total)
	}
	return nil
}
