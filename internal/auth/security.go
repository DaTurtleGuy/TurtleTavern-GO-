package auth

import (
	"encoding/base64"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

func parseBasicAuth(r *http.Request) (username, password string, ok bool) {
	header := r.Header.Get("Authorization")
	if header == "" {
		return "", "", false
	}
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || parts[0] != "Basic" || parts[1] == "" {
		return "", "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return "", "", false
	}
	cred := string(decoded)
	idx := strings.Index(cred, ":")
	if idx < 0 {
		return cred, "", true
	}
	return cred[:idx], cred[idx+1:], true
}

func (s *Session) unauthorizedHTML(publicDir string) string {
	for _, candidate := range []string{
		filepath.Join(publicDir, "error", "unauthorized.html"),
	} {
		if data, err := os.ReadFile(candidate); err == nil {
			return string(data)
		}
	}
	return "Unauthorized"
}

func (s *Session) BasicAuth(publicDir string) func(http.Handler) http.Handler {
	page := s.unauthorizedHTML(publicDir)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			deny := func() {
				w.Header().Set("WWW-Authenticate", `Basic realm="TurtleTavern (Go)", charset="UTF-8"`)
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(page))
			}
			username, password, ok := parseBasicAuth(r)
			if !ok {
				deny()
				return
			}
			usePerUser := s.cfg.PerUserBasicAuth && s.cfg.EnableUserAccounts
			if !usePerUser && secureEqual(username, s.cfg.BasicAuthUser.Username) && secureEqual(password, s.cfg.BasicAuthUser.Password) {
				next.ServeHTTP(w, r)
				return
			}
			if usePerUser {
				if u, found := s.users.GetUser(username); found {
					if u.Enabled && u.Password != "" && secureEqual(u.Password, HashPassword(password, u.Salt)) {
						next.ServeHTTP(w, r)
						return
					}
				}
			}
			deny()
		})
	}
}

func (s *Session) basicUserLogin(w http.ResponseWriter, r *http.Request) bool {
	username, password, ok := parseBasicAuth(r)
	if !ok {
		return false
	}
	for _, u := range s.users.ListUsers() {
		if username == u.Handle && u.Enabled && u.Password != "" && secureEqual(u.Password, HashPassword(password, u.Salt)) {
			s.bindSession(w, r, u.Handle)
			return true
		}
	}
	return false
}

type ipMatcher struct {
	ip  net.IP
	net *net.IPNet
}

func parseWhitelistEntry(entry string) (ipMatcher, bool) {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return ipMatcher{}, false
	}
	if strings.Contains(entry, "/") {
		_, network, err := net.ParseCIDR(entry)
		if err != nil {
			return ipMatcher{}, false
		}
		return ipMatcher{net: network}, true
	}
	ip := net.ParseIP(entry)
	if ip == nil {
		return ipMatcher{}, false
	}
	return ipMatcher{ip: ip}, true
}

func (m ipMatcher) matches(ipStr string) bool {
	ip := net.ParseIP(strings.TrimSpace(ipStr))
	if ip == nil {
		return false
	}
	if m.net != nil {
		return m.net.Contains(ip)
	}
	return m.ip.Equal(ip)
}

type Whitelist struct {
	mu            sync.Mutex
	matchers      []ipMatcher
	forwarded     bool
	forbiddenPage string
}

func NewWhitelist(entries []string, forwarded, dockerHosts bool, publicDir string) *Whitelist {
	return buildWhitelist(entries, forwarded, dockerHosts, publicDir)
}

func buildWhitelist(entries []string, forwarded, dockerHosts bool, publicDir string) *Whitelist {
	if data, err := os.ReadFile(filepath.Join(".", "whitelist.txt")); err == nil {
		var lines []string
		for _, line := range strings.Split(string(data), "\n") {
			if trimmed := strings.TrimSpace(line); trimmed != "" {
				lines = append(lines, trimmed)
			}
		}
		entries = lines
	}
	w := &Whitelist{forwarded: forwarded}
	for _, e := range entries {
		if m, ok := parseWhitelistEntry(e); ok {
			w.matchers = append(w.matchers, m)
		}
	}
	if dockerHosts {
		if _, err := os.Stat("/.dockerenv"); err == nil {
			for _, host := range []string{"host.docker.internal", "gateway.docker.internal"} {
				if ips, err := net.LookupIP(host); err == nil {
					for _, ip := range ips {
						w.matchers = append(w.matchers, ipMatcher{ip: ip})
					}
				}
			}
		}
	}
	for _, candidate := range []string{
		filepath.Join(publicDir, "error", "forbidden-by-whitelist.html"),
	} {
		if data, err := os.ReadFile(candidate); err == nil {
			w.forbiddenPage = string(data)
			break
		}
	}
	return w
}

func clientIPOf(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func forwardedIPOf(r *http.Request) string {
	if real := strings.TrimSpace(r.Header.Get("X-Real-IP")); real != "" {
		return real
	}
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		parts := strings.Split(fwd, ",")
		if len(parts) > 0 {
			return strings.TrimSpace(parts[0])
		}
	}
	return ""
}

func (w *Whitelist) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(wr http.ResponseWriter, r *http.Request) {
			clientIP := clientIPOf(r)
			allowed := false
			for _, m := range w.matchers {
				if m.matches(clientIP) {
					allowed = true
					break
				}
			}
			var forwarded string
			if w.forwarded {
				forwarded = forwardedIPOf(r)
				if forwarded != "" {
					fwdAllowed := false
					for _, m := range w.matchers {
						if m.matches(forwarded) {
							fwdAllowed = true
							break
						}
					}
					if !fwdAllowed {
						allowed = false
					}
				}
			}
			if !allowed {
				page := w.forbiddenPage
				page = strings.ReplaceAll(page, "{{ipDetails}}", clientIP)
				wr.WriteHeader(http.StatusForbidden)
				_, _ = wr.Write([]byte(page))
				return
			}
			next.ServeHTTP(wr, r)
		})
	}
}

type HostGuard struct {
	mu      sync.Mutex
	enabled bool
	scan    bool
	hosts   []string
	seen    map[string]bool
	page    string
}

func NewHostGuard(enabled, scan bool, hosts []string, publicDir string) *HostGuard {
	g := &HostGuard{enabled: enabled, scan: scan, hosts: hosts, seen: map[string]bool{}}
	for _, candidate := range []string{
		filepath.Join(publicDir, "error", "host-not-allowed.html"),
	} {
		if data, err := os.ReadFile(candidate); err == nil {
			g.page = string(data)
			break
		}
	}
	return g
}

func hostAllowed(host string, allowed []string) bool {
	h := host
	if strings.HasPrefix(h, "[") {
		if idx := strings.LastIndex(h, "]"); idx >= 0 {
			h = h[1:idx]
		}
	} else if idx := strings.LastIndex(h, ":"); idx >= 0 && !strings.Contains(h[idx+1:], ":") {
		h = h[:idx]
	}
	lower := strings.ToLower(h)
	if lower == "localhost" {
		return true
	}
	if net.ParseIP(h) != nil {
		return true
	}
	for _, a := range allowed {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == "" {
			continue
		}
		if strings.HasPrefix(a, ".") {
			if lower == a[1:] || strings.HasSuffix(lower, a) {
				return true
			}
			continue
		}
		if lower == a {
			return true
		}
	}
	return false
}

func (g *HostGuard) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hostValue := r.Host
			if g.scan && !hostAllowed(hostValue, g.hosts) {
				g.mu.Lock()
				_, seen := g.seen[hostValue]
				if !seen && len(g.seen) < 1000 {
					g.seen[hostValue] = true
				}
				g.mu.Unlock()
				_ = seen
			}
			if g.enabled && !hostAllowed(hostValue, g.hosts) {
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(g.page))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
