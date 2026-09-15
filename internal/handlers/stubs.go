package handlers

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
)

func stubJSON(message string) http.HandlerFunc {
	if message == "" {
		message = "Not implemented in this backend"
	}
	body, _ := json.Marshal(map[string]any{"error": map[string]any{"message": message}})
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotImplemented)
		_, _ = w.Write(body)
	}
}

func RegisterStubRoutes(r chi.Router) {
	imageMsg := "image generation is not implemented in this backend"
	ttsMsg := "text-to-speech is not implemented in this backend"

	// Backend-free local listing (mirrors Node: reads the workflows dir,
	// no generation backend involved).
	r.Post("/api/sd/comfy/workflows", comfyWorkflows)

	r.Post("/api/sd/*", stubJSON(imageMsg))
	r.Post("/api/speech/*", stubJSON(ttsMsg))
	r.Post("/api/openai/*", stubJSON("openai media endpoints are not implemented in this backend"))
	r.Post("/api/google/*", stubJSON("google media endpoints are not implemented in this backend"))
	r.Post("/api/anthropic/*", stubJSON("anthropic captioning is not implemented in this backend"))
	r.Post("/api/azure/*", stubJSON(ttsMsg))
	r.Post("/api/minimax/*", stubJSON(ttsMsg))
	r.Post("/api/volcengine/*", stubJSON(ttsMsg))
	r.Post("/api/extra/*", stubJSON("extras endpoints are not implemented in this backend"))
	r.Post("/api/tts/*", stubJSON(ttsMsg))
	r.Post("/api/text-to-speech/*", stubJSON(ttsMsg))
	r.Post("/api/plugins/*", stubJSON("server plugins are not implemented in this backend"))
	r.Get("/api/sd/*", stubJSON(imageMsg))
	r.Get("/api/speech/*", stubJSON(ttsMsg))
	r.Get("/api/openai/*", stubJSON("openai media endpoints are not implemented in this backend"))
	r.Get("/api/google/*", stubJSON("google media endpoints are not implemented in this backend"))
	r.Get("/api/anthropic/*", stubJSON("anthropic captioning is not implemented in this backend"))
	r.Get("/api/azure/*", stubJSON(ttsMsg))
	r.Get("/api/minimax/*", stubJSON(ttsMsg))
	r.Get("/api/volcengine/*", stubJSON(ttsMsg))
	r.Get("/api/extra/*", stubJSON("extras endpoints are not implemented in this backend"))
	r.Get("/api/tts/*", stubJSON(ttsMsg))
	r.Get("/api/text-to-speech/*", stubJSON(ttsMsg))
	r.Get("/api/plugins/*", stubJSON("server plugins are not implemented in this backend"))
}

func comfyWorkflows(w http.ResponseWriter, r *http.Request) {
	uc := getUserCtx(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	entries, err := os.ReadDir(filepath.Join(uc.Directories.Root, "user", "workflows"))
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	out := []string{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		if !strings.HasSuffix(strings.ToLower(name), ".json") {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}
