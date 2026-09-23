package handlers

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/TurtleTavern/turtletavern/internal/character"
	"github.com/TurtleTavern/turtletavern/internal/models"
	"github.com/TurtleTavern/turtletavern/internal/util"
	"github.com/go-chi/chi/v5"
)

type GroupHandler struct{}

func NewGroupHandler() *GroupHandler {
	return &GroupHandler{}
}

func (h *GroupHandler) RegisterRoutes(r chi.Router) {
	r.Route("/api/groups", func(r chi.Router) {
		r.Post("/all", h.All)
		r.Post("/create", h.Create)
		r.Post("/edit", h.Edit)
		r.Post("/delete", h.Delete)
	})
}

func (h *GroupHandler) All(w http.ResponseWriter, r *http.Request) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	os.MkdirAll(uc.Directories.Groups, 0o755)

	var groups []models.GroupData
	groups = []models.GroupData{}

	chatEntries, _ := os.ReadDir(uc.Directories.GroupChats)
	chatNames := make(map[string]bool)
	for _, ce := range chatEntries {
		if !ce.IsDir() && strings.HasSuffix(ce.Name(), ".jsonl") {
			chatNames[strings.TrimSuffix(ce.Name(), ".jsonl")] = true
		}
	}

	files, err := os.ReadDir(uc.Directories.Groups)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]models.GroupData{})
		return
	}

	// Per-group reads are independent (distinct files, read-only shared
	// chatNames), so they run concurrently with a small semaphore cap.
	// Results land in indexed slots to preserve the serial listing order.
	var groupNames []string
	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
			continue
		}
		groupNames = append(groupNames, f.Name())
	}
	type groupResult struct {
		gd models.GroupData
		ok bool
	}
	results := make([]groupResult, len(groupNames))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, name := range groupNames {
		wg.Add(1)
		go func(idx int, fileName string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			var gd models.GroupData
			filePath := filepath.Join(uc.Directories.Groups, fileName)
			if err := util.ReadJSONFile(filePath, &gd); err != nil {
				return
			}

			// Creation dates are repaired by the recompute action, never here: this used
			// to backfill date_added from the group file's mtime, so every listing after
			// a restore stamped old groups with the copy time.
			var chatSize int64
			var dateLastChat float64
			for _, chatID := range gd.Chats {
				if !chatNames[chatID] {
					continue
				}
				chatPath := filepath.Join(uc.Directories.GroupChats, chatID+".jsonl")
				chatInfo, err := os.Stat(chatPath)
				if err != nil {
					continue
				}
				chatSize += chatInfo.Size()
				// The last message in the chats is the truth. The value stored in the
				// group file is only a fallback for a chat with no timestamped message.
				if ts, ok := character.ChatFileSendDate(chatPath); ok && ts > dateLastChat {
					dateLastChat = ts
				}
			}
			if dateLastChat == 0 {
				dateLastChat = gd.DateLastChat
			}
			gd.DateLastChat = dateLastChat
			gd.ChatSize = chatSize
			results[idx] = groupResult{gd: gd, ok: true}
		}(i, name)
	}
	wg.Wait()
	for _, r := range results {
		if r.ok {
			groups = append(groups, r.gd)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(groups)
}

func (h *GroupHandler) Create(w http.ResponseWriter, r *http.Request) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	os.MkdirAll(uc.Directories.Groups, 0o755)

	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	id := time.Now().Format("20060102150405000")

	gd := models.GroupData{
		ID:                       id,
		Name:                     getStringFromBody(body, "name", "New Group"),
		Members:                  getStringSliceFromBody(body, "members"),
		AllowSelfResponses:       getBoolFromBody(body, "allow_self_responses"),
		ActivationStrategy:       getIntFromBody(body, "activation_strategy"),
		GenerationMode:           getIntFromBody(body, "generation_mode"),
		ChatID:                   getStringFromBody(body, "chat_id", id),
		AutoModeDelay:            getIntFromBody(body, "auto_mode_delay", 5),
		GenerationModeJoinPrefix: getStringFromBody(body, "generation_mode_join_prefix"),
		GenerationModeJoinSuffix: getStringFromBody(body, "generation_mode_join_suffix"),
	}
	if chats := body["chats"]; chats != nil {
		gd.Chats = getStringSliceFromBody(body, "chats")
	} else {
		gd.Chats = []string{id}
	}
	if body["avatar_url"] != nil {
		gd.AvatarURL, _ = body["avatar_url"].(string)
	}
	if body["disabled_members"] != nil {
		gd.DisabledMembers = getStringSliceFromBody(body, "disabled_members")
	}
	if body["fav"] != nil {
		gd.Fav = body["fav"]
	}

	pathToFile := filepath.Join(uc.Directories.Groups, util.SanitizeFileName(id+".json"))
	util.WriteJSONFile(pathToFile, &gd)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(gd)
}

func (h *GroupHandler) Edit(w http.ResponseWriter, r *http.Request) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["id"] == nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !validFileField(body, "id") {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	id, _ := body["id"].(string)
	pathToFile := filepath.Join(uc.Directories.Groups, util.SanitizeFileName(id+".json"))
	data, _ := json.MarshalIndent(body, "", "    ")
	util.AtomicWrite(pathToFile, data)
	w.Write([]byte(`{"ok":true}`))
}

func (h *GroupHandler) Delete(w http.ResponseWriter, r *http.Request) {
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
	if !validFileField(map[string]any{"id": body.ID}, "id") {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	pathToGroup := filepath.Join(uc.Directories.Groups, util.SanitizeFileName(body.ID+".json"))

	var gd models.GroupData
	if err := util.ReadJSONFile(pathToGroup, &gd); err == nil {
		for _, chatID := range gd.Chats {
			chatFile := filepath.Join(uc.Directories.GroupChats, util.SanitizeFileName(chatID+".jsonl"))
			util.TryDeleteFile(chatFile)
		}
	}
	util.TryDeleteFile(pathToGroup)
	w.Write([]byte(`{"ok":true}`))
}

func getStringFromBody(m map[string]any, key string, def ...string) string {
	v := m[key]
	if v == nil {
		if len(def) > 0 {
			return def[0]
		}
		return ""
	}
	s, _ := v.(string)
	if s == "" && len(def) > 0 {
		return def[0]
	}
	return s
}

func getStringSliceFromBody(m map[string]any, key string) []string {
	v := m[key]
	if v == nil {
		return []string{}
	}
	switch val := v.(type) {
	case []any:
		var result []string
		for _, item := range val {
			if s, ok := item.(string); ok {
				result = append(result, s)
			}
		}
		return result
	case []string:
		return val
	default:
		return []string{}
	}
}

func getBoolFromBody(m map[string]any, key string) bool {
	v := m[key]
	if v == nil {
		return false
	}
	b, _ := v.(bool)
	return b
}

func getIntFromBody(m map[string]any, key string, def ...int) int {
	v := m[key]
	if v == nil {
		if len(def) > 0 {
			return def[0]
		}
		return 0
	}
	switch val := v.(type) {
	case float64:
		return int(val)
	case int:
		return val
	default:
		if len(def) > 0 {
			return def[0]
		}
		return 0
	}
}
