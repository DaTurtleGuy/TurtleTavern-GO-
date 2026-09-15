package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/TurtleTavern/turtletavern/internal/character"
	"github.com/TurtleTavern/turtletavern/internal/config"
	"github.com/TurtleTavern/turtletavern/internal/models"
	"github.com/TurtleTavern/turtletavern/internal/util"
	"github.com/go-chi/chi/v5"
)

type ChatHandler struct {
	UseCharacterIndex bool
	Cfg               *config.Config
	Index             *character.Index
}

func NewChatHandler(cfg *config.Config, idx *character.Index) *ChatHandler {
	return &ChatHandler{Cfg: cfg, Index: idx}
}

func (h *ChatHandler) RegisterRoutes(r chi.Router) {
	r.Route("/api/chats", func(r chi.Router) {
		r.Post("/save", h.Save)
		r.Post("/get", h.Get)
		r.Post("/delete", h.Delete)
		r.Post("/rename", h.Rename)
		r.Post("/export", h.Export)
		r.Post("/import", h.Import)
		r.Post("/search", h.Search)
		r.Post("/recent", h.Recent)
		r.Post("/group/save", h.GroupSave)
		r.Post("/group/get", h.GroupGet)
		r.Post("/group/info", h.GroupInfo)
		r.Post("/group/delete", h.GroupDelete)
		r.Post("/group/import", h.GroupImport)
	})
}

type integrityMismatchError struct{ msg string }

func (e *integrityMismatchError) Error() string { return e.msg }

type throttleEntry struct {
	mu       sync.Mutex
	timer    *time.Timer
	pending  func()
	interval time.Duration
}

func (t *throttleEntry) call(fn func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.timer == nil {
		fn()
		t.timer = time.AfterFunc(t.interval, func() {
			t.mu.Lock()
			pending := t.pending
			t.pending = nil
			t.timer = nil
			t.mu.Unlock()
			if pending != nil {
				pending()
			}
		})
		return
	}
	t.pending = fn
}

var (
	backupThrottlersMu sync.Mutex
	backupThrottlers   = map[string]*throttleEntry{}
)

func (h *ChatHandler) backupChatThrottled(handle, directory, name, data string) {
	interval := time.Duration(h.Cfg.Backups.Chat.ThrottleInterval) * time.Millisecond
	if interval <= 0 {
		interval = 10 * time.Second
	}
	backupThrottlersMu.Lock()
	th, ok := backupThrottlers[handle]
	if !ok {
		th = &throttleEntry{interval: interval}
		backupThrottlers[handle] = th
	}
	backupThrottlersMu.Unlock()
	th.call(func() {
		h.backupChat(directory, name, data)
	})
}

func (h *ChatHandler) backupChat(directory, name, data string) {
	if !h.Cfg.Backups.Chat.Enabled {
		return
	}
	if !util.EnsureDirectory(directory) {
		return
	}
	safeName := strings.ToLower(sanitizeAlnum(name))
	ts := util.GenerateTimestamp()
	backupFile := filepath.Join(directory, "chat_"+safeName+"_"+ts+".jsonl")
	_ = util.AtomicWriteString(backupFile, data)
	util.RemoveOldBackups(directory, "chat_"+safeName+"_", h.Cfg.Backups.Common.NumberOfBackups)
	if h.Cfg.Backups.Chat.MaxTotalBackups >= 0 {
		util.RemoveOldBackups(directory, "chat_", h.Cfg.Backups.Chat.MaxTotalBackups)
	}
}

func sanitizeAlnum(name string) string {
	var sb strings.Builder
	for _, r := range strings.ToLower(util.SanitizeFileName(name)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			sb.WriteRune(r)
		} else {
			sb.WriteByte('_')
		}
	}
	return sb.String()
}

func checkChatIntegrity(filePath, slug string) bool {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return true
	}
	lines := strings.SplitN(string(data), "\n", 2)
	if len(lines) == 0 {
		return true
	}
	var first map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		return true
	}
	meta, ok := first["chat_metadata"].(map[string]any)
	if !ok {
		return true
	}
	existing, _ := meta["integrity"].(string)
	if existing == "" {
		return true
	}
	return existing == slug
}

func (h *ChatHandler) trySaveChat(chatData []map[string]any, filePath, handle, cardName, backupDir string, skipIntegrityCheck bool) error {
	var sb strings.Builder
	for i, msg := range chatData {
		b, err := json.Marshal(msg)
		if err != nil {
			continue
		}
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.Write(b)
	}
	doCheck := h.Cfg.Backups.Chat.CheckIntegrity && !skipIntegrityCheck
	var slug string
	if doCheck && len(chatData) > 0 {
		if meta, ok := chatData[0]["chat_metadata"].(map[string]any); ok {
			slug, _ = meta["integrity"].(string)
		}
	}
	if slug != "" && !checkChatIntegrity(filePath, slug) {
		return &integrityMismatchError{msg: "Chat integrity check failed for \"" + filePath + "\""}
	}
	if err := util.AtomicWriteString(filePath, sb.String()); err != nil {
		return err
	}
	h.backupChatThrottled(handle, backupDir, cardName, sb.String())
	return nil
}

func getChatData(chatFilePath string) []map[string]any {
	data, err := os.ReadFile(chatFilePath)
	if err != nil {
		log.Printf("[Chats] cannot read chat file %s: %v", chatFilePath, err)
		return nil
	}
	var result []map[string]any
	skipped := 0
	// strings.Split, not bufio.Scanner: Scanner's default 64 KiB token limit
	// silently aborts on the first oversized line (one long roleplay message is
	// enough) and truncates the rest of the chat.
	for i, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		var msg map[string]any
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			skipped++
			if skipped <= 5 {
				log.Printf("[Chats] skipping malformed line %d in %s: %v", i+1, filepath.Base(chatFilePath), err)
			}
			continue
		}
		result = append(result, msg)
	}
	if skipped > 0 {
		log.Printf("[Chats] %s: loaded %d messages, skipped %d malformed lines", filepath.Base(chatFilePath), len(result), skipped)
	}
	return result
}

func (h *ChatHandler) Save(w http.ResponseWriter, r *http.Request) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	var body struct {
		AvatarURL string           `json:"avatar_url"`
		FileName  string           `json:"file_name"`
		Chat      []map[string]any `json:"chat"`
		Force     bool             `json:"force"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Chat == nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !validFileField(map[string]any{"avatar_url": body.AvatarURL}, "avatar_url") {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	cardName := strings.TrimSuffix(body.AvatarURL, ".png")
	chatFileName := body.FileName + ".jsonl"
	chatDir := filepath.Join(uc.Directories.Chats, cardName)
	chatFilePath := filepath.Join(chatDir, util.SanitizeFileName(chatFileName))

	if !util.IsPathUnderParent(uc.Directories.Chats, chatFilePath) {
		http.Error(w, "path traversal", http.StatusBadRequest)
		return
	}

	if err := h.trySaveChat(body.Chat, chatFilePath, uc.Profile.Handle, cardName, uc.Directories.Backups, body.Force); err != nil {
		if _, ok := err.(*integrityMismatchError); ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"integrity"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"An error has occurred, see the console logs for more information."}`))
		return
	}
	// The character list is served from a cache, so record the new recency here
	// instead of making the user press recompute after every message.
	if h.Index != nil {
		if ts := character.ChatFileRecency(chatFilePath); ts > 0 {
			h.Index.UpdateDateLastChat(uc.Directories.Characters, body.AvatarURL, ts)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true}`))
}

func (h *ChatHandler) Get(w http.ResponseWriter, r *http.Request) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	var body struct {
		AvatarURL string `json:"avatar_url"`
		FileName  string `json:"file_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !validFileField(map[string]any{"avatar_url": body.AvatarURL}, "avatar_url") {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	dirName := strings.TrimSuffix(body.AvatarURL, ".png")
	chatDir := filepath.Join(uc.Directories.Chats, dirName)
	if !util.IsPathUnderParent(uc.Directories.Chats, chatDir) {
		http.Error(w, "path traversal", http.StatusBadRequest)
		return
	}
	if !util.FileExists(chatDir) {
		os.MkdirAll(chatDir, 0o755)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("{}"))
		return
	}
	if body.FileName == "" {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("{}"))
		return
	}
	chatFilePath := filepath.Join(chatDir, util.SanitizeFileName(body.FileName+".jsonl"))
	data := getChatData(chatFilePath)
	if data == nil {
		data = []map[string]any{}
	}
	log.Printf("[Chats] loaded %q for %q: %d messages", body.FileName, body.AvatarURL, len(data))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func (h *ChatHandler) Delete(w http.ResponseWriter, r *http.Request) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	var body struct {
		AvatarURL string `json:"avatar_url"`
		ChatFile  string `json:"chatfile"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !validFileField(map[string]any{"avatar_url": body.AvatarURL}, "avatar_url") {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !strings.HasSuffix(body.ChatFile, ".jsonl") {
		body.ChatFile += ".jsonl"
	}
	dirName := strings.TrimSuffix(body.AvatarURL, ".png")
	chatFilePath := filepath.Join(uc.Directories.Chats, dirName, util.SanitizeFileName(body.ChatFile))
	if !util.IsPathUnderParent(uc.Directories.Chats, chatFilePath) {
		http.Error(w, "path traversal", http.StatusBadRequest)
		return
	}
	if util.TryDeleteFile(chatFilePath) {
		w.Write([]byte(`{"ok":true}`))
	} else {
		http.Error(w, "not found", http.StatusBadRequest)
	}
}

func (h *ChatHandler) Rename(w http.ResponseWriter, r *http.Request) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	var body struct {
		OriginalFile string `json:"original_file"`
		RenamedFile  string `json:"renamed_file"`
		AvatarURL    string `json:"avatar_url"`
		IsGroup      bool   `json:"is_group"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.OriginalFile == "" || body.RenamedFile == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !validFileField(map[string]any{"avatar_url": body.AvatarURL}, "avatar_url") {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	folderPath := ""
	if body.IsGroup {
		folderPath = uc.Directories.GroupChats
	} else {
		folderPath = filepath.Join(uc.Directories.Chats, strings.TrimSuffix(body.AvatarURL, ".png"))
		if !util.IsPathUnderParent(uc.Directories.Chats, folderPath) {
			http.Error(w, "path traversal", http.StatusBadRequest)
			return
		}
	}
	srcPath := filepath.Join(folderPath, util.SanitizeFileName(body.OriginalFile))
	dstPath := filepath.Join(folderPath, util.SanitizeFileName(body.RenamedFile))

	if !util.FileExists(srcPath) || util.FileExists(dstPath) {
		http.Error(w, "source or destination unavailable", http.StatusBadRequest)
		return
	}
	util.CopyFile(srcPath, dstPath)
	os.Remove(srcPath)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "sanitizedFileName": strings.TrimSuffix(body.RenamedFile, filepath.Ext(body.RenamedFile))})
}

func (h *ChatHandler) Export(w http.ResponseWriter, r *http.Request) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	var body struct {
		File      string `json:"file"`
		AvatarURL string `json:"avatar_url"`
		IsGroup   bool   `json:"is_group"`
		Format    string `json:"format"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.File == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !validFileField(map[string]any{"avatar_url": body.AvatarURL}, "avatar_url") {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	folderPath := ""
	if body.IsGroup {
		folderPath = uc.Directories.GroupChats
	} else {
		folderPath = filepath.Join(uc.Directories.Chats, strings.TrimSuffix(body.AvatarURL, ".png"))
		if !util.IsPathUnderParent(uc.Directories.Chats, filepath.Join(folderPath, body.File)) {
			http.Error(w, "path traversal", http.StatusBadRequest)
			return
		}
	}
	filename := filepath.Join(folderPath, util.SanitizeFileName(body.File))
	if !util.FileExists(filename) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if body.Format == "jsonl" {
		data, err := os.ReadFile(filename)
		if err != nil {
			http.Error(w, "read error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"result": string(data)})
		return
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		http.Error(w, "read error", http.StatusInternalServerError)
		return
	}
	var buffer strings.Builder
	// Same 64 KiB scanner trap as getChatData: one long message used to cut
	// the exported transcript short.
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		var msg models.ChatMessage
		if json.Unmarshal([]byte(line), &msg) != nil {
			continue
		}
		if msg.IsSystem {
			continue
		}
		if msg.Mes != "" {
			display := msg.Mes
			if msg.Extra != nil {
				if dt, ok := msg.Extra["display_text"].(string); ok && dt != "" {
					display = dt
				}
			}
			buffer.WriteString(fmt.Sprintf("%s: %s\n\n", msg.Name, display))
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"result": buffer.String()})
}

func (h *ChatHandler) Import(w http.ResponseWriter, r *http.Request) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	file, _, err := r.FormFile("avatar")
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	format := r.FormValue("file_type")
	avatarURL := strings.TrimSuffix(r.FormValue("avatar_url"), ".png")
	if !validFileField(map[string]any{"avatar_url": r.FormValue("avatar_url")}, "avatar_url") {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	charName := util.SanitizeFileName(r.FormValue("character_name"))
	if charName == "" {
		charName = "Character"
	}
	userName := util.SanitizeFileName(r.FormValue("user_name"))
	if userName == "" {
		userName = "User"
	}
	dirPath := filepath.Join(uc.Directories.Chats, avatarURL)
	if !util.IsPathUnderParent(uc.Directories.Chats, dirPath) {
		http.Error(w, "path traversal", http.StatusBadRequest)
		return
	}
	os.MkdirAll(dirPath, 0o755)
	defer r.MultipartForm.RemoveAll()

	data, _ := io.ReadAll(file)
	writeChat := func(chat string) string {
		fileName := fmt.Sprintf("%s - %s imported.jsonl", charName, util.HumanizedDateTime(0))
		_ = util.AtomicWrite(filepath.Join(dirPath, fileName), []byte(chat))
		return fileName
	}

	if format == "json" {
		var jsonData map[string]any
		if err := json.Unmarshal(data, &jsonData); err != nil {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"error":true}`))
			return
		}
		var chats []string
		_, hasSavedSettings := jsonData["savedsettings"]
		_, hasHistories := jsonData["histories"]
		_, hasDataVisible := jsonData["data_visible"]
		_, hasMessages := jsonData["messages"]
		typeVal, _ := jsonData["type"].(string)
		switch {
		case hasSavedSettings:
			chats = []string{importKoboldLiteChat(jsonData)}
		case hasHistories:
			chats = importCAIChat(userName, charName, jsonData)
		case hasDataVisible:
			if _, ok := jsonData["data_visible"].([]any); ok {
				chats = []string{importOobaChat(userName, charName, jsonData)}
			}
		case hasMessages:
			if _, ok := jsonData["messages"].([]any); ok {
				chats = []string{importAgnaiChat(userName, charName, jsonData)}
			}
		case typeVal == "risuChat":
			chats = []string{importRisuChat(userName, charName, jsonData)}
		}
		if len(chats) == 0 {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"error":true}`))
			return
		}
		var fileNames []string
		for _, chat := range chats {
			fileNames = append(fileNames, writeChat(chat))
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"res": true, "fileNames": fileNames})
		return
	}

	if format == "jsonl" {
		lines := strings.Split(string(data), "\n")
		if len(lines) == 0 {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"error":true}`))
			return
		}
		var header map[string]any
		_ = json.Unmarshal([]byte(lines[0]), &header)
		_, hasUserName := header["user_name"]
		_, hasName := header["name"]
		_, hasMeta := header["chat_metadata"]
		if !hasUserName && !hasName && !hasMeta {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"error":true}`))
			return
		}
		flattened := flattenChubChat(userName, charName, lines)
		fileName := fmt.Sprintf("%s - %s imported.jsonl", charName, util.HumanizedDateTime(0))
		if flattened != string(data) {
			_ = util.AtomicWrite(filepath.Join(dirPath, fileName), []byte(flattened))
		} else {
			_ = util.AtomicWrite(filepath.Join(dirPath, fileName), data)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"res": true, "fileNames": []string{fileName}})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"error":true}`))
}

func isoNow() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

func chatHeaderLine() string {
	header, _ := json.Marshal(map[string]any{
		"chat_metadata":  map[string]any{},
		"user_name":      "unused",
		"character_name": "unused",
	})
	return string(header)
}

func chatLines(msgs []map[string]any) string {
	lines := make([]string, 0, len(msgs)+1)
	lines = append(lines, chatHeaderLine())
	for _, m := range msgs {
		if b, err := json.Marshal(m); err == nil {
			lines = append(lines, string(b))
		}
	}
	return strings.Join(lines, "\n")
}

func importOobaChat(userName, characterName string, jsonData map[string]any) string {
	var msgs []map[string]any
	if pairs, ok := jsonData["data_visible"].([]any); ok {
		for _, p := range pairs {
			arr, ok := p.([]any)
			if !ok || len(arr) < 2 {
				continue
			}
			if s, ok := arr[0].(string); ok && s != "" {
				msgs = append(msgs, map[string]any{
					"name": userName, "is_user": true, "send_date": isoNow(), "mes": s, "extra": map[string]any{},
				})
			}
			if s, ok := arr[1].(string); ok && s != "" {
				msgs = append(msgs, map[string]any{
					"name": characterName, "is_user": false, "send_date": isoNow(), "mes": s, "extra": map[string]any{},
				})
			}
		}
	}
	return chatLines(msgs)
}

func importAgnaiChat(userName, characterName string, jsonData map[string]any) string {
	var msgs []map[string]any
	if arr, ok := jsonData["messages"].([]any); ok {
		for _, item := range arr {
			mm, ok := item.(map[string]any)
			if !ok {
				continue
			}
			_, isUser := mm["userId"]
			name := characterName
			if isUser {
				name = userName
			}
			text, _ := mm["msg"].(string)
			msgs = append(msgs, map[string]any{
				"name": name, "is_user": isUser, "send_date": isoNow(), "mes": text, "extra": map[string]any{},
			})
		}
	}
	return chatLines(msgs)
}

func importCAIChat(userName, characterName string, jsonData map[string]any) []string {
	var histories []any
	if h, ok := jsonData["histories"].(map[string]any); ok {
		if arr, ok := h["histories"].([]any); ok {
			histories = arr
		}
	}
	var out []string
	for _, h := range histories {
		hm, ok := h.(map[string]any)
		if !ok {
			continue
		}
		var msgs []map[string]any
		if arr, ok := hm["msgs"].([]any); ok {
			for _, item := range arr {
				mm, ok := item.(map[string]any)
				if !ok {
					continue
				}
				src, _ := mm["src"].(map[string]any)
				isHuman, _ := src["is_human"].(bool)
				name := characterName
				if isHuman {
					name = userName
				}
				text, _ := mm["text"].(string)
				msgs = append(msgs, map[string]any{
					"name": name, "is_user": isHuman, "send_date": isoNow(), "mes": text, "extra": map[string]any{},
				})
			}
		}
		out = append(out, chatLines(msgs))
	}
	return out
}

func importKoboldLiteChat(jsonData map[string]any) string {
	settings, _ := jsonData["savedsettings"].(map[string]any)
	userName := ""
	characterName := ""
	if settings != nil {
		userName, _ = settings["chatname"].(string)
		if opp, ok := settings["chatopponent"].(string); ok {
			characterName = strings.Split(opp, "||$||")[0]
		}
	}
	process := func(msg string) map[string]any {
		isUser := strings.Contains(msg, "{{[INPUT]}}")
		name := characterName
		if isUser {
			name = userName
		}
		text := strings.ReplaceAll(strings.ReplaceAll(msg, "{{[INPUT]}}", ""), "{{[OUTPUT]}}", "")
		return map[string]any{
			"name": name, "is_user": isUser, "mes": strings.TrimSpace(text),
			"send_date": isoNow(), "extra": map[string]any{},
		}
	}
	var msgs []map[string]any
	if actions, ok := jsonData["actions"].([]any); ok {
		for _, a := range actions {
			if s, ok := a.(string); ok {
				msgs = append(msgs, process(s))
			}
		}
	}
	if prompt, ok := jsonData["prompt"].(string); ok && prompt != "" {
		msgs = append([]map[string]any{process(prompt)}, msgs...)
	}
	return chatLines(msgs)
}

func importRisuChat(userName, characterName string, jsonData map[string]any) string {
	var msgs []map[string]any
	if data, ok := jsonData["data"].(map[string]any); ok {
		if arr, ok := data["message"].([]any); ok {
			for _, item := range arr {
				mm, ok := item.(map[string]any)
				if !ok {
					continue
				}
				role, _ := mm["role"].(string)
				isUser := role == "user"
				name := characterName
				if n, ok := mm["name"].(string); ok && n != "" {
					name = n
				} else if isUser {
					name = userName
				}
				sendDate := isoNow()
				switch t := mm["time"].(type) {
				case float64:
					sendDate = time.UnixMilli(int64(t)).UTC().Format("2006-01-02T15:04:05.000Z")
				}
				text, _ := mm["data"].(string)
				msgs = append(msgs, map[string]any{
					"name": name, "is_user": isUser, "send_date": sendDate, "mes": text, "extra": map[string]any{},
				})
			}
		}
	}
	return chatLines(msgs)
}

func flattenChubChat(userName, characterName string, lines []string) string {
	_ = userName
	_ = characterName
	convert := func(line string) string {
		var data map[string]any
		if err := json.Unmarshal([]byte(line), &data); err != nil {
			return line
		}
		if mes, ok := data["mes"].(map[string]any); ok {
			if inner, ok := mes["message"].(string); ok {
				data["mes"] = inner
			}
		}
		if swipes, ok := data["swipes"].([]any); ok {
			flat := make([]any, 0, len(swipes))
			for _, s := range swipes {
				if sm, ok := s.(map[string]any); ok {
					if msg, ok := sm["message"].(string); ok {
						flat = append(flat, msg)
						continue
					}
				}
				flat = append(flat, s)
			}
			data["swipes"] = flat
		}
		if out, err := json.Marshal(data); err == nil {
			return string(out)
		}
		return line
	}
	converted := make([]string, 0, len(lines))
	for _, line := range lines {
		converted = append(converted, convert(line))
	}
	return strings.Join(converted, "\n")
}

func (h *ChatHandler) Search(w http.ResponseWriter, r *http.Request) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	var body struct {
		Query     string `json:"query"`
		AvatarURL string `json:"avatar_url"`
		GroupID   string `json:"group_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !validFileField(map[string]any{"avatar_url": body.AvatarURL}, "avatar_url") {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	var chatFiles []string
	if body.GroupID != "" {
		groupFiles, _ := os.ReadDir(uc.Directories.Groups)
		for _, gf := range groupFiles {
			if gf.IsDir() || !strings.HasSuffix(gf.Name(), ".json") {
				continue
			}
			var gd models.GroupData
			if err := util.ReadJSONFile(filepath.Join(uc.Directories.Groups, gf.Name()), &gd); err != nil {
				continue
			}
			if gd.ID == body.GroupID {
				for _, chatID := range gd.Chats {
					chatFile := filepath.Join(uc.Directories.GroupChats, chatID+".jsonl")
					if util.FileExists(chatFile) {
						chatFiles = append(chatFiles, chatFile)
					}
				}
				break
			}
		}
	} else {
		charName := strings.TrimSuffix(body.AvatarURL, ".png")
		chatDir := filepath.Join(uc.Directories.Chats, charName)
		if _, err := os.Stat(chatDir); err != nil {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
			return
		}
		entries, _ := os.ReadDir(chatDir)
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
				chatFiles = append(chatFiles, filepath.Join(chatDir, e.Name()))
			}
		}
	}

	fragments := strings.Fields(strings.ToLower(strings.TrimSpace(body.Query)))
	hasTextMatch := func(texts []string) bool {
		if len(fragments) == 0 {
			return true
		}
		for _, frag := range fragments {
			found := false
			for _, text := range texts {
				if strings.Contains(strings.ToLower(text), frag) {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
		return true
	}
	var matcher func([]string) bool
	if body.Query != "" {
		matcher = hasTextMatch
	}
	type searchResult struct {
		FileName     string `json:"file_name"`
		FileSize     string `json:"file_size"`
		MessageCount int    `json:"message_count"`
		LastMes      any    `json:"last_mes"`
		PreviewMes   string `json:"preview_message"`
	}
	results := []searchResult{}
	for _, cf := range chatFiles {
		info := getChatFileInfoEx(cf, false, nil, matcher)
		if info.FileName == "" {
			continue
		}
		hasMatch := info.Match || hasTextMatch([]string{info.FileID})
		if body.Query != "" && info.ChatItems == 0 && !hasMatch {
			continue
		}
		if body.Query == "" || hasMatch {
			preview := info.Mes
			if len(preview) > 400 {
				preview = "..." + preview[len(preview)-400:]
			}
			results = append(results, searchResult{
				FileName:     info.FileID,
				FileSize:     info.FileSize,
				MessageCount: info.ChatItems,
				LastMes:      info.LastMes,
				PreviewMes:   preview,
			})
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
}

func (h *ChatHandler) Recent(w http.ResponseWriter, r *http.Request) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	var body struct {
		Max      any              `json:"max"`
		Metadata bool             `json:"metadata"`
		Pinned   []map[string]any `json:"pinned"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	type pinnedChat struct {
		FileName string
		Avatar   string
		Group    string
	}
	var pinnedList []pinnedChat
	for _, p := range body.Pinned {
		fileName, _ := p["file_name"].(string)
		avatar, _ := p["avatar"].(string)
		group, _ := p["group"].(string)
		pinnedList = append(pinnedList, pinnedChat{FileName: fileName, Avatar: avatar, Group: group})
	}
	type chatFile struct {
		PNGFile  string
		GroupID  string
		FilePath string
		Mtime    int64
	}
	var allChatFiles []chatFile

	charEntries, _ := os.ReadDir(uc.Directories.Characters)
	for _, e := range charEntries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".png") {
			continue
		}
		charName := strings.TrimSuffix(e.Name(), ".png")
		chatDir := filepath.Join(uc.Directories.Chats, charName)
		chatEntries, _ := os.ReadDir(chatDir)
		for _, ce := range chatEntries {
			if !ce.IsDir() && strings.HasSuffix(ce.Name(), ".jsonl") {
				info, err := ce.Info()
				if err == nil {
					allChatFiles = append(allChatFiles, chatFile{
						PNGFile:  e.Name(),
						FilePath: filepath.Join(chatDir, ce.Name()),
						Mtime:    info.ModTime().UnixMilli(),
					})
				}
			}
		}
	}

	groupEntries, _ := os.ReadDir(uc.Directories.Groups)
	for _, ge := range groupEntries {
		if ge.IsDir() || !strings.HasSuffix(ge.Name(), ".json") {
			continue
		}
		var gd models.GroupData
		if err := util.ReadJSONFile(filepath.Join(uc.Directories.Groups, ge.Name()), &gd); err != nil {
			continue
		}
		for _, chatID := range gd.Chats {
			chatFile2 := filepath.Join(uc.Directories.GroupChats, chatID+".jsonl")
			if !util.FileExists(chatFile2) {
				continue
			}
			info, err := os.Stat(chatFile2)
			if err == nil {
				allChatFiles = append(allChatFiles, chatFile{
					GroupID:  gd.ID,
					FilePath: chatFile2,
					Mtime:    info.ModTime().UnixMilli(),
				})
			}
		}
	}

	if rootEntries, err := os.ReadDir(uc.Directories.Chats); err == nil {
		for _, e := range rootEntries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
				continue
			}
			info, err := e.Info()
			if err == nil {
				allChatFiles = append(allChatFiles, chatFile{
					FilePath: filepath.Join(uc.Directories.Chats, e.Name()),
					Mtime:    info.ModTime().UnixMilli(),
				})
			}
		}
	}

	isPinned := func(cf chatFile) bool {
		base := filepath.Base(cf.FilePath)
		for _, p := range pinnedList {
			if p.FileName == base && (p.Avatar == cf.PNGFile || p.Group == cf.GroupID) {
				return true
			}
		}
		return false
	}
	sort.Slice(allChatFiles, func(i, j int) bool {
		pi, pj := isPinned(allChatFiles[i]), isPinned(allChatFiles[j])
		if pi && !pj {
			return true
		}
		if !pi && pj {
			return false
		}
		return allChatFiles[i].Mtime > allChatFiles[j].Mtime
	})

	max := len(allChatFiles)
	if body.Max != nil {
		parsed := -1
		switch v := body.Max.(type) {
		case float64:
			parsed = int(v)
		case string:
			if n, err := strconv.Atoi(v); err == nil {
				parsed = n
			}
		}
		if parsed >= 0 {
			max = parsed + len(pinnedList)
		}
	}
	if max > len(allChatFiles) {
		max = len(allChatFiles)
	}
	if max < 0 {
		max = 0
	}
	recentChats := allChatFiles[:max]

	type chatInfoResult struct {
		Match     bool   `json:"match"`
		FileID    string `json:"file_id"`
		FileName  string `json:"file_name"`
		FileSize  string `json:"file_size"`
		ChatItems int    `json:"chat_items"`
		Mes       string `json:"mes"`
		LastMes   any    `json:"last_mes"`
		Avatar    string `json:"avatar,omitempty"`
		Group     string `json:"group,omitempty"`
	}
	results := []chatInfoResult{}
	withMetadata := body.Metadata
	for _, cf := range recentChats {
		additional := map[string]any{}
		if cf.GroupID != "" {
			additional["group"] = cf.GroupID
		} else {
			additional["avatar"] = cf.PNGFile
		}
		info := getChatFileInfoEx(cf.FilePath, withMetadata, additional, nil)
		if info.FileName == "" {
			continue
		}
		results = append(results, chatInfoResult{
			Match:     info.Match,
			FileID:    info.FileID,
			FileName:  info.FileName,
			FileSize:  info.FileSize,
			ChatItems: info.ChatItems,
			Mes:       info.Mes,
			LastMes:   info.LastMes,
			Avatar:    cf.PNGFile,
			Group:     cf.GroupID,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
}

func (h *ChatHandler) GroupSave(w http.ResponseWriter, r *http.Request) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	var body struct {
		ID    string           `json:"id"`
		Chat  []map[string]any `json:"chat"`
		Force bool             `json:"force"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Chat == nil || body.ID == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	chatFilePath := filepath.Join(uc.Directories.GroupChats, util.SanitizeFileName(body.ID+".jsonl"))
	if err := h.trySaveChat(body.Chat, chatFilePath, uc.Profile.Handle, body.ID, uc.Directories.Backups, body.Force); err != nil {
		if _, ok := err.(*integrityMismatchError); ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"integrity"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"An error has occurred, see the console logs for more information."}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true}`))
}

func (h *ChatHandler) GroupGet(w http.ResponseWriter, r *http.Request) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	chatFilePath := filepath.Join(uc.Directories.GroupChats, util.SanitizeFileName(body.ID+".jsonl"))
	data := getChatData(chatFilePath)
	if data == nil {
		data = []map[string]any{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func (h *ChatHandler) GroupInfo(w http.ResponseWriter, r *http.Request) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	chatFilePath := filepath.Join(uc.Directories.GroupChats, util.SanitizeFileName(body.ID+".jsonl"))
	info := getChatFileInfo(chatFilePath)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}

func (h *ChatHandler) GroupDelete(w http.ResponseWriter, r *http.Request) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	chatFilePath := filepath.Join(uc.Directories.GroupChats, util.SanitizeFileName(body.ID+".jsonl"))
	if util.TryDeleteFile(chatFilePath) {
		w.Write([]byte(`{"ok":true}`))
	} else {
		http.Error(w, "not found", http.StatusBadRequest)
	}
}

func (h *ChatHandler) GroupImport(w http.ResponseWriter, r *http.Request) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	file, _, err := r.FormFile("avatar")
	if err != nil {
		http.Error(w, "no file", http.StatusBadRequest)
		return
	}
	defer file.Close()
	chatName := util.HumanizedDateTime(0)
	dstPath := filepath.Join(uc.Directories.GroupChats, chatName+".jsonl")
	data, _ := io.ReadAll(file)
	util.AtomicWrite(dstPath, data)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"res": chatName})
}
