package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/TurtleTavern/turtletavern/internal/config"
)

type EmbedClient struct {
	Cfg      *config.Config
	client   *http.Client
	userRoot string
	body     map[string]any
}

func NewEmbedClient(cfg *config.Config, userRoot string, body map[string]any) *EmbedClient {
	return &EmbedClient{Cfg: cfg, client: NewHTTPClient(cfg), userRoot: userRoot, body: body}
}

func (e *EmbedClient) secret(key string) string {
	return readSecret(e.userRoot, key)
}

func (e *EmbedClient) bodyStr(key string) string {
	if s, ok := e.body[key].(string); ok {
		return s
	}
	return ""
}

type embedSource struct {
	secretKey string
	url       string
	model     string
	headers   map[string]string
	nullModel bool
}

var openAIEmbedSources = map[string]embedSource{
	"togetherai":   {secretKey: "api_key_togetherai", url: "https://api.together.xyz/v1", model: "togethercomputer/m2-bert-80M-32k-retrieval"},
	"mistral":      {secretKey: "api_key_mistralai", url: "https://api.mistral.ai/v1", model: "mistral-embed"},
	"openai":       {secretKey: "api_key_openai", url: "https://api.openai.com/v1", model: "text-embedding-ada-002"},
	"electronhub":  {secretKey: "api_key_electronhub", url: "https://api.electronhub.ai/v1", model: "text-embedding-3-small"},
	"openrouter":   {secretKey: "api_key_openrouter", url: "https://openrouter.ai/api/v1", model: "openai/text-embedding-3-large", headers: openRouterHeaders},
	"chutes":       {secretKey: "api_key_chutes", url: "https://{{MODEL}}.chutes.ai/v1", model: "chutes-qwen-qwen3-embedding-8b", nullModel: true},
	"nanogpt":      {secretKey: "api_key_nanogpt", url: "https://nano-gpt.com/api/v1", model: "text-embedding-3-small"},
	"siliconflow":  {secretKey: "api_key_siliconflow", url: "https://api.siliconflow.com/v1", model: "Qwen/Qwen3-Embedding-0.6B"},
}

func toFloatSlice(v any) []float64 {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]float64, 0, len(arr))
	for _, item := range arr {
		if f, ok := item.(float64); ok {
			out = append(out, f)
		}
	}
	return out
}

func (e *EmbedClient) openAIBatch(ctx context.Context, texts []string, source, model, urlOverride string) ([][]float64, error) {
	cfg, ok := openAIEmbedSources[source]
	if !ok {
		return nil, fmt.Errorf("unknown source %s", source)
	}
	key := e.secret(cfg.secretKey)
	if key == "" {
		return nil, fmt.Errorf("no API key found")
	}
	modelName := model
	if modelName == "" {
		modelName = cfg.model
	}
	endpoint := urlOverride
	if endpoint == "" {
		endpoint = strings.ReplaceAll(cfg.url, "{{MODEL}}", modelName)
	}
	payload := map[string]any{"input": texts, "model": modelName}
	if cfg.nullModel {
		payload["model"] = nil
	}
	headers := map[string]string{"Content-Type": "application/json", "Authorization": "Bearer " + key}
	for k, v := range cfg.headers {
		headers[k] = v
	}
	res, err := DoJSON(e.client, ctx, http.MethodPost, strings.TrimSuffix(endpoint, "/")+"/embeddings", headers, payload)
	if err != nil {
		return nil, err
	}
	if res.Status < 200 || res.Status >= 300 {
		return nil, fmt.Errorf("API request failed")
	}
	var data map[string]any
	if err := json.Unmarshal(res.Body, &data); err != nil {
		return nil, err
	}
	arr, ok := data["data"].([]any)
	if !ok {
		return nil, fmt.Errorf("API response was not an array")
	}
	sort.Slice(arr, func(i, j int) bool {
		ii, _ := arr[i].(map[string]any)["index"].(float64)
		jj, _ := arr[j].(map[string]any)["index"].(float64)
		return ii < jj
	})
	var out [][]float64
	for _, item := range arr {
		if mm, ok := item.(map[string]any); ok {
			out = append(out, toFloatSlice(mm["embedding"]))
		}
	}
	return out, nil
}

func (e *EmbedClient) cohereBatch(ctx context.Context, texts []string, isQuery bool, model string) ([][]float64, error) {
	key := e.secret("api_key_cohere")
	if key == "" {
		return nil, fmt.Errorf("no API key found")
	}
	inputType := "search_document"
	if isQuery {
		inputType = "search_query"
	}
	res, err := DoJSON(e.client, ctx, http.MethodPost, "https://api.cohere.ai/v2/embed",
		map[string]string{"Content-Type": "application/json", "Authorization": "Bearer " + key},
		map[string]any{
			"texts": texts, "model": model, "embedding_types": []string{"float"},
			"input_type": inputType, "truncate": "END",
		})
	if err != nil {
		return nil, err
	}
	if res.Status < 200 || res.Status >= 300 {
		return nil, fmt.Errorf("API request failed")
	}
	var data map[string]any
	if err := json.Unmarshal(res.Body, &data); err != nil {
		return nil, err
	}
	emb, ok := data["embeddings"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("API response was not an array")
	}
	arr, ok := emb["float"].([]any)
	if !ok {
		return nil, fmt.Errorf("API response was not an array")
	}
	var out [][]float64
	for _, item := range arr {
		out = append(out, toFloatSlice(item))
	}
	return out, nil
}

func (e *EmbedClient) nomicBatch(ctx context.Context, texts []string) ([][]float64, error) {
	key := e.secret("api_key_nomicai")
	if key == "" {
		return nil, fmt.Errorf("no API key found")
	}
	res, err := DoJSON(e.client, ctx, http.MethodPost, "https://api-atlas.nomic.ai/v1/embedding/text",
		map[string]string{"Content-Type": "application/json", "Authorization": "Bearer " + key},
		map[string]any{"texts": texts, "model": "nomic-embed-text-v1.5"})
	if err != nil {
		return nil, err
	}
	if res.Status < 200 || res.Status >= 300 {
		return nil, fmt.Errorf("API request failed")
	}
	var data map[string]any
	if err := json.Unmarshal(res.Body, &data); err != nil {
		return nil, err
	}
	arr, ok := data["embeddings"].([]any)
	if !ok {
		return nil, fmt.Errorf("API response was not an array")
	}
	var out [][]float64
	for _, item := range arr {
		out = append(out, toFloatSlice(item))
	}
	return out, nil
}

func (e *EmbedClient) ollamaBatch(ctx context.Context, texts []string, apiURL, model string, keep bool) ([][]float64, error) {
	u := strings.TrimSuffix(apiURL, "/") + "/api/embed"
	headers := map[string]string{"Content-Type": "application/json"}
	for k, v := range AdditionalHeadersByType(TextGenOllama, apiURL, e.userRoot, e.Cfg) {
		headers[k] = v
	}
	payload := map[string]any{"input": texts, "model": model, "truncate": true}
	if keep {
		payload["keep_alive"] = -1
	}
	res, err := DoJSON(e.client, ctx, http.MethodPost, u, headers, payload)
	if err != nil {
		return nil, err
	}
	if res.Status < 200 || res.Status >= 300 {
		return nil, fmt.Errorf("ollama request failed: %s", string(res.Body))
	}
	var data map[string]any
	if err := json.Unmarshal(res.Body, &data); err != nil {
		return nil, err
	}
	arr, ok := data["embeddings"].([]any)
	if !ok {
		return nil, fmt.Errorf("API response was not an array")
	}
	var out [][]float64
	for _, item := range arr {
		out = append(out, toFloatSlice(item))
	}
	return out, nil
}

func (e *EmbedClient) oaiStyleBatch(ctx context.Context, texts []string, apiURL, apiType, model string) ([][]float64, error) {
	u := strings.TrimSuffix(TrimV1(apiURL), "/") + "/v1/embeddings"
	headers := map[string]string{"Content-Type": "application/json"}
	for k, v := range AdditionalHeadersByType(apiType, apiURL, e.userRoot, e.Cfg) {
		headers[k] = v
	}
	payload := map[string]any{"input": texts}
	if model != "" {
		payload["model"] = model
	}
	res, err := DoJSON(e.client, ctx, http.MethodPost, u, headers, payload)
	if err != nil {
		return nil, err
	}
	if res.Status < 200 || res.Status >= 300 {
		return nil, fmt.Errorf("request failed: %s", string(res.Body))
	}
	var data map[string]any
	if err := json.Unmarshal(res.Body, &data); err != nil {
		return nil, err
	}
	arr, ok := data["data"].([]any)
	if !ok {
		return nil, fmt.Errorf("API response was not an array")
	}
	sort.Slice(arr, func(i, j int) bool {
		ii, _ := arr[i].(map[string]any)["index"].(float64)
		jj, _ := arr[j].(map[string]any)["index"].(float64)
		return ii < jj
	})
	var out [][]float64
	for _, item := range arr {
		if mm, ok := item.(map[string]any); ok {
			out = append(out, toFloatSlice(mm["embedding"]))
		}
	}
	return out, nil
}

func (e *EmbedClient) extrasBatch(ctx context.Context, texts []string, apiURL, apiKey string) ([][]float64, error) {
	u := strings.TrimSuffix(apiURL, "/") + "/api/embeddings/compute"
	headers := map[string]string{"Content-Type": "application/json"}
	if apiKey != "" {
		headers["Authorization"] = "Bearer " + apiKey
	}
	res, err := DoJSON(e.client, ctx, http.MethodPost, u, headers, map[string]any{"text": texts})
	if err != nil {
		return nil, err
	}
	if res.Status < 200 || res.Status >= 300 {
		return nil, fmt.Errorf("extras request failed")
	}
	var data map[string]any
	if err := json.Unmarshal(res.Body, &data); err != nil {
		return nil, err
	}
	emb := data["embedding"]
	if arr, ok := emb.([]any); ok && len(arr) > 0 {
		if _, ok := arr[0].([]any); ok {
			var out [][]float64
			for _, item := range arr {
				out = append(out, toFloatSlice(item))
			}
			return out, nil
		}
		return [][]float64{toFloatSlice(arr)}, nil
	}
	return nil, fmt.Errorf("unexpected extras response")
}

func (e *EmbedClient) googleURL(model, endpoint string) (string, map[string]string, error) {
	useVertex := e.bodyStr("api") == "vertexai"
	region := e.bodyStr("vertexai_region")
	if region == "" {
		region = "us-central1"
	}
	apiVersion := e.Cfg.Gemini.APIVersion
	if apiVersion == "" {
		apiVersion = "v1beta"
	}
	headers := map[string]string{"Content-Type": "application/json"}
	if useVertex {
		mode := e.bodyStr("vertexai_auth_mode")
		if mode == "" {
			mode = "express"
		}
		if reverseProxy := e.bodyStr("reverse_proxy"); reverseProxy != "" {
			base := strings.TrimSuffix(reverseProxy, "/") + "/v1"
			headers["Authorization"] = "Bearer " + e.bodyStr("proxy_password")
			return base + "/publishers/google/models/" + model + ":" + endpoint, headers, nil
		}
		if mode == "express" {
			key := e.secret("api_key_vertexai")
			if key == "" {
				return "", nil, fmt.Errorf("API key is required for Vertex AI Express mode")
			}
			projectID := e.bodyStr("vertexai_express_project_id")
			base := "https://" + region + "-aiplatform.googleapis.com/v1"
			if region == "global" {
				base = "https://aiplatform.googleapis.com/v1"
			}
			if projectID != "" {
				return base + "/projects/" + projectID + "/locations/" + region + "/publishers/google/models/" + model + ":" + endpoint + "?key=" + key, headers, nil
			}
			return base + "/publishers/google/models/" + model + ":" + endpoint + "?key=" + key, headers, nil
		}
		saJSON := e.secret("vertexai_service_account_json")
		if saJSON == "" {
			return "", nil, fmt.Errorf("service Account JSON is required for Vertex AI Full mode")
		}
		var sa map[string]any
		if err := json.Unmarshal([]byte(saJSON), &sa); err != nil {
			return "", nil, fmt.Errorf("failed to extract project ID from Service Account JSON")
		}
		projectID, _ := sa["project_id"].(string)
		if projectID == "" {
			return "", nil, fmt.Errorf("failed to extract project ID from Service Account JSON")
		}
		token, err := vertexAccessToken(sa)
		if err != nil {
			return "", nil, err
		}
		base := "https://" + region + "-aiplatform.googleapis.com/v1"
		if region == "global" {
			base = "https://aiplatform.googleapis.com/v1"
		}
		headers["Authorization"] = "Bearer " + token
		return base + "/projects/" + projectID + "/locations/" + region + "/publishers/google/models/" + model + ":" + endpoint, headers, nil
	}
	var key string
	var base string
	if reverseProxy := e.bodyStr("reverse_proxy"); reverseProxy != "" {
		key = e.bodyStr("proxy_password")
		base = strings.TrimSuffix(reverseProxy, "/") + "/" + apiVersion
	} else {
		key = e.secret("api_key_makersuite")
		base = strings.TrimSuffix(APIMakersuite, "/") + "/" + apiVersion
	}
	headers["x-goog-api-key"] = key
	return base + "/models/" + model + ":" + endpoint, headers, nil
}

func (e *EmbedClient) makerSuiteBatch(ctx context.Context, texts []string, model string) ([][]float64, error) {
	if e.bodyStr("api") == "vertexai" {
		endpoint, headers, err := e.googleURL(model, "predict")
		if err != nil {
			return nil, err
		}
		var reqs []any
		for _, t := range texts {
			reqs = append(reqs, map[string]any{"content": t})
		}
		res, err := DoJSON(e.client, ctx, http.MethodPost, endpoint, headers, map[string]any{"instances": reqs})
		if err != nil {
			return nil, err
		}
		if res.Status < 200 || res.Status >= 300 {
			return nil, fmt.Errorf("batch request failed")
		}
		var data map[string]any
		if err := json.Unmarshal(res.Body, &data); err != nil {
			return nil, err
		}
		preds, ok := data["predictions"].([]any)
		if !ok {
			return nil, fmt.Errorf("did not return an array")
		}
		var out [][]float64
		for _, p := range preds {
			pm, ok := p.(map[string]any)
			if !ok {
				continue
			}
			emb, ok := pm["embeddings"].(map[string]any)
			if !ok {
				continue
			}
			out = append(out, toFloatSlice(emb["values"]))
		}
		return out, nil
	}
	endpoint, headers, err := e.googleURL(model, "batchEmbedContents")
	if err != nil {
		return nil, err
	}
	var reqs []any
	for _, t := range texts {
		reqs = append(reqs, map[string]any{
			"model":   "models/" + model,
			"content": map[string]any{"parts": []any{map[string]any{"text": t}}},
		})
	}
	res, err := DoJSON(e.client, ctx, http.MethodPost, endpoint, headers, map[string]any{"requests": reqs})
	if err != nil {
		return nil, err
	}
	if res.Status < 200 || res.Status >= 300 {
		return nil, fmt.Errorf("batch request failed")
	}
	var data map[string]any
	if err := json.Unmarshal(res.Body, &data); err != nil {
		return nil, err
	}
	arr, ok := data["embeddings"].([]any)
	if !ok {
		return nil, fmt.Errorf("did not return an array")
	}
	var out [][]float64
	for _, item := range arr {
		if mm, ok := item.(map[string]any); ok {
			out = append(out, toFloatSlice(mm["values"]))
		}
	}
	return out, nil
}

func (e *EmbedClient) Batch(ctx context.Context, source string, texts []string, model, urlOverride, extrasURL, extrasKey, apiURL string, keep bool, precomputed map[string][]float64) ([][]float64, error) {
	switch source {
	case "nomicai":
		return e.nomicBatch(ctx, texts)
	case "togetherai", "mistral", "openai", "electronhub", "openrouter", "chutes", "nanogpt", "siliconflow":
		return e.openAIBatch(ctx, texts, source, model, urlOverride)
	case "cohere":
		return e.cohereBatch(ctx, texts, false, model)
	case "llamacpp":
		return e.oaiStyleBatch(ctx, texts, apiURL, TextGenLlamaCpp, "")
	case "vllm":
		return e.oaiStyleBatch(ctx, texts, apiURL, TextGenVLLM, model)
	case "ollama":
		return e.ollamaBatch(ctx, texts, apiURL, model, keep)
	case "extras":
		return e.extrasBatch(ctx, texts, extrasURL, extrasKey)
	case "palm", "vertexai":
		return e.makerSuiteBatch(ctx, texts, model)
	case "webllm", "koboldcpp":
		out := make([][]float64, 0, len(texts))
		for _, t := range texts {
			out = append(out, precomputed[t])
		}
		return out, nil
	case "transformers":
		return nil, fmt.Errorf("local transformer embeddings are not implemented in this backend")
	default:
		return nil, fmt.Errorf("unknown vector source %s", source)
	}
}

func (e *EmbedClient) Single(ctx context.Context, source, text string, isQuery bool, model, urlOverride, extrasURL, extrasKey, apiURL string, keep bool, precomputed map[string][]float64) ([]float64, error) {
	if source == "cohere" {
		out, err := e.cohereBatch(ctx, []string{text}, isQuery, model)
		if err != nil {
			return nil, err
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("empty embedding")
		}
		return out[0], nil
	}
	out, err := e.Batch(ctx, source, []string{text}, model, urlOverride, extrasURL, extrasKey, apiURL, keep, precomputed)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("empty embedding")
	}
	return out[0], nil
}
