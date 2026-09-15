package llm

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/TurtleTavern/turtletavern/internal/config"
	"github.com/go-chi/chi/v5"
)

type KoboldHandler struct {
	Cfg    *config.Config
	client *http.Client
}

func NewKoboldHandler(cfg *config.Config) *KoboldHandler {
	return &KoboldHandler{Cfg: cfg, client: NewHTTPClient(cfg)}
}

func (h *KoboldHandler) RegisterRoutes(r chi.Router) {
	r.Route("/api/backends/kobold", func(r chi.Router) {
		r.Post("/generate", h.Generate)
		r.Post("/status", h.Status)
		r.Post("/transcribe-audio", h.TranscribeAudio)
		r.Post("/embed", h.Embed)
	})
}

func localhostToLoopback(s string) string {
	return strings.ReplaceAll(s, "localhost", "127.0.0.1")
}

func (h *KoboldHandler) Generate(w http.ResponseWriter, r *http.Request) {
	body := decodeBody(r)
	if len(body) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if srv, ok := body["api_server"].(string); ok {
		body["api_server"] = localhostToLoopback(srv)
	}
	apiServer := bodyStr(body, "api_server")
	settings := map[string]any{
		"prompt":             body["prompt"],
		"use_story":          false,
		"use_memory":         false,
		"use_authors_note":   false,
		"use_world_info":     false,
		"max_context_length": body["max_context_length"],
		"max_length":         body["max_length"],
	}
	if _, hasGUI := body["gui_settings"]; !hasGUI {
		settings = map[string]any{
			"prompt":                  body["prompt"],
			"use_story":               false,
			"use_memory":              false,
			"use_authors_note":        false,
			"use_world_info":          false,
			"max_context_length":      body["max_context_length"],
			"max_length":              body["max_length"],
			"rep_pen":                 body["rep_pen"],
			"rep_pen_range":           body["rep_pen_range"],
			"rep_pen_slope":           body["rep_pen_slope"],
			"temperature":             body["temperature"],
			"tfs":                     body["tfs"],
			"top_a":                   body["top_a"],
			"top_k":                   body["top_k"],
			"top_p":                   body["top_p"],
			"min_p":                   body["min_p"],
			"typical":                 body["typical"],
			"sampler_order":           body["sampler_order"],
			"singleline":              body["singleline"] != nil && isTruthy(body["singleline"]),
			"use_default_badwordsids": body["use_default_badwordsids"],
			"mirostat":                body["mirostat"],
			"mirostat_eta":            body["mirostat_eta"],
			"mirostat_tau":            body["mirostat_tau"],
			"grammar":                 body["grammar"],
			"sampler_seed":            body["sampler_seed"],
		}
		if _, ok := body["stop_sequence"]; ok {
			settings["stop_sequence"] = body["stop_sequence"]
		}
	}
	compactNils(settings)
	outHeaders := map[string]string{"Content-Type": "application/json"}
	if u, err := url.Parse(apiServer); err == nil {
		for k, v := range OverrideHeaders(h.Cfg, u.Host) {
			outHeaders[k] = v
		}
	}
	streaming := bodyBool(body, "streaming")
	endpoint := strings.TrimSuffix(apiServer, "/") + "/v1/generate"
	if streaming {
		endpoint = strings.TrimSuffix(apiServer, "/") + "/extra/generate/stream"
	}
	canAbort := bodyBool(body, "can_abort")
	if streaming {
		upstream, err := DoStream(h.client, r.Context(), http.MethodPost, endpoint, outHeaders, settings)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"error":true}`))
			return
		}
		ForwardStream(upstream, w, r)
		return
	}
	var lastErr error
	for i := 0; i < 50; i++ {
		res, err := DoJSON(h.client, r.Context(), http.MethodPost, endpoint, outHeaders, settings)
		if err != nil {
			lastErr = err
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"error":true}`))
			return
		}
		if res.Status == http.StatusForbidden || res.Status == http.StatusServiceUnavailable {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(2500 * time.Millisecond):
				continue
			}
		}
		if res.Status < 200 || res.Status >= 300 {
			msg := string(res.Body)
			var parsed map[string]any
			if err := json.Unmarshal(res.Body, &parsed); err == nil {
				if detail, ok := parsed["detail"].(map[string]any); ok {
					if m, ok := detail["msg"].(string); ok {
						msg = m
					}
				}
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": msg}})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(res.Body)
		return
	}
	_ = lastErr
	_ = canAbort
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"error":true}`))
}

func (h *KoboldHandler) Status(w http.ResponseWriter, r *http.Request) {
	body := decodeBody(r)
	if len(body) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	apiServer := localhostToLoopback(bodyStr(body, "api_server"))
	headers := map[string]string{"Content-Type": "application/json"}
	for k, v := range AdditionalHeadersByType("", apiServer, userRootOf(r), h.Cfg) {
		headers[k] = v
	}
	type fetchResult struct {
		data map[string]any
		ok   bool
	}
	get := func(path string) fetchResult {
		res, err := DoJSON(h.client, r.Context(), http.MethodGet, strings.TrimSuffix(apiServer, "/")+path, headers, nil)
		if err != nil || res.Status < 200 || res.Status >= 300 {
			return fetchResult{}
		}
		var data map[string]any
		if err := json.Unmarshal(res.Body, &data); err != nil {
			return fetchResult{}
		}
		return fetchResult{data: data, ok: true}
	}
	united := get("/v1/info/version")
	if !united.ok {
		united.data = map[string]any{"result": "0.0.0"}
	}
	extra := get("/extra/version")
	if !extra.ok {
		extra.data = map[string]any{"version": "0.0"}
	}
	model := get("/v1/model")
	modelName := "no_connection"
	if model.ok {
		if res, _ := model.data["result"].(string); res != "" && res != "ReadOnly" {
			modelName = res
		}
	}
	unitedVersion, _ := united.data["result"].(string)
	extraVersion := ""
	if v, ok := extra.data["result"].(string); ok {
		extraVersion = v
	} else if v, ok := extra.data["version"].(string); ok {
		extraVersion = v
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"koboldUnitedVersion": unitedVersion,
		"koboldCppVersion":    extraVersion,
		"model":               modelName,
	})
}

func (h *KoboldHandler) TranscribeAudio(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(500 << 20); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	defer r.MultipartForm.RemoveAll()
	server := r.FormValue("server")
	if server == "" {
		var body map[string]any
		if data, err := io.ReadAll(r.Body); err == nil && len(data) > 0 {
			_ = json.Unmarshal(data, &body)
			server, _ = body["server"].(string)
		}
	}
	if server == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var audioB64 string
	if r.MultipartForm != nil {
		for _, files := range r.MultipartForm.File {
			if len(files) == 0 {
				continue
			}
			f, err := files[0].Open()
			if err != nil {
				continue
			}
			data, err := io.ReadAll(f)
			f.Close()
			if err != nil {
				continue
			}
			audioB64 = base64.StdEncoding.EncodeToString(data)
			break
		}
	}
	if audioB64 == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	headers := AdditionalHeadersByType(TextGenKoboldCpp, server, userRootOf(r), h.Cfg)
	u, err := url.Parse(server)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	u.Path = "/api/extra/transcribe"
	res, err := DoJSON(h.client, r.Context(), http.MethodPost, u.String(), headers,
		map[string]any{"prompt": "", "audio_data": audioB64})
	if err != nil || res.Status < 200 || res.Status >= 300 {
		w.WriteHeader(http.StatusInternalServerError)
		if err == nil {
			_, _ = w.Write(res.Body)
		} else {
			_, _ = w.Write([]byte("Internal server error"))
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(res.Body)
}

func (h *KoboldHandler) Embed(w http.ResponseWriter, r *http.Request) {
	body := decodeBody(r)
	server := bodyStr(body, "server")
	if server == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	headers := AdditionalHeadersByType(TextGenKoboldCpp, server, userRootOf(r), h.Cfg)
	u, err := url.Parse(server)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	u.Path = "/api/extra/embeddings"
	res, err := DoJSON(h.client, r.Context(), http.MethodPost, u.String(), headers,
		map[string]any{"input": body["items"]})
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("Internal server error"))
		return
	}
	var data map[string]any
	if err := json.Unmarshal(res.Body, &data); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("Internal server error"))
		return
	}
	arr, ok := data["data"].([]any)
	if !ok {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	type indexed struct {
		index     int
		embedding any
	}
	var items []indexed
	for _, item := range arr {
		var emb any
		idx := 0
		switch t := item.(type) {
		case []any:
			if len(t) > 0 {
				if im, ok := t[0].(map[string]any); ok {
					emb = im["embedding"]
					if n, ok := im["index"].(float64); ok {
						idx = int(n)
					}
				} else {
					emb = t[0]
				}
			}
		case map[string]any:
			emb = t["embedding"]
			if n, ok := t["index"].(float64); ok {
				idx = int(n)
			}
		default:
			emb = item
		}
		items = append(items, indexed{index: idx, embedding: emb})
	}
	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			if items[j].index < items[i].index {
				items[i], items[j] = items[j], items[i]
			}
		}
	}
	embeddings := make([]any, 0, len(items))
	for _, it := range items {
		embeddings = append(embeddings, it.embedding)
	}
	model, _ := data["model"].(string)
	if model == "" {
		model = "unknown"
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"model": model, "embeddings": embeddings})
}
