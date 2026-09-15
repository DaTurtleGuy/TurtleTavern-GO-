package handlers

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/TurtleTavern/turtletavern/internal/character"
	"github.com/TurtleTavern/turtletavern/internal/config"
	"github.com/TurtleTavern/turtletavern/internal/llm"
	"github.com/TurtleTavern/turtletavern/internal/media"
	"github.com/TurtleTavern/turtletavern/internal/util"
	"github.com/go-chi/chi/v5"
)

const importUserAgent = "SillyTavern"

var uuidRe = regexp.MustCompile(`[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}`)
var perchanceUUIDRe = regexp.MustCompile(`^\w+~[a-f0-9]{32}\.gz$`)
var risuURLRe = regexp.MustCompile(`^https?://realm\.risuai\.net/character/([a-f0-9-]+)/?$`)

type ImportHandler struct {
	Cfg        *config.Config
	client     *http.Client
	PublicDir  string
	ServerRoot string
}

func NewImportHandler(cfg *config.Config, publicDir string) *ImportHandler {
	return &ImportHandler{Cfg: cfg, client: llm.NewHTTPClient(cfg), PublicDir: publicDir}
}

func (h *ImportHandler) RegisterRoutes(r chi.Router) {
	r.Route("/api/content", func(r chi.Router) {
		r.Post("/importURL", h.ImportURL)
		r.Post("/importUUID", h.ImportUUID)
	})
}

type downloadedContent struct {
	buffer   []byte
	fileName string
	fileType string
}

func (h *ImportHandler) fetchBytes(ctx context.Context, target string, headers map[string]string) ([]byte, string, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, "", 0, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, "", 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	if err != nil {
		return nil, "", resp.StatusCode, err
	}
	return data, resp.Header.Get("Content-Type"), resp.StatusCode, nil
}

func getHost(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func (h *ImportHandler) hostWhitelisted(host string) bool {
	for _, d := range h.Cfg.WhitelistImportDomains {
		if d == host {
			return true
		}
	}
	return false
}

func uuidFromURL(rawURL string) string {
	return uuidRe.FindString(rawURL)
}

func parseChubURL(s string) (id, typ string, ok bool) {
	parts := strings.Split(s, "/")
	if len(parts) < 2 {
		return "", "", false
	}
	domainIdx := -1
	for i, part := range parts {
		if part == "www.chub.ai" || part == "chub.ai" || part == "www.characterhub.org" || part == "characterhub.org" {
			domainIdx = i
		}
	}
	var tail []string
	if domainIdx != -1 {
		tail = parts[domainIdx+1:]
	} else {
		tail = parts
	}
	if len(tail) == 0 {
		return "", "", false
	}
	first := strings.ToLower(tail[0])
	if first == "characters" || first == "lorebooks" {
		t := "character"
		if first == "lorebooks" {
			t = "lorebook"
		}
		if t == "character" {
			return strings.Join(tail[1:], "/"), t, true
		}
		return strings.Join(tail, "/"), t, true
	}
	if len(parts) == 2 {
		return strings.Join(tail, "/"), "character", true
	}
	return "", "", false
}

func parseAICC(rawURL string) string {
	if isValidURL(rawURL) {
		if u, err := url.Parse(rawURL); err == nil {
			parts := []string{}
			for _, p := range strings.Split(u.Path, "/") {
				if p != "" {
					parts = append(parts, p)
				}
			}
			if len(parts) >= 2 {
				return parts[len(parts)-2] + "/" + parts[len(parts)-1]
			}
			return ""
		}
	}
	parts := []string{}
	for _, p := range strings.Split(rawURL, "/") {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) >= 2 {
		return parts[len(parts)-2] + "/" + parts[len(parts)-1]
	}
	return ""
}

func parseRisuURL(rawURL string) string {
	if m := risuURLRe.FindStringSubmatch(rawURL); len(m) == 2 {
		return m[1]
	}
	return ""
}

func parsePerchanceSlug(rawURL string) string {
	if idx := strings.Index(rawURL, "~"); idx >= 0 {
		return rawURL[idx+1:]
	}
	return ""
}

func isPerchanceUUID(s string) bool {
	return s != "" && perchanceUUIDRe.MatchString(s)
}

func (h *ImportHandler) downloadChubLorebook(ctx context.Context, id string) (*downloadedContent, error) {
	parts := strings.Split(id, "/")
	if len(parts) < 3 {
		return nil, errImport("Failed to fetch lorebook metadata")
	}
	metaData, _, code, err := h.getJSON(ctx, "https://api.chub.ai/api/"+parts[0]+"/"+parts[1]+"/"+parts[2], true)
	if err != nil || code < 200 || code >= 300 {
		return nil, errImport("Failed to fetch lorebook metadata")
	}
	node, _ := metaData["node"].(map[string]any)
	var projectID float64
	if node != nil {
		projectID, _ = node["id"].(float64)
	}
	if projectID == 0 {
		return nil, errImport("Project ID not found in lorebook metadata")
	}
	dlURL := "https://api.chub.ai/api/v4/projects/" + itoaFloat(projectID) + "/repository/files/raw%252Fsillytavern_raw.json/raw"
	data, contentType, code, err := h.fetchBytes(ctx, dlURL, map[string]string{"Accept": "application/json", "User-Agent": importUserAgent})
	if err != nil || code < 200 || code >= 300 {
		return nil, errImport("Failed to download lorebook")
	}
	name := parts[len(parts)-1]
	return &downloadedContent{buffer: data, fileName: util.SanitizeFileName(name) + ".json", fileType: contentType}, nil
}

func itoaFloat(f float64) string {
	return strings.TrimSuffix(strings.TrimSuffix(jsonNumber(f), ".0"), ".00")
}

func jsonNumber(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}

func (h *ImportHandler) getJSON(ctx context.Context, target string, auth bool) (map[string]any, string, int, error) {
	_ = auth
	data, contentType, code, err := h.fetchBytes(ctx, target, map[string]string{"Accept": "application/json", "User-Agent": importUserAgent})
	if err != nil {
		return nil, "", code, err
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, contentType, code, err
	}
	return parsed, contentType, code, nil
}

func (h *ImportHandler) downloadChubCharacter(ctx context.Context, id string) (*downloadedContent, error) {
	parts := strings.Split(id, "/")
	if len(parts) < 2 {
		return nil, errImport("Failed to fetch character metadata")
	}
	meta, _, code, err := h.getJSON(ctx, "https://api.chub.ai/api/characters/"+parts[0]+"/"+parts[1]+"?full=true", true)
	if err != nil || code < 200 || code >= 300 {
		return nil, errImport("Failed to fetch character metadata")
	}
	node, _ := meta["node"].(map[string]any)
	if node == nil {
		return nil, errImport("Failed to fetch character metadata")
	}
	definition, _ := node["definition"].(map[string]any)
	topics, _ := node["topics"].([]any)
	var topicNames []string
	for _, t := range topics {
		if s, ok := t.(string); ok {
			topicNames = append(topicNames, s)
		}
	}
	strField := func(m map[string]any, key string) string {
		s, _ := m[key].(string)
		return s
	}
	card := map[string]any{
		"data": map[string]any{
			"name":                      strField(definition, "name"),
			"description":               strField(definition, "personality"),
			"personality":               strField(definition, "tavern_personality"),
			"scenario":                  strField(definition, "scenario"),
			"first_mes":                 strField(definition, "first_message"),
			"mes_example":               strField(definition, "example_dialogs"),
			"creator_notes":             strField(definition, "description"),
			"system_prompt":             strField(definition, "system_prompt"),
			"post_history_instructions": strField(definition, "post_history_instructions"),
			"alternate_greetings":       definition["alternate_greetings"],
			"tags":                      topicNames,
			"creator":                   "",
			"character_version":         "",
			"character_book":            definition["embedded_lorebook"],
			"extensions":                definition["extensions"],
		},
		"spec": "chara_card_v2", "spec_version": "2.0",
	}
	imageURL := ""
	if n, ok := node["max_res_url"].(string); ok {
		imageURL = n
	}
	imageBuffer := h.defaultAvatar()
	if imageURL != "" {
		if data, _, code, err := h.fetchBytes(ctx, imageURL, nil); err == nil && code >= 200 && code < 300 {
			imageBuffer = data
		}
	}
	cardJSON, _ := json.Marshal(card)
	embedded, err := character.WriteCharacterDataToPNG(imageBuffer, string(cardJSON))
	if err != nil {
		return nil, err
	}
	name, _ := definition["name"].(string)
	return &downloadedContent{buffer: embedded, fileName: util.SanitizeFileName(name) + ".png", fileType: "image/png"}, nil
}

func (h *ImportHandler) defaultAvatar() []byte {
	candidates := []string{
		filepath.Join(h.ServerRoot, "public", "img", "ai4.png"),
		filepath.Join(h.PublicDir, "img", "ai4.png"),
	}
	for _, p := range candidates {
		if data, err := os.ReadFile(p); err == nil {
			return data
		}
	}
	return []byte{}
}

func (h *ImportHandler) downloadPygmalionCharacter(ctx context.Context, id string) (*downloadedContent, error) {
	data, contentType, code, err := h.fetchBytes(ctx, "https://server.pygmalion.chat/api/export/character/"+id+"/v2",
		map[string]string{"Accept": "application/json"})
	if err != nil || code < 200 || code >= 300 {
		return nil, errImport("Failed to download character")
	}
	_ = contentType
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, errImport("Failed to download character")
	}
	characterData, ok := parsed["character"].(map[string]any)
	if !ok {
		return nil, errImport("Failed to download character")
	}
	if avatarURL, ok := characterData["data"].(map[string]any)["avatar"].(string); ok && avatarURL != "" {
		if avatarData, _, code, err := h.fetchBytes(ctx, avatarURL, nil); err == nil && code >= 200 && code < 300 {
			cardJSON, _ := json.Marshal(characterData)
			if embedded, err := character.WriteCharacterDataToPNG(avatarData, string(cardJSON)); err == nil {
				return &downloadedContent{buffer: embedded, fileName: util.SanitizeFileName(id) + ".png", fileType: "image/png"}, nil
			}
		}
	}
	return &downloadedContent{buffer: data, fileName: util.SanitizeFileName(id) + ".json", fileType: "application/json"}, nil
}

func (h *ImportHandler) downloadJannyCharacter(ctx context.Context, uuid string) (*downloadedContent, error) {
	payload, _ := json.Marshal(map[string]string{"characterId": uuid})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.jannyai.com/api/v1/download", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respData, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, errImport("Failed to download character")
	}
	var parsed map[string]any
	if err := json.Unmarshal(respData, &parsed); err != nil {
		return nil, err
	}
	if status, _ := parsed["status"].(string); status != "ok" {
		return nil, errImport("Failed to download character")
	}
	downloadURL, _ := parsed["downloadUrl"].(string)
	imgData, contentType, code, err := h.fetchBytes(ctx, downloadURL, nil)
	if err != nil || code < 200 || code >= 300 {
		return nil, errImport("Failed to download character")
	}
	return &downloadedContent{buffer: imgData, fileName: util.SanitizeFileName(uuid) + ".png", fileType: contentType}, nil
}

func (h *ImportHandler) downloadAICCCharacter(ctx context.Context, id string) (*downloadedContent, error) {
	data, contentType, code, err := h.fetchBytes(ctx, "https://aicharactercards.com/wp-json/pngapi/v1/image/"+id, nil)
	if err != nil || code < 200 || code >= 300 {
		return nil, errImport("Failed to download character")
	}
	if contentType == "" {
		contentType = "image/png"
	}
	return &downloadedContent{buffer: data, fileName: util.SanitizeFileName(id) + ".png", fileType: contentType}, nil
}

func (h *ImportHandler) downloadGenericPNG(ctx context.Context, rawURL string) (*downloadedContent, error) {
	data, contentType, code, err := h.fetchBytes(ctx, rawURL, nil)
	if err != nil || code < 200 || code >= 300 {
		return nil, errImport("Error downloading file")
	}
	if contentType == "" {
		contentType = "image/png"
	}
	u, _ := url.Parse(rawURL)
	segments := strings.Split(strings.TrimSuffix(u.Path, "/"), "/")
	fileName := util.SanitizeFileName(segments[len(segments)-1])
	if contentType == "image/png" {
		matched := false
		if dot := strings.LastIndex(fileName, "."); dot >= 0 {
			if len(fileName)-dot-1 >= 1 && len(fileName)-dot-1 <= 5 {
				matched = true
			}
		}
		if !matched {
			fileName += ".png"
		}
	}
	return &downloadedContent{buffer: data, fileName: fileName, fileType: contentType}, nil
}

func (h *ImportHandler) downloadRisuCharacter(ctx context.Context, uuid string) (*downloadedContent, error) {
	data, _, code, err := h.fetchBytes(ctx, "https://realm.risuai.net/api/v1/download/png-v3/"+uuid+"?non_commercial=true", nil)
	if err != nil || code < 200 || code >= 300 {
		return nil, errImport("Failed to download character")
	}
	return &downloadedContent{buffer: data, fileName: util.SanitizeFileName(uuid) + ".png", fileType: "image/png"}, nil
}

func (h *ImportHandler) downloadPerchanceCharacter(ctx context.Context, slug string) (*downloadedContent, error) {
	charURL := "https://user.uploads.dev/file/" + slug
	data, _, code, err := h.fetchBytes(ctx, charURL, map[string]string{"Content-Type": "application/json", "User-Agent": importUserAgent})
	if err != nil || code < 200 || code >= 300 {
		return nil, errImport("Failed to download character")
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, errImport("Failed to download character: Invalid Perchance character data")
	}
	raw, err := io.ReadAll(io.LimitReader(gz, 256<<20))
	gz.Close()
	if err != nil || len(raw) == 0 {
		return nil, errImport("Failed to download character: Invalid Perchance character data")
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, errImport("Failed to download character: Invalid Perchance character data")
	}
	addChar, ok := parsed["addCharacter"].(map[string]any)
	if !ok {
		return nil, errImport("Failed to download character: Invalid Perchance character data")
	}
	strField := func(m map[string]any, key string) string {
		s, _ := m[key].(string)
		return s
	}
	name := strField(addChar, "name")
	if name == "" {
		name = "Unnamed Perchance Character"
	}
	avatarURL := ""
	if av, ok := addChar["avatar"].(map[string]any); ok {
		avatarURL, _ = av["url"].(string)
	}
	isBase64 := strings.HasPrefix(avatarURL, "data:image/")
	charData := map[string]any{
		"name": name, "first_mes": "", "tags": []string{},
		"description":         strField(addChar, "roleInstruction"),
		"creator":             strField(addChar, "metaTitle"),
		"creator_notes":       strField(addChar, "metaDescription"),
		"alternate_greetings": []string{}, "character_version": "",
		"mes_example": "", "post_history_instructions": "", "system_prompt": "",
		"scenario": "", "personality": strField(addChar, "reminderMessage"),
		"extensions": map[string]any{"perchance_data": map[string]any{
			"slug": slug, "char_url": charURL, "uuid": addChar["uuid"],
			"avatar_url": func() any {
				if isBase64 {
					return nil
				}
				if avatarURL != "" {
					return avatarURL
				}
				return nil
			}(),
			"folder_path": addChar["folderPath"], "folder_name": addChar["folderName"],
			"custom_data": withDefaultMap(addChar["customData"]),
		}},
	}
	avatarBuffer := h.fetchPerchanceAvatar(ctx, avatarURL, isBase64)
	cardJSON, _ := json.Marshal(map[string]any{"spec": "chara_card_v2", "spec_version": "2.0", "data": charData})
	embedded, err := character.WriteCharacterDataToPNG(avatarBuffer, string(cardJSON))
	if err != nil {
		return nil, err
	}
	return &downloadedContent{buffer: embedded, fileName: name + ".png", fileType: "image/png"}, nil
}

func withDefaultMap(v any) any {
	if v == nil {
		return map[string]any{}
	}
	return v
}

func (h *ImportHandler) fetchPerchanceAvatar(ctx context.Context, avatarURL string, isBase64 bool) []byte {
	if len(h.defaultAvatar()) > 0 && (avatarURL == "" || (!isBase64 && !isValidURL(avatarURL))) {
		return h.defaultAvatar()
	}
	if isBase64 {
		parts := strings.SplitN(avatarURL, ",", 2)
		if len(parts) != 2 {
			return h.defaultAvatar()
		}
		if raw, err := base64.StdEncoding.DecodeString(parts[1]); err == nil {
			if strings.HasPrefix(avatarURL, "data:image/png;base64,") {
				return raw
			}
			if img, err := media.DecodeImage(raw); err == nil {
				if out, err := media.EncodePNG(img); err == nil {
					return out
				}
			}
		}
		return h.defaultAvatar()
	}
	data, contentType, code, err := h.fetchBytes(ctx, avatarURL, map[string]string{"User-Agent": importUserAgent})
	if err != nil || code < 200 || code >= 300 {
		return h.defaultAvatar()
	}
	if contentType == "image/png" {
		return data
	}
	if img, err := media.DecodeImage(data); err == nil {
		if out, err := media.EncodePNG(img); err == nil {
			return out
		}
	}
	return h.defaultAvatar()
}

func decodeBase64(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}

func (h *ImportHandler) ImportURL(w http.ResponseWriter, r *http.Request) {
	body := translateBody(r)
	rawURL, _ := body["url"].(string)
	if rawURL == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	host := getHost(rawURL)
	isChub := strings.Contains(host, "chub.ai") || strings.Contains(host, "characterhub.org")
	isJanny := strings.Contains(host, "janitorai")
	isPygmalion := strings.Contains(host, "pygmalion.chat")
	isAICC := strings.Contains(host, "aicharactercards.com")
	isRisu := strings.Contains(host, "realm.risuai.net")
	isPerchance := strings.Contains(host, "perchance.org")
	isGeneric := h.hostWhitelisted(host)

	var result *downloadedContent
	var contentType string
	var err error
	ctx := r.Context()
	switch {
	case isPygmalion:
		uuid := uuidFromURL(rawURL)
		if uuid == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		contentType = "character"
		result, err = h.downloadPygmalionCharacter(ctx, uuid)
	case isJanny:
		uuid := uuidFromURL(rawURL)
		if uuid == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		contentType = "character"
		result, err = h.downloadJannyCharacter(ctx, uuid)
	case isAICC:
		parsed := parseAICC(rawURL)
		if parsed == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		contentType = "character"
		result, err = h.downloadAICCCharacter(ctx, parsed)
	case isChub:
		id, typ, ok := parseChubURL(rawURL)
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		contentType = typ
		if typ == "character" {
			result, err = h.downloadChubCharacter(ctx, id)
		} else {
			result, err = h.downloadChubLorebook(ctx, id)
		}
	case isRisu:
		uuid := parseRisuURL(rawURL)
		if uuid == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		contentType = "character"
		result, err = h.downloadRisuCharacter(ctx, uuid)
	case isPerchance:
		slug := parsePerchanceSlug(rawURL)
		if slug == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		contentType = "character"
		result, err = h.downloadPerchanceCharacter(ctx, slug)
	case isGeneric:
		contentType = "character"
		result, err = h.downloadGenericPNG(ctx, rawURL)
	default:
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if err != nil || result == nil {
		if err != nil {
			log.Printf("ImportURL fetch error: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
		return
	}
	if result.fileType != "" {
		w.Header().Set("Content-Type", result.fileType)
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+encodeURIFilename(result.fileName)+`"`)
	w.Header().Set("X-Custom-Content-Type", contentType)
	_, _ = w.Write(result.buffer)
}

func (h *ImportHandler) ImportUUID(w http.ResponseWriter, r *http.Request) {
	body := translateBody(r)
	uuid, _ := body["url"].(string)
	if uuid == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	isJanny := strings.Contains(uuid, "_character")
	isPygmalion := !isJanny && len(uuid) == 36
	isAICC := strings.HasPrefix(uuid, "AICC/")
	isPerchance := isPerchanceUUID(uuid)
	uuidType := "character"
	if strings.Contains(uuid, "lorebook") {
		uuidType = "lorebook"
	}
	var result *downloadedContent
	var err error
	ctx := r.Context()
	switch {
	case isPygmalion:
		result, err = h.downloadPygmalionCharacter(ctx, uuid)
	case isJanny:
		parts := strings.Split(uuid, "_")
		result, err = h.downloadJannyCharacter(ctx, parts[0])
	case isAICC:
		parts := strings.Split(uuid, "/")
		if len(parts) < 3 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		result, err = h.downloadAICCCharacter(ctx, parts[1]+"/"+parts[2])
	case isPerchance:
		slug := parsePerchanceSlug(uuid)
		result, err = h.downloadPerchanceCharacter(ctx, slug)
	default:
		if uuidType == "character" {
			result, err = h.downloadChubCharacter(ctx, uuid)
		} else {
			result, err = h.downloadChubLorebook(ctx, uuid)
		}
	}
	if err != nil || result == nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if result.fileType != "" {
		w.Header().Set("Content-Type", result.fileType)
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+encodeURIFilename(result.fileName)+`"`)
	w.Header().Set("X-Custom-Content-Type", uuidType)
	_, _ = w.Write(result.buffer)
}

type importError struct{ msg string }

func (e *importError) Error() string { return e.msg }
func errImport(msg string) error     { return &importError{msg: msg} }

func encodeURIFilename(name string) string {
	return strings.ReplaceAll(url.PathEscape(name), "+", "%20")
}
