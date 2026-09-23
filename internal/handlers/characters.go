package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/TurtleTavern/turtletavern/internal/character"
	"github.com/TurtleTavern/turtletavern/internal/media"
	"github.com/TurtleTavern/turtletavern/internal/models"
	"github.com/TurtleTavern/turtletavern/internal/util"
	"github.com/go-chi/chi/v5"
)

type CharacterHandler struct {
	Index     *character.Index
	recompute *recomputeProgress
}

func NewCharacterHandler(idx *character.Index) *CharacterHandler {
	return &CharacterHandler{Index: idx, recompute: &recomputeProgress{}}
}

func (h *CharacterHandler) RegisterRoutes(r chi.Router) {
	r.Route("/api/characters", func(r chi.Router) {
		r.Post("/create", h.Create)
		r.Post("/edit", h.Edit)
		r.Post("/edit-avatar", h.EditAvatar)
		r.Post("/edit-attribute", h.EditAttribute)
		r.Post("/merge-attributes", h.MergeAttributes)
		r.Post("/delete", h.Delete)
		r.Post("/rename", h.Rename)
		r.Post("/duplicate", h.Duplicate)
		r.Post("/import", h.Import)
		r.Post("/export", h.Export)
		r.Post("/all", h.All)
		r.Post("/get", h.Get)
		r.Post("/chats", h.Chats)
		r.Post("/rebuild-index", h.RebuildIndex)
		r.Post("/recompute-recent", h.RecomputeRecent)
		r.Get("/recompute-recent/status", h.RecomputeStatus)
	})
}

func extractFormData(r *http.Request) map[string]any {
	formData := make(map[string]any)
	for k, v := range r.Form {
		if len(v) > 0 {
			formData[k] = v[0]
		}
	}
	return formData
}

func (h *CharacterHandler) Create(w http.ResponseWriter, r *http.Request) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil && err != http.ErrNotMultipart {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	chName := util.SanitizeFileName(r.FormValue("ch_name"))
	if chName == "" || chName == "." {
		http.Error(w, "invalid name", http.StatusBadRequest)
		return
	}
	fileName := r.FormValue("file_name")
	if fileName != "" && !validFileField(map[string]any{"file_name": fileName}, "file_name") {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if fileName == "" {
		fileName = util.GetPngName(chName, uc.Directories.Characters)
	}
	formData := extractFormData(r)
	formData["ch_name"] = chName

	char := character.CharaFormatData(formData, uc.Directories)
	charJSON, _ := json.Marshal(char)
	os.MkdirAll(filepath.Join(uc.Directories.Chats, fileName), 0o755)

	var inputImage []byte
	if file, _, err := r.FormFile("avatar"); err == nil {
		defer file.Close()
		inputImage, _ = io.ReadAll(file)
		if rawCrop := r.URL.Query().Get("crop"); rawCrop != "" && inputImage != nil {
			if cropped, err := cropAvatarImage(inputImage, rawCrop); err == nil {
				inputImage = cropped
			}
		}
	}

	if err := character.WriteCharacterDataToFile(inputImage, string(charJSON), fileName, uc.Directories); err != nil {
		log.Printf("create character: %v", err)
		http.Error(w, "write failed", http.StatusInternalServerError)
		return
	}
	avatarName := fileName + ".png"
	h.Index.UpsertCharacter(uc.Directories.Characters, models.ShallowCharacter{Avatar: avatarName, Name: chName})
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte(avatarName))
}

func (h *CharacterHandler) Edit(w http.ResponseWriter, r *http.Request) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil && err != http.ErrNotMultipart {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	chName := r.FormValue("ch_name")
	avatarURL := r.FormValue("avatar_url")
	if chName == "" || chName == "." || avatarURL == "" {
		http.Error(w, "invalid params", http.StatusBadRequest)
		return
	}
	if !validFileField(map[string]any{"avatar_url": avatarURL}, "avatar_url") {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	targetFile := strings.TrimSuffix(avatarURL, ".png")
	formData := extractFormData(r)
	formData["ch_name"] = chName

	char := character.CharaFormatData(formData, uc.Directories)
	if chat := r.FormValue("chat"); chat != "" {
		char["chat"] = chat
	}
	if cd := r.FormValue("create_date"); cd != "" {
		char["create_date"] = cd
	}
	charJSON, _ := json.Marshal(char)

	if file, _, err := r.FormFile("avatar"); err == nil {
		defer file.Close()
		inputImage, _ := io.ReadAll(file)
		media.InvalidateThumbnail(uc.Directories.Root, media.ThumbAvatar, avatarURL)
		character.WriteCharacterDataToFile(inputImage, string(charJSON), targetFile, uc.Directories)
	} else {
		avatarPath := filepath.Join(uc.Directories.Characters, avatarURL)
		inputImage, _ := os.ReadFile(avatarPath)
		character.WriteCharacterDataToFile(inputImage, string(charJSON), targetFile, uc.Directories)
	}
	h.Index.UpsertCharacter(uc.Directories.Characters, models.ShallowCharacter{Avatar: avatarURL, Name: chName})
	w.WriteHeader(http.StatusOK)
}

func (h *CharacterHandler) EditAvatar(w http.ResponseWriter, r *http.Request) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil && err != http.ErrNotMultipart {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	avatarURL := r.FormValue("avatar_url")
	if avatarURL == "" {
		http.Error(w, "missing avatar_url", http.StatusBadRequest)
		return
	}
	if !validFileField(map[string]any{"avatar_url": avatarURL}, "avatar_url") {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	file, _, err := r.FormFile("avatar")
	if err != nil {
		http.Error(w, "no file uploaded", http.StatusBadRequest)
		return
	}
	defer file.Close()
	inputImage, _ := io.ReadAll(file)
	charPath := filepath.Join(uc.Directories.Characters, avatarURL)
	if !util.FileExists(charPath) {
		http.Error(w, "character not found", http.StatusBadRequest)
		return
	}
	data, err := character.ReadCharacterDataFromFile(charPath)
	if err != nil {
		http.Error(w, "cannot read character", http.StatusBadRequest)
		return
	}
	targetFile := strings.TrimSuffix(avatarURL, ".png")
	character.WriteCharacterDataToFile(inputImage, data, targetFile, uc.Directories)
	media.InvalidateThumbnail(uc.Directories.Root, media.ThumbAvatar, avatarURL)
	w.WriteHeader(http.StatusOK)
}

func (h *CharacterHandler) EditAttribute(w http.ResponseWriter, r *http.Request) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	var body struct {
		AvatarURL string `json:"avatar_url"`
		ChName    string `json:"ch_name"`
		Field     string `json:"field"`
		Value     any    `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.AvatarURL == "" || body.Field == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !validFileField(map[string]any{"avatar_url": body.AvatarURL}, "avatar_url") {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if body.Field == "json_data" {
		http.Error(w, "cannot edit json_data", http.StatusBadRequest)
		return
	}
	charPath := filepath.Join(uc.Directories.Characters, body.AvatarURL)
	charData, err := character.ReadCharacterDataFromFile(charPath)
	if err != nil {
		http.Error(w, "read failed", http.StatusInternalServerError)
		return
	}
	var charMap map[string]any
	json.Unmarshal([]byte(charData), &charMap)
	if _, ok := charMap[body.Field]; !ok {
		if data, ok := charMap["data"].(map[string]any); !ok {
			http.Error(w, "Error: invalid field.", http.StatusBadRequest)
			return
		} else if _, ok := data[body.Field]; !ok {
			http.Error(w, "Error: invalid field.", http.StatusBadRequest)
			return
		}
	}
	charMap[body.Field] = body.Value
	if data, ok := charMap["data"].(map[string]any); ok {
		data[body.Field] = body.Value
	}
	charJSON, _ := json.Marshal(charMap)
	targetFile := strings.TrimSuffix(body.AvatarURL, ".png")
	imgBytes, _ := os.ReadFile(charPath)
	character.WriteCharacterDataToFile(imgBytes, string(charJSON), targetFile, uc.Directories)
	w.WriteHeader(http.StatusOK)
}

func (h *CharacterHandler) MergeAttributes(w http.ResponseWriter, r *http.Request) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	var update map[string]any
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil || update == nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	avatar, _ := update["avatar"].(string)
	if avatar == "" {
		http.Error(w, "missing avatar", http.StatusBadRequest)
		return
	}
	if !validFileField(map[string]any{"avatar": avatar}, "avatar") {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	charPath := filepath.Join(uc.Directories.Characters, avatar)
	pngData, err := character.ReadCharacterDataFromFile(charPath)
	if err != nil {
		http.Error(w, "read failed", http.StatusBadRequest)
		return
	}
	var character2 map[string]any
	if err := json.Unmarshal([]byte(pngData), &character2); err != nil || character2 == nil {
		http.Error(w, "failed to parse character data", http.StatusInternalServerError)
		return
	}
	delete(update, "json_data")
	delete(character2, "json_data")
	merged := util.DeepMerge(character2, update)
	charJSON, _ := json.Marshal(merged)
	targetFile := strings.TrimSuffix(avatar, ".png")
	imgBytes, _ := os.ReadFile(charPath)
	character.WriteCharacterDataToFile(imgBytes, string(charJSON), targetFile, uc.Directories)
	mergedName, _ := merged["name"].(string)
	h.Index.UpsertCharacter(uc.Directories.Characters, models.ShallowCharacter{Avatar: avatar, Name: mergedName})
	w.WriteHeader(http.StatusOK)
}

func removeDateAddedEntry(charactersDir, dirName string) {
	if dirName == "" {
		return
	}
	dictPath := filepath.Join(charactersDir, "date_added.json")
	data, err := os.ReadFile(dictPath)
	if err != nil {
		return
	}
	var dict map[string]any
	if err := json.Unmarshal(data, &dict); err != nil {
		return
	}
	if _, ok := dict[dirName]; !ok {
		return
	}
	delete(dict, dirName)
	if out, err := json.Marshal(dict); err == nil {
		_ = util.AtomicWrite(dictPath, out)
	}
}

func (h *CharacterHandler) Delete(w http.ResponseWriter, r *http.Request) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	var body struct {
		AvatarURL   string `json:"avatar_url"`
		DeleteChats bool   `json:"delete_chats"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.AvatarURL == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !validFileField(map[string]any{"avatar_url": body.AvatarURL}, "avatar_url") {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	safeName := util.SanitizeFileName(body.AvatarURL)
	if safeName != body.AvatarURL {
		http.Error(w, "malicious filename", http.StatusForbidden)
		return
	}
	avatarPath := filepath.Join(uc.Directories.Characters, body.AvatarURL)
	if !util.FileExists(avatarPath) {
		http.Error(w, "not found", http.StatusBadRequest)
		return
	}
	os.Remove(avatarPath)
	media.InvalidateThumbnail(uc.Directories.Root, media.ThumbAvatar, body.AvatarURL)
	dirName := strings.TrimSuffix(body.AvatarURL, ".png")
	removeDateAddedEntry(uc.Directories.Characters, dirName)
	if dirName == "" {
		http.Error(w, "malicious dirname", http.StatusForbidden)
		return
	}

	h.Index.DeleteCharacter(uc.Directories.Characters, body.AvatarURL)

	if body.DeleteChats && dirName != "" {
		os.RemoveAll(filepath.Join(uc.Directories.Chats, dirName))
	}
	w.WriteHeader(http.StatusOK)
}

func (h *CharacterHandler) Rename(w http.ResponseWriter, r *http.Request) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	var body struct {
		AvatarURL string `json:"avatar_url"`
		NewName   string `json:"new_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.AvatarURL == "" || body.NewName == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !validFileField(map[string]any{"avatar_url": body.AvatarURL}, "avatar_url") {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	newName := util.SanitizeFileName(body.NewName)
	oldInternal := strings.TrimSuffix(body.AvatarURL, ".png")
	newInternal := util.GetPngName(newName, uc.Directories.Characters)
	oldPath := filepath.Join(uc.Directories.Characters, body.AvatarURL)

	charData, err := character.ReadCharacterDataFromFile(oldPath)
	if err != nil {
		http.Error(w, "read failed", http.StatusInternalServerError)
		return
	}
	var charMap map[string]any
	if err := json.Unmarshal([]byte(charData), &charMap); err != nil || charMap == nil {
		http.Error(w, "failed to parse character data", http.StatusInternalServerError)
		return
	}
	charMap["name"] = newName
	if data, ok := charMap["data"].(map[string]any); ok {
		data["name"] = newName
	}
	charJSON, _ := json.Marshal(charMap)

	imgBytes, _ := os.ReadFile(oldPath)
	character.WriteCharacterDataToFile(imgBytes, string(charJSON), newInternal, uc.Directories)

	oldChatsPath := filepath.Join(uc.Directories.Chats, oldInternal)
	newChatsPath := filepath.Join(uc.Directories.Chats, newInternal)
	if util.FileExists(oldChatsPath) && !util.FileExists(newChatsPath) {
		util.MoveDir(oldChatsPath, newChatsPath)
		os.RemoveAll(oldChatsPath)
	}

	os.Remove(oldPath)
	h.Index.DeleteCharacter(uc.Directories.Characters, body.AvatarURL)
	h.Index.UpsertCharacter(uc.Directories.Characters, models.ShallowCharacter{Avatar: newInternal + ".png", Name: newName})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"avatar": newInternal + ".png"})
}

func (h *CharacterHandler) Duplicate(w http.ResponseWriter, r *http.Request) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	var body struct {
		AvatarURL string `json:"avatar_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.AvatarURL == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !validFileField(map[string]any{"avatar_url": body.AvatarURL}, "avatar_url") {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	srcPath := filepath.Join(uc.Directories.Characters, body.AvatarURL)
	if !util.FileExists(srcPath) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	baseName := strings.TrimSuffix(body.AvatarURL, ".png")
	parts := strings.Split(baseName, "_")
	suffix := 1
	nameBase := baseName
	lastPart := parts[len(parts)-1]
	if n, err := strconv.Atoi(lastPart); err == nil && len(parts) > 1 {
		suffix = n + 1
		nameBase = strings.Join(parts[:len(parts)-1], "_")
	}
	newFilename := fmt.Sprintf("%s_%d.png", nameBase, suffix)
	for util.FileExists(filepath.Join(uc.Directories.Characters, newFilename)) {
		suffix++
		newFilename = fmt.Sprintf("%s_%d.png", nameBase, suffix)
	}
	dstPath := filepath.Join(uc.Directories.Characters, newFilename)
	util.CopyFile(srcPath, dstPath)
	h.Index.UpsertCharacter(uc.Directories.Characters, models.ShallowCharacter{Avatar: newFilename, Name: nameBase})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"path": newFilename})
}
