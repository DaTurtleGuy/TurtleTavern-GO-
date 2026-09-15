package middleware

import (
	"bytes"
	"compress/gzip"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/TurtleTavern/turtletavern/internal/config"
	"github.com/go-chi/chi/v5"
)

// SecurityHeaders adds standard security headers to every response.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-XSS-Protection", "1; mode=block")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "accelerometer=(), camera=(), geolocation=(), gyroscope=(), magnetometer=(), microphone=(), payment=(), usb=()")
		next.ServeHTTP(w, r)
	})
}

// ResponseTime adds an X-Response-Time header with the request duration.
func ResponseTime(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		duration := time.Since(start)
		sw.Header().Set("X-Response-Time", duration.String())
	})
}

// AccessLog mirrors the Node server's request logging: one line per request
// with method, path, status and duration. Output goes through the standard
// logger so the mobile shell's ring buffer (and logcat) picks it up.
func AccessLog(cfg *config.Config) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !cfg.Logging.EnableAccessLog {
				next.ServeHTTP(w, r)
				return
			}
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r)
			log.Printf("[access] %s %s -> %d (%s)", r.Method, r.URL.Path, sw.status,
				time.Since(start).Round(time.Millisecond))
		})
	}
}

// BodyLimit restricts the request body to the specified number of bytes.
// Requests exceeding the limit receive a 413 response.
func BodyLimit(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
				// Multipart uploads (backup restore) are streamed to a temp
				// file in the handler, capped there by its own bomb limit —
				// MaxBytesReader would reset the connection mid-upload.
				next.ServeHTTP(w, r)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			next.ServeHTTP(w, r)
		})
	}
}

// CORS returns middleware that sets CORS headers from config.
func CORS(cfg config.CORSConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !cfg.Enabled {
				next.ServeHTTP(w, r)
				return
			}

			origin := r.Header.Get("Origin")
			if originAllowed(origin, cfg.Origin) {
				w.Header().Set("Access-Control-Allow-Origin", origin)
			}

			if len(cfg.Methods) > 0 {
				w.Header().Set("Access-Control-Allow-Methods", strings.Join(cfg.Methods, ", "))
			}
			if len(cfg.AllowedHeaders) > 0 {
				w.Header().Set("Access-Control-Allow-Headers", strings.Join(cfg.AllowedHeaders, ", "))
			}
			if len(cfg.ExposedHeaders) > 0 {
				w.Header().Set("Access-Control-Expose-Headers", strings.Join(cfg.ExposedHeaders, ", "))
			}
			if cfg.Credentials {
				w.Header().Set("Access-Control-Allow-Credentials", "true")
			}
			if cfg.MaxAge != nil {
				w.Header().Set("Access-Control-Max-Age", strconv.Itoa(*cfg.MaxAge))
			}

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// Gzip compresses response bodies for clients that accept gzip encoding.
func Gzip(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}

		gz, err := gzip.NewWriterLevel(w, gzip.BestSpeed)
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}
		grw := &gzipResponseWriter{ResponseWriter: w, gz: gz}
		defer func() {
			if !grw.bypass {
				_ = gz.Close()
			}
		}()

		next.ServeHTTP(grw, r)
	})
}

// AllowKeysResponse exposes API keys in the response if enabled in config.
// This is a placeholder — actual key exposure logic is per-route.
func AllowKeysResponse(next http.Handler) http.Handler {
	return next
}

// BufferRequestBody reads the full request body up front and replaces it so
// handlers can decode it normally. Express buffers every body before routing;
// without this the Go server closes connections on unread bodies, which
// clients observe as resets that can swallow the response.
func BufferRequestBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
				// Multipart uploads (backup restore) stream to disk in the
				// handler; buffering would pull the whole archive into RAM
				// and kill any large restore.
				next.ServeHTTP(w, r)
				return
			}
			data, err := io.ReadAll(r.Body)
			_ = r.Body.Close()
			if err != nil {
				http.Error(w, "failed to read request body",
					http.StatusBadRequest)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(data))
		}
		next.ServeHTTP(w, r)
	})
}

// SetupMiddleware registers all global middleware on the chi router.
func SetupMiddleware(r chi.Router, cfg *config.Config) {
	r.Use(SecurityHeaders)
	r.Use(ResponseTime)
	r.Use(AccessLog(cfg))
	r.Use(Gzip)
	r.Use(CORS(cfg.CORS))
	r.Use(BodyLimit(500 << 20)) // 500 MB
	r.Use(BufferRequestBody)
}

// --- helpers ---

type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (sw *statusWriter) WriteHeader(code int) {
	if sw.wroteHeader {
		return
	}
	sw.wroteHeader = true
	sw.status = code
	sw.ResponseWriter.WriteHeader(code)
}

type gzipResponseWriter struct {
	http.ResponseWriter
	gz          *gzip.Writer
	bypass      bool
	wroteHeader bool
}

func (w *gzipResponseWriter) WriteHeader(code int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	ct := w.Header().Get("Content-Type")
	if code == http.StatusNoContent || code == http.StatusNotModified ||
		strings.HasPrefix(ct, "text/event-stream") {
		w.bypass = true
		w.Header().Del("Content-Encoding")
	} else {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Del("Content-Length")
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *gzipResponseWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if w.bypass {
		return w.ResponseWriter.Write(b)
	}
	return w.gz.Write(b)
}

// ReadFrom must be defined or io.Copy (http.ServeFile) promotes the embedded
// ResponseWriter's version and writes file bodies uncompressed.
func (w *gzipResponseWriter) ReadFrom(r io.Reader) (int64, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if w.bypass {
		return io.Copy(w.ResponseWriter, r)
	}
	return io.Copy(w.gz, r)
}

func (w *gzipResponseWriter) Flush() {
	if !w.bypass {
		_ = w.gz.Flush()
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func originAllowed(origin string, allowed []string) bool {
	if origin == "" {
		return false
	}
	for _, a := range allowed {
		if a == "*" || a == origin {
			return true
		}
	}
	return false
}
