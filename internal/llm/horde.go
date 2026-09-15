package llm

import (
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/TurtleTavern/turtletavern/internal/config"
	"github.com/go-chi/chi/v5"
)

const hordeClientAgent = "TurtleTavern-Go:1.0"

type hordeCacheEntry struct {
	data    []byte
	expires time.Time
}

type HordeHandler struct {
	Cfg    *config.Config
	client *http.Client
	mu     sync.Mutex
	cache  map[string]hordeCacheEntry
}

func NewHordeHandler(cfg *config.Config) *HordeHandler {
	return &HordeHandler{Cfg: cfg, client: NewHTTPClient(cfg), cache: make(map[string]hordeCacheEntry)}
}

func (h *HordeHandler) RegisterRoutes(r chi.Router) {
	r.Route("/api/horde", func(r chi.Router) {
		r.Post("/text-workers", h.TextWorkers)
		r.Post("/text-models", h.TextModels)
		r.Post("/status", h.Status)
		r.Post("/cancel-task", h.CancelTask)
		r.Post("/task-status", h.TaskStatus)
		r.Post("/generate-text", h.GenerateText)
		r.Post("/user-info", h.UserInfo)
		r.Post("/sd-samplers", notImplemented("image generation is not implemented in this backend"))
		r.Post("/sd-models", notImplemented("image generation is not implemented in this backend"))
		r.Post("/caption-image", notImplemented("image captioning is not implemented in this backend"))
		r.Post("/generate-image", notImplemented("image generation is not implemented in this backend"))
	})
}

func (h *HordeHandler) cached(key string, force bool, fetch func() ([]byte, error)) ([]byte, error) {
	if !force {
		h.mu.Lock()
		if e, ok := h.cache[key]; ok && time.Now().Before(e.expires) {
			data := e.data
			h.mu.Unlock()
			return data, nil
		}
		h.mu.Unlock()
	}
	data, err := fetch()
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	h.cache[key] = hordeCacheEntry{data: data, expires: time.Now().Add(time.Minute)}
	h.mu.Unlock()
	return data, nil
}

func (h *HordeHandler) hordeGet(url string) ([]byte, int, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Client-Agent", hordeClientAgent)
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return data, resp.StatusCode, nil
}

func (h *HordeHandler) TextWorkers(w http.ResponseWriter, r *http.Request) {
	body := decodeBody(r)
	force := bodyBool(body, "force")
	data, err := h.cached("workers", force, func() ([]byte, error) {
		data, _, err := h.hordeGet("https://aihorde.net/api/v2/workers?type=text")
		return data, err
	})
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

func (h *HordeHandler) TextModels(w http.ResponseWriter, r *http.Request) {
	body := decodeBody(r)
	force := bodyBool(body, "force")
	data, err := h.cached("models", force, func() ([]byte, error) {
		data, code, err := h.hordeGet("https://aihorde.net/api/v2/status/models?type=text")
		if err != nil || code < 200 || code >= 300 {
			return data, err
		}
		var models []any
		if err := json.Unmarshal(data, &models); err != nil {
			return data, nil
		}
		metaData, metaCode, metaErr := h.hordeGet("https://raw.githubusercontent.com/db0/AI-Horde-text-model-reference/main/db.json")
		if metaErr != nil || metaCode < 200 || metaCode >= 300 {
			out, _ := json.Marshal(models)
			return out, nil
		}
		var meta map[string]any
		if err := json.Unmarshal(metaData, &meta); err != nil {
			out, _ := json.Marshal(models)
			return out, nil
		}
		for _, m := range models {
			mm, ok := m.(map[string]any)
			if !ok {
				continue
			}
			name, _ := mm["name"].(string)
			if md, ok := meta[name].(map[string]any); ok {
				for k, v := range md {
					mm[k] = v
				}
				mm["is_whitelisted"] = true
			} else {
				mm["is_whitelisted"] = false
			}
		}
		out, _ := json.Marshal(models)
		return out, nil
	})
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

func (h *HordeHandler) Status(w http.ResponseWriter, r *http.Request) {
	_, code, err := h.hordeGet("https://aihorde.net/api/v2/status/heartbeat")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if code >= 200 && code < 300 {
		_, _ = w.Write([]byte(`{"ok":true}`))
	} else {
		_, _ = w.Write([]byte(`{"ok":false}`))
	}
}

func (h *HordeHandler) CancelTask(w http.ResponseWriter, r *http.Request) {
	body := decodeBody(r)
	taskID := bodyStr(body, "taskId")
	req, err := http.NewRequest(http.MethodDelete, "https://aihorde.net/api/v2/generate/text/status/"+taskID, nil)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	req.Header.Set("Client-Agent", hordeClientAgent)
	resp, err := h.client.Do(req)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	data, _ := io.ReadAll(resp.Body)
	_, _ = w.Write(data)
}

func (h *HordeHandler) TaskStatus(w http.ResponseWriter, r *http.Request) {
	body := decodeBody(r)
	taskID := bodyStr(body, "taskId")
	data, code, err := h.hordeGet("https://aihorde.net/api/v2/generate/text/status/" + taskID)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write(data)
}

func (h *HordeHandler) GenerateText(w http.ResponseWriter, r *http.Request) {
	body := decodeBody(r)
	apiKey := h.secret(r, "api_key_horde")
	if apiKey == "" {
		apiKey = "0000000000"
	}
	res, err := DoJSON(h.client, r.Context(), http.MethodPost, "https://aihorde.net/api/v2/generate/text/async",
		map[string]string{
			"Content-Type": "application/json",
			"apikey":       apiKey,
			"Client-Agent": hordeClientAgent,
		}, body)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":true}`))
		return
	}
	if res.Status < 200 || res.Status >= 300 {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": string(res.Body)}})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(res.Body)
}

func (h *HordeHandler) UserInfo(w http.ResponseWriter, r *http.Request) {
	apiKey := h.secret(r, "api_key_horde")
	if apiKey == "" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"anonymous":true}`))
		return
	}
	req, err := http.NewRequest(http.MethodGet, "https://aihorde.net/api/v2/find_user", nil)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	req.Header.Set("Client-Agent", hordeClientAgent)
	req.Header.Set("apikey", apiKey)
	resp, err := h.client.Do(req)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()
	var user any
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"user": user, "sharedKey": nil, "anonymous": false})
}

func (h *HordeHandler) secret(r *http.Request, key string) string {
	root := userRootOf(r)
	if root == "" {
		return ""
	}
	return readSecret(root, key)
}
