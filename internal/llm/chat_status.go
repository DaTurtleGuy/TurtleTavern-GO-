package llm

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func (h *ChatHandler) Status(w http.ResponseWriter, r *http.Request) {
	body := decodeBody(r)
	if len(body) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	source := bodyStr(body, "chat_completion_source")
	reverseProxy := bodyStr(body, "reverse_proxy")
	if Debug {
		dbg("chat/status: source=%q reverse_proxy=%q", source, reverseProxy)
	}
	var apiURL, apiKey string
	headers := map[string]string{}
	query := map[string]string{}

	statusKey := func(secretKey string) string {
		if reverseProxy != "" {
			return bodyStr(body, "proxy_password")
		}
		return h.secret(r, secretKey)
	}

	switch source {
	case SrcOpenAI:
		if reverseProxy != "" {
			apiURL = reverseProxy
		} else {
			apiURL = APIOpenAI
		}
		apiKey = statusKey("api_key_openai")
	case SrcOpenRouter:
		apiURL = APIOpenRouter
		apiKey = h.secret(r, "api_key_openrouter")
		for k, v := range openRouterHeaders {
			headers[k] = v
		}
	case SrcMistralAI:
		if reverseProxy != "" {
			apiURL = reverseProxy
		} else {
			apiURL = APIMistral
		}
		apiKey = statusKey("api_key_mistralai")
	case SrcCustom:
		apiURL = bodyStr(body, "custom_url")
		apiKey = h.secret(r, "api_key_custom")
		var customHeaders map[string]any
		if ch := bodyStr(body, "custom_include_headers"); ch != "" {
			var parsed map[string]any
			if err := json.Unmarshal([]byte(ch), &parsed); err == nil {
				customHeaders = parsed
			}
		}
		_ = customHeaders
		MergeHeadersWithYAML(headers, bodyStr(body, "custom_include_headers"))
	case SrcCohere:
		apiURL = APICohereV1
		apiKey = h.secret(r, "api_key_cohere")
	case SrcChutes:
		apiURL = APIChutes
		apiKey = h.secret(r, "api_key_chutes")
	case SrcElectronHub:
		apiURL = APIElectronHub
		apiKey = h.secret(r, "api_key_electronhub")
	case SrcNanoGPT:
		apiURL = APINanoGPT
		apiKey = h.secret(r, "api_key_nanogpt")
		query["detailed"] = "true"
	case SrcDeepSeek:
		base := strings.TrimSuffix(APIDeepSeek, "/beta")
		if reverseProxy != "" {
			apiURL = reverseProxy
		} else {
			apiURL = base
		}
		apiKey = statusKey("api_key_deepseek")
	case SrcXAI:
		if reverseProxy != "" {
			apiURL = reverseProxy
		} else {
			apiURL = APIXAI
		}
		apiKey = statusKey("api_key_xai")
	case SrcAIMLAPI:
		apiURL = APIAIMLAPI
		apiKey = h.secret(r, "api_key_aimlapi")
		for k, v := range aimlapiHeaders {
			headers[k] = v
		}
	case SrcPollinations:
		apiURL = "https://gen.pollinations.ai/text"
		apiKey = h.secret(r, "api_key_pollinations")
	case SrcGroq:
		apiURL = APIGroq
		apiKey = h.secret(r, "api_key_groq")
	case SrcCometAPI:
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"This provider is temporarily disabled."}}`))
		return
	case SrcMoonshot:
		if reverseProxy != "" {
			apiURL = reverseProxy
		} else {
			apiURL = APIMoonshot
		}
		apiKey = statusKey("api_key_moonshot")
	case SrcFireworks:
		apiURL = APIFireworks
		apiKey = h.secret(r, "api_key_fireworks")
	case SrcMakersuite:
		h.statusMakerSuite(w, r, body)
		return
	case SrcAzureOpenAI:
		h.statusAzure(w, r, body)
		return
	case SrcSiliconFlow:
		apiURL = APISiliconFlow
		if bodyStr(body, "siliconflow_endpoint") == "cn" {
			apiURL = APISiliconFlowCN
		}
		apiKey = h.secret(r, "api_key_siliconflow")
		query["type"] = "text"
		query["sub_type"] = "chat"
	default:
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":true}`))
		return
	}

	if apiKey == "" && reverseProxy == "" && source != SrcCustom {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":true}`))
		return
	}
	modelsURL := strings.TrimSuffix(apiURL, "/") + "/models"
	if len(query) > 0 {
		q := url.Values{}
		for k, v := range query {
			q.Set(k, v)
		}
		modelsURL += "?" + q.Encode()
	}
	outHeaders := map[string]string{"Authorization": "Bearer " + apiKey}
	for k, v := range headers {
		outHeaders[k] = v
	}
	res, err := DoJSON(h.client, r.Context(), http.MethodGet, modelsURL, outHeaders, nil)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":true}`))
		return
	}
	if res.Status < 200 || res.Status >= 300 {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":true,"data":{"data":[]}}`))
		return
	}
	var data any
	if err := json.Unmarshal(res.Body, &data); err != nil {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":true,"data":{"data":[]}}`))
		return
	}
	if source == SrcPollinations {
		if arr, ok := data.([]any); ok {
			mapped := make([]any, 0, len(arr))
			for _, m := range arr {
				if mm, ok := m.(map[string]any); ok {
					if id, ok := mm["name"]; ok {
						mm["id"] = id
					}
					mapped = append(mapped, mm)
				}
			}
			data = map[string]any{"data": mapped}
		}
	}
	if source == SrcChutes {
		if m, ok := data.(map[string]any); ok {
			if arr, ok := m["data"].([]any); ok {
				var mapped []any
				for _, item := range arr {
					im, ok := item.(map[string]any)
					if !ok {
						continue
					}
					if _, ok := im["id"]; !ok {
						continue
					}
					if pricing, ok := im["pricing"].(map[string]any); ok {
						if p, ok := pricing["prompt"]; ok {
							pricing["input"] = p
						}
						if c, ok := pricing["completion"]; ok {
							pricing["output"] = c
						}
					}
					mapped = append(mapped, im)
				}
				m["data"] = mapped
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(data)
}

func (h *ChatHandler) statusMakerSuite(w http.ResponseWriter, r *http.Request, body map[string]any) {
	reverseProxy := bodyStr(body, "reverse_proxy")
	var apiKey string
	if reverseProxy != "" {
		apiKey = bodyStr(body, "proxy_password")
	} else {
		apiKey = h.secret(r, "api_key_makersuite")
	}
	base := APIMakersuite
	if reverseProxy != "" {
		base = reverseProxy
	}
	base = strings.TrimSuffix(base, "/")
	apiVersion := h.Cfg.Gemini.APIVersion
	if apiVersion == "" {
		apiVersion = "v1beta"
	}
	modelsURL := base + "/" + apiVersion + "/models"
	if apiKey != "" || reverseProxy == "" {
		if apiKey != "" {
			modelsURL += "?key=" + url.QueryEscape(apiKey)
		}
	}
	if apiKey == "" && reverseProxy == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":true}`))
		return
	}
	res, err := DoJSON(h.client, r.Context(), http.MethodGet, modelsURL, nil, nil)
	if err != nil || res.Status < 200 || res.Status >= 300 {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":true,"bypass":true,"data":{"data":[]}}`))
		return
	}
	var parsed map[string]any
	if err := json.Unmarshal(res.Body, &parsed); err != nil {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":true,"bypass":true,"data":{"data":[]}}`))
		return
	}
	var models []any
	if arr, ok := parsed["models"].([]any); ok {
		for _, m := range arr {
			mm, ok := m.(map[string]any)
			if !ok {
				continue
			}
			methods, _ := mm["supportedGenerationMethods"].([]any)
			supported := false
			for _, mt := range methods {
				if s, ok := mt.(string); ok && s == "generateContent" {
					supported = true
					break
				}
			}
			if !supported {
				continue
			}
			name, _ := mm["name"].(string)
			models = append(models, map[string]any{"id": strings.TrimPrefix(name, "models/")})
		}
	}
	if models == nil {
		models = []any{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"data": models})
}

func (h *ChatHandler) statusAzure(w http.ResponseWriter, r *http.Request, body map[string]any) {
	baseURL := bodyStr(body, "azure_base_url")
	deployment := bodyStr(body, "azure_deployment_name")
	apiVersion := bodyStr(body, "azure_api_version")
	apiKey := h.secret(r, "api_key_azure_openai")
	if apiKey == "" || baseURL == "" || deployment == "" || apiVersion == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":true,"message":"Azure configuration is incomplete."}`))
		return
	}
	modelsURL := strings.TrimSuffix(baseURL, "/") + "/openai/models?api-version=" + url.QueryEscape(apiVersion)
	res, err := DoJSON(h.client, r.Context(), http.MethodGet, modelsURL, map[string]string{
		"api-key": apiKey, "Accept": "application/json",
	}, nil)
	if err != nil || res.Status < 200 || res.Status >= 300 {
		status := http.StatusInternalServerError
		msg := "Failed to connect to the Azure endpoint."
		if err == nil {
			status = res.Status
			switch res.Status {
			case 400:
				msg = "API version may be invalid for this resource."
			case 401, 403:
				msg = "Invalid API key or insufficient permissions."
			case 404:
				msg = "Endpoint URL appears incorrect (404)."
			default:
				msg = "Azure Models endpoint error: " + http.StatusText(res.Status)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": true, "message": msg})
		return
	}
	chatURL := strings.TrimSuffix(baseURL, "/") + "/openai/deployments/" + deployment + "/chat/completions?api-version=" + url.QueryEscape(apiVersion)
	probe, err := DoJSON(h.client, r.Context(), http.MethodPost, chatURL, map[string]string{
		"api-key": apiKey, "Content-Type": "application/json", "Accept": "application/json",
	}, map[string]any{
		"messages": []any{map[string]any{"role": "user", "content": "Say word Hi"}},
		"stream": false, "max_completion_tokens": 5,
	})
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":true,"message":"Failed to connect to the Azure endpoint."}`))
		return
	}
	var parsed map[string]any
	_ = json.Unmarshal(probe.Body, &parsed)
	modelID, _ := parsed["model"].(string)
	w.Header().Set("Content-Type", "application/json")
	if modelID == "" {
		_, _ = w.Write([]byte(`{"data":[]}`))
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": modelID}}})
}

func (h *ChatHandler) Bias(w http.ResponseWriter, r *http.Request) {
	var entries []any
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&entries); err != nil || entries == nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	result := map[string]any{}
	for _, e := range entries {
		em, ok := e.(map[string]any)
		if !ok {
			continue
		}
		text, _ := em["text"].(string)
		if text == "" {
			continue
		}
		value := em["value"]
		trimmed := strings.TrimSpace(text)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			var nums []any
			if err := json.Unmarshal([]byte(trimmed), &nums); err == nil {
				allNums := true
				for _, n := range nums {
					if _, ok := n.(float64); !ok {
						allNums = false
						break
					}
				}
				if allNums {
					for _, n := range nums {
						if f, ok := n.(float64); ok {
							result[strconv.Itoa(int(f))] = value
						}
					}
					continue
				}
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func (h *ChatHandler) Process(w http.ResponseWriter, r *http.Request) {
	body := decodeBody(r)
	msgs, ok := body["messages"].([]any)
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"Invalid messages format"}`))
		return
	}
	procType, _ := body["type"].(string)
	valid := false
	for _, t := range []string{ProcNone, ProcClaude, ProcMerge, ProcMergeTools, ProcSemi, ProcSemiTools, ProcStrict, ProcStrictTools, ProcSingle} {
		if t == procType {
			valid = true
			break
		}
	}
	if !valid {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"Unknown processing type"}`))
		return
	}
	out := PostProcessPrompt(msgs, procType, PromptNamesFromBody(body))
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"messages": out})
}

func (h *ChatHandler) proxyModelList(urlStr string, headers map[string]string, extract func(data any) []string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		res, err := DoJSON(h.client, r.Context(), http.MethodGet, urlStr, headers, nil)
		if err != nil || res.Status < 200 || res.Status >= 300 {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
			return
		}
		var data any
		if err := json.Unmarshal(res.Body, &data); err != nil {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(extract(data))
	}
}

func strSlice(v []any) []string {
	var out []string
	for _, item := range v {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func (h *ChatHandler) multimodalPollinations(w http.ResponseWriter, r *http.Request) {
	h.proxyModelList("https://gen.pollinations.ai/models", nil, func(data any) []string {
		arr, ok := data.([]any)
		if !ok {
			return []string{}
		}
		var out []string
		for _, m := range arr {
			mm, ok := m.(map[string]any)
			if !ok {
				continue
			}
			modalities, _ := mm["input_modalities"].([]any)
			for _, mod := range modalities {
				if s, ok := mod.(string); ok && s == "image" {
					if name, ok := mm["name"].(string); ok {
						out = append(out, name)
					}
					break
				}
			}
		}
		return out
	})(w, r)
}

func (h *ChatHandler) multimodalAIMLAPI(w http.ResponseWriter, r *http.Request) {
	h.proxyModelList("https://api.aimlapi.com/v1/models", nil, func(data any) []string {
		m, ok := data.(map[string]any)
		if !ok {
			return []string{}
		}
		arr, _ := m["data"].([]any)
		var out []string
		for _, item := range arr {
			im, ok := item.(map[string]any)
			if !ok {
				continue
			}
			features, _ := im["features"].([]any)
			for _, f := range features {
				if s, ok := f.(string); ok && s == "openai/chat-completion.vision" {
					if id, ok := im["id"].(string); ok {
						out = append(out, id)
					}
					break
				}
			}
		}
		return out
	})(w, r)
}

func (h *ChatHandler) multimodalNanoGPT(w http.ResponseWriter, r *http.Request) {
	h.proxyModelList("https://nano-gpt.com/api/v1/models?detailed=true", nil, func(data any) []string {
		m, ok := data.(map[string]any)
		if !ok {
			return []string{}
		}
		arr, _ := m["data"].([]any)
		var out []string
		for _, item := range arr {
			im, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if caps, ok := im["capabilities"].(map[string]any); ok {
				if vision, _ := caps["vision"].(bool); vision {
					if id, ok := im["id"].(string); ok {
						out = append(out, id)
					}
				}
			}
		}
		return out
	})(w, r)
}

func (h *ChatHandler) multimodalElectronHub(w http.ResponseWriter, r *http.Request) {
	h.proxyModelList("https://api.electronhub.ai/v1/models", nil, func(data any) []string {
		m, ok := data.(map[string]any)
		if !ok {
			return []string{}
		}
		arr, _ := m["data"].([]any)
		var out []string
		for _, item := range arr {
			im, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if meta, ok := im["metadata"].(map[string]any); ok {
				if vision, _ := meta["vision"].(bool); vision {
					if id, ok := im["id"].(string); ok {
						out = append(out, id)
					}
				}
			}
		}
		return out
	})(w, r)
}

func (h *ChatHandler) multimodalChutes(w http.ResponseWriter, r *http.Request) {
	key := h.secret(r, "api_key_chutes")
	if key == "" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
		return
	}
	h.proxyModelList("https://llm.chutes.ai/v1/models", map[string]string{"Authorization": "Bearer " + key}, func(data any) []string {
		m, ok := data.(map[string]any)
		if !ok {
			return []string{}
		}
		arr, _ := m["data"].([]any)
		var out []string
		for _, item := range arr {
			im, ok := item.(map[string]any)
			if !ok {
				continue
			}
			modalities, _ := im["input_modalities"].([]any)
			for _, mod := range modalities {
				if s, ok := mod.(string); ok && s == "image" {
					if id, ok := im["id"].(string); ok {
						out = append(out, id)
					}
					break
				}
			}
		}
		return out
	})(w, r)
}

func (h *ChatHandler) multimodalMistral(w http.ResponseWriter, r *http.Request) {
	key := h.secret(r, "api_key_mistralai")
	if key == "" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
		return
	}
	h.proxyModelList("https://api.mistral.ai/v1/models", map[string]string{"Authorization": "Bearer " + key}, func(data any) []string {
		m, ok := data.(map[string]any)
		if !ok {
			return []string{}
		}
		arr, _ := m["data"].([]any)
		var out []string
		for _, item := range arr {
			im, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if caps, ok := im["capabilities"].(map[string]any); ok {
				if vision, _ := caps["vision"].(bool); vision {
					if id, ok := im["id"].(string); ok {
						out = append(out, id)
					}
				}
			}
		}
		return out
	})(w, r)
}

func (h *ChatHandler) multimodalXAI(w http.ResponseWriter, r *http.Request) {
	key := h.secret(r, "api_key_xai")
	if key == "" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
		return
	}
	h.proxyModelList("https://api.x.ai/v1/language-models", map[string]string{"Authorization": "Bearer " + key}, func(data any) []string {
		m, ok := data.(map[string]any)
		if !ok {
			return []string{}
		}
		arr, _ := m["models"].([]any)
		var out []string
		for _, item := range arr {
			im, ok := item.(map[string]any)
			if !ok {
				continue
			}
			modalities, _ := im["input_modalities"].([]any)
			for _, mod := range modalities {
				if s, ok := mod.(string); ok && s == "image" {
					if id, ok := im["id"].(string); ok {
						out = append(out, id)
					}
					break
				}
			}
		}
		found := false
		for _, id := range out {
			if id == "grok-4-0709" {
				found = true
				break
			}
		}
		if !found {
			out = append(out, "grok-4-0709")
		}
		return out
	})(w, r)
}

func (h *ChatHandler) multimodalMoonshot(w http.ResponseWriter, r *http.Request) {
	key := h.secret(r, "api_key_moonshot")
	if key == "" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
		return
	}
	h.proxyModelList("https://api.moonshot.ai/v1/models", map[string]string{"Authorization": "Bearer " + key}, func(data any) []string {
		m, ok := data.(map[string]any)
		if !ok {
			return []string{}
		}
		arr, _ := m["data"].([]any)
		var out []string
		for _, item := range arr {
			im, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if v, ok := im["supports_image_in"].(bool); ok && v {
				if id, ok := im["id"].(string); ok {
					out = append(out, id)
				}
			}
		}
		return out
	})(w, r)
}
