package handlers

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/TurtleTavern/turtletavern/internal/config"
	"github.com/TurtleTavern/turtletavern/internal/llm"
	"github.com/go-chi/chi/v5"
)

type ProxyHandler struct {
	Cfg    *config.Config
	client *http.Client
}

func NewProxyHandler(cfg *config.Config) *ProxyHandler {
	return &ProxyHandler{Cfg: cfg, client: llm.NewHTTPClient(cfg)}
}

func (h *ProxyHandler) RegisterRoutes(r chi.Router) {
	if !h.Cfg.EnableCorsProxy {
		r.Handle("/proxy/*", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("CORS proxy is disabled. Enable it in config.yaml or use the --corsProxy flag."))
		}))
		return
	}
	r.Handle("/proxy/*", http.HandlerFunc(h.Proxy))
}

func (h *ProxyHandler) Proxy(w http.ResponseWriter, r *http.Request) {
	// Chi matches on the escaped path, so the wildcard still holds the
	// frontend's percent-encoded URL (Express decodes route params, which is
	// why upstream never noticed). Decode before using it as a URL.
	target, err := url.PathUnescape(chi.URLParam(r, "*"))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if target == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if strings.HasPrefix(target, scheme+"://"+r.Host) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("Circular requests are not allowed"))
		return
	}
	strip := []string{
		"X-Csrf-Token", "Host", "Referer", "Origin", "Cookie",
		"X-Forwarded-For", "X-Forwarded-Protocol", "X-Forwarded-Proto",
		"X-Forwarded-Host", "X-Real-Ip", "Sec-Fetch-Mode",
		"Sec-Fetch-Site", "Sec-Fetch-Dest",
	}
	outHeaders := map[string]string{}
	for k, vv := range r.Header {
		skip := false
		for _, s := range strip {
			if strings.EqualFold(k, s) {
				skip = true
				break
			}
		}
		if !skip && len(vv) > 0 {
			outHeaders[k] = vv[0]
		}
	}
	var body []byte
	if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch {
		if r.Body != nil {
			body, _ = io.ReadAll(r.Body)
		}
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, reader)
	if err != nil {
		log.Printf("proxy: bad request for %s: %v", target, err)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("Error occurred while trying to proxy to the requested URL"))
		return
	}
	for k, v := range outHeaders {
		req.Header.Set(k, v)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		log.Printf("proxy: request failed for %s: %v", target, err)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("Error occurred while trying to proxy to the requested URL"))
		return
	}
	defer resp.Body.Close()
	llm.RawPipe(resp, w, r)
}
