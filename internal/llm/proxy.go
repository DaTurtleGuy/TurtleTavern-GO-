package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/TurtleTavern/turtletavern/internal/config"
	"github.com/TurtleTavern/turtletavern/internal/secrets"
)

var activeStreams int64

func ActiveStreams() int64 { return atomic.LoadInt64(&activeStreams) }

func NewHTTPClient(cfg *config.Config) *http.Client {
	if Debug {
		if cfg != nil && cfg.RequestProxy.Enabled && cfg.RequestProxy.URL != "" {
			dbg("http client: using request proxy %s (bypass=%v)", cfg.RequestProxy.URL, cfg.RequestProxy.Bypass)
		} else {
			dbg("http client: direct connection (no request proxy)")
		}
	}
	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	}
	if cfg != nil && cfg.RequestProxy.Enabled && cfg.RequestProxy.URL != "" {
		if proxyURL, err := url.Parse(cfg.RequestProxy.URL); err == nil {
			bypass := append([]string{}, cfg.RequestProxy.Bypass...)
			transport.Proxy = func(req *http.Request) (*url.URL, error) {
				host := req.URL.Hostname()
				for _, b := range bypass {
					b = strings.TrimSpace(b)
					if b == "" {
						continue
					}
					if host == b || strings.HasSuffix(host, "."+strings.TrimPrefix(b, ".")) {
						return nil, nil
					}
				}
				return proxyURL, nil
			}
		}
	}
	return &http.Client{Transport: transport}
}

func ForwardStream(upstream *http.Response, w http.ResponseWriter, r *http.Request) {
	status := upstream.StatusCode
	if status == http.StatusUnauthorized {
		status = http.StatusBadRequest
	}
	for k, vv := range upstream.Header {
		if strings.EqualFold(k, "Content-Length") || strings.EqualFold(k, "Transfer-Encoding") {
			continue
		}
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(status)
	flusher, ok := w.(http.Flusher)
	if !ok {
		_, _ = io.Copy(w, upstream.Body)
		_ = upstream.Body.Close()
		return
	}
	flusher.Flush()
	atomic.AddInt64(&activeStreams, 1)
	defer atomic.AddInt64(&activeStreams, -1)
	defer upstream.Body.Close()
	done := r.Context().Done()
	buf := make([]byte, 32*1024)
	for {
		select {
		case <-done:
			return
		default:
		}
		n, err := upstream.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			flusher.Flush()
		}
		if err != nil {
			return
		}
	}
}

func RawPipe(upstream *http.Response, w http.ResponseWriter, r *http.Request) {
	status := upstream.StatusCode
	if status == http.StatusUnauthorized {
		status = http.StatusBadRequest
	}
	for k, vv := range upstream.Header {
		if strings.EqualFold(k, "Content-Length") || strings.EqualFold(k, "Transfer-Encoding") {
			continue
		}
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(status)
	flusher, _ := w.(http.Flusher)
	atomic.AddInt64(&activeStreams, 1)
	defer atomic.AddInt64(&activeStreams, -1)
	defer upstream.Body.Close()
	done := r.Context().Done()
	buf := make([]byte, 32*1024)
	for {
		select {
		case <-done:
			return
		default:
		}
		n, err := upstream.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

type UpstreamResult struct {
	Status int
	Body   []byte
	Header http.Header
}

func DoJSON(client *http.Client, ctx context.Context, method, urlStr string, headers map[string]string, body any) (*UpstreamResult, error) {
	var payload []byte
	var err error
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	if _, ok := headers["Content-Type"]; !ok && body != nil {
		if headers == nil {
			headers = map[string]string{}
		}
		headers["Content-Type"] = "application/json"
	}
	if Debug {
		dbg("--> %s %s", method, urlStr)
		for k, v := range headers {
			dbg("    hdr %s: %s", k, maskValue(k, v))
		}
		if payload != nil {
			dbg("    body(%d bytes): %s", len(payload), preview(payload, 600))
		}
		ctx = withTrace(ctx, urlStr)
	}
	var reader io.Reader
	if payload != nil {
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, urlStr, reader)
	if err != nil {
		dbg("--> request build error: %v", err)
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		dbg("--> ERROR %s %s: %v", method, urlStr, err)
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		dbg("--> READ ERROR %s %s: %v", method, urlStr, err)
		return nil, err
	}
	if Debug {
		dbg("<-- %s (%d bytes) content-type=%s", resp.Status, len(data), resp.Header.Get("Content-Type"))
		dbg("    resp: %s", preview(data, 600))
	}
	return &UpstreamResult{Status: resp.StatusCode, Body: data, Header: resp.Header}, nil
}

func DoStream(client *http.Client, ctx context.Context, method, urlStr string, headers map[string]string, body any) (*http.Response, error) {
	var payload []byte
	var err error
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	if Debug {
		dbg("--> (stream) %s %s", method, urlStr)
		for k, v := range headers {
			dbg("    hdr %s: %s", k, maskValue(k, v))
		}
		if payload != nil {
			dbg("    body(%d bytes): %s", len(payload), preview(payload, 600))
		}
		ctx = withTrace(ctx, urlStr)
	}
	var reader io.Reader
	if payload != nil {
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, urlStr, reader)
	if err != nil {
		dbg("--> stream request build error: %v", err)
		return nil, err
	}
	if _, ok := headers["Content-Type"]; !ok && body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		dbg("--> STREAM ERROR %s %s: %v", method, urlStr, err)
		return nil, err
	}
	if Debug {
		dbg("<-- (stream) %s content-type=%s", resp.Status, resp.Header.Get("Content-Type"))
	}
	return resp, nil
}

func TryParseJSON(data []byte) any {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return nil
	}
	return v
}

func readSecret(userRoot, key string) string {
	if userRoot == "" {
		return ""
	}
	return secrets.ReadActiveSecret(userRoot, key)
}

func notImplemented(message string) http.HandlerFunc {
	if message == "" {
		message = "Not implemented in this backend"
	}
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotImplemented)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": message}})
	}
}
