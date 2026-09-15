package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"strings"
)

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func main() {
	port := flag.Int("port", 18081, "port to listen on")
	flag.Parse()

	mux := http.NewServeMux()

	mux.HandleFunc("/models", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"data": []any{map[string]any{"id": "mock-model"}}})
	})

	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"data": []any{map[string]any{"id": "mock-model"}}})
	})

	chat := func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		stream, _ := body["stream"].(bool)
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)
			flusher, _ := w.(http.Flusher)
			chunk, _ := json.Marshal(map[string]any{
				"id": "mock-1", "choices": []any{map[string]any{"delta": map[string]any{"content": "Hi"}}},
			})
			_, _ = w.Write([]byte("data: " + string(chunk) + "\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			return
		}
		writeJSON(w, map[string]any{
			"id": "mock-1",
			"choices": []any{map[string]any{
				"message": map[string]any{"content": "Hi"},
			}},
		})
	}
	mux.HandleFunc("/chat/completions", chat)
	mux.HandleFunc("/v1/chat/completions", chat)

	mux.HandleFunc("/v1/completions", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		stream, _ := body["stream"].(bool)
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"text": "Hi"}}})
			_, _ = w.Write([]byte("data: " + string(chunk) + "\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			return
		}
		writeJSON(w, map[string]any{"choices": []any{map[string]any{"text": "Hi"}}})
	})

	mux.HandleFunc("/v1/embeddings", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		n := 1
		if arr, ok := body["input"].([]any); ok {
			n = len(arr)
		}
		data := make([]any, 0, n)
		for i := 0; i < n; i++ {
			data = append(data, map[string]any{"index": i, "embedding": []float64{0.1, 0.2, 0.3}})
		}
		writeJSON(w, map[string]any{"data": data})
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			writeJSON(w, map[string]any{"data": []any{map[string]any{"id": "mock-model"}}})
			return
		}
		http.NotFound(w, r)
	})

	addr := fmt.Sprintf("127.0.0.1:%d", *port)
	fmt.Printf("mock upstream listening on %s\n", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		fmt.Println("mock error:", err)
	}
}
