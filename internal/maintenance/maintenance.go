package maintenance

import (
	"net/http"
	"strings"
	"sync/atomic"
)

var busy atomic.Bool

func Begin() { busy.Store(true) }
func End()   { busy.Store(false) }

func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if busy.Load() && strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Retry-After", "5")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"a restore is in progress, try again shortly"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}
