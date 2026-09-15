package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/TurtleTavern/turtletavern/internal/config"
	"github.com/TurtleTavern/turtletavern/internal/content"
	"github.com/TurtleTavern/turtletavern/internal/util"
	"github.com/go-chi/chi/v5"
)

type SettingsHandler struct {
	Cfg        *config.Config
	DefaultDir string
	mu         sync.Mutex
	lastBackup map[string]time.Time
}

func NewSettingsHandler(cfg *config.Config) *SettingsHandler {
	return &SettingsHandler{Cfg: cfg, DefaultDir: content.DefaultDir(), lastBackup: make(map[string]time.Time)}
}

func (h *SettingsHandler) RegisterRoutes(r chi.Router) {
	r.Route("/api/settings", func(r chi.Router) {
		r.Post("/save", h.Save)
		r.Post("/get", h.Get)
		r.Post("/get-snapshots", h.GetSnapshots)
		r.Post("/load-snapshot", h.LoadSnapshot)
		r.Post("/make-snapshot", h.MakeSnapshot)
		r.Post("/restore-snapshot", h.RestoreSnapshot)
	})
	r.Route("/api/presets", func(r chi.Router) {
		r.Post("/save", h.PresetSave)
		r.Post("/delete", h.PresetDelete)
		r.Post("/restore", h.PresetRestore)
	})
	r.Route("/api/themes", func(r chi.Router) {
		r.Post("/save", h.ThemeSave)
		r.Post("/delete", h.ThemeDelete)
	})
	r.Route("/api/moving-ui", func(r chi.Router) {
		r.Post("/save", h.MovingUISave)
	})
	r.Route("/api/quick-replies", func(r chi.Router) {
		r.Post("/save", h.QuickReplySave)
		r.Post("/delete", h.QuickReplyDelete)
	})
}

func marshalPretty(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "    ")
	_ = enc.Encode(v)
	return bytes.TrimRight(buf.Bytes(), "\n")
}

func readJSONFiles(dir string) []any {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	var out []any
	for _, n := range names {
		data, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			continue
		}
		var v any
		if err := json.Unmarshal(data, &v); err != nil {
			continue
		}
		out = append(out, v)
	}
	return out
}

func readPresetDir(dir string, stripExt bool) (contents []string, names []string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".json") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			continue
		}
		var v any
		if err := json.Unmarshal(data, &v); err != nil {
			continue
		}
		contents = append(contents, string(data))
		if stripExt {
			names = append(names, strings.TrimSuffix(f, filepath.Ext(f)))
		} else {
			names = append(names, f)
		}
	}
	return contents, names
}

func presetFolder(root, apiID string) string {
	switch apiID {
	case "kobold", "koboldhorde":
		return filepath.Join(root, "KoboldAI Settings")
	case "novel":
		return filepath.Join(root, "NovelAI Settings")
	case "textgenerationwebui":
		return filepath.Join(root, "TextGen Settings")
	case "openai":
		return filepath.Join(root, "OpenAI Settings")
	case "instruct":
		return filepath.Join(root, "instruct")
	case "context":
		return filepath.Join(root, "context")
	case "sysprompt":
		return filepath.Join(root, "sysprompt")
	case "reasoning":
		return filepath.Join(root, "reasoning")
	default:
		return ""
	}
}

func parseByteSize(s string) int64 {
	s = strings.TrimSpace(strings.ToLower(s))
	mult := int64(1)
	num := s
	switch {
	case strings.HasSuffix(s, "gb"):
		mult = 1024 * 1024 * 1024
		num = strings.TrimSpace(s[:len(s)-2])
	case strings.HasSuffix(s, "mb"):
		mult = 1024 * 1024
		num = strings.TrimSpace(s[:len(s)-2])
	case strings.HasSuffix(s, "kb"):
		mult = 1024
		num = strings.TrimSpace(s[:len(s)-2])
	case strings.HasSuffix(s, "b"):
		num = strings.TrimSpace(s[:len(s)-1])
	}
	var f float64
	if parsed, err := strconv.ParseFloat(num, 64); err != nil {
		return 0
	} else {
		f = parsed
	}
	return int64(f * float64(mult))
}

func (h *SettingsHandler) Save(w http.ResponseWriter, r *http.Request) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	var body any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if err := util.AtomicWrite(filepath.Join(uc.Directories.Root, "settings.json"), marshalPretty(body)); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	h.triggerAutoSave(uc.Profile.Handle, uc.Directories.Root)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"result":"ok"}`))
}

func (h *SettingsHandler) triggerAutoSave(handle, root string) {
	h.mu.Lock()
	last, ok := h.lastBackup[handle]
	if ok && time.Since(last) < 10*time.Minute {
		h.mu.Unlock()
		return
	}
	h.lastBackup[handle] = time.Now()
	h.mu.Unlock()
	backupSettingsFile(root, handle, true, h.backupLimit())
}

func (h *SettingsHandler) backupLimit() int {
	if h.Cfg.Backups.Common.NumberOfBackups > 0 {
		return h.Cfg.Backups.Common.NumberOfBackups
	}
	return 50
}

func backupSettingsFile(root, handle string, preventDuplicates bool, limit int) {
	src := filepath.Join(root, "settings.json")
	if _, err := os.Stat(src); err != nil {
		return
	}
	backupsDir := filepath.Join(root, "backups")
	_ = os.MkdirAll(backupsDir, 0o755)
	prefix := "settings_" + handle + "_"
	if preventDuplicates {
		entries, err := os.ReadDir(backupsDir)
		if err == nil {
			var latest string
			var latestMod time.Time
			for _, e := range entries {
				if e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
					continue
				}
				if info, err := e.Info(); err == nil && info.ModTime().After(latestMod) {
					latestMod = info.ModTime()
					latest = e.Name()
				}
			}
			if latest != "" {
				a, err1 := os.ReadFile(filepath.Join(backupsDir, latest))
				b, err2 := os.ReadFile(src)
				if err1 == nil && err2 == nil && bytes.Equal(a, b) {
					return
				}
			}
		}
	}
	dst := filepath.Join(backupsDir, prefix+util.GenerateTimestamp()+".json")
	_ = util.CopyFile(src, dst)
	util.RemoveOldBackups(backupsDir, "settings_"+handle, limit)
}

func (h *SettingsHandler) Get(w http.ResponseWriter, r *http.Request) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	settings, err := os.ReadFile(filepath.Join(uc.Directories.Root, "settings.json"))
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	root := uc.Directories.Root
	sub := func(name string) string { return filepath.Join(root, name) }
	novelaiSettings, novelaiNames := readPresetDir(sub("NovelAI Settings"), true)
	openaiSettings, openaiNames := readPresetDir(sub("OpenAI Settings"), true)
	textgenPresets, textgenNames := readPresetDir(sub("TextGen Settings"), true)
	koboldSettings, koboldNames := readPresetDir(sub("KoboldAI Settings"), true)
	worldNames := worldFileNames(sub("worlds"))
	resp := map[string]any{
		"settings":                         string(settings),
		"koboldai_settings":                koboldSettings,
		"koboldai_setting_names":           koboldNames,
		"world_names":                      worldNames,
		"novelai_settings":                 novelaiSettings,
		"novelai_setting_names":            novelaiNames,
		"openai_settings":                  openaiSettings,
		"openai_setting_names":             openaiNames,
		"textgenerationwebui_presets":      textgenPresets,
		"textgenerationwebui_preset_names": textgenNames,
		"themes":                           readJSONFiles(sub("themes")),
		"movingUIPresets":                  readJSONFiles(sub("movingUI")),
		"quickReplyPresets":                readJSONFiles(sub("QuickReplies")),
		"instruct":                         readJSONFiles(sub("instruct")),
		"context":                          readJSONFiles(sub("context")),
		"sysprompt":                        readJSONFiles(sub("sysprompt")),
		"reasoning":                        readJSONFiles(sub("reasoning")),
		"enable_extensions":                h.Cfg.Extensions.Enabled,
		"enable_extensions_auto_update":    h.Cfg.Extensions.AutoUpdate,
		"enable_accounts":                  h.Cfg.EnableUserAccounts,
		"request_compression": map[string]any{
			"enabled":        h.Cfg.Performance.RequestCompression.Enabled,
			"minPayloadSize": parseByteSize(h.Cfg.Performance.RequestCompression.MinPayloadSize),
			"maxPayloadSize": parseByteSize(h.Cfg.Performance.RequestCompression.MaxPayloadSize),
			"timeout":        h.Cfg.Performance.RequestCompression.Timeout,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func worldFileNames(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".json") {
			names = append(names, strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())))
		}
	}
	sort.Strings(names)
	return names
}

func (h *SettingsHandler) snapshotPrefix(handle string) string {
	return "settings_" + handle + "_"
}

func (h *SettingsHandler) GetSnapshots(w http.ResponseWriter, r *http.Request) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	backupsDir := filepath.Join(uc.Directories.Root, "backups")
	entries, err := os.ReadDir(backupsDir)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	prefix := h.snapshotPrefix(uc.Profile.Handle)
	type snap struct {
		Date float64 `json:"date"`
		Name string  `json:"name"`
		Size int64   `json:"size"`
	}
	var out []snap
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, snap{Date: float64(info.ModTime().UnixMilli()), Name: e.Name(), Size: info.Size()})
	}
	if out == nil {
		out = []snap{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (h *SettingsHandler) validSnapshotName(ucHandle, name string) bool {
	return name != "" && strings.HasPrefix(name, h.snapshotPrefix(ucHandle))
}

func (h *SettingsHandler) LoadSnapshot(w http.ResponseWriter, r *http.Request) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !h.validSnapshotName(uc.Profile.Handle, body.Name) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"Invalid snapshot name"}`))
		return
	}
	name := body.Name
	if !validFileField(map[string]any{"name": name}, "name") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"Invalid snapshot name"}`))
		return
	}
	p := filepath.Join(uc.Directories.Root, "backups", name)
	data, err := os.ReadFile(p)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	_, _ = w.Write(data)
}

func (h *SettingsHandler) MakeSnapshot(w http.ResponseWriter, r *http.Request) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	backupSettingsFile(uc.Directories.Root, uc.Profile.Handle, false, h.backupLimit())
	w.WriteHeader(http.StatusNoContent)
}

func (h *SettingsHandler) RestoreSnapshot(w http.ResponseWriter, r *http.Request) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !h.validSnapshotName(uc.Profile.Handle, body.Name) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"Invalid snapshot name"}`))
		return
	}
	name := body.Name
	if !validFileField(map[string]any{"name": name}, "name") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"Invalid snapshot name"}`))
		return
	}
	src := filepath.Join(uc.Directories.Root, "backups", name)
	if _, err := os.Stat(src); err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	dst := filepath.Join(uc.Directories.Root, "settings.json")
	_ = os.Remove(dst)
	if err := util.CopyFile(src, dst); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *SettingsHandler) PresetSave(w http.ResponseWriter, r *http.Request) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	var body struct {
		Name   string `json:"name"`
		Preset any    `json:"preset"`
		APIID  string `json:"apiId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	name := util.SanitizeFileName(body.Name)
	folder := presetFolder(uc.Directories.Root, body.APIID)
	if body.Preset == nil || name == "" || folder == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if err := util.AtomicWrite(filepath.Join(folder, name+".json"), marshalPretty(body.Preset)); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"name": name})
}

func (h *SettingsHandler) PresetDelete(w http.ResponseWriter, r *http.Request) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	var body struct {
		Name  string `json:"name"`
		APIID string `json:"apiId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	name := util.SanitizeFileName(body.Name)
	folder := presetFolder(uc.Directories.Root, body.APIID)
	if name == "" || folder == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	p := filepath.Join(folder, name+".json")
	if _, err := os.Stat(p); err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	_ = os.Remove(p)
	w.WriteHeader(http.StatusOK)
}

func (h *SettingsHandler) PresetRestore(w http.ResponseWriter, r *http.Request) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	var body struct {
		Name  string `json:"name"`
		APIID string `json:"apiId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	name := util.SanitizeFileName(body.Name)
	folder := presetFolder(uc.Directories.Root, body.APIID)
	result := map[string]any{"isDefault": false, "preset": map[string]any{}}
	for _, p := range content.GetDefaultPresets(uc.Directories.Root) {
		if p.Name == name && p.Folder == folder {
			if v := content.GetDefaultPresetFile(p.Filename); v != nil {
				result["isDefault"] = true
				result["preset"] = v
			}
			break
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func (h *SettingsHandler) namedJSONSave(w http.ResponseWriter, r *http.Request, dir string) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	raw := map[string]any{}
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	nameVal, _ := raw["name"].(string)
	if nameVal == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if err := util.AtomicWrite(filepath.Join(dir, util.SanitizeFileName(nameVal+".json")), marshalPretty(raw)); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *SettingsHandler) namedJSONDelete(w http.ResponseWriter, r *http.Request, dir string, missingIs404 bool) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	raw := map[string]any{}
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	nameVal, _ := raw["name"].(string)
	if nameVal == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	p := filepath.Join(dir, util.SanitizeFileName(nameVal+".json"))
	if _, err := os.Stat(p); err != nil {
		if missingIs404 {
			w.WriteHeader(http.StatusNotFound)
		} else {
			w.WriteHeader(http.StatusOK)
		}
		return
	}
	_ = os.Remove(p)
	w.WriteHeader(http.StatusOK)
}

func (h *SettingsHandler) ThemeSave(w http.ResponseWriter, r *http.Request) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	h.namedJSONSave(w, r, filepath.Join(uc.Directories.Root, "themes"))
}

func (h *SettingsHandler) ThemeDelete(w http.ResponseWriter, r *http.Request) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	h.namedJSONDelete(w, r, filepath.Join(uc.Directories.Root, "themes"), true)
}

func (h *SettingsHandler) MovingUISave(w http.ResponseWriter, r *http.Request) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	h.namedJSONSave(w, r, filepath.Join(uc.Directories.Root, "movingUI"))
}

func (h *SettingsHandler) QuickReplySave(w http.ResponseWriter, r *http.Request) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	h.namedJSONSave(w, r, filepath.Join(uc.Directories.Root, "QuickReplies"))
}

func (h *SettingsHandler) QuickReplyDelete(w http.ResponseWriter, r *http.Request) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	h.namedJSONDelete(w, r, filepath.Join(uc.Directories.Root, "QuickReplies"), false)
}
