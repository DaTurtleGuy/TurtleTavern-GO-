package handlers

import (
	"encoding/json"
	"net/http"
	"path/filepath"

	"github.com/TurtleTavern/turtletavern/internal/config"
	"github.com/TurtleTavern/turtletavern/internal/secrets"
	"github.com/go-chi/chi/v5"
)

var secretKeyList = []string{
	"api_key_horde", "api_key_mancer", "api_key_vllm", "api_key_aphrodite",
	"api_key_tabby", "api_key_openai", "api_key_novel", "api_key_claude",
	"deepl", "libre", "libre_url", "lingva_url", "api_key_openrouter",
	"api_key_ai21", "oneringtranslator_url", "deeplx_url", "api_key_makersuite",
	"api_key_vertexai", "api_key_serpapi", "api_key_togetherai", "api_key_mistralai",
	"api_key_custom", "api_key_ooba", "api_key_infermaticai", "api_key_dreamgen",
	"api_key_nomicai", "api_key_koboldcpp", "api_key_llamacpp", "api_key_cohere",
	"api_key_perplexity", "api_key_groq", "api_key_azure_tts", "api_key_featherless",
	"api_key_huggingface", "api_key_stability", "api_key_custom_openai_tts",
	"api_key_tavily", "api_key_chutes", "api_key_electronhub", "api_key_nanogpt",
	"api_key_bfl", "api_key_comfy_runpod", "api_key_falai", "api_key_generic",
	"api_key_deepseek", "api_key_serper", "api_key_aimlapi", "api_key_xai",
	"api_key_fireworks", "vertexai_service_account_json", "api_key_minimax",
	"minimax_group_id", "api_key_moonshot", "api_key_cometapi", "api_key_azure_openai",
	"api_key_zai", "api_key_siliconflow", "api_key_elevenlabs", "api_key_pollinations",
	"volcengine_app_id", "volcengine_access_key",
}

type SecretsHandler struct {
	AllowKeysExposure bool
}

func NewSecretsHandler(cfg *config.Config) *SecretsHandler {
	return &SecretsHandler{AllowKeysExposure: cfg.AllowKeysExposure}
}

func (h *SecretsHandler) manager(root string) *secrets.Manager {
	m := secrets.NewManager(root, filepath.Join(root, "backups"), h.AllowKeysExposure)
	m.MigrateFlatSecrets()
	return m
}

func (h *SecretsHandler) RegisterRoutes(r chi.Router) {
	r.Route("/api/secrets", func(r chi.Router) {
		r.Post("/write", h.Write)
		r.Post("/read", h.Read)
		r.Post("/view", h.View)
		r.Post("/find", h.Find)
		r.Post("/delete", h.Delete)
		r.Post("/rotate", h.Rotate)
		r.Post("/rename", h.Rename)
		r.Post("/settings", h.Settings)
	})
}

func (h *SecretsHandler) Write(w http.ResponseWriter, r *http.Request) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	raw := map[string]any{}
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	key, _ := raw["key"].(string)
	value, valueIsString := raw["value"].(string)
	if key == "" || !valueIsString {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("Invalid key or value"))
		return
	}
	label, _ := raw["label"].(string)
	id := h.manager(uc.Directories.Root).WriteSecret(key, value, label)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"id": id})
}

func (h *SecretsHandler) Read(w http.ResponseWriter, r *http.Request) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	state := h.manager(uc.Directories.Root).GetSecretState(secretKeyList)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(state)
}

func (h *SecretsHandler) View(w http.ResponseWriter, r *http.Request) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if !h.AllowKeysExposure {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	all := h.manager(uc.Directories.Root).GetAllSecrets()
	if all == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(all)
}

func (h *SecretsHandler) Find(w http.ResponseWriter, r *http.Request) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	var body struct {
		Key string `json:"key"`
		ID  string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if body.Key == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("Key is required"))
		return
	}
	if !h.AllowKeysExposure && !secrets.ExportableKeys[body.Key] {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	m := h.manager(uc.Directories.Root)
	state := m.GetSecretState(secretKeyList)
	if state[body.Key] == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"value": m.ReadSecret(body.Key, body.ID)})
}

func (h *SecretsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	var body struct {
		Key string `json:"key"`
		ID  string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if body.Key == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("Key and ID are required"))
		return
	}
	h.manager(uc.Directories.Root).DeleteSecret(body.Key, body.ID)
	w.WriteHeader(http.StatusNoContent)
}

func (h *SecretsHandler) Rotate(w http.ResponseWriter, r *http.Request) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	var body struct {
		Key string `json:"key"`
		ID  string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if body.Key == "" || body.ID == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("Key and ID are required"))
		return
	}
	h.manager(uc.Directories.Root).RotateSecret(body.Key, body.ID)
	w.WriteHeader(http.StatusNoContent)
}

func (h *SecretsHandler) Rename(w http.ResponseWriter, r *http.Request) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	var body struct {
		Key   string `json:"key"`
		ID    string `json:"id"`
		Label string `json:"label"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if body.Key == "" || body.ID == "" || body.Label == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("Key, ID, and label are required"))
		return
	}
	h.manager(uc.Directories.Root).RenameSecret(body.Key, body.ID, body.Label)
	w.WriteHeader(http.StatusNoContent)
}

func (h *SecretsHandler) Settings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"allowKeysExposure": h.AllowKeysExposure})
}
