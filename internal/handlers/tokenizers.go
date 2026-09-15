package handlers

import (
	"encoding/json"
	"math"
	"net/http"
	"strings"

	"github.com/TurtleTavern/turtletavern/internal/auth"
	"github.com/TurtleTavern/turtletavern/internal/config"
	"github.com/TurtleTavern/turtletavern/internal/llm"
	"github.com/go-chi/chi/v5"
	tiktoken "github.com/tiktoken-go/tokenizer"
)

var localTokenizerModels = []string{
	"gpt2", "llama", "nerdstash", "nerdstash_v2", "mistral",
	"yi", "claude", "llama3", "gemma", "jamba", "qwen2",
	"command-r", "command-a", "nemo", "deepseek",
}

type TokenizersHandler struct {
	Cfg    *config.Config
	client *http.Client
}

func NewTokenizersHandler(cfg *config.Config) *TokenizersHandler {
	return &TokenizersHandler{Cfg: cfg, client: llm.NewHTTPClient(cfg)}
}

func (h *TokenizersHandler) RegisterRoutes(r chi.Router) {
	r.Route("/api/tokenizers", func(r chi.Router) {
		for _, model := range localTokenizerModels {
			m := model
			r.Post("/"+m+"/encode", notLocalTokenizer)
			r.Post("/"+m+"/decode", notLocalTokenizer)
		}
		r.Post("/openai/encode", h.OpenAIEncode)
		r.Post("/openai/decode", h.OpenAIDecode)
		r.Post("/openai/count", h.OpenAICount)
		r.Post("/remote/kobold/count", h.RemoteKoboldCount)
		r.Post("/remote/textgenerationwebui/encode", h.RemoteTextgenEncode)
	})
}

func notLocalTokenizer(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotImplemented)
	_, _ = w.Write([]byte(`{"error":"local tokenizers are not implemented in this backend"}`))
}

func guesstimateTokens(s string) int {
	return int(math.Ceil(float64(len([]byte(s))) / 3.35))
}

func tiktokenModelFor(queryModel string) (string, bool) {
	q := queryModel
	switch {
	case strings.Contains(q, "gpt-4o") || strings.Contains(q, "chatgpt-4o-latest") ||
		strings.Contains(q, "gpt-4.1") || strings.Contains(q, "gpt-4.5") ||
		strings.Contains(q, "gpt-5") || strings.Contains(q, "o3") ||
		strings.Contains(q, "o4-mini") || q == "o1" ||
		strings.Contains(q, "o1-preview") || strings.Contains(q, "o1-mini") ||
		strings.Contains(q, "o3-mini"):
		return q, true
	case strings.Contains(q, "gpt-4-32k"):
		return "gpt-4-32k", true
	case strings.Contains(q, "gpt-4"):
		return "gpt-4", true
	case strings.Contains(q, "gpt-3.5-turbo-0301"):
		return "gpt-3.5-turbo-0301", true
	case strings.Contains(q, "gpt-3.5-turbo"):
		return "gpt-3.5-turbo", true
	case strings.Contains(q, "text-davinci"), strings.Contains(q, "text-curie"),
		strings.Contains(q, "text-babbage"), strings.Contains(q, "text-ada"),
		strings.Contains(q, "code-"):
		return q, true
	}
	for _, fam := range []string{"claude", "llama", "mistral", "yi", "deepseek",
		"gemma", "gemini", "learnlm", "jamba", "qwen2", "command-r",
		"command-a", "nemo", "nerdstash"} {
		if strings.Contains(q, fam) {
			return "", false
		}
	}
	return "gpt-3.5-turbo", true
}

func openaiCodec(queryModel string) (tiktoken.Codec, bool) {
	name, ok := tiktokenModelFor(queryModel)
	if !ok {
		return nil, false
	}
	if c, err := tiktoken.ForModel(tiktoken.Model(name)); err == nil {
		return c, true
	}
	if c, err := tiktoken.ForModel(tiktoken.Model("gpt-3.5-turbo")); err == nil {
		return c, true
	}
	return nil, false
}

func (h *TokenizersHandler) OpenAIEncode(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	body := translateBody(r)
	if len(body) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	text, _ := body["text"].(string)
	queryModel := r.URL.Query().Get("model")
	if codec, ok := openaiCodec(queryModel); ok {
		if ids, chunks, err := codec.Encode(text); err == nil {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ids": ids, "count": len(ids), "chunks": chunks,
			})
			return
		}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ids": []any{}, "count": guesstimateTokens(text), "chunks": []any{},
	})
}

func (h *TokenizersHandler) OpenAIDecode(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	body := translateBody(r)
	if len(body) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	queryModel := r.URL.Query().Get("model")
	if codec, ok := openaiCodec(queryModel); ok {
		var ids []uint
		if arr, ok := body["ids"].([]any); ok {
			for _, v := range arr {
				if f, ok := v.(float64); ok && f >= 0 {
					ids = append(ids, uint(f))
				}
			}
		}
		if text, err := codec.Decode(ids); err == nil {
			_ = json.NewEncoder(w).Encode(map[string]any{"text": text})
			return
		}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"text": ""})
}

func (h *TokenizersHandler) OpenAICount(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	var messages []any
	if err := json.NewDecoder(r.Body).Decode(&messages); err != nil || messages == nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	queryModel := r.URL.Query().Get("model")
	tokensPerMessage := 3
	tokensPerName := 1
	if strings.Contains(queryModel, "gpt-3.5-turbo-0301") {
		tokensPerMessage = 4
		tokensPerName = -1
	}
	codec, exact := openaiCodec(queryModel)
	encodeLen := func(s string) int {
		if exact {
			if ids, _, err := codec.Encode(s); err == nil {
				return len(ids)
			}
		}
		return guesstimateTokens(s)
	}
	numTokens := 0
	for _, m := range messages {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		numTokens += tokensPerMessage
		for k, v := range mm {
			s, _ := v.(string)
			numTokens += encodeLen(s)
			if k == "name" {
				numTokens += tokensPerName
			}
		}
	}
	numTokens += 3
	if strings.Contains(queryModel, "gpt-3.5-turbo-0301") {
		numTokens += 9
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"token_count": numTokens})
}

func (h *TokenizersHandler) RemoteKoboldCount(w http.ResponseWriter, r *http.Request) {
	body := translateBody(r)
	if len(body) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	text, _ := body["text"].(string)
	baseURL, _ := body["url"].(string)
	endpoint := strings.TrimSuffix(baseURL, "/") + "/extra/tokencount"
	res, err := llm.DoJSON(h.client, r.Context(), http.MethodPost, endpoint,
		map[string]string{"Content-Type": "application/json"},
		map[string]any{"prompt": text})
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":true}`))
		return
	}
	if res.Status < 200 || res.Status >= 300 {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":true}`))
		return
	}
	var data map[string]any
	if err := json.Unmarshal(res.Body, &data); err != nil {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":true}`))
		return
	}
	ids, _ := data["ids"].([]any)
	if ids == nil {
		ids = []any{}
	}
	count, _ := data["value"].(float64)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"count": count, "ids": ids})
}

func (h *TokenizersHandler) RemoteTextgenEncode(w http.ResponseWriter, r *http.Request) {
	body := translateBody(r)
	if len(body) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	text, _ := body["text"].(string)
	baseURL, _ := body["url"].(string)
	model, _ := body["model"].(string)
	apiType, _ := body["api_type"].(string)
	trimmed := strings.TrimSuffix(strings.TrimSuffix(baseURL, "/"), "/v1")
	var endpoint string
	var payload map[string]any
	switch apiType {
	case llm.TextGenTabby:
		endpoint = trimmed + "/v1/token/encode"
		payload = map[string]any{"text": text, "add_bos_token": false}
	case llm.TextGenKoboldCpp:
		endpoint = trimmed + "/api/extra/tokencount"
		payload = map[string]any{"prompt": text, "special": false}
	case llm.TextGenLlamaCpp:
		endpoint = trimmed + "/tokenize"
		payload = map[string]any{"model": model, "content": text}
	case llm.TextGenVLLM:
		endpoint = trimmed + "/tokenize"
		payload = map[string]any{"model": model, "prompt": text}
	case llm.TextGenAphrodite:
		endpoint = trimmed + "/v1/tokenize"
		payload = map[string]any{"model": model, "prompt": text}
	default:
		endpoint = trimmed + "/v1/internal/encode"
		payload = map[string]any{"text": text}
	}
	headers := map[string]string{"Content-Type": "application/json"}
	ucRoot := ""
	if uc := auth.UserFromRequest(r); uc != nil {
		ucRoot = uc.Directories.Root
	}
	for k, v := range llm.AdditionalHeadersByType(apiType, baseURL, ucRoot, h.Cfg) {
		headers[k] = v
	}
	res, err := llm.DoJSON(h.client, r.Context(), http.MethodPost, endpoint, headers, payload)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":true}`))
		return
	}
	if res.Status < 200 || res.Status >= 300 {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":true}`))
		return
	}
	var data map[string]any
	var arr []any
	count := -1.0
	var ids []any
	if err := json.Unmarshal(res.Body, &data); err == nil {
		if n, ok := numVal(data["length"]); ok {
			count = n
		} else if n, ok := numVal(data["count"]); ok {
			count = n
		} else if n, ok := numVal(data["value"]); ok {
			count = n
		} else if t, ok := data["tokens"].([]any); ok {
			count = float64(len(t))
		}
		if t, ok := data["tokens"].([]any); ok {
			ids = t
		} else if t, ok := data["ids"].([]any); ok {
			ids = t
		}
	} else if err := json.Unmarshal(res.Body, &arr); err == nil {
		count = float64(len(arr))
		for _, item := range arr {
			ids = append(ids, item)
		}
	}
	if ids == nil {
		ids = []any{}
	}
	if count < 0 {
		count = float64(len(ids))
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"count": count, "ids": ids})
}

func numVal(v any) (float64, bool) {
	if f, ok := v.(float64); ok {
		return f, true
	}
	return 0, false
}
