package handlers

import (
	"encoding/json"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"

	"github.com/TurtleTavern/turtletavern/internal/auth"
	"github.com/TurtleTavern/turtletavern/internal/config"
	"github.com/TurtleTavern/turtletavern/internal/llm"
	"github.com/TurtleTavern/turtletavern/internal/util"
	"github.com/go-chi/chi/v5"
)

var vectorSources = []string{
	"transformers", "mistral", "openai", "extras", "palm", "togetherai",
	"nomicai", "cohere", "ollama", "llamacpp", "vllm", "webllm", "koboldcpp",
	"vertexai", "electronhub", "openrouter", "chutes", "nanogpt", "siliconflow",
}

type vectorItem struct {
	Vector   []float64      `json:"vector"`
	Metadata vectorMetadata `json:"metadata"`
}

type vectorMetadata struct {
	Hash  float64 `json:"hash"`
	Text  string  `json:"text"`
	Index int     `json:"index"`
}

type VectorsHandler struct {
	Cfg *config.Config
}

func NewVectorsHandler(cfg *config.Config) *VectorsHandler {
	return &VectorsHandler{Cfg: cfg}
}

func (h *VectorsHandler) RegisterRoutes(r chi.Router) {
	r.Route("/api/vector", func(r chi.Router) {
		r.Post("/query", h.Query)
		r.Post("/query-multi", h.QueryMulti)
		r.Post("/insert", h.Insert)
		r.Post("/list", h.List)
		r.Post("/delete", h.Delete)
		r.Post("/purge-all", h.PurgeAll)
		r.Post("/purge", h.Purge)
	})
}

func (h *VectorsHandler) userDirs(r *http.Request) (string, string) {
	uc := auth.UserFromRequest(r)
	if uc == nil {
		return "", ""
	}
	return uc.Directories.Root, filepath.Join(uc.Directories.Root, "vectors")
}

func vectorIndexDir(vectorsRoot, source, collectionID, model string) string {
	return filepath.Join(vectorsRoot,
		util.SanitizeFileName(source),
		util.SanitizeFileName(collectionID),
		util.SanitizeFileName(model))
}

func vectorIndexFile(vectorsRoot, source, collectionID, model string) string {
	return filepath.Join(vectorIndexDir(vectorsRoot, source, collectionID, model), "index.json")
}

func loadVectorItems(path string) ([]vectorItem, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var items []vectorItem
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, err
	}
	return items, nil
}

func saveVectorItems(path string, items []vectorItem) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(items)
	if err != nil {
		return err
	}
	return util.AtomicWrite(path, data)
}

func cosineSimilarity(a, b []float64) float64 {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	var dot, na, nb float64
	for i := 0; i < n; i++ {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

type vectorSettings struct {
	model        string
	urlOverride  string
	extrasURL    string
	extrasKey    string
	apiURL       string
	keep         bool
	precomputed  map[string][]float64
}

func vectorSettingsFromBody(source string, body map[string]any) vectorSettings {
	strVal := func(key, def string) string {
		if s, ok := body[key].(string); ok && s != "" {
			return s
		}
		return def
	}
	vs := vectorSettings{model: strVal("model", "")}
	switch source {
	case "electronhub":
		if vs.model == "" {
			vs.model = "text-embedding-3-small"
		}
	case "openrouter":
		if vs.model == "" {
			vs.model = "openai/text-embedding-3-large"
		}
	case "mistral":
		vs.model = "mistral-embed"
	case "nomicai":
		vs.model = "nomic-embed-text-v1.5"
	case "chutes":
		if vs.model == "" {
			vs.model = "chutes-qwen-qwen3-embedding-8b"
		}
	case "nanogpt":
		if vs.model == "" {
			vs.model = "text-embedding-3-small"
		}
	case "siliconflow":
		if vs.model == "" {
			vs.model = "Qwen/Qwen3-Embedding-0.6B"
		}
		if ep, ok := body["siliconflow_endpoint"].(string); ok && ep == "cn" {
			vs.urlOverride = "https://api.siliconflow.cn/v1"
		}
	case "palm", "vertexai":
		if vs.model == "" {
			vs.model = "text-embedding-005"
		}
	case "llamacpp":
		vs.apiURL = strVal("apiUrl", "")
	case "vllm":
		vs.apiURL = strVal("apiUrl", "")
		vs.model = strVal("model", "")
	case "ollama":
		vs.apiURL = strVal("apiUrl", "")
		vs.model = strVal("model", "")
		if k, ok := body["keep"].(bool); ok {
			vs.keep = k
		}
	case "extras":
		vs.extrasURL = strVal("extrasUrl", "")
		vs.extrasKey = strVal("extrasKey", "")
	case "webllm", "koboldcpp":
		vs.precomputed = make(map[string][]float64)
		if emb, ok := body["embeddings"].(map[string]any); ok {
			for k, v := range emb {
				vs.precomputed[k] = toFloats(v)
			}
		}
	}
	return vs
}

func toFloats(v any) []float64 {
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

func (h *VectorsHandler) embedder(root string, body map[string]any) *llm.EmbedClient {
	return llm.NewEmbedClient(h.Cfg, root, body)
}

func (h *VectorsHandler) Query(w http.ResponseWriter, r *http.Request) {
	root, vectorsRoot := h.userDirs(r)
	if root == "" {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body := translateBody(r)
	collectionID, _ := body["collectionId"].(string)
	searchText, _ := body["searchText"].(string)
	if collectionID == "" || searchText == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	topK := 10
	if n, ok := body["topK"].(float64); ok && n != 0 {
		topK = int(n)
	}
	threshold := 0.0
	if n, ok := body["threshold"].(float64); ok {
		threshold = n
	}
	source, _ := body["source"].(string)
	if source == "" {
		source = "transformers"
	}
	vs := vectorSettingsFromBody(source, body)
	ec := h.embedder(root, body)
	vector, err := ec.Single(r.Context(), source, searchText, true,
		vs.model, vs.urlOverride, vs.extrasURL, vs.extrasKey, vs.apiURL, vs.keep, vs.precomputed)
	if err != nil {
		h.corruptedIndex(w, r, vectorsRoot, collectionID, source, vs.model, err)
		return
	}
	items, err := loadVectorItems(vectorIndexFile(vectorsRoot, source, collectionID, vs.model))
	if err != nil {
		h.corruptedIndex(w, r, vectorsRoot, collectionID, source, vs.model, err)
		return
	}
	type scored struct {
		item  vectorItem
		score float64
	}
	var ranked []scored
	for _, item := range items {
		ranked = append(ranked, scored{item: item, score: cosineSimilarity(vector, item.Vector)})
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })
	if len(ranked) > topK {
		ranked = ranked[:topK]
	}
	var metadata []vectorMetadata
	for _, s := range ranked {
		if s.score >= threshold {
			metadata = append(metadata, s.item.Metadata)
		}
	}
	if metadata == nil {
		metadata = []vectorMetadata{}
	}
	var hashes []float64
	for _, s := range ranked {
		hashes = append(hashes, s.item.Metadata.Hash)
	}
	if hashes == nil {
		hashes = []float64{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"metadata": metadata, "hashes": hashes})
}

func (h *VectorsHandler) corruptedIndex(w http.ResponseWriter, r *http.Request, vectorsRoot, collectionID, source, model string, err error) {
	if _, ok := r.URL.Query()["regenerated"]; !ok && collectionID != "" && source != "" {
		dir := vectorIndexDir(vectorsRoot, source, collectionID, model)
		if st, statErr := os.Stat(dir); statErr == nil && st.IsDir() {
			_ = os.RemoveAll(dir)
			target := r.URL.Path
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery + "&regenerated=true"
			} else {
				target += "?regenerated=true"
			}
			http.Redirect(w, r, target, http.StatusTemporaryRedirect)
			return
		}
	}
	w.WriteHeader(http.StatusInternalServerError)
}

func (h *VectorsHandler) QueryMulti(w http.ResponseWriter, r *http.Request) {
	root, vectorsRoot := h.userDirs(r)
	if root == "" {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body := translateBody(r)
	rawIDs, _ := body["collectionIds"].([]any)
	searchText, _ := body["searchText"].(string)
	if len(rawIDs) == 0 || searchText == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	topK := 10
	if n, ok := body["topK"].(float64); ok && n != 0 {
		topK = int(n)
	}
	threshold := 0.0
	if n, ok := body["threshold"].(float64); ok {
		threshold = n
	}
	source, _ := body["source"].(string)
	if source == "" {
		source = "transformers"
	}
	vs := vectorSettingsFromBody(source, body)
	ec := h.embedder(root, body)
	vector, err := ec.Single(r.Context(), source, searchText, true,
		vs.model, vs.urlOverride, vs.extrasURL, vs.extrasKey, vs.apiURL, vs.keep, vs.precomputed)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	type hit struct {
		collectionID string
		item         vectorItem
		score        float64
	}
	var all []hit
	for _, rawID := range rawIDs {
		collectionID, _ := rawID.(string)
		items, err := loadVectorItems(vectorIndexFile(vectorsRoot, source, collectionID, vs.model))
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		for _, item := range items {
			score := cosineSimilarity(vector, item.Vector)
			if len(all) < topK || score > 0 {
				all = append(all, hit{collectionID: collectionID, item: item, score: score})
			}
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].score > all[j].score })
	var filtered []hit
	for _, x := range all {
		if x.score >= threshold {
			filtered = append(filtered, x)
		}
	}
	if len(filtered) > topK {
		filtered = filtered[:topK]
	}
	grouped := map[string]any{}
	for _, x := range filtered {
		g, ok := grouped[x.collectionID].(map[string]any)
		if !ok {
			g = map[string]any{"hashes": []float64{}, "metadata": []vectorMetadata{}}
			grouped[x.collectionID] = g
		}
		g["hashes"] = append(g["hashes"].([]float64), x.item.Metadata.Hash)
		g["metadata"] = append(g["metadata"].([]vectorMetadata), x.item.Metadata)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(grouped)
}

func (h *VectorsHandler) Insert(w http.ResponseWriter, r *http.Request) {
	root, vectorsRoot := h.userDirs(r)
	if root == "" {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body := translateBody(r)
	rawItems, _ := body["items"].([]any)
	collectionID, _ := body["collectionId"].(string)
	if len(rawItems) == 0 || collectionID == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	source, _ := body["source"].(string)
	if source == "" {
		source = "transformers"
	}
	vs := vectorSettingsFromBody(source, body)
	type inputItem struct {
		hash  float64
		text  string
		index int
	}
	var inputs []inputItem
	var texts []string
	for _, raw := range rawItems {
		mm, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		hash, _ := mm["hash"].(float64)
		text, _ := mm["text"].(string)
		index := 0
		if n, ok := mm["index"].(float64); ok {
			index = int(n)
		}
		inputs = append(inputs, inputItem{hash: hash, text: text, index: index})
		texts = append(texts, text)
	}
	ec := h.embedder(root, body)
	var vectors [][]float64
	for i := 0; i < len(texts); i += 10 {
		end := i + 10
		if end > len(texts) {
			end = len(texts)
		}
		batch, err := ec.Batch(r.Context(), source, texts[i:end],
			vs.model, vs.urlOverride, vs.extrasURL, vs.extrasKey, vs.apiURL, vs.keep, vs.precomputed)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		vectors = append(vectors, batch...)
	}
	path := vectorIndexFile(vectorsRoot, source, collectionID, vs.model)
	items, err := loadVectorItems(path)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	for i, input := range inputs {
		var vec []float64
		if i < len(vectors) {
			vec = vectors[i]
		}
		items = append(items, vectorItem{Vector: vec, Metadata: vectorMetadata{Hash: input.hash, Text: input.text, Index: input.index}})
	}
	if err := saveVectorItems(path, items); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *VectorsHandler) List(w http.ResponseWriter, r *http.Request) {
	_, vectorsRoot := h.userDirs(r)
	if vectorsRoot == "" {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body := translateBody(r)
	collectionID, _ := body["collectionId"].(string)
	if collectionID == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	source, _ := body["source"].(string)
	if source == "" {
		source = "transformers"
	}
	vs := vectorSettingsFromBody(source, body)
	items, err := loadVectorItems(vectorIndexFile(vectorsRoot, source, collectionID, vs.model))
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	hashes := make([]float64, 0, len(items))
	for _, item := range items {
		hashes = append(hashes, item.Metadata.Hash)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(hashes)
}

func (h *VectorsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	_, vectorsRoot := h.userDirs(r)
	if vectorsRoot == "" {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body := translateBody(r)
	rawHashes, _ := body["hashes"].([]any)
	collectionID, _ := body["collectionId"].(string)
	if len(rawHashes) == 0 || collectionID == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	source, _ := body["source"].(string)
	if source == "" {
		source = "transformers"
	}
	vs := vectorSettingsFromBody(source, body)
	want := make(map[float64]bool, len(rawHashes))
	for _, hsh := range rawHashes {
		if n, ok := hsh.(float64); ok {
			want[n] = true
		}
	}
	path := vectorIndexFile(vectorsRoot, source, collectionID, vs.model)
	items, err := loadVectorItems(path)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	kept := items[:0]
	for _, item := range items {
		if !want[item.Metadata.Hash] {
			kept = append(kept, item)
		}
	}
	if kept == nil {
		kept = []vectorItem{}
	}
	if err := saveVectorItems(path, kept); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *VectorsHandler) PurgeAll(w http.ResponseWriter, r *http.Request) {
	_, vectorsRoot := h.userDirs(r)
	if vectorsRoot == "" {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	for _, source := range vectorSources {
		sourcePath := filepath.Join(vectorsRoot, util.SanitizeFileName(source))
		if _, err := os.Stat(sourcePath); err != nil {
			continue
		}
		_ = os.RemoveAll(sourcePath)
	}
	w.WriteHeader(http.StatusOK)
}

func (h *VectorsHandler) Purge(w http.ResponseWriter, r *http.Request) {
	_, vectorsRoot := h.userDirs(r)
	if vectorsRoot == "" {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body := translateBody(r)
	collectionID, _ := body["collectionId"].(string)
	if collectionID == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	for _, source := range vectorSources {
		sourcePath := filepath.Join(vectorsRoot,
			util.SanitizeFileName(source), util.SanitizeFileName(collectionID))
		if _, err := os.Stat(sourcePath); err != nil {
			continue
		}
		_ = os.RemoveAll(sourcePath)
	}
	w.WriteHeader(http.StatusOK)
}
