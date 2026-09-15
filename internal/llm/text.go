package llm

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/TurtleTavern/turtletavern/internal/auth"
	"github.com/TurtleTavern/turtletavern/internal/config"
	"github.com/go-chi/chi/v5"
)

var togetherAIKeys = []string{
	"model", "prompt", "max_tokens", "temperature", "top_p", "top_k",
	"repetition_penalty", "min_p", "presence_penalty", "frequency_penalty", "stream", "stop",
}

var ollamaKeys = []string{
	"num_predict", "num_ctx", "num_batch", "stop", "temperature", "repeat_penalty",
	"presence_penalty", "frequency_penalty", "top_k", "top_p", "tfs_z", "typical_p",
	"seed", "repeat_last_n", "min_p",
}

var infermaticAIKeys = []string{
	"model", "prompt", "max_tokens", "temperature", "top_p", "top_k",
	"repetition_penalty", "stream", "stop", "presence_penalty", "frequency_penalty",
	"min_p", "seed", "ignore_eos", "n", "best_of", "min_tokens",
	"spaces_between_special_tokens", "skip_special_tokens", "logprobs",
}

var openaiKeys = []string{
	"model", "prompt", "stream", "temperature", "top_p", "frequency_penalty",
	"presence_penalty", "stop", "seed", "logit_bias", "logprobs", "max_tokens", "n", "best_of",
}

var vllmKeys = []string{
	"model", "prompt", "best_of", "echo", "frequency_penalty", "logit_bias", "logprobs",
	"max_tokens", "n", "presence_penalty", "seed", "stop", "stream", "suffix",
	"temperature", "top_p", "user", "use_beam_search", "top_k", "min_p",
	"repetition_penalty", "length_penalty", "early_stopping", "stop_token_ids",
	"ignore_eos", "min_tokens", "skip_special_tokens", "spaces_between_special_tokens",
	"truncate_prompt_tokens", "include_stop_str_in_output", "response_format",
	"guided_json", "guided_regex", "guided_choice", "guided_grammar",
	"guided_decoding_backend", "guided_whitespace_pattern",
}

var openrouterTextKeys = []string{
	"max_tokens", "temperature", "top_k", "top_p", "presence_penalty",
	"frequency_penalty", "repetition_penalty", "min_p", "top_a", "seed",
	"logit_bias", "model", "stream", "prompt", "stop", "provider", "include_reasoning",
}

var featherlessKeys = append([]string{}, vllmKeys...)

type TextHandler struct {
	Cfg    *config.Config
	client *http.Client
}

func NewTextHandler(cfg *config.Config) *TextHandler {
	return &TextHandler{Cfg: cfg, client: NewHTTPClient(cfg)}
}

func (h *TextHandler) RegisterRoutes(r chi.Router) {
	r.Route("/api/backends/text-completions", func(r chi.Router) {
		r.Post("/status", h.Status)
		r.Post("/props", h.Props)
		r.Post("/generate", h.Generate)
		r.Route("/ollama", func(r chi.Router) {
			r.Post("/download", h.OllamaDownload)
			r.Post("/caption-image", h.OllamaCaptionImage)
		})
		r.Route("/llamacpp", func(r chi.Router) {
			r.Post("/props", h.LlamaCppProps)
			r.Post("/slots", h.LlamaCppSlots)
		})
		r.Route("/tabby", func(r chi.Router) {
			r.Post("/download", h.TabbyDownload)
		})
	})
}

func pickKeys(body map[string]any, keys []string) map[string]any {
	allowed := make(map[string]bool, len(keys))
	for _, k := range keys {
		allowed[k] = true
	}
	out := make(map[string]any, len(body))
	for k, v := range body {
		if allowed[k] {
			out[k] = v
		}
	}
	return out
}

func (h *TextHandler) Status(w http.ResponseWriter, r *http.Request) {
	body := decodeBody(r)
	if len(body) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	apiServer := strings.ReplaceAll(bodyStr(body, "api_server"), "localhost", "127.0.0.1")
	baseURL := TrimV1(apiServer)
	headers := map[string]string{"Content-Type": "application/json"}
	for k, v := range AdditionalHeadersByType(bodyStr(body, "api_type"), baseURL, userRootOf(r), h.Cfg) {
		headers[k] = v
	}
	apiType := bodyStr(body, "api_type")
	modelsPath := "/v1/models"
	switch apiType {
	case TextGenDreamGen:
		modelsPath = "/api/openai/v1/models"
	case TextGenMancer:
		modelsPath = "/oai/v1/models"
	case TextGenTabby:
		modelsPath = "/v1/model/list"
	case TextGenTogetherAI:
		modelsPath = "/api/models?&info"
	case TextGenOllama:
		modelsPath = "/api/tags"
	case TextGenHuggingFace:
		modelsPath = "/info"
	}
	res, err := DoJSON(h.client, r.Context(), http.MethodGet, baseURL+modelsPath, headers, nil)
	if err != nil || res.Status < 200 || res.Status >= 300 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var data any
	if err := json.Unmarshal(res.Body, &data); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if apiType == TextGenTogetherAI {
		if arr, ok := data.([]any); ok {
			mapped := make([]any, 0, len(arr))
			for _, item := range arr {
				if mm, ok := item.(map[string]any); ok {
					if name, ok := mm["name"]; ok {
						mm["id"] = name
					}
					mapped = append(mapped, mm)
				}
			}
			data = map[string]any{"data": mapped}
		}
	}
	if apiType == TextGenOllama {
		if m, ok := data.(map[string]any); ok {
			if arr, ok := m["models"].([]any); ok {
				mapped := make([]any, 0, len(arr))
				for _, item := range arr {
					if mm, ok := item.(map[string]any); ok {
						if name, ok := mm["name"]; ok {
							mm["id"] = name
						}
						mapped = append(mapped, mm)
					}
				}
				m["data"] = mapped
			}
		}
	}
	if apiType == TextGenHuggingFace {
		data = map[string]any{"data": []any{}}
	}
	m, ok := data.(map[string]any)
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	arr, ok := m["data"].([]any)
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	result := "Valid"
	if len(arr) > 0 {
		if first, ok := arr[0].(map[string]any); ok {
			if id, ok := first["id"].(string); ok {
				result = id
			}
		}
	}
	respHeaders := map[string]string{}
	if apiType == TextGenOoba && res.Header.Get("x-powered-by") != "Express" {
		infoRes, err := DoJSON(h.client, r.Context(), http.MethodGet, baseURL+"/v1/internal/model/info", headers, nil)
		if err == nil && infoRes.Status >= 200 && infoRes.Status < 300 {
			var info map[string]any
			if json.Unmarshal(infoRes.Body, &info) == nil {
				if name, ok := info["model_name"].(string); ok && name != "" {
					result = name
				}
				respHeaders["x-supports-tokenization"] = "true"
			}
		}
	}
	if apiType == TextGenTabby {
		infoRes, err := DoJSON(h.client, r.Context(), http.MethodGet, baseURL+"/v1/model", headers, nil)
		if err == nil && infoRes.Status >= 200 && infoRes.Status < 300 {
			var info map[string]any
			if json.Unmarshal(infoRes.Body, &info) == nil {
				if id, ok := info["id"].(string); ok && id != "" {
					result = id
				} else {
					result = "None"
				}
			}
		} else {
			result = "None"
		}
	}
	for k, v := range respHeaders {
		w.Header().Set(k, v)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"result": result, "data": arr})
}

func (h *TextHandler) Props(w http.ResponseWriter, r *http.Request) {
	body := decodeBody(r)
	if bodyStr(body, "api_server") == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	baseURL := TrimV1(bodyStr(body, "api_server"))
	headers := map[string]string{}
	for k, v := range AdditionalHeadersByType(bodyStr(body, "api_type"), baseURL, userRootOf(r), h.Cfg) {
		headers[k] = v
	}
	propsURL := baseURL + "/props"
	if bodyStr(body, "api_type") == TextGenLlamaCpp && bodyStr(body, "model") != "" {
		propsURL += "?model=" + url.QueryEscape(bodyStr(body, "model"))
	}
	res, err := DoJSON(h.client, r.Context(), http.MethodGet, propsURL, headers, nil)
	if err != nil || res.Status < 200 || res.Status >= 300 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var props map[string]any
	if err := json.Unmarshal(res.Body, &props); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if bodyStr(body, "api_type") == TextGenLlamaCpp {
		if tmpl, ok := props["chat_template"].(string); ok && len(tmpl) > 0 && tmpl[len(tmpl)-1] == 0 {
			props["chat_template"] = tmpl[:len(tmpl)-1] + "\n"
		}
	}
	if tmpl, ok := props["chat_template"].(string); ok {
		sum := sha256.Sum256([]byte(tmpl))
		props["chat_template_hash"] = fmt.Sprintf("%x", sum)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(props)
}

func (h *TextHandler) Generate(w http.ResponseWriter, r *http.Request) {
	body := decodeBody(r)
	if len(body) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if srv, ok := body["api_server"].(string); ok {
		body["api_server"] = strings.ReplaceAll(srv, "localhost", "127.0.0.1")
	}
	apiType := bodyStr(body, "api_type")
	baseURL := TrimV1(bodyStr(body, "api_server"))
	urlPath := "/v1/completions"
	switch apiType {
	case TextGenDreamGen:
		urlPath = "/api/openai/v1/completions"
	case TextGenMancer:
		urlPath = "/oai/v1/completions"
	case TextGenLlamaCpp:
		urlPath = "/completion"
	case TextGenOllama:
		urlPath = "/api/generate"
	case TextGenOpenRouter:
		urlPath = "/v1/chat/completions"
	}
	endpoint := baseURL + urlPath
	outHeaders := map[string]string{"Content-Type": "application/json"}
	for k, v := range AdditionalHeadersByType(apiType, bodyStr(body, "api_server"), userRootOf(r), h.Cfg) {
		outHeaders[k] = v
	}
	payload := body
	switch apiType {
	case TextGenTogetherAI:
		payload = pickKeys(body, togetherAIKeys)
	case TextGenInfermatic:
		payload = pickKeys(body, infermaticAIKeys)
	case TextGenFeatherless:
		payload = pickKeys(body, featherlessKeys)
	case TextGenGeneric:
		payload = pickKeys(body, openaiKeys)
		if stop, ok := payload["stop"].([]any); ok {
			if len(stop) > 4 {
				payload["stop"] = stop[:4]
			}
		}
	case TextGenOpenRouter:
		if prov, ok := body["provider"].([]any); ok && len(prov) > 0 {
			allowFallbacks := true
			if af, ok := body["allow_fallbacks"].(bool); ok {
				allowFallbacks = af
			}
			body["provider"] = map[string]any{"allow_fallbacks": allowFallbacks, "order": prov}
		} else {
			delete(body, "provider")
		}
		if q, ok := body["quantizations"].([]any); ok && len(q) > 0 {
			prov, _ := body["provider"].(map[string]any)
			if prov == nil {
				prov = map[string]any{}
			}
			prov["quantizations"] = q
			body["provider"] = prov
		}
		payload = pickKeys(body, openrouterTextKeys)
	case TextGenVLLM:
		payload = pickKeys(body, vllmKeys)
	case TextGenOllama:
		options := pickKeys(body, ollamaKeys)
		keepAlive := h.Cfg.Ollama.KeepAlive
		if keepAlive == 0 {
			keepAlive = -1
		}
		ollamaBody := map[string]any{
			"model": body["model"], "prompt": body["prompt"],
			"stream": body["stream"], "keep_alive": keepAlive, "raw": true, "options": options,
		}
		if _, ok := body["stream"]; !ok {
			ollamaBody["stream"] = false
		}
		if h.Cfg.Ollama.BatchSize > 0 {
			if opts, ok := ollamaBody["options"].(map[string]any); ok {
				opts["num_batch"] = h.Cfg.Ollama.BatchSize
			}
		}
		compactNils(ollamaBody)
		payload = ollamaBody
	}
	stream := bodyBool(body, "stream")
	if apiType == TextGenOllama && stream {
		upstream, err := DoStream(h.client, r.Context(), http.MethodPost, endpoint, outHeaders, payload)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":true}`))
			return
		}
		pipeOllamaStream(upstream, w, r)
		return
	}
	if stream {
		upstream, err := DoStream(h.client, r.Context(), http.MethodPost, endpoint, outHeaders, payload)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":true}`))
			return
		}
		ForwardStream(upstream, w, r)
		return
	}
	res, err := DoJSON(h.client, r.Context(), http.MethodPost, endpoint, outHeaders, payload)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"error": true, "status": "UNKNOWN", "response": err.Error()})
		return
	}
	if res.Status < 200 || res.Status >= 300 {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"error": true, "status": res.Status, "response": string(res.Body)})
		return
	}
	var data map[string]any
	if err := json.Unmarshal(res.Body, &data); err != nil {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(res.Body)
		return
	}
	if apiType == TextGenInfermatic {
		if choices, ok := data["choices"].([]any); ok {
			var mapped []any
			for i, c := range choices {
				cm, ok := c.(map[string]any)
				if !ok {
					continue
				}
				text := ""
				if t, ok := cm["text"].(string); ok {
					text = t
				} else if msg, ok := cm["message"].(map[string]any); ok {
					text, _ = msg["content"].(string)
				}
				mapped = append(mapped, map[string]any{"text": text, "logprobs": cm["logprobs"], "index": i})
			}
			data["choices"] = mapped
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(data)
}

func pipeOllamaStream(upstream *http.Response, w http.ResponseWriter, r *http.Request) {
	defer upstream.Body.Close()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(upstream.StatusCode)
	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}
	flusher.Flush()
	dec := json.NewDecoder(upstream.Body)
	done := r.Context().Done()
	for {
		select {
		case <-done:
			return
		default:
		}
		var obj map[string]any
		if err := dec.Decode(&obj); err != nil {
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			flusher.Flush()
			return
		}
		text, _ := obj["response"].(string)
		thinking, _ := obj["thinking"].(string)
		chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"text": text, "thinking": thinking}}})
		_, _ = w.Write([]byte("data: " + string(chunk) + "\n\n"))
		flusher.Flush()
	}
}

func (h *TextHandler) OllamaDownload(w http.ResponseWriter, r *http.Request) {
	body := decodeBody(r)
	if bodyStr(body, "name") == "" || bodyStr(body, "api_server") == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	base := strings.TrimSuffix(bodyStr(body, "api_server"), "/")
	res, err := DoJSON(h.client, r.Context(), http.MethodPost, base+"/api/pull",
		map[string]string{"Content-Type": "application/json"},
		map[string]any{"name": body["name"], "stream": false})
	if err != nil || res.Status < 200 || res.Status >= 300 {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":true}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func (h *TextHandler) OllamaCaptionImage(w http.ResponseWriter, r *http.Request) {
	body := decodeBody(r)
	if bodyStr(body, "server_url") == "" || bodyStr(body, "model") == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	base := TrimV1(bodyStr(body, "server_url"))
	res, err := DoJSON(h.client, r.Context(), http.MethodPost, base+"/api/generate",
		map[string]string{"Content-Type": "application/json"},
		map[string]any{
			"model": body["model"], "prompt": body["prompt"],
			"images": []any{body["image"]}, "stream": false,
		})
	if err != nil || res.Status < 200 || res.Status >= 300 {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":true}`))
		return
	}
	var data map[string]any
	if err := json.Unmarshal(res.Body, &data); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":true}`))
		return
	}
	caption, _ := data["response"].(string)
	if caption == "" {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":true}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"caption": caption})
}

func (h *TextHandler) LlamaCppProps(w http.ResponseWriter, r *http.Request) {
	body := decodeBody(r)
	if bodyStr(body, "server_url") == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	base := TrimV1(bodyStr(body, "server_url"))
	res, err := DoJSON(h.client, r.Context(), http.MethodGet, base+"/props", nil, nil)
	if err != nil || res.Status < 200 || res.Status >= 300 {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":true}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(res.Body)
}

func (h *TextHandler) LlamaCppSlots(w http.ResponseWriter, r *http.Request) {
	body := decodeBody(r)
	if bodyStr(body, "server_url") == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if matched, _ := regexp.MatchString(`^(erase|info|restore|save)$`, bodyStr(body, "action")); !matched {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	base := TrimV1(bodyStr(body, "server_url"))
	action := bodyStr(body, "action")
	if action == "info" {
		res, err := DoJSON(h.client, r.Context(), http.MethodGet, base+"/slots", nil, nil)
		if err != nil || res.Status < 200 || res.Status >= 300 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":true}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(res.Body)
		return
	}
	if matched, _ := regexp.MatchString(`^\d+$`, bodyStr(body, "id_slot")); !matched {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if action != "erase" && bodyStr(body, "filename") == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	payload := map[string]any{}
	if action != "erase" {
		payload["filename"] = bodyStr(body, "filename")
	}
	res, err := DoJSON(h.client, r.Context(), http.MethodPost,
		base+"/slots/"+bodyStr(body, "id_slot")+"?action="+url.QueryEscape(action),
		map[string]string{"Content-Type": "application/json"}, payload)
	if err != nil || res.Status < 200 || res.Status >= 300 {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":true}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(res.Body)
}

func (h *TextHandler) TabbyDownload(w http.ResponseWriter, r *http.Request) {
	body := decodeBody(r)
	base := strings.TrimSuffix(bodyStr(body, "api_server"), "/")
	headers := map[string]string{"Content-Type": "application/json"}
	for k, v := range AdditionalHeadersByType(TextGenTabby, base, userRootOf(r), h.Cfg) {
		headers[k] = v
	}
	perm, err := DoJSON(h.client, r.Context(), http.MethodGet, base+"/v1/auth/permission", headers, nil)
	if err != nil || perm.Status < 200 || perm.Status >= 300 {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":true}`))
		return
	}
	var permJSON map[string]any
	if err := json.Unmarshal(perm.Body, &permJSON); err == nil {
		if p, _ := permJSON["permission"].(string); p != "" && p != "admin" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":true}`))
			return
		}
	}
	dl, err := DoJSON(h.client, r.Context(), http.MethodPost, base+"/v1/download", headers, body)
	if err != nil || dl.Status < 200 || dl.Status >= 300 {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":true}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func userRootOf(r *http.Request) string {
	uc := auth.UserFromRequest(r)
	if uc == nil {
		return ""
	}
	return uc.Directories.Root
}
