package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/TurtleTavern/turtletavern/internal/config"
	"github.com/TurtleTavern/turtletavern/internal/models"
)

type sessionData struct {
	ID        string
	Handle    string
	CSRFToken string
	Created   time.Time
}

type Session struct {
	Secret       string
	CookieName   string
	MaxAge       time.Duration
	CSRFDisabled bool
	cfg          *config.Config
	users        *UserStore
	mu           sync.RWMutex
	sessions     map[string]*sessionData
	loginLimiter *RateLimiter
	recoverLimit *RateLimiter
	mfaCodes     *CodeCache
	resetCodes   *CodeCache
}

func New(cfg *config.Config) (*Session, error) {
	secret, err := loadOrCreateSecret(filepath.Join(cfg.DataDir(), "cookie-secret.txt"))
	if err != nil {
		return nil, err
	}
	maxAge := sessionMaxAge(cfg.SessionTimeout)
	s := &Session{
		Secret:       secret,
		CookieName:   "session-" + cfg.HostHash(),
		MaxAge:       maxAge,
		CSRFDisabled: cfg.DisableCsrfProtection,
		cfg:          cfg,
		users:        NewUserStore(cfg.DataDir()),
		sessions:     make(map[string]*sessionData),
		loginLimiter: NewRateLimiter(5, time.Minute),
		recoverLimit: NewRateLimiter(5, 5*time.Minute),
		mfaCodes:     NewCodeCache(5 * time.Minute),
		resetCodes:   NewCodeCache(5 * time.Minute),
	}
	s.ensureDefaultUser()
	EnsureUserDirs(cfg.DataDir(), DefaultHandle)
	return s, nil
}

func sessionMaxAge(timeoutSec int) time.Duration {
	if timeoutSec > 0 {
		return time.Duration(timeoutSec) * time.Second
	}
	if timeoutSec < 0 {
		return 400 * 24 * time.Hour
	}
	return 0
}

func loadOrCreateSecret(path string) (string, error) {
	if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
		return string(data), nil
	}
	b := make([]byte, 64)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	secret := base64.StdEncoding.EncodeToString(b)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "secret-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	if _, err := tmp.WriteString(secret); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return "", err
	}
	tmp.Close()
	if err := os.Rename(tmpName, path); err != nil {
		return "", err
	}
	_ = os.Chmod(path, 0o600)
	return secret, nil
}

func newSessionID() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Session) newSessionData() *sessionData {
	tok := make([]byte, 32)
	_, _ = rand.Read(tok)
	return &sessionData{ID: newSessionID(), CSRFToken: hex.EncodeToString(tok), Created: time.Now()}
}

func (s *Session) sessionFromRequest(r *http.Request) *sessionData {
	c, err := r.Cookie(s.CookieName)
	if err != nil || c.Value == "" {
		return nil
	}
	s.mu.RLock()
	sess, ok := s.sessions[c.Value]
	if !ok {
		s.mu.RUnlock()
		return nil
	}
	if s.MaxAge > 0 && time.Since(sess.Created) > s.MaxAge {
		// Expired: upgrade to a write lock and re-check before deleting,
		// since another goroutine may have removed it already.
		s.mu.RUnlock()
		s.mu.Lock()
		if cur, ok := s.sessions[c.Value]; ok && time.Since(cur.Created) > s.MaxAge {
			delete(s.sessions, c.Value)
		}
		s.mu.Unlock()
		return nil
	}
	s.mu.RUnlock()
	return sess
}

func (s *Session) ensureSession(w http.ResponseWriter, r *http.Request) *sessionData {
	if sess := s.sessionFromRequest(r); sess != nil {
		return sess
	}
	sess := s.newSessionData()
	s.mu.Lock()
	if s.MaxAge > 0 {
		cutoff := time.Now().Add(-s.MaxAge)
		for id, old := range s.sessions {
			if old.Created.Before(cutoff) {
				delete(s.sessions, id)
			}
		}
	}
	s.sessions[sess.ID] = sess
	s.mu.Unlock()
	cookie := &http.Cookie{
		Name:     s.CookieName,
		Value:    sess.ID,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
	if s.MaxAge > 0 {
		cookie.MaxAge = int(s.MaxAge.Seconds())
	}
	http.SetCookie(w, cookie)
	return sess
}

func (s *Session) CookieMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.ensureSession(w, r)
		next.ServeHTTP(w, r)
	})
}

func (s *Session) CSRFTokenHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if s.CSRFDisabled {
			_ = json.NewEncoder(w).Encode(map[string]string{"token": "disabled"})
			return
		}
		sess := s.ensureSession(w, r)
		_ = json.NewEncoder(w).Encode(map[string]string{"token": sess.CSRFToken})
	})
}

func (s *Session) ValidCSRF(r *http.Request) bool {
	if s.CSRFDisabled {
		return true
	}
	sess := s.sessionFromRequest(r)
	if sess == nil {
		return false
	}
	token := r.Header.Get("X-CSRF-Token")
	if token == "" || len(token) != len(sess.CSRFToken) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(sess.CSRFToken)) == 1
}

func (s *Session) SetUserData(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.cfg.EnableUserAccounts {
			dirs := EnsureUserDirs(s.cfg.DataDir(), DefaultHandle)
			uc := &models.UserContext{
				Directories: dirs,
				Profile:     models.UserProfile{Handle: DefaultHandle, Name: "User", Admin: true, Enabled: true},
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), models.UserContextKey{}, uc)))
			return
		}
		sess := s.sessionFromRequest(r)
		if sess == nil || sess.Handle == "" {
			next.ServeHTTP(w, r)
			return
		}
		u, ok := s.users.GetUser(sess.Handle)
		if !ok || !u.Enabled {
			next.ServeHTTP(w, r)
			return
		}
		uc := &models.UserContext{
			Directories: EnsureUserDirs(s.cfg.DataDir(), u.Handle),
			Profile:     models.UserProfile{Handle: u.Handle, Name: u.Name, Admin: u.Admin, Enabled: u.Enabled},
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), models.UserContextKey{}, uc)))
	})
}

func UserFromRequest(r *http.Request) *models.UserContext {
	v := r.Context().Value(models.UserContextKey{})
	if v == nil {
		return nil
	}
	uc, ok := v.(*models.UserContext)
	if !ok {
		return nil
	}
	return uc
}

func (s *Session) RequireLogin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if UserFromRequest(r) == nil {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Session) RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uc := UserFromRequest(r)
		if uc == nil || !uc.Profile.Admin {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Session) ensureDefaultUser() {
	if len(s.users.ListUsers()) == 0 {
		_ = s.users.SaveUser(&User{
			Handle:  DefaultHandle,
			Name:    "User",
			Created: time.Now().UnixMilli(),
			Admin:   true,
			Enabled: true,
		})
	}
}

func GenerateSessionToken() string {
	return newSessionID()
}
