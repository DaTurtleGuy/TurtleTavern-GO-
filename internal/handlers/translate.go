package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/TurtleTavern/turtletavern/internal/auth"
	"github.com/TurtleTavern/turtletavern/internal/config"
	"github.com/TurtleTavern/turtletavern/internal/llm"
	"github.com/TurtleTavern/turtletavern/internal/secrets"
	"github.com/go-chi/chi/v5"
)

const (
	lingvaDefault  = "https://lingva.ml/api/v1"
	deeplxDefault  = "http://127.0.0.1:1188/translate"
	oneringDefault = "http://127.0.0.1:4990/translate"
)

type TranslateHandler struct {
	Cfg    *config.Config
	client *http.Client
}

func NewTranslateHandler(cfg *config.Config) *TranslateHandler {
	return &TranslateHandler{Cfg: cfg, client: llm.NewHTTPClient(cfg)}
}

func (h *TranslateHandler) RegisterRoutes(r chi.Router) {
	r.Route("/api/translate", func(r chi.Router) {
		r.Post("/libre", h.Libre)
		r.Post("/google", h.Google)
		r.Post("/yandex", h.Yandex)
		r.Post("/lingva", h.Lingva)
		r.Post("/deepl", h.DeepL)
		r.Post("/onering", h.OneRing)
		r.Post("/deeplx", h.DeepLX)
		r.Post("/bing", h.Bing)
	})
}

func (h *TranslateHandler) secret(r *http.Request, key string) string {
	uc := auth.UserFromRequest(r)
	if uc == nil {
		return ""
	}
	return secrets.ReadActiveSecret(uc.Directories.Root, key)
}

func uuidHex32() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func translateBody(r *http.Request) map[string]any {	body := map[string]any{}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	if body == nil {
		body = map[string]any{}
	}
	return body
}

func (h *TranslateHandler) Libre(w http.ResponseWriter, r *http.Request) {
	body := translateBody(r)
	key := h.secret(r, "libre")
	apiURL := h.secret(r, "libre_url")
	if apiURL == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	lang, _ := body["lang"].(string)
	switch lang {
	case "zh-CN":
		lang = "zh"
	case "zh-TW":
		lang = "zt"
	case "pt-BR", "pt-PT":
		lang = "pt"
	}
	text, _ := body["text"].(string)
	if text == "" || lang == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	res, err := llm.DoJSON(h.client, r.Context(), http.MethodPost, apiURL,
		map[string]string{"Content-Type": "application/json"},
		map[string]any{"q": text, "source": "auto", "target": lang, "format": "text", "api_key": key})
	if err != nil || res.Status < 200 || res.Status >= 300 {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	var parsed map[string]any
	if err := json.Unmarshal(res.Body, &parsed); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	translated, _ := parsed["translatedText"].(string)
	_, _ = w.Write([]byte(translated))
}

func (h *TranslateHandler) Google(w http.ResponseWriter, r *http.Request) {
	body := translateBody(r)
	lang, _ := body["lang"].(string)
	if lang == "pt-BR" {
		lang = "pt"
	}
	text, _ := body["text"].(string)
	if text == "" || lang == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	endpoint := "https://translate.google.com/translate_a/single?client=gtx&sl=auto&tl=" +
		url.QueryEscape(lang) + "&dt=t&q=" + url.QueryEscape(text)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, endpoint, nil)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := h.client.Do(req)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var parsed []any
	if err := json.Unmarshal(data, &parsed); err != nil || len(parsed) == 0 {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	var sb strings.Builder
	if sentences, ok := parsed[0].([]any); ok {
		for _, s := range sentences {
			if parts, ok := s.([]any); ok && len(parts) > 0 {
				if t, ok := parts[0].(string); ok {
					sb.WriteString(t)
				}
			}
		}
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(sb.String()))
}

func (h *TranslateHandler) Yandex(w http.ResponseWriter, r *http.Request) {
	body := translateBody(r)
	lang, _ := body["lang"].(string)
	if lang == "pt-PT" {
		lang = "pt"
	}
	if lang == "zh-CN" || lang == "zh-TW" {
		lang = "zh"
	}
	chunks, _ := body["chunks"].([]any)
	if len(chunks) == 0 || lang == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	form := url.Values{}
	for _, c := range chunks {
		if s, ok := c.(string); ok {
			form.Add("text", s)
		}
	}
	form.Set("lang", lang)
	ucid := uuidHex32()
	req, reqErr := http.NewRequestWithContext(r.Context(), http.MethodPost,
		"https://translate.yandex.net/api/v1/tr.json/translate?ucid="+ucid+"&srv=android&format=text",
		strings.NewReader(form.Encode()))
	if reqErr != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := h.client.Do(req)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	texts, _ := parsed["text"].([]any)
	var parts []string
	for _, t := range texts {
		if s, ok := t.(string); ok {
			parts = append(parts, s)
		}
	}
	_, _ = w.Write([]byte(strings.Join(parts, ",")))
}

func (h *TranslateHandler) Lingva(w http.ResponseWriter, r *http.Request) {
	body := translateBody(r)
	baseURL := h.secret(r, "lingva_url")
	if baseURL == "" {
		baseURL = lingvaDefault
	}
	lang, _ := body["lang"].(string)
	if lang == "zh-CN" || lang == "zh-TW" {
		lang = "zh"
	}
	if lang == "pt-BR" || lang == "pt-PT" {
		lang = "pt"
	}
	text, _ := body["text"].(string)
	if text == "" || lang == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	endpoint := strings.TrimSuffix(baseURL, "/") + "/auto/" + lang + "/" + url.PathEscape(text)
	res, err := llm.DoJSON(h.client, r.Context(), http.MethodGet, endpoint, nil, nil)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	var parsed map[string]any
	if err := json.Unmarshal(res.Body, &parsed); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	translation, _ := parsed["translation"].(string)
	_, _ = w.Write([]byte(translation))
}

func (h *TranslateHandler) DeepL(w http.ResponseWriter, r *http.Request) {
	body := translateBody(r)
	key := h.secret(r, "deepl")
	if key == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	lang, _ := body["lang"].(string)
	if lang == "zh-CN" || lang == "zh-TW" {
		lang = "ZH"
	}
	text, _ := body["text"].(string)
	if text == "" || lang == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	form := url.Values{}
	form.Set("text", text)
	form.Set("target_lang", lang)
	for _, l := range []string{"de", "fr", "it", "es", "nl", "ja", "ru", "pt-BR", "pt-PT"} {
		if lang == l {
			form.Set("formality", h.Cfg.DeepL.Formality)
			break
		}
	}
	endpoint := "https://api-free.deepl.com/v2/translate"
	if ep, ok := body["endpoint"].(string); ok && ep == "pro" {
		endpoint = "https://api.deepl.com/v2/translate"
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "DeepL-Auth-Key "+key)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := h.client.Do(req)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	translations, _ := parsed["translations"].([]any)
	if len(translations) == 0 {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	first, _ := translations[0].(map[string]any)
	t, _ := first["text"].(string)
	_, _ = w.Write([]byte(t))
}

func (h *TranslateHandler) OneRing(w http.ResponseWriter, r *http.Request) {
	body := translateBody(r)
	apiURL := h.secret(r, "oneringtranslator_url")
	if apiURL == "" {
		apiURL = oneringDefault
	}
	lang, _ := body["lang"].(string)
	if lang == "pt-BR" || lang == "pt-PT" {
		lang = "pt"
	}
	text, _ := body["text"].(string)
	fromLang, _ := body["from_lang"].(string)
	toLang, _ := body["to_lang"].(string)
	if text == "" || fromLang == "" || toLang == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	q := url.Values{}
	q.Set("text", text)
	q.Set("from_lang", fromLang)
	q.Set("to_lang", toLang)
	endpoint := apiURL + "?" + q.Encode()
	if strings.Contains(apiURL, "?") {
		endpoint = apiURL + "&" + q.Encode()
	}
	res, err := llm.DoJSON(h.client, r.Context(), http.MethodGet, endpoint, nil, nil)
	if err != nil || res.Status < 200 || res.Status >= 300 {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	var parsed map[string]any
	if err := json.Unmarshal(res.Body, &parsed); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	result, _ := parsed["result"].(string)
	_, _ = w.Write([]byte(result))
}

func (h *TranslateHandler) DeepLX(w http.ResponseWriter, r *http.Request) {
	body := translateBody(r)
	apiURL := h.secret(r, "deeplx_url")
	if apiURL == "" {
		apiURL = deeplxDefault
	}
	text, _ := body["text"].(string)
	lang, _ := body["lang"].(string)
	if lang == "zh-CN" || lang == "zh-TW" {
		lang = "ZH"
	}
	if text == "" || lang == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	res, err := llm.DoJSON(h.client, r.Context(), http.MethodPost, apiURL,
		map[string]string{"Accept": "application/json", "Content-Type": "application/json"},
		map[string]any{"text": text, "source_lang": "auto", "target_lang": lang})
	if err != nil || res.Status < 200 || res.Status >= 300 {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	var parsed map[string]any
	if err := json.Unmarshal(res.Body, &parsed); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	data, _ := parsed["data"].(string)
	_, _ = w.Write([]byte(data))
}

var bingIGRe = regexp.MustCompile(`"ig":"([0-9a-f]+)"`)
var bingIIDRe = regexp.MustCompile(`data-iid="([^"]+)"`)

func (h *TranslateHandler) Bing(w http.ResponseWriter, r *http.Request) {
	body := translateBody(r)
	text, _ := body["text"].(string)
	lang, _ := body["lang"].(string)
	switch lang {
	case "zh-CN":
		lang = "zh-Hans"
	case "zh-TW":
		lang = "zh-Hant"
	case "pt-BR":
		lang = "pt"
	}
	if text == "" || lang == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	pageReq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, "https://www.bing.com/translator", nil)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	pageReq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/123.0.0.0 Safari/537.36")
	pageResp, err := h.client.Do(pageReq)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	pageData, _ := io.ReadAll(pageResp.Body)
	pageResp.Body.Close()
	cookies := pageResp.Cookies()
	ig := ""
	if m := bingIGRe.FindStringSubmatch(string(pageData)); len(m) == 2 {
		ig = m[1]
	}
	iid := ""
	if m := bingIIDRe.FindStringSubmatch(string(pageData)); len(m) == 2 {
		iid = m[1]
	}
	if ig == "" {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	form := url.Values{}
	form.Set("fromLang", "auto-detect")
	form.Set("to", lang)
	form.Set("text", text)
	endpoint := "https://www.bing.com/ttranslatev3?isVertical=1&IG=" + url.QueryEscape(ig)
	if iid != "" {
		endpoint += "&IID=" + url.QueryEscape(iid)
	}
	treq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	treq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	treq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/123.0.0.0 Safari/537.36")
	for _, c := range cookies {
		treq.AddCookie(c)
	}
	tresp, err := h.client.Do(treq)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	defer tresp.Body.Close()
	tdata, _ := io.ReadAll(tresp.Body)
	if tresp.StatusCode < 200 || tresp.StatusCode >= 300 {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	var parsed []any
	if err := json.Unmarshal(tdata, &parsed); err != nil || len(parsed) == 0 {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	first, _ := parsed[0].(map[string]any)
	translations, _ := first["translations"].([]any)
	if len(translations) == 0 {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	t0, _ := translations[0].(map[string]any)
	t, _ := t0["text"].(string)
	_, _ = w.Write([]byte(t))
}
