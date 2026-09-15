package handlers

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/TurtleTavern/turtletavern/internal/character"
	"github.com/TurtleTavern/turtletavern/internal/media"
	"github.com/TurtleTavern/turtletavern/internal/models"
	"github.com/TurtleTavern/turtletavern/internal/util"
	"gopkg.in/yaml.v3"
)

func (h *CharacterHandler) All(w http.ResponseWriter, r *http.Request) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	userFolder := uc.Directories.Characters

	if h.Index.NeedsRebuild(userFolder) {
		log.Println("[Character Index] Rebuilding character index...")
		rebuilt := h.Index.RebuildIndex(userFolder, character.ProcessCharacter, uc.Directories)
		sort.Slice(rebuilt, func(i, j int) bool {
			if rebuilt[i].DateLastChat != rebuilt[j].DateLastChat {
				return rebuilt[i].DateLastChat > rebuilt[j].DateLastChat
			}
			return rebuilt[i].Name < rebuilt[j].Name
		})
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(rebuilt)
		return
	}

	indexed := h.Index.GetAllCharacters(userFolder)
	if len(indexed) > 0 {
		sort.Slice(indexed, func(i, j int) bool {
			if indexed[i].DateLastChat != indexed[j].DateLastChat {
				return indexed[i].DateLastChat > indexed[j].DateLastChat
			}
			return indexed[i].Name < indexed[j].Name
		})
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(indexed)
		return
	}

	rebuilt := h.Index.RebuildIndex(userFolder, character.ProcessCharacter, uc.Directories)
	sort.Slice(rebuilt, func(i, j int) bool {
		if rebuilt[i].DateLastChat != rebuilt[j].DateLastChat {
			return rebuilt[i].DateLastChat > rebuilt[j].DateLastChat
		}
		return rebuilt[i].Name < rebuilt[j].Name
	})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(rebuilt)
}

func (h *CharacterHandler) Get(w http.ResponseWriter, r *http.Request) {
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
	filePath := filepath.Join(uc.Directories.Characters, body.AvatarURL)
	if !util.FileExists(filePath) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	data, err := character.ProcessCharacterFull(body.AvatarURL, uc.Directories)
	if err != nil {
		http.Error(w, "error processing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func (h *CharacterHandler) Chats(w http.ResponseWriter, r *http.Request) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	var body struct {
		AvatarURL string `json:"avatar_url"`
		Simple    bool   `json:"simple"`
		Metadata  bool   `json:"metadata"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.AvatarURL == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !validFileField(map[string]any{"avatar_url": body.AvatarURL}, "avatar_url") {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	charDir := strings.TrimSuffix(body.AvatarURL, ".png")
	chatsDir := filepath.Join(uc.Directories.Chats, charDir)
	if !util.FileExists(chatsDir) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"error":true}`))
		return
	}
	entries, err := os.ReadDir(chatsDir)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"error":true}`))
		return
	}
	var jsonFiles []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
			jsonFiles = append(jsonFiles, e.Name())
		}
	}
	if len(jsonFiles) == 0 {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("[]"))
		return
	}
	if body.Simple {
		type simpleChat struct {
			FileName string `json:"file_name"`
			FileID   string `json:"file_id"`
		}
		var result []simpleChat
		for _, f := range jsonFiles {
			result = append(result, simpleChat{
				FileName: f,
				FileID:   strings.TrimSuffix(f, ".jsonl"),
			})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
		return
	}

	var results []models.ChatInfo
	for _, f := range jsonFiles {
		info := getChatFileInfo(filepath.Join(chatsDir, f))
		results = append(results, info)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
}

func (h *CharacterHandler) RebuildIndex(w http.ResponseWriter, r *http.Request) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	userFolder := uc.Directories.Characters
	log.Println("[Character Index] Manual rebuild requested...")
	h.Index.ClearUserIndex(userFolder)
	rebuilt := h.Index.RebuildIndex(userFolder, character.ProcessCharacter, uc.Directories)
	log.Printf("[Character Index] Rebuilt %d characters", len(rebuilt))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"success": true, "count": len(rebuilt)})
}

func (h *CharacterHandler) Import(w http.ResponseWriter, r *http.Request) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	file, header, err := r.FormFile("avatar")
	if err != nil {
		http.Error(w, "no file", http.StatusBadRequest)
		return
	}
	defer file.Close()

	format := r.FormValue("file_type")
	preservedName := ""
	hadPreservedName := false
	if pn := r.FormValue("preserved_name"); pn != "" {
		preservedName = strings.TrimSuffix(filepath.Base(pn), filepath.Ext(pn))
		hadPreservedName = true
	}

	inputData, _ := io.ReadAll(file)

	isoNow := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	var charJSON map[string]any
	importFailed := false

	switch format {
	case "json":
		var jsonData map[string]any
		if err := json.Unmarshal(inputData, &jsonData); err != nil {
			importFailed = true
			break
		}
		if _, ok := jsonData["spec"]; ok {
			character.ImportRisuSprites(uc.Directories, jsonData)
			util.UnsetPrivateFields(jsonData)
			if fixed, _ := character.ReadFromV2(jsonData); fixed != nil {
				jsonData = fixed
			}
			jsonData["create_date"] = isoNow
			pngName := preservedName
			if pngName == "" {
				name := ""
				if data, ok := jsonData["data"].(map[string]any); ok {
					name, _ = data["name"].(string)
				}
				if name == "" {
					name, _ = jsonData["name"].(string)
				}
				pngName = util.GetPngName(name, uc.Directories.Characters)
			}
			out, _ := json.Marshal(jsonData)
			if err := character.WriteCharacterDataToFile(character.DefaultAvatarPNG, string(out), pngName, uc.Directories); err != nil {
				importFailed = true
				break
			}
			charJSON = jsonData
			preservedName = pngName
		} else if name, ok := jsonData["name"].(string); ok {
			name = util.SanitizeFileName(name)
			if notes, ok := jsonData["creator_notes"].(string); ok {
				jsonData["creator_notes"] = strings.ReplaceAll(notes, "Creator's notes go here.", "")
			}
			pngName := preservedName
			if pngName == "" {
				pngName = util.GetPngName(name, uc.Directories.Characters)
			}
			built := map[string]any{
				"name": name, "description": orAny(jsonData["description"]),
				"creatorcomment": orAny(jsonData["creatorcomment"], jsonData["creator_notes"]),
				"personality":    orAny(jsonData["personality"]),
				"first_mes":      orAny(jsonData["first_mes"]), "avatar": "none",
				"chat":        name + " - " + util.HumanizedDateTime(0),
				"mes_example": orAny(jsonData["mes_example"]), "scenario": orAny(jsonData["scenario"]),
				"create_date": isoNow, "talkativeness": orAny(jsonData["talkativeness"], 0.5),
				"creator": orAny(jsonData["creator"]), "tags": orAny(jsonData["tags"], ""),
			}
			converted := convertV1ToV2(built, name)
			out, _ := json.Marshal(converted)
			if err := character.WriteCharacterDataToFile(character.DefaultAvatarPNG, string(out), pngName, uc.Directories); err != nil {
				importFailed = true
				break
			}
			charJSON = converted
			preservedName = pngName
		} else if charName, ok := jsonData["char_name"].(string); ok {
			charName = util.SanitizeFileName(charName)
			if notes, ok := jsonData["creator_notes"].(string); ok {
				jsonData["creator_notes"] = strings.ReplaceAll(notes, "Creator's notes go here.", "")
			}
			pngName := preservedName
			if pngName == "" {
				pngName = util.GetPngName(charName, uc.Directories.Characters)
			}
			built := map[string]any{
				"name": charName, "description": orAny(jsonData["char_persona"]),
				"creatorcomment": orAny(jsonData["creatorcomment"], jsonData["creator_notes"]),
				"personality":    "", "first_mes": orAny(jsonData["char_greeting"]),
				"avatar": "none", "chat": charName + " - " + util.HumanizedDateTime(0),
				"mes_example": orAny(jsonData["example_dialogue"]),
				"scenario":    orAny(jsonData["world_scenario"]),
				"create_date": isoNow, "talkativeness": orAny(jsonData["talkativeness"], 0.5),
				"creator": orAny(jsonData["creator"]), "tags": orAny(jsonData["tags"], ""),
			}
			converted := convertV1ToV2(built, charName)
			out, _ := json.Marshal(converted)
			if err := character.WriteCharacterDataToFile(character.DefaultAvatarPNG, string(out), pngName, uc.Directories); err != nil {
				importFailed = true
				break
			}
			charJSON = converted
			preservedName = pngName
		} else {
			importFailed = true
		}

	case "png":
		jsonStr, err := character.ReadCharacterDataFromPNG(inputData)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"error":true}`))
			return
		}
		var pngData map[string]any
		_ = json.Unmarshal([]byte(jsonStr), &pngData)
		var dataName, topName string
		if data, ok := pngData["data"].(map[string]any); ok {
			dataName, _ = data["name"].(string)
		}
		topName, _ = pngData["name"].(string)
		name := util.SanitizeFileName(firstNonEmpty(dataName, topName))
		pngName := preservedName
		if pngName == "" {
			pngName = util.GetPngName(name, uc.Directories.Characters)
		}
		if _, ok := pngData["spec"]; ok {
			character.ImportRisuSprites(uc.Directories, pngData)
			util.UnsetPrivateFields(pngData)
			if fixed, _ := character.ReadFromV2(pngData); fixed != nil {
				pngData = fixed
			}
			pngData["create_date"] = isoNow
			out, _ := json.Marshal(pngData)
			if err := character.WriteCharacterDataToFile(inputData, string(out), pngName, uc.Directories); err != nil {
				importFailed = true
				break
			}
			charJSON = pngData
			preservedName = pngName
		} else if topName != "" {
			if notes, ok := pngData["creator_notes"].(string); ok {
				pngData["creator_notes"] = strings.ReplaceAll(notes, "Creator's notes go here.", "")
			}
			built := map[string]any{
				"name": name, "description": orAny(pngData["description"]),
				"creatorcomment": orAny(pngData["creatorcomment"], pngData["creator_notes"]),
				"personality":    orAny(pngData["personality"]),
				"first_mes":      orAny(pngData["first_mes"]), "avatar": "none",
				"chat":        name + " - " + util.HumanizedDateTime(0),
				"mes_example": orAny(pngData["mes_example"]), "scenario": orAny(pngData["scenario"]),
				"create_date": isoNow, "talkativeness": orAny(pngData["talkativeness"], 0.5),
				"creator": orAny(pngData["creator"]), "tags": orAny(pngData["tags"], ""),
			}
			converted := convertV1ToV2(built, name)
			out, _ := json.Marshal(converted)
			if err := character.WriteCharacterDataToFile(inputData, string(out), pngName, uc.Directories); err != nil {
				importFailed = true
				break
			}
			charJSON = converted
			preservedName = pngName
		} else {
			importFailed = true
		}

	case "yaml", "yml":
		var yamlData map[string]any
		if err := yaml.Unmarshal(inputData, &yamlData); err != nil {
			importFailed = true
			break
		}
		yamlName := util.SanitizeFileName(orAnyString(yamlData["name"]))
		pngName := preservedName
		if pngName == "" {
			pngName = util.GetPngName(yamlName, uc.Directories.Characters)
		}
		built := map[string]any{
			"name": yamlName, "description": orAny(yamlData["context"]),
			"first_mes": orAny(yamlData["greeting"]), "avatar": "none",
			"chat":        yamlName + " - " + util.HumanizedDateTime(0),
			"personality": "", "creatorcomment": "", "mes_example": "",
			"scenario": "", "create_date": isoNow, "talkativeness": 0.5,
			"creator": "", "tags": "",
		}
		converted := convertV1ToV2(built, yamlName)
		out, _ := json.Marshal(converted)
		if err := character.WriteCharacterDataToFile(character.DefaultAvatarPNG, string(out), pngName, uc.Directories); err != nil {
			importFailed = true
			break
		}
		charJSON = converted
		preservedName = pngName

	case "charx":
		parsed, err := character.ParseCharX(inputData)
		if err != nil {
			importFailed = true
			break
		}
		processed := parsed.Card
		if fixed, _ := character.ReadFromV2(processed); fixed != nil {
			processed = fixed
		}
		util.UnsetPrivateFields(processed)
		processed["create_date"] = isoNow
		procName, _ := processed["name"].(string)
		procName = util.SanitizeFileName(procName)
		processed["name"] = procName
		fileName := preservedName
		if fileName == "" {
			fileName = util.GetPngName(procName, uc.Directories.Characters)
		}
		if len(parsed.AuxiliaryAssets) > 0 {
			character.PersistCharXAssets(parsed.AuxiliaryAssets, parsed.ExtractedBuffers, uc.Directories, procName)
		}
		avatar := parsed.Avatar
		if avatar == nil {
			avatar = character.DefaultAvatarPNG
		}
		out, _ := json.Marshal(processed)
		if err := character.WriteCharacterDataToFile(avatar, string(out), fileName, uc.Directories); err != nil {
			importFailed = true
			break
		}
		charJSON = processed
		preservedName = fileName

	case "byaf":
		byafData, err := character.ParseBYAF(inputData)
		if err != nil {
			importFailed = true
			break
		}
		card := byafData.Card
		if fixed, _ := character.ReadFromV2(card); fixed != nil {
			card = fixed
		}
		cardName, _ := card["name"].(string)
		displayName := ""
		if byafData.Character != nil {
			displayName, _ = byafData.Character["displayName"].(string)
		}
		if displayName == "" {
			displayName = cardName
		}
		fileName := preservedName
		if fileName == "" {
			fileName = util.GetPngName(util.SanitizeFileName(displayName), uc.Directories.Characters)
		}
		userName := r.FormValue("user_name")
		if !hadPreservedName {
			for i := range byafData.ChatBackgrounds {
				bg := &byafData.ChatBackgrounds[i]
				ext := ".png"
				if len(bg.Paths) > 0 {
					if e := filepath.Ext(bg.Paths[0]); e != "" {
						ext = e
					}
				}
				base := filepath.Base(fileName) + "_bg"
				dir := filepath.Join(uc.Directories.UserImages, fileName)
				_ = os.MkdirAll(dir, 0o755)
				unique := goUniqueName(base, func(name string) bool {
					_, err := os.Stat(filepath.Join(dir, name+ext))
					return err == nil
				})
				newFile := unique + ext
				_ = util.AtomicWrite(filepath.Join(dir, newFile), bg.Data)
				bg.Name = clientRelativePath(uc.Directories.Root, filepath.Join(dir, newFile))
			}
			var chats []string
			for _, sc := range byafData.Scenarios {
				title, _ := sc["title"].(string)
				if title == "" {
					title = cardName
				}
				chatName := util.SanitizeFileName(title + " - " + util.HumanizedDateTime(0) + " imported.jsonl")
				chatName = strings.TrimSuffix(chatName, ".jsonl") + ".jsonl"
				chatDir := filepath.Join(uc.Directories.Chats, filepath.Base(fileName))
				_ = os.MkdirAll(chatDir, 0o755)
				chatContent := character.ByafChatFromScenario(sc, userName, cardName, byafData.ChatBackgrounds)
				_ = util.AtomicWrite(filepath.Join(chatDir, chatName), []byte(chatContent))
				chats = append(chats, chatName)
			}
			if len(chats) > 0 {
				card["chat"] = strings.TrimSuffix(chats[0], ".jsonl")
			}
			altFolder := filepath.Join(uc.Directories.Characters, util.SanitizeFileName(cardName))
			for _, icon := range byafData.Images[1:] {
				_ = os.MkdirAll(altFolder, 0o755)
				ext := filepath.Ext(icon.Filename)
				if ext == "" {
					ext = ".png"
				}
				label := util.SanitizeFileName(icon.Label)
				if label == "" {
					label = "alt"
				}
				unique := goUniqueName(label, func(name string) bool {
					_, err := os.Stat(filepath.Join(altFolder, name+ext))
					return err == nil
				})
				_ = util.AtomicWrite(filepath.Join(altFolder, unique+ext), icon.Image)
			}
		}
		avatar := character.DefaultAvatarPNG
		if len(byafData.Images) > 0 && len(byafData.Images[0].Image) > 0 {
			avatar = byafData.Images[0].Image
		}
		out, _ := json.Marshal(card)
		if err := character.WriteCharacterDataToFile(avatar, string(out), fileName, uc.Directories); err != nil {
			importFailed = true
			break
		}
		charJSON = card
		preservedName = fileName

	default:
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":true}`))
		return
	}

	if importFailed || charJSON == nil {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":true}`))
		return
	}

	name := ""
	if data, ok := charJSON["data"].(map[string]any); ok {
		name, _ = data["name"].(string)
	}
	if name == "" {
		name, _ = charJSON["name"].(string)
	}
	if name == "" {
		name = header.Filename
	}

	fileName := preservedName
	if hadPreservedName {
		media.InvalidateThumbnail(uc.Directories.Root, media.ThumbAvatar, fileName+".png")
	}

	h.Index.UpsertCharacter(uc.Directories.Characters, models.ShallowCharacter{
		Avatar: fileName + ".png", Name: name,
	})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"file_name": fileName})
}

func (h *CharacterHandler) Export(w http.ResponseWriter, r *http.Request) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	var body struct {
		Format    string `json:"format"`
		AvatarURL string `json:"avatar_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.AvatarURL == "" || body.Format == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !validFileField(map[string]any{"avatar_url": body.AvatarURL}, "avatar_url") {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	filename := filepath.Join(uc.Directories.Characters, body.AvatarURL)
	if !util.FileExists(filename) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	switch body.Format {
	case "png":
		data, err := os.ReadFile(filename)
		if err != nil {
			http.Error(w, "read failed", http.StatusInternalServerError)
			return
		}
		jsonStr, _ := character.ReadCharacterDataFromPNG(data)
		var charMap map[string]any
		json.Unmarshal([]byte(jsonStr), &charMap)
		util.UnsetPrivateFields(charMap)
		cleanJSON, _ := json.Marshal(charMap)
		cleanPNG, _ := character.WriteCharacterDataToPNG(data, string(cleanJSON))
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Content-Disposition", `attachment; filename="`+filepath.Base(filename)+`"`)
		w.Write(cleanPNG)

	case "json":
		jsonStr, err := character.ReadCharacterDataFromFile(filename)
		if err != nil {
			http.Error(w, "read failed", http.StatusInternalServerError)
			return
		}
		var charMap map[string]any
		json.Unmarshal([]byte(jsonStr), &charMap)
		charResult, _ := character.GetCharaCardV2(charMap, uc.Directories)
		util.UnsetPrivateFields(charResult)
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "    ")
		enc.Encode(charResult)

	default:
		http.Error(w, "unsupported format", http.StatusBadRequest)
	}
}

func convertV1ToV2(char map[string]any, name string) map[string]any {
	result := character.CharaFormatData(map[string]any{
		"json_data":           toJSON(char),
		"ch_name":             name,
		"description":         char["description"],
		"personality":         char["personality"],
		"scenario":            char["scenario"],
		"first_mes":           char["first_mes"],
		"mes_example":         char["mes_example"],
		"creator_notes":       char["creatorcomment"],
		"talkativeness":       char["talkativeness"],
		"fav":                 char["fav"],
		"creator":             char["creator"],
		"tags":                char["tags"],
		"depth_prompt_prompt": char["depth_prompt_prompt"],
		"depth_prompt_depth":  char["depth_prompt_depth"],
		"depth_prompt_role":   char["depth_prompt_role"],
	}, models.UserDirectories{})
	if chat, ok := char["chat"].(string); ok && chat != "" {
		result["chat"] = chat
	}
	if cd, ok := char["create_date"]; ok && cd != nil {
		result["create_date"] = cd
	}
	return result
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func getChatFileInfo(path string) models.ChatInfo {
	return getChatFileInfoEx(path, false, nil, nil)
}

func getChatFileInfoEx(path string, withMetadata bool, additional map[string]any, matcher func([]string) bool) models.ChatInfo {
	info := models.ChatInfo{Match: false, Mes: "[The chat is empty]"}
	stat, err := os.Stat(path)
	if err != nil {
		return models.ChatInfo{}
	}
	info.FileName = filepath.Base(path)
	info.FileID = strings.TrimSuffix(info.FileName, ".jsonl")
	info.FileSize = util.FormatBytes(stat.Size())
	info.LastMes = float64(stat.ModTime().UnixMilli())
	applyAdditionalData(&info, additional)
	if stat.Size() == 0 {
		return info
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return models.ChatInfo{}
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var lastLine map[string]any
	matched := false
	var buffer []string
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var msg map[string]any
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		if i == 0 && withMetadata {
			if meta, ok := msg["chat_metadata"].(map[string]any); ok {
				info.ChatMetadata = meta
			}
		}
		if matcher != nil && !matched && i > 0 {
			if mes, ok := msg["mes"].(string); ok {
				buffer = append(buffer, mes)
				if matcher(buffer) {
					matched = true
					buffer = nil
				}
			}
		}
		lastLine = msg
	}
	if lastLine == nil {
		return models.ChatInfo{}
	}
	_, hasName := lastLine["name"]
	_, hasCharName := lastLine["character_name"]
	_, hasMeta := lastLine["chat_metadata"]
	if !hasName && !hasCharName && !hasMeta {
		return models.ChatInfo{}
	}
	count := 0
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	info.ChatItems = count - 1
	if mes, ok := lastLine["mes"].(string); ok && mes != "" {
		info.Mes = mes
	} else {
		info.Mes = "[The message is empty]"
	}
	if sendDate, ok := lastLine["send_date"]; ok && sendDate != nil {
		info.LastMes = sendDate
	}
	if matcher != nil {
		info.Match = matched
	} else {
		info.Match = true
	}
	return info
}

func applyAdditionalData(info *models.ChatInfo, additional map[string]any) {
	if additional == nil {
		return
	}
	if v, ok := additional["avatar"].(string); ok {
		info.Avatar = v
	}
	if v, ok := additional["group"].(string); ok {
		info.Group = v
	}
	if v, ok := additional["chat_metadata"]; ok {
		info.ChatMetadata = v
	}
}
