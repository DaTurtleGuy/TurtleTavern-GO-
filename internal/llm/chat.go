package llm

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/TurtleTavern/turtletavern/internal/auth"
	"github.com/TurtleTavern/turtletavern/internal/config"
	"github.com/TurtleTavern/turtletavern/internal/secrets"
	"github.com/go-chi/chi/v5"
)

const (
	APIOpenAI      = "https://api.openai.com/v1"
	APIClaude      = "https://api.anthropic.com/v1"
	APIMistral     = "https://api.mistral.ai/v1"
	APICohereV1    = "https://api.cohere.ai/v1"
	APICohereV2    = "https://api.cohere.ai/v2"
	APIPerplexity  = "https://api.perplexity.ai"
	APIGroq        = "https://api.groq.com/openai/v1"
	APIMakersuite  = "https://generativelanguage.googleapis.com"
	APIVertexAI    = "https://us-central1-aiplatform.googleapis.com"
	APIAI21        = "https://api.ai21.com/studio/v1"
	APIChutes      = "https://llm.chutes.ai/v1"
	APIElectronHub = "https://api.electronhub.ai/v1"
	APINanoGPT     = "https://nano-gpt.com/api/v1"
	APIDeepSeek    = "https://api.deepseek.com/beta"
	APIXAI         = "https://api.x.ai/v1"
	APIAIMLAPI     = "https://api.aimlapi.com/v1"
	APIPollinations = "https://gen.pollinations.ai/v1"
	APIMoonshot    = "https://api.moonshot.ai/v1"
	APIFireworks   = "https://api.fireworks.ai/inference/v1"
	APICometAPI    = "https://api.cometapi.com/v1"
	APIZAICWrite   = "https://api.z.ai/api/paas/v4"
	APIZAICoding   = "https://api.z.ai/api/coding/paas/v4"
	APISiliconFlow = "https://api.siliconflow.com/v1"
	APISiliconFlowCN = "https://api.siliconflow.cn/v1"
	APIOpenRouter  = "https://openrouter.ai/api/v1"
)

const (
	SrcOpenAI     = "openai"
	SrcClaude     = "claude"
	SrcOpenRouter = "openrouter"
	SrcAI21       = "ai21"
	SrcMakersuite = "makersuite"
	SrcVertexAI   = "vertexai"
	SrcMistralAI  = "mistralai"
	SrcCustom     = "custom"
	SrcCohere     = "cohere"
	SrcPerplexity = "perplexity"
	SrcGroq       = "groq"
	SrcChutes     = "chutes"
	SrcElectronHub = "electronhub"
	SrcNanoGPT    = "nanogpt"
	SrcDeepSeek   = "deepseek"
	SrcAIMLAPI    = "aimlapi"
	SrcXAI        = "xai"
	SrcPollinations = "pollinations"
	SrcMoonshot   = "moonshot"
	SrcFireworks  = "fireworks"
	SrcCometAPI   = "cometapi"
	SrcAzureOpenAI = "azure_openai"
	SrcZAI        = "zai"
	SrcSiliconFlow = "siliconflow"
)

var textCompletionModels = map[string]bool{
	"gpt-3.5-turbo-instruct": true, "gpt-3.5-turbo-instruct-0914": true,
	"text-davinci-003": true, "text-davinci-002": true, "text-davinci-001": true,
	"text-curie-001": true, "text-babbage-001": true, "text-ada-001": true,
	"code-davinci-002": true, "code-davinci-001": true, "code-cushman-002": true,
	"code-cushman-001": true, "text-davinci-edit-001": true, "code-davinci-edit-001": true,
	"text-embedding-ada-002": true, "text-similarity-davinci-001": true,
	"text-similarity-curie-001": true, "text-similarity-babbage-001": true,
	"text-similarity-ada-001": true, "text-search-davinci-doc-001": true,
	"text-search-curie-doc-001": true, "text-search-babbage-doc-001": true,
	"text-search-ada-doc-001": true, "code-search-babbage-code-001": true,
	"code-search-ada-code-001": true,
}

var openaiReasoningEffortModels = []string{
	"o1", "o3-mini", "o3-mini-2025-01-31", "o4-mini", "o4-mini-2025-04-16",
	"o3", "o3-2025-04-16", "gpt-5", "gpt-5-2025-08-07", "gpt-5-mini",
	"gpt-5-mini-2025-08-07", "gpt-5-nano", "gpt-5-nano-2025-08-07",
	"gpt-5.1", "gpt-5.1-2025-11-13", "gpt-5.1-chat-latest", "gpt-5.2",
	"gpt-5.2-2025-12-11", "gpt-5.2-chat-latest", "gpt-5.3-chat-latest", "gpt-5.4",
}

var openaiFixedReasoningEffort = map[string]string{"gpt-5.3-chat-latest": "medium"}
var openaiReasoningEffortMap = map[string]string{"min": "minimal"}
var openaiVerbosityRe = regexp.MustCompile(`^gpt-5`)
var nanogptReasoningEffortMap = map[string]string{
	"min": "none", "low": "minimal", "medium": "low", "high": "medium", "max": "high",
}

type ChatHandler struct {
	Cfg    *config.Config
	client *http.Client
}

func NewChatHandler(cfg *config.Config) *ChatHandler {
	return &ChatHandler{Cfg: cfg, client: NewHTTPClient(cfg)}
}

func (h *ChatHandler) RegisterRoutes(r chi.Router) {
	r.Route("/api/backends/chat-completions", func(r chi.Router) {
		r.Post("/generate", h.Generate)
		r.Post("/status", h.Status)
		r.Post("/bias", h.Bias)
		r.Post("/process", h.Process)
		r.Route("/multimodal-models", func(r chi.Router) {
			r.Post("/pollinations", h.multimodalPollinations)
			r.Post("/aimlapi", h.multimodalAIMLAPI)
			r.Post("/nanogpt", h.multimodalNanoGPT)
			r.Post("/electronhub", h.multimodalElectronHub)
			r.Post("/chutes", h.multimodalChutes)
			r.Post("/mistral", h.multimodalMistral)
			r.Post("/xai", h.multimodalXAI)
			r.Post("/moonshot", h.multimodalMoonshot)
		})
	})
}

func decodeBody(r *http.Request) map[string]any {
	body := map[string]any{}
	if r.Body == nil {
		return body
	}
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&body); err != nil {
		return map[string]any{}
	}
	if body == nil {
		return map[string]any{}
	}
	return body
}

func bodyStr(body map[string]any, key string) string {
	if s, ok := body[key].(string); ok {
		return s
	}
	return ""
}

func bodyBool(body map[string]any, key string) bool {
	if b, ok := body[key].(bool); ok {
		return b
	}
	return false
}

func bodyFloat(body map[string]any, key string) (float64, bool) {
	switch v := body[key].(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case json.Number:
		if f, err := v.Float64(); err == nil {
			return f, true
		}
	}
	return 0, false
}

func bodyMessages(body map[string]any) []any {
	if arr, ok := body["messages"].([]any); ok {
		return arr
	}
	return nil
}

func (h *ChatHandler) userRoot(r *http.Request) string {
	uc := auth.UserFromRequest(r)
	if uc == nil {
		return ""
	}
	return uc.Directories.Root
}

func (h *ChatHandler) secret(r *http.Request, key string) string {
	root := h.userRoot(r)
	if root == "" {
		return ""
	}
	return secrets.ReadActiveSecret(root, key)
}

func (h *ChatHandler) cacheTTL() string {
	if h.Cfg.Claude.ExtendedTTL {
		return "1h"
	}
	return "5m"
}

func (h *ChatHandler) cachingAtDepth() int {
	if h.Cfg.Claude.CachingAtDepth >= 0 {
		return h.Cfg.Claude.CachingAtDepth
	}
	return -1
}

func writeUpstreamError(w http.ResponseWriter, status int, message string, quota bool) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": message}, "quota_error": quota})
}

func (h *ChatHandler) finishNonStream(w http.ResponseWriter, res *UpstreamResult, provider string) {
	if res.Status >= 200 && res.Status < 300 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(res.Status)
		_, _ = w.Write(res.Body)
		return
	}
	message := http.StatusText(res.Status)
	if message == "" {
		message = "Unknown error occurred"
	}
	quota := false
	if res.Status == 429 {
		if parsed := TryParseJSON(res.Body); parsed != nil {
			if m, ok := parsed.(map[string]any); ok {
				if e, ok := m["error"].(map[string]any); ok && e["type"] == "insufficient_quota" {
					quota = true
				}
			}
		}
	}
	log.Printf("%s request error: %s %s", provider, message, string(res.Body))
	writeUpstreamError(w, http.StatusInternalServerError, message, quota)
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func (h *ChatHandler) Generate(w http.ResponseWriter, r *http.Request) {
	body := decodeBody(r)
	if len(body) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":true}`))
		return
	}
	if Debug {
		dbg("chat/generate: source=%q model=%q stream=%v reverse_proxy=%q custom_url=%q",
			bodyStr(body, "chat_completion_source"), bodyStr(body, "model"),
			body["stream"], bodyStr(body, "reverse_proxy"), bodyStr(body, "custom_url"))
	}
	if postProc, ok := body["custom_prompt_post_processing"].(string); ok && postProc != "" {
		if msgs := bodyMessages(body); msgs != nil {
			body["messages"] = PostProcessPrompt(msgs, postProc, PromptNamesFromBody(body))
		}
	}
	if js, ok := body["json_schema"].(map[string]any); ok {
		if val, ok := js["value"]; ok {
			js["value"] = FlattenSchema(val, bodyStr(body, "chat_completion_source"))
		}
	}
	switch bodyStr(body, "chat_completion_source") {
	case SrcClaude:
		h.sendClaude(w, r, body)
		return
	case SrcAI21:
		h.sendAI21(w, r, body)
		return
	case SrcMakersuite, SrcVertexAI:
		h.sendMakerSuite(w, r, body)
		return
	case SrcMistralAI:
		h.sendMistral(w, r, body)
		return
	case SrcCohere:
		h.sendCohere(w, r, body)
		return
	case SrcDeepSeek:
		h.sendDeepSeek(w, r, body)
		return
	case SrcAIMLAPI:
		h.sendAIMLAPI(w, r, body)
		return
	case SrcXAI:
		h.sendXAI(w, r, body)
		return
	case SrcChutes:
		h.sendChutes(w, r, body)
		return
	case SrcElectronHub:
		h.sendElectronHub(w, r, body)
		return
	case SrcAzureOpenAI:
		h.sendAzure(w, r, body)
		return
	}

	var apiURL, apiKey string
	headers := map[string]string{}
	bodyParams := map[string]any{}
	model := bodyStr(body, "model")
	isTextCompletion := textCompletionModels[model]
	if _, ok := body["messages"].(string); ok {
		isTextCompletion = true
	}
	source := bodyStr(body, "chat_completion_source")
	reverseProxy := bodyStr(body, "reverse_proxy")

	switch source {
	case SrcOpenAI:
		if reverseProxy != "" {
			apiURL = reverseProxy
		} else {
			apiURL = APIOpenAI
		}
		if reverseProxy != "" {
			apiKey = bodyStr(body, "proxy_password")
		} else {
			apiKey = h.secret(r, "api_key_openai")
		}
		if lp, ok := bodyFloat(body, "logprobs"); ok && !isTextCompletion && lp > 0 {
			bodyParams["top_logprobs"] = lp
			bodyParams["logprobs"] = true
		} else if lp, ok := bodyFloat(body, "logprobs"); ok {
			bodyParams["logprobs"] = lp
			bodyParams["top_logprobs"] = nil
		}
		if h.Cfg.OpenAI.RandomizeUserID {
			bodyParams["user"] = newUUID()
		}
		if msgs := bodyMessages(body); msgs != nil {
			EmbedOpenRouterMedia(msgs, true, false)
		}
	case SrcOpenRouter:
		apiURL = APIOpenRouter
		apiKey = h.secret(r, "api_key_openrouter")
		for k, v := range openRouterHeaders {
			headers[k] = v
		}
		includeReasoning := bodyBool(body, "include_reasoning")
		bodyParams["transforms"] = openRouterTransforms(body)
		bodyParams["plugins"] = openRouterPlugins(body)
		bodyParams["reasoning"] = map[string]any{"exclude": !includeReasoning}
		if v, ok := body["min_p"]; ok {
			bodyParams["min_p"] = v
		}
		if v, ok := body["top_a"]; ok {
			bodyParams["top_a"] = v
		}
		if v, ok := body["repetition_penalty"]; ok {
			bodyParams["repetition_penalty"] = v
		}
		if prov, ok := body["provider"].([]any); ok && len(prov) > 0 {
			allowFallbacks := true
			if af, ok := body["allow_fallbacks"].(bool); ok {
				allowFallbacks = af
			}
			bodyParams["provider"] = map[string]any{"allow_fallbacks": allowFallbacks, "order": prov}
		}
		if q, ok := body["quantizations"].([]any); ok && len(q) > 0 {
			prov, _ := bodyParams["provider"].(map[string]any)
			if prov == nil {
				prov = map[string]any{}
			}
			prov["quantizations"] = q
			bodyParams["provider"] = prov
		}
		if bodyBool(body, "use_fallback") {
			bodyParams["route"] = "fallback"
		}
		if re, ok := body["reasoning_effort"]; ok {
			if rm, ok := bodyParams["reasoning"].(map[string]any); ok {
				rm["effort"] = re
			}
		}
		if v, ok := body["verbosity"]; ok {
			bodyParams["verbosity"] = v
		}
		if js, ok := body["json_schema"].(map[string]any); ok {
			bodyParams["response_format"] = map[string]any{
				"type": "json_schema",
				"json_schema": map[string]any{
					"name":   js["name"],
					"strict": withDefault(js["strict"], true),
					"schema": js["value"],
				},
			}
		}
		isClaudeModel := regexp.MustCompile(`^anthropic/claude`).MatchString(model)
		isGeminiModel := regexp.MustCompile(`google/gemini`).MatchString(model)
		if msgs := bodyMessages(body); msgs != nil {
			EmbedOpenRouterMedia(msgs, true, true)
			AddOpenRouterSignatures(msgs, model, h.Cfg.Gemini.ThoughtSignatures)
			if isClaudeModel {
				if h.Cfg.Gemini.EnableSystemPromptCache {
					CachingSystemPromptForOpenRouter(msgs, h.cacheTTL())
				}
				if h.cachingAtDepth() != -1 {
					CachingAtDepthForOpenRouterClaude(msgs, h.cachingAtDepth(), h.cacheTTL())
				}
			}
			if isGeminiModel && h.Cfg.Gemini.EnableSystemPromptCache && isOpenRouterGeminiCacheable(h.client, model) {
				CachingSystemPromptForOpenRouter(msgs, "")
			}
		}
		if isGeminiModel {
			bodyParams["safety_settings"] = geminiSafetySettings()
		}
	case SrcCustom:
		apiURL = bodyStr(body, "custom_url")
		apiKey = h.secret(r, "api_key_custom")
		if lp, ok := bodyFloat(body, "logprobs"); ok && !isTextCompletion && lp > 0 {
			bodyParams["top_logprobs"] = lp
			bodyParams["logprobs"] = true
		} else if lp, ok := bodyFloat(body, "logprobs"); ok {
			bodyParams["logprobs"] = lp
			bodyParams["top_logprobs"] = nil
		}
		MergeObjectWithYAML(bodyParams, bodyStr(body, "custom_include_body"))
		MergeHeadersWithYAML(headers, bodyStr(body, "custom_include_headers"))
		if msgs := bodyMessages(body); msgs != nil {
			EmbedOpenRouterMedia(msgs, true, false)
		}
	case SrcPerplexity:
		apiURL = APIPerplexity
		apiKey = h.secret(r, "api_key_perplexity")
		bodyParams["reasoning_effort"] = body["reasoning_effort"]
		if msgs := bodyMessages(body); msgs != nil {
			body["messages"] = PostProcessPrompt(msgs, ProcStrict, PromptNamesFromBody(body))
		}
		if js, ok := body["json_schema"].(map[string]any); ok {
			bodyParams["response_format"] = map[string]any{
				"type": "json_schema", "json_schema": map[string]any{"schema": js["value"]},
			}
		}
	case SrcGroq:
		apiURL = APIGroq
		apiKey = h.secret(r, "api_key_groq")
		if js, ok := body["json_schema"].(map[string]any); ok {
			bodyParams["response_format"] = map[string]any{
				"type": "json_schema",
				"json_schema": map[string]any{
					"name": js["name"], "description": js["description"],
					"schema": js["value"], "strict": withDefault(js["strict"], true),
				},
			}
		}
	case SrcFireworks:
		apiURL = APIFireworks
		apiKey = h.secret(r, "api_key_fireworks")
		if js, ok := body["json_schema"].(map[string]any); ok {
			bodyParams["response_format"] = map[string]any{
				"type": "json_schema",
				"json_schema": map[string]any{
					"name": js["name"], "description": js["description"],
					"schema": js["value"], "strict": withDefault(js["strict"], true),
				},
			}
		}
	case SrcNanoGPT:
		apiURL = APINanoGPT
		apiKey = h.secret(r, "api_key_nanogpt")
		if enableWS, ok := body["enable_web_search"]; ok && isTruthy(enableWS) {
			if matched, _ := regexp.MatchString(`:online$`, model); !matched {
				body["model"] = model + ":online"
				model = bodyStr(body, "model")
			}
		}
		if v, ok := body["min_p"]; ok {
			bodyParams["min_p"] = v
		}
		if v, ok := body["top_a"]; ok {
			bodyParams["top_a"] = v
		}
		if v, ok := body["repetition_penalty"]; ok {
			bodyParams["repetition_penalty"] = v
		}
		if re, ok := body["reasoning_effort"].(string); ok && re != "" {
			if mapped, ok := nanogptReasoningEffortMap[re]; ok {
				bodyParams["reasoning"] = map[string]any{"effort": mapped}
			}
		}
		if matched, _ := regexp.MatchString(`(?:^|/)claude[-_]`, model); matched && h.Cfg.Claude.EnableSystemPromptCache {
			bodyParams["cache_control"] = map[string]any{"enabled": true, "ttl": h.cacheTTL()}
		}
	case SrcPollinations:
		apiURL = APIPollinations
		apiKey = h.secret(r, "api_key_pollinations")
		seed := body["seed"]
		if seed == nil {
			seed = math.Floor(randFloat() * 99999999)
		}
		bodyParams["reasoning_effort"] = body["reasoning_effort"]
		bodyParams["seed"] = seed
		if js, ok := body["json_schema"].(map[string]any); ok {
			bodyParams["response_format"] = map[string]any{
				"type": "json_schema", "json_schema": map[string]any{"schema": js["value"]},
			}
		}
	case SrcMoonshot:
		if reverseProxy != "" {
			apiURL = reverseProxy
		} else {
			apiURL = APIMoonshot
		}
		if reverseProxy != "" {
			apiKey = bodyStr(body, "proxy_password")
		} else {
			apiKey = h.secret(r, "api_key_moonshot")
		}
		thinking := "disabled"
		if bodyBool(body, "include_reasoning") {
			thinking = "enabled"
		}
		bodyParams["thinking"] = map[string]any{"type": thinking}
		if js, ok := body["json_schema"].(map[string]any); ok {
			if msgs := bodyMessages(body); msgs != nil {
				body["messages"] = SetJSONObjectFormat(bodyParams, msgs, js)
			}
		} else if msgs := bodyMessages(body); msgs != nil {
			body["messages"] = AddAssistantPrefix(msgs, toolsOf(body), "partial")
		}
	case SrcCometAPI:
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"This provider is temporarily disabled."}}`))
		return
	case SrcZAI:
		defaultURL := APIZAICWrite
		if bodyStr(body, "zai_endpoint") == "coding" {
			defaultURL = APIZAICoding
		}
		if reverseProxy != "" {
			apiURL = reverseProxy
		} else {
			apiURL = defaultURL
		}
		if reverseProxy != "" {
			apiKey = bodyStr(body, "proxy_password")
		} else {
			apiKey = h.secret(r, "api_key_zai")
		}
		headers["Accept-Language"] = "en-US,en"
		thinking := "disabled"
		if bodyBool(body, "include_reasoning") {
			thinking = "enabled"
		}
		bodyParams["thinking"] = map[string]any{"type": thinking}
		if js, ok := body["json_schema"].(map[string]any); ok {
			if msgs := bodyMessages(body); msgs != nil {
				body["messages"] = SetJSONObjectFormat(bodyParams, msgs, js)
			}
		}
	case SrcSiliconFlow:
		apiURL = APISiliconFlow
		if bodyStr(body, "siliconflow_endpoint") == "cn" {
			apiURL = APISiliconFlowCN
		}
		apiKey = h.secret(r, "api_key_siliconflow")
		if js, ok := body["json_schema"].(map[string]any); ok {
			if msgs := bodyMessages(body); msgs != nil {
				body["messages"] = SetJSONObjectFormat(bodyParams, msgs, js)
			}
		}
	default:
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":true}`))
		return
	}

	if re, ok := body["reasoning_effort"]; ok && (source == SrcCustom || source == SrcOpenAI) {
		for _, m := range openaiReasoningEffortModels {
			if m == model {
				if fixed, ok := openaiFixedReasoningEffort[model]; ok {
					bodyParams["reasoning_effort"] = fixed
				} else if rs, ok := re.(string); ok {
					if mapped, ok := openaiReasoningEffortMap[rs]; ok {
						bodyParams["reasoning_effort"] = mapped
					} else {
						bodyParams["reasoning_effort"] = re
					}
				} else {
					bodyParams["reasoning_effort"] = re
				}
				break
			}
		}
	}
	if _, ok := body["verbosity"]; ok && (source == SrcCustom || source == SrcOpenAI) {
		if openaiVerbosityRe.MatchString(model) {
			bodyParams["verbosity"] = body["verbosity"]
		}
	}
	if apiKey == "" && reverseProxy == "" && source != SrcCustom {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":true}`))
		return
	}
	if stop, ok := body["stop"].([]any); ok && len(stop) > 0 {
		bodyParams["stop"] = stop
	}

	var textPrompt string
	if isTextCompletion {
		textPrompt = ConvertTextCompletionPrompt(body["messages"])
	}
	endpoint := strings.TrimSuffix(apiURL, "/") + "/chat/completions"
	if isTextCompletion && source != SrcOpenRouter {
		endpoint = strings.TrimSuffix(apiURL, "/") + "/completions"
	}

	if tools, ok := body["tools"].([]any); ok && len(tools) > 0 && !isTextCompletion {
		bodyParams["tools"] = tools
		bodyParams["tool_choice"] = body["tool_choice"]
	}
	if js, ok := body["json_schema"].(map[string]any); ok {
		if _, has := bodyParams["response_format"]; !has {
			bodyParams["response_format"] = map[string]any{
				"type": "json_schema",
				"json_schema": map[string]any{
					"name": js["name"], "strict": withDefault(js["strict"], true), "schema": js["value"],
				},
			}
		}
	}

	requestBody := map[string]any{
		"model": model, "temperature": body["temperature"],
		"max_tokens": body["max_tokens"], "max_completion_tokens": body["max_completion_tokens"],
		"stream": body["stream"], "presence_penalty": body["presence_penalty"],
		"frequency_penalty": body["frequency_penalty"], "top_p": body["top_p"],
		"top_k": body["top_k"], "logit_bias": body["logit_bias"],
		"seed": body["seed"], "n": body["n"],
	}
	if !isTextCompletion {
		requestBody["messages"] = body["messages"]
		if _, ok := body["stop"]; ok {
			requestBody["stop"] = body["stop"]
		}
	} else {
		requestBody["prompt"] = textPrompt
	}
	for k, v := range bodyParams {
		if v == nil {
			continue
		}
		requestBody[k] = v
	}
	compactNils(requestBody)
	if source == SrcCustom {
		ExcludeKeysByYAML(requestBody, bodyStr(body, "custom_exclude_body"))
	}

	outHeaders := map[string]string{
		"Content-Type":  "application/json",
		"Authorization": "Bearer " + apiKey,
	}
	for k, v := range headers {
		outHeaders[k] = v
	}
	stream := bodyBool(body, "stream")
	if stream {
		upstream, err := DoStream(h.client, r.Context(), http.MethodPost, endpoint, outHeaders, requestBody)
		if err != nil {
			writeUpstreamError(w, http.StatusBadGateway, connectErrorMessage(err), false)
			return
		}
		ForwardStream(upstream, w, r)
		return
	}
	res, err := DoJSON(h.client, r.Context(), http.MethodPost, endpoint, outHeaders, requestBody)
	if err != nil {
		writeUpstreamError(w, http.StatusBadGateway, connectErrorMessage(err), false)
		return
	}
	h.finishNonStream(w, res, source)
}

func withDefault(v any, d any) any {
	if v == nil {
		return d
	}
	return v
}

func isTruthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t != "" && t != "false" && t != "0"
	case float64:
		return t != 0
	default:
		return v != nil
	}
}

func toolsOf(body map[string]any) []any {
	if t, ok := body["tools"].([]any); ok {
		return t
	}
	return nil
}

func randFloat() float64 {
	var b [8]byte
	_, _ = rand.Read(b[:])
	n := uint64(b[0])<<56 | uint64(b[1])<<48 | uint64(b[2])<<40 | uint64(b[3])<<32 |
		uint64(b[4])<<24 | uint64(b[5])<<16 | uint64(b[6])<<8 | uint64(b[7])
	return float64(n>>11) / (1 << 53)
}

var openRouterCacheableModels = map[string]bool{}
var openRouterCacheMu sync.Mutex

func isOpenRouterGeminiCacheable(client *http.Client, modelID string) bool {
	openRouterCacheMu.Lock()
	if openRouterCacheableModels[modelID] {
		openRouterCacheMu.Unlock()
		return true
	}
	openRouterCacheMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, APIOpenRouter+"/models", nil)
	if err != nil {
		return false
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false
	}
	var data map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return false
	}
	arr, ok := data["data"].([]any)
	if !ok {
		return false
	}
	for _, m := range arr {
		mm, ok := m.(map[string]any)
		if !ok || mm["id"] != modelID {
			continue
		}
		if pricing, ok := mm["pricing"].(map[string]any); ok {
			if _, ok := pricing["input_cache_write"]; ok {
				openRouterCacheMu.Lock()
				openRouterCacheableModels[modelID] = true
				openRouterCacheMu.Unlock()
				return true
			}
		}
		return false
	}
	return false
}

func openRouterTransforms(body map[string]any) any {	switch bodyStr(body, "middleout") {
	case "on":
		return []string{"middle-out"}
	case "off":
		return []string{}
	default:
		return nil
	}
}

func openRouterPlugins(body map[string]any) []any {
	var plugins []any
	if isTruthy(body["enable_web_search"]) {
		plugins = append(plugins, map[string]any{"id": "web"})
	}
	if plugins == nil {
		return []any{}
	}
	return plugins
}

func geminiSafetySettings() []any {
	cats := []string{
		"HARM_CATEGORY_HARASSMENT", "HARM_CATEGORY_HATE_SPEECH",
		"HARM_CATEGORY_SEXUALLY_EXPLICIT", "HARM_CATEGORY_DANGEROUS_CONTENT",
		"HARM_CATEGORY_CIVIC_INTEGRITY",
	}
	var out []any
	for _, c := range cats {
		out = append(out, map[string]any{"category": c, "threshold": "OFF"})
	}
	return out
}

func compactNils(m map[string]any) {
	for k, v := range m {
		if v == nil {
			delete(m, k)
		}
	}
}

func connectErrorMessage(err error) string {
	msg := err.Error()
	if strings.Contains(msg, "connection refused") {
		return "Connection refused: " + msg
	}
	if msg == "" {
		return "Unknown error occurred"
	}
	return msg
}
