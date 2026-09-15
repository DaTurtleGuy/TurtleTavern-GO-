package llm

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/TurtleTavern/turtletavern/internal/config"
	"github.com/go-chi/chi/v5"
)

type OpenRouterHandler struct {
	Cfg    *config.Config
	client *http.Client
}

func NewOpenRouterHandler(cfg *config.Config) *OpenRouterHandler {
	return &OpenRouterHandler{Cfg: cfg, client: NewHTTPClient(cfg)}
}

func (h *OpenRouterHandler) RegisterRoutes(r chi.Router) {
	r.Route("/api/openrouter", func(r chi.Router) {
		r.Post("/models/providers", h.ModelProviders)
		r.Post("/models/multimodal", h.ModelsMultimodal)
		r.Post("/models/embedding", h.ModelsEmbedding)
		r.Post("/models/image", h.ModelsImage)
		r.Post("/image/generate", notImplemented("image generation is not implemented in this backend"))
	})
}

func (h *OpenRouterHandler) ModelProviders(w http.ResponseWriter, r *http.Request) {
	body := decodeBody(r)
	model := bodyStr(body, "model")
	res, err := DoJSON(h.client, r.Context(), http.MethodGet,
		APIOpenRouter+"/models/"+model+"/endpoints",
		map[string]string{"Accept": "application/json"}, nil)
	if err != nil || res.Status < 200 || res.Status >= 300 {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
		return
	}
	var data map[string]any
	if err := json.Unmarshal(res.Body, &data); err != nil {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
		return
	}
	var providers []string
	if d, ok := data["data"].(map[string]any); ok {
		if endpoints, ok := d["endpoints"].([]any); ok {
			for _, e := range endpoints {
				if em, ok := e.(map[string]any); ok {
					if name, ok := em["provider_name"].(string); ok {
						providers = append(providers, name)
					}
				}
			}
		}
	}
	if providers == nil {
		providers = []string{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(providers)
}

func (h *OpenRouterHandler) modelsByModality(w http.ResponseWriter, r *http.Request, endpoint, inMod, outMod string, idsOnly bool) {
	res, err := DoJSON(h.client, r.Context(), http.MethodGet, APIOpenRouter+endpoint,
		map[string]string{"Accept": "application/json"}, nil)
	if err != nil || res.Status < 200 || res.Status >= 300 {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
		return
	}
	var data map[string]any
	if err := json.Unmarshal(res.Body, &data); err != nil {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
		return
	}
	arr, _ := data["data"].([]any)
	type named struct {
		id   string
		name string
		raw  map[string]any
	}
	var filtered []named
	for _, m := range arr {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		arch, ok := mm["architecture"].(map[string]any)
		if !ok {
			continue
		}
		inModalities := strList(arch["input_modalities"])
		outModalities := strList(arch["output_modalities"])
		if !containsStr(inModalities, inMod) || !containsStr(outModalities, outMod) {
			continue
		}
		id, _ := mm["id"].(string)
		name, _ := mm["name"].(string)
		filtered = append(filtered, named{id: id, name: name, raw: mm})
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].id < filtered[j].id })
	w.Header().Set("Content-Type", "application/json")
	if idsOnly {
		ids := make([]string, 0, len(filtered))
		for _, f := range filtered {
			ids = append(ids, f.id)
		}
		_ = json.NewEncoder(w).Encode(ids)
		return
	}
	raw := make([]any, 0, len(filtered))
	for _, f := range filtered {
		raw = append(raw, f.raw)
	}
	_ = json.NewEncoder(w).Encode(raw)
}

func strList(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, item := range arr {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func containsStr(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

func (h *OpenRouterHandler) ModelsMultimodal(w http.ResponseWriter, r *http.Request) {
	h.modelsByModality(w, r, "/models", "image", "text", true)
}

func (h *OpenRouterHandler) ModelsEmbedding(w http.ResponseWriter, r *http.Request) {
	h.modelsByModality(w, r, "/embeddings/models", "text", "embeddings", false)
}

func (h *OpenRouterHandler) ModelsImage(w http.ResponseWriter, r *http.Request) {
	res, err := DoJSON(h.client, r.Context(), http.MethodGet, APIOpenRouter+"/models",
		map[string]string{"Accept": "application/json"}, nil)
	if err != nil || res.Status < 200 || res.Status >= 300 {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
		return
	}
	var data map[string]any
	if err := json.Unmarshal(res.Body, &data); err != nil {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
		return
	}
	arr, _ := data["data"].([]any)
	type named struct {
		id   string
		name string
	}
	var filtered []named
	for _, m := range arr {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		arch, ok := mm["architecture"].(map[string]any)
		if !ok {
			continue
		}
		if !containsStr(strList(arch["input_modalities"]), "text") ||
			!containsStr(strList(arch["output_modalities"]), "image") {
			continue
		}
		id, _ := mm["id"].(string)
		name, _ := mm["name"].(string)
		if name == "" {
			name = id
		}
		filtered = append(filtered, named{id: id, name: name})
	}
	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].id == "" || filtered[j].id == "" {
			return false
		}
		return strings.Compare(filtered[i].id, filtered[j].id) < 0
	})
	out := make([]any, 0, len(filtered))
	for _, f := range filtered {
		out = append(out, map[string]any{"value": f.id, "text": f.name})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}
