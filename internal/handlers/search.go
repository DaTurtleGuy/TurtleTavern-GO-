package handlers

import (
	"encoding/json"
	"io"
	"net"
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

var visitHeaders = map[string]string{
	"Accept":          "text/html",
	"User-Agent":      "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/123.0.0.0 Safari/537.36",
	"Accept-Language": "en-US,en;q=0.5",
	"Accept-Encoding": "gzip, deflate, br",
	"Connection":      "keep-alive",
	"Cache-Control":   "no-cache",
	"Pragma":          "no-cache",
	"TE":              "trailers",
	"DNT":             "1",
	"Sec-Fetch-Dest":  "document",
	"Sec-Fetch-Mode":  "navigate",
	"Sec-Fetch-Site":  "none",
	"Sec-Fetch-User":  "?1",
}

var transcriptRe = regexp.MustCompile(`<text start="([^"]*)" dur="([^"]*)">([^<]*)</text>`)
var htmlEntityRe = regexp.MustCompile(`&(#\d+|#x[0-9a-fA-F]+|[a-zA-Z]+);`)

var htmlEntities = map[string]string{
	"amp": "&", "lt": "<", "gt": ">", "quot": `"`, "apos": "'",
	"nbsp": " ", "copy": "©", "reg": "®", "hellip": "…",
	"ldquo": "\u201c", "rdquo": "\u201d", "lsquo": "\u2018", "rsquo": "\u2019",
	"ndash": "–", "mdash": "—",
}

func decodeHTMLEntities(s string) string {
	return htmlEntityRe.ReplaceAllStringFunc(s, func(m string) string {
		inner := m[1 : len(m)-1]
		if named, ok := htmlEntities[inner]; ok {
			return named
		}
		if strings.HasPrefix(inner, "#x") || strings.HasPrefix(inner, "#X") {
			var n int
			for _, c := range inner[2:] {
				var d int
				switch {
				case c >= '0' && c <= '9':
					d = int(c - '0')
				case c >= 'a' && c <= 'f':
					d = int(c-'a') + 10
				case c >= 'A' && c <= 'F':
					d = int(c-'A') + 10
				default:
					return m
				}
				n = n*16 + d
			}
			return string(rune(n))
		}
		if strings.HasPrefix(inner, "#") {
			n := 0
			for _, c := range inner[1:] {
				if c < '0' || c > '9' {
					return m
				}
				n = n*10 + int(c-'0')
			}
			return string(rune(n))
		}
		return m
	})
}

type SearchHandler struct {
	Cfg    *config.Config
	client *http.Client
}

func NewSearchHandler(cfg *config.Config) *SearchHandler {
	return &SearchHandler{Cfg: cfg, client: llm.NewHTTPClient(cfg)}
}

func (h *SearchHandler) RegisterRoutes(r chi.Router) {
	r.Route("/api/search", func(r chi.Router) {
		r.Post("/serpapi", h.SerpAPI)
		r.Post("/transcript", h.Transcript)
		r.Post("/searxng", h.SearXNG)
		r.Post("/tavily", h.Tavily)
		r.Post("/koboldcpp", h.KoboldCpp)
		r.Post("/serper", h.Serper)
		r.Post("/zai", h.ZAI)
		r.Post("/visit", h.Visit)
	})
}

func (h *SearchHandler) secret(r *http.Request, key string) string {
	uc := auth.UserFromRequest(r)
	if uc == nil {
		return ""
	}
	return secrets.ReadActiveSecret(uc.Directories.Root, key)
}

func (h *SearchHandler) SerpAPI(w http.ResponseWriter, r *http.Request) {
	body := translateBody(r)
	key := h.secret(r, "api_key_serpapi")
	if key == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	query, _ := body["query"].(string)
	endpoint := "https://serpapi.com/search.json?q=" + url.QueryEscape(query) + "&api_key=" + url.QueryEscape(key)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, endpoint, nil)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	resp, err := h.client.Do(req)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write(data)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

func (h *SearchHandler) extractTranscript(pageBody, lang string) (string, error) {
	parts := strings.Split(pageBody, `"captions":`)
	if len(parts) <= 1 {
		if strings.Contains(pageBody, `class="g-recaptcha"`) {
			return "", errTooManyRequests
		}
		if !strings.Contains(pageBody, `"playabilityStatus":`) {
			return "", errVideoUnavailable
		}
		return "", errNoTranscript
	}
	rest := parts[1]
	if idx := strings.Index(rest, `,"videoDetails`); idx >= 0 {
		rest = rest[:idx]
	}
	rest = strings.ReplaceAll(rest, "\n", "")
	var captions struct {
		PlayerCaptionsTracklistRenderer *struct {
			CaptionTracks []struct {
				BaseURL      string `json:"baseUrl"`
				LanguageCode string `json:"languageCode"`
			} `json:"captionTracks"`
		} `json:"playerCaptionsTracklistRenderer"`
	}
	if err := json.Unmarshal([]byte(rest), &captions); err != nil ||
		captions.PlayerCaptionsTracklistRenderer == nil {
		return "", errTranscriptDisabled
	}
	tracks := captions.PlayerCaptionsTracklistRenderer.CaptionTracks
	if len(tracks) == 0 {
		return "", errNoTranscript
	}
	trackURL := tracks[0].BaseURL
	trackLang := tracks[0].LanguageCode
	if lang != "" {
		found := false
		for _, t := range tracks {
			if t.LanguageCode == lang {
				trackURL = t.BaseURL
				trackLang = t.LanguageCode
				found = true
				break
			}
		}
		if !found {
			return "", errLangUnavailable
		}
	}
	_ = trackLang
	req, err := http.NewRequest(http.MethodGet, trackURL, nil)
	if err != nil {
		return "", err
	}
	if lang != "" {
		req.Header.Set("Accept-Language", lang)
	}
	req.Header.Set("User-Agent", visitHeaders["User-Agent"])
	resp, err := h.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", errTranscriptFetch
	}
	xmlBody, _ := io.ReadAll(resp.Body)
	matches := transcriptRe.FindAllStringSubmatch(string(xmlBody), -1)
	var parts2 []string
	for _, m := range matches {
		parts2 = append(parts2, decodeHTMLEntities(decodeHTMLEntities(m[3])))
	}
	return strings.Join(parts2, " "), nil
}

type transcriptError string

func (e transcriptError) Error() string { return string(e) }

const (
	errTooManyRequests    = transcriptError("Too many requests")
	errVideoUnavailable   = transcriptError("Video is not available")
	errNoTranscript       = transcriptError("Transcript not available")
	errTranscriptDisabled = transcriptError("Transcript disabled")
	errLangUnavailable    = transcriptError("Transcript not available in this language")
	errTranscriptFetch    = transcriptError("Transcript request failed")
)

func (h *SearchHandler) Transcript(w http.ResponseWriter, r *http.Request) {
	body := translateBody(r)
	id, _ := body["id"].(string)
	lang, _ := body["lang"].(string)
	asJSON, _ := body["json"].(bool)
	if id == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	pageReq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, "https://www.youtube.com/watch?v="+id, nil)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if lang != "" {
		pageReq.Header.Set("Accept-Language", lang)
	}
	pageReq.Header.Set("User-Agent", visitHeaders["User-Agent"])
	pageResp, err := h.client.Do(pageReq)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	pageData, _ := io.ReadAll(pageResp.Body)
	pageResp.Body.Close()
	text, err := h.extractTranscript(string(pageData), lang)
	if err != nil {
		if asJSON {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"html": string(pageData), "transcript": ""})
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if asJSON {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"transcript": text, "html": string(pageData)})
		return
	}
	_, _ = w.Write([]byte(text))
}

func (h *SearchHandler) SearXNG(w http.ResponseWriter, r *http.Request) {
	body := translateBody(r)
	baseURL, _ := body["baseUrl"].(string)
	query, _ := body["query"].(string)
	if baseURL == "" || query == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	mainReq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, baseURL, nil)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	for k, v := range visitHeaders {
		mainReq.Header.Set(k, v)
	}
	mainResp, err := h.client.Do(mainReq)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	mainText, _ := io.ReadAll(mainResp.Body)
	mainResp.Body.Close()
	if mainResp.StatusCode < 200 || mainResp.StatusCode >= 300 {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	cssRe := regexp.MustCompile(`href="(/client.+\.css)"`)
	if m := cssRe.FindStringSubmatch(string(mainText)); len(m) == 2 {
		cssReq, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, strings.TrimSuffix(baseURL, "/")+m[1], nil)
		if cssReq != nil {
			for k, v := range visitHeaders {
				cssReq.Header.Set(k, v)
			}
			if cssResp, err := h.client.Do(cssReq); err == nil {
				io.Copy(io.Discard, cssResp.Body)
				cssResp.Body.Close()
			}
		}
	}
	q := url.Values{}
	q.Set("q", query)
	if prefs, ok := body["preferences"].(string); ok && prefs != "" {
		q.Set("preferences", prefs)
	}
	if cats, ok := body["categories"].(string); ok && cats != "" {
		q.Set("categories", cats)
	}
	searchURL := strings.TrimSuffix(baseURL, "/") + "/search?" + q.Encode()
	searchReq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, searchURL, nil)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	for k, v := range visitHeaders {
		searchReq.Header.Set(k, v)
	}
	searchResp, err := h.client.Do(searchReq)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	defer searchResp.Body.Close()
	data, _ := io.ReadAll(searchResp.Body)
	if searchResp.StatusCode < 200 || searchResp.StatusCode >= 300 {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write(data)
		return
	}
	_, _ = w.Write(data)
}

func (h *SearchHandler) Tavily(w http.ResponseWriter, r *http.Request) {
	body := translateBody(r)
	key := h.secret(r, "api_key_tavily")
	if key == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	query, _ := body["query"].(string)
	includeImages, _ := body["include_images"].(bool)
	res, err := llm.DoJSON(h.client, r.Context(), http.MethodPost, "https://api.tavily.com/search",
		map[string]string{"Content-Type": "application/json"},
		map[string]any{
			"query": query, "api_key": key, "search_depth": "basic", "topic": "general",
			"include_answer": true, "include_raw_content": false,
			"include_images": includeImages, "include_image_descriptions": false,
			"include_domains": []any{}, "max_results": 10,
		})
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if res.Status < 200 || res.Status >= 300 {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write(res.Body)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(res.Body)
}

func (h *SearchHandler) KoboldCpp(w http.ResponseWriter, r *http.Request) {
	body := translateBody(r)
	apiURL, _ := body["url"].(string)
	if apiURL == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	query, _ := body["query"].(string)
	baseURL := llm.TrimV1(apiURL)
	headers := map[string]string{"Content-Type": "application/json"}
	for k, v := range llm.AdditionalHeadersByType(llm.TextGenKoboldCpp, baseURL, h.userRoot(r), h.Cfg) {
		headers[k] = v
	}
	res, err := llm.DoJSON(h.client, r.Context(), http.MethodPost, baseURL+"/api/extra/websearch",
		headers, map[string]any{"q": query})
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if res.Status < 200 || res.Status >= 300 {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write(res.Body)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(res.Body)
}

func (h *SearchHandler) userRoot(r *http.Request) string {
	uc := auth.UserFromRequest(r)
	if uc == nil {
		return ""
	}
	return uc.Directories.Root
}

func (h *SearchHandler) Serper(w http.ResponseWriter, r *http.Request) {
	body := translateBody(r)
	key := h.secret(r, "api_key_serper")
	if key == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	query, _ := body["query"].(string)
	endpoint := "https://google.serper.dev/search"
	if images, _ := body["images"].(bool); images {
		endpoint = "https://google.serper.dev/images"
	}
	res, err := llm.DoJSON(h.client, r.Context(), http.MethodPost, endpoint,
		map[string]string{"X-API-KEY": key, "Content-Type": "application/json"},
		map[string]any{"q": query})
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if res.Status < 200 || res.Status >= 300 {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write(res.Body)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(res.Body)
}

func (h *SearchHandler) ZAI(w http.ResponseWriter, r *http.Request) {
	body := translateBody(r)
	key := h.secret(r, "api_key_zai")
	if key == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	query, _ := body["query"].(string)
	if query == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	res, err := llm.DoJSON(h.client, r.Context(), http.MethodPost, "https://api.z.ai/api/paas/v4/web_search",
		map[string]string{"Content-Type": "application/json", "Authorization": "Bearer " + key},
		map[string]any{"search_engine": "search-prime", "search_query": query})
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if res.Status < 200 || res.Status >= 300 {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write(res.Body)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(res.Body)
}

func isIPAddress(host string) bool {
	if ip := net.ParseIP(host); ip != nil {
		return true
	}
	return false
}

func (h *SearchHandler) Visit(w http.ResponseWriter, r *http.Request) {
	body := translateBody(r)
	rawURL, _ := body["url"].(string)
	wantHTML := true
	if v, ok := body["html"]; ok {
		if b, ok := v.(bool); ok {
			wantHTML = b
		}
	}
	if rawURL == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" ||
		(u.Scheme != "http" && u.Scheme != "https") ||
		u.Port() != "" || isIPAddress(u.Hostname()) {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, rawURL, nil)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	for k, v := range visitHeaders {
		req.Header.Set(k, v)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	contentType := resp.Header.Get("Content-Type")
	if wantHTML {
		if !strings.Contains(contentType, "text/html") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		data, _ := io.ReadAll(resp.Body)
		_, _ = w.Write(data)
		return
	}
	w.Header().Set("Content-Type", contentType)
	_, _ = io.Copy(w, resp.Body)
}
