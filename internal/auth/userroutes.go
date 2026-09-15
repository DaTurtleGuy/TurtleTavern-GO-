package auth

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

type userViewModel struct {
	Handle   string `json:"handle"`
	Name     string `json:"name"`
	Created  int64  `json:"created,omitempty"`
	Avatar   string `json:"avatar"`
	Password bool   `json:"password"`
	Admin    bool   `json:"admin,omitempty"`
	Enabled  *bool  `json:"enabled,omitempty"`
}

func boolPtr(b bool) *bool { return &b }

func (s *Session) viewModel(u *User, full bool) userViewModel {
	dirs := EnsureUserDirs(s.cfg.DataDir(), u.Handle)
	vm := userViewModel{
		Handle:   u.Handle,
		Name:     u.Name,
		Created:  u.Created,
		Avatar:   ResolveAvatar(s.users, dirs, u.Handle),
		Password: u.Password != "",
	}
	if full {
		vm.Admin = u.Admin
		vm.Enabled = boolPtr(u.Enabled)
	}
	return vm
}

func readJSONBody(r *http.Request) map[string]any {
	body := map[string]any{}
	if r.Body == nil {
		return body
	}
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&body); err != nil {
		return map[string]any{}
	}
	return body
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func clientIP(r *http.Request, preferReal bool) string {
	if preferReal {
		if ip := r.Header.Get("X-Real-IP"); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Session) RegisterPublicUserRoutes(r chi.Router) {
	r.Route("/api/users", func(r chi.Router) {
		r.Post("/list", s.handleUserList)
		r.Post("/login", s.handleLogin)
		r.Post("/recover-step1", s.handleRecoverStep1)
		r.Post("/recover-step2", s.handleRecoverStep2)
	})
}

func (s *Session) RegisterPrivateUserRoutes(r chi.Router) {
	r.Post("/api/users/logout", s.handleLogout)
	r.Get("/api/users/me", s.handleMe)
	r.Post("/api/users/change-avatar", s.handleChangeAvatar)
	r.Post("/api/users/change-password", s.handleChangePassword)
	r.Post("/api/users/reset-settings", s.handleResetSettings)
	r.Post("/api/users/change-name", s.handleChangeName)
	r.Post("/api/users/reset-step1", s.handleResetStep1)
	r.Post("/api/users/reset-step2", s.handleResetStep2)
}

func (s *Session) RegisterAdminUserRoutes(r chi.Router) {
	r.Post("/api/users/get", s.RequireAdmin(http.HandlerFunc(s.handleAdminGet)).ServeHTTP)
	r.Post("/api/users/disable", s.RequireAdmin(http.HandlerFunc(s.handleAdminDisable)).ServeHTTP)
	r.Post("/api/users/enable", s.RequireAdmin(http.HandlerFunc(s.handleAdminEnable)).ServeHTTP)
	r.Post("/api/users/promote", s.RequireAdmin(http.HandlerFunc(s.handleAdminPromote)).ServeHTTP)
	r.Post("/api/users/demote", s.RequireAdmin(http.HandlerFunc(s.handleAdminDemote)).ServeHTTP)
	r.Post("/api/users/create", s.RequireAdmin(http.HandlerFunc(s.handleAdminCreate)).ServeHTTP)
	r.Post("/api/users/delete", s.RequireAdmin(http.HandlerFunc(s.handleAdminDelete)).ServeHTTP)
	r.Post("/api/users/slugify", s.RequireAdmin(http.HandlerFunc(s.handleAdminSlugify)).ServeHTTP)
}

func (s *Session) handleUserList(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.cfg.EnableDiscreetLogin {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	users := s.users.ListUsers()
	vms := make([]userViewModel, 0, len(users))
	for _, u := range users {
		if !u.Enabled {
			continue
		}
		vms = append(vms, s.viewModel(u, false))
	}
	_ = json.NewEncoder(w).Encode(vms)
}

func (s *Session) handleLogin(w http.ResponseWriter, r *http.Request) {
	body := readJSONBody(r)
	handle, _ := body["handle"].(string)
	if handle == "" {
		writeJSONError(w, http.StatusBadRequest, "Missing required fields")
		return
	}
	ip := clientIP(r, s.cfg.RateLimiting.PreferRealIPHeader)
	if !s.loginLimiter.Consume(ip) {
		writeJSONError(w, 429, "Too many attempts. Try again later or recover your password.")
		return
	}
	u, ok := s.users.GetUser(handle)
	if !ok {
		writeJSONError(w, http.StatusForbidden, "Incorrect credentials")
		return
	}
	if !u.Enabled {
		writeJSONError(w, http.StatusForbidden, "User is disabled")
		return
	}
	password, _ := body["password"].(string)
	if u.Password != "" && !secureEqual(u.Password, HashPassword(password, u.Salt)) {
		writeJSONError(w, http.StatusForbidden, "Incorrect credentials")
		return
	}
	sess := s.ensureSession(w, r)
	s.mu.Lock()
	sess.Handle = u.Handle
	s.mu.Unlock()
	s.loginLimiter.Reset(ip)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"handle": u.Handle})
}

func (s *Session) handleRecoverStep1(w http.ResponseWriter, r *http.Request) {
	body := readJSONBody(r)
	handle, _ := body["handle"].(string)
	if handle == "" {
		writeJSONError(w, http.StatusBadRequest, "Missing required fields")
		return
	}
	ip := clientIP(r, s.cfg.RateLimiting.PreferRealIPHeader)
	if !s.recoverLimit.Consume(ip) {
		writeJSONError(w, 429, "Too many attempts. Try again later or contact your admin.")
		return
	}
	u, ok := s.users.GetUser(handle)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "User not found")
		return
	}
	if !u.Enabled {
		writeJSONError(w, http.StatusForbidden, "User is disabled")
		return
	}
	code := NewRecoveryCode()
	fmt.Printf("\n%s, your password recovery code is: %s\n\n", u.Name, code)
	s.mfaCodes.Set(u.Handle, code)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Session) handleRecoverStep2(w http.ResponseWriter, r *http.Request) {
	body := readJSONBody(r)
	handle, _ := body["handle"].(string)
	code, _ := body["code"].(string)
	if handle == "" || code == "" {
		writeJSONError(w, http.StatusBadRequest, "Missing required fields")
		return
	}
	u, ok := s.users.GetUser(handle)
	ip := clientIP(r, s.cfg.RateLimiting.PreferRealIPHeader)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "User not found")
		return
	}
	if !u.Enabled {
		writeJSONError(w, http.StatusForbidden, "User is disabled")
		return
	}
	stored := s.mfaCodes.Get(u.Handle)
	if stored == "" || len(stored) != len(code) || subtle.ConstantTimeCompare([]byte(stored), []byte(code)) != 1 {
		s.recoverLimit.Consume(ip)
		writeJSONError(w, http.StatusForbidden, "Incorrect code")
		return
	}
	if newPassword, _ := body["newPassword"].(string); newPassword != "" {
		salt := NewSalt()
		u.Password = HashPassword(newPassword, salt)
		u.Salt = salt
	} else {
		u.Password = ""
		u.Salt = ""
	}
	_ = s.users.SaveUser(u)
	s.recoverLimit.Reset(ip)
	s.mfaCodes.Remove(u.Handle)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Session) handleLogout(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(s.CookieName)
	if err == nil {
		s.mu.Lock()
		delete(s.sessions, c.Value)
		s.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: s.CookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Session) handleMe(w http.ResponseWriter, r *http.Request) {
	uc := UserFromRequest(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	u, ok := s.users.GetUser(uc.Profile.Handle)
	if !ok {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	vm := s.viewModel(u, true)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(vm)
}

func (s *Session) targetUser(r *http.Request, handle string) (*User, int, string) {
	uc := UserFromRequest(r)
	if uc == nil {
		return nil, http.StatusForbidden, ""
	}
	if handle == "" {
		return nil, http.StatusBadRequest, "Missing required fields"
	}
	if handle != uc.Profile.Handle && !uc.Profile.Admin {
		return nil, http.StatusForbidden, "Unauthorized"
	}
	u, ok := s.users.GetUser(handle)
	if !ok {
		return nil, http.StatusNotFound, "User not found"
	}
	return u, 0, ""
}

func (s *Session) handleChangeAvatar(w http.ResponseWriter, r *http.Request) {
	body := readJSONBody(r)
	handle, _ := body["handle"].(string)
	avatar, _ := body["avatar"].(string)
	u, status, msg := s.targetUser(r, handle)
	if status != 0 {
		if msg == "" {
			w.WriteHeader(status)
		} else {
			writeJSONError(w, status, msg)
		}
		return
	}
	_ = u
	if !strings.HasPrefix(avatar, "data:image/") && avatar != "" {
		writeJSONError(w, http.StatusBadRequest, "Invalid data URL")
		return
	}
	_ = s.users.SetAvatar(handle, avatar)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Session) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	body := readJSONBody(r)
	handle, _ := body["handle"].(string)
	u, status, msg := s.targetUser(r, handle)
	if status != 0 {
		if msg == "" {
			w.WriteHeader(status)
		} else {
			writeJSONError(w, status, msg)
		}
		return
	}
	uc := UserFromRequest(r)
	if !u.Enabled {
		writeJSONError(w, http.StatusForbidden, "User is disabled")
		return
	}
	oldPassword, _ := body["oldPassword"].(string)
	if !uc.Profile.Admin && u.Password != "" && !secureEqual(u.Password, HashPassword(oldPassword, u.Salt)) {
		writeJSONError(w, http.StatusForbidden, "Incorrect password")
		return
	}
	if newPassword, _ := body["newPassword"].(string); newPassword != "" {
		salt := NewSalt()
		u.Password = HashPassword(newPassword, salt)
		u.Salt = salt
	} else {
		u.Password = ""
		u.Salt = ""
	}
	_ = s.users.SaveUser(u)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Session) handleResetSettings(w http.ResponseWriter, r *http.Request) {
	uc := UserFromRequest(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body := readJSONBody(r)
	u, ok := s.users.GetUser(uc.Profile.Handle)
	if !ok {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	password, _ := body["password"].(string)
	if u.Password != "" && !secureEqual(u.Password, HashPassword(password, u.Salt)) {
		writeJSONError(w, http.StatusForbidden, "Incorrect password")
		return
	}
	_ = os.Remove(filepath.Join(UserRoot(s.cfg.DataDir(), u.Handle), settingsFile))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Session) handleChangeName(w http.ResponseWriter, r *http.Request) {
	body := readJSONBody(r)
	name, _ := body["name"].(string)
	handle, _ := body["handle"].(string)
	if name == "" {
		writeJSONError(w, http.StatusBadRequest, "Missing required fields")
		return
	}
	u, status, msg := s.targetUser(r, handle)
	if status != 0 {
		if msg == "" {
			w.WriteHeader(status)
		} else {
			writeJSONError(w, status, msg)
		}
		return
	}
	u.Name = name
	_ = s.users.SaveUser(u)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Session) handleResetStep1(w http.ResponseWriter, r *http.Request) {
	uc := UserFromRequest(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	code := NewRecoveryCode()
	fmt.Printf("\n%s, your account reset code is: %s\n\n", uc.Profile.Name, code)
	s.resetCodes.Set(uc.Profile.Handle, code)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Session) handleResetStep2(w http.ResponseWriter, r *http.Request) {
	uc := UserFromRequest(r)
	if uc == nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body := readJSONBody(r)
	code, _ := body["code"].(string)
	if code == "" {
		writeJSONError(w, http.StatusBadRequest, "Missing required fields")
		return
	}
	u, ok := s.users.GetUser(uc.Profile.Handle)
	if !ok {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	password, _ := body["password"].(string)
	if u.Password != "" && !secureEqual(u.Password, HashPassword(password, u.Salt)) {
		writeJSONError(w, http.StatusBadRequest, "Incorrect password")
		return
	}
	stored := s.resetCodes.Get(u.Handle)
	if stored == "" || len(stored) != len(code) || subtle.ConstantTimeCompare([]byte(stored), []byte(code)) != 1 {
		writeJSONError(w, http.StatusBadRequest, "Incorrect code")
		return
	}
	_ = os.RemoveAll(UserRoot(s.cfg.DataDir(), u.Handle))
	EnsureUserDirs(s.cfg.DataDir(), u.Handle)
	s.resetCodes.Remove(u.Handle)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Session) handleAdminGet(w http.ResponseWriter, r *http.Request) {
	users := s.users.ListUsers()
	vms := make([]userViewModel, 0, len(users))
	for _, u := range users {
		vms = append(vms, s.viewModel(u, true))
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(vms)
}

func (s *Session) adminTarget(r *http.Request) (*User, int, string) {
	body := readJSONBody(r)
	handle, _ := body["handle"].(string)
	if handle == "" {
		return nil, http.StatusBadRequest, "Missing required fields"
	}
	u, ok := s.users.GetUser(handle)
	if !ok {
		return nil, http.StatusNotFound, "User not found"
	}
	return u, 0, ""
}

func (s *Session) handleAdminDisable(w http.ResponseWriter, r *http.Request) {
	uc := UserFromRequest(r)
	u, status, msg := s.adminTarget(r)
	if status != 0 {
		writeJSONError(w, status, msg)
		return
	}
	if uc != nil && u.Handle == uc.Profile.Handle {
		writeJSONError(w, http.StatusBadRequest, "Cannot disable yourself")
		return
	}
	u.Enabled = false
	_ = s.users.SaveUser(u)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Session) handleAdminEnable(w http.ResponseWriter, r *http.Request) {
	u, status, msg := s.adminTarget(r)
	if status != 0 {
		writeJSONError(w, status, msg)
		return
	}
	u.Enabled = true
	_ = s.users.SaveUser(u)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Session) handleAdminPromote(w http.ResponseWriter, r *http.Request) {
	u, status, msg := s.adminTarget(r)
	if status != 0 {
		writeJSONError(w, status, msg)
		return
	}
	u.Admin = true
	_ = s.users.SaveUser(u)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Session) handleAdminDemote(w http.ResponseWriter, r *http.Request) {
	uc := UserFromRequest(r)
	u, status, msg := s.adminTarget(r)
	if status != 0 {
		writeJSONError(w, status, msg)
		return
	}
	if uc != nil && u.Handle == uc.Profile.Handle {
		writeJSONError(w, http.StatusBadRequest, "Cannot demote yourself")
		return
	}
	u.Admin = false
	_ = s.users.SaveUser(u)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Session) handleAdminCreate(w http.ResponseWriter, r *http.Request) {
	body := readJSONBody(r)
	rawHandle, _ := body["handle"].(string)
	name, _ := body["name"].(string)
	if rawHandle == "" || name == "" {
		writeJSONError(w, http.StatusBadRequest, "Missing required fields")
		return
	}
	handle := Slugify(rawHandle)
	if handle == "" {
		writeJSONError(w, http.StatusBadRequest, "Invalid handle")
		return
	}
	if _, exists := s.users.GetUser(handle); exists {
		writeJSONError(w, http.StatusConflict, "User already exists")
		return
	}
	salt := NewSalt()
	password := ""
	if pw, _ := body["password"].(string); pw != "" {
		password = HashPassword(pw, salt)
	}
	admin, _ := body["admin"].(bool)
	u := &User{Handle: handle, Name: name, Created: time.Now().UnixMilli(), Password: password, Salt: salt, Admin: admin, Enabled: true}
	_ = s.users.SaveUser(u)
	EnsureUserDirs(s.cfg.DataDir(), handle)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"handle": handle})
}

func (s *Session) handleAdminDelete(w http.ResponseWriter, r *http.Request) {
	uc := UserFromRequest(r)
	body := readJSONBody(r)
	handle, _ := body["handle"].(string)
	if handle == "" {
		writeJSONError(w, http.StatusBadRequest, "Missing required fields")
		return
	}
	if uc != nil && handle == uc.Profile.Handle {
		writeJSONError(w, http.StatusBadRequest, "Cannot delete yourself")
		return
	}
	if handle == DefaultHandle {
		writeJSONError(w, http.StatusBadRequest, "Sorry, but the default user cannot be deleted. It is required as a fallback.")
		return
	}
	if Slugify(handle) != handle {
		writeJSONError(w, http.StatusBadRequest, "Invalid handle")
		return
	}
	_ = s.users.DeleteUser(handle)
	if purge, _ := body["purge"].(bool); purge {
		_ = os.RemoveAll(UserRoot(s.cfg.DataDir(), handle))
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Session) handleAdminSlugify(w http.ResponseWriter, r *http.Request) {
	body := readJSONBody(r)
	text, _ := body["text"].(string)
	if text == "" {
		writeJSONError(w, http.StatusBadRequest, "Missing required fields")
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte(Slugify(text)))
}

func (s *Session) TryAutoLogin(w http.ResponseWriter, r *http.Request) bool {
	if !s.cfg.EnableUserAccounts || UserFromRequest(r) != nil {
		return false
	}
	if _, ok := r.URL.Query()["noauto"]; ok {
		return false
	}
	if s.singleUserLogin(w, r) {
		return true
	}
	if s.cfg.SSO.AutheliaAuth && s.ssoLogin(w, r, "Remote-User") {
		return true
	}
	if s.cfg.SSO.AuthentikAuth && s.ssoLogin(w, r, "X-Authentik-Username") {
		return true
	}
	if s.cfg.BasicAuthMode && s.cfg.PerUserBasicAuth && s.basicUserLogin(w, r) {
		return true
	}
	return false
}

func (s *Session) bindSession(w http.ResponseWriter, r *http.Request, handle string) {
	sess := s.ensureSession(w, r)
	s.mu.Lock()
	sess.Handle = handle
	s.mu.Unlock()
}

func (s *Session) singleUserLogin(w http.ResponseWriter, r *http.Request) bool {
	users := s.users.ListUsers()
	if len(users) != 1 {
		return false
	}
	if users[0].Password != "" {
		return false
	}
	s.bindSession(w, r, users[0].Handle)
	return true
}

func (s *Session) ssoLogin(w http.ResponseWriter, r *http.Request, header string) bool {
	remoteUser := strings.TrimSpace(r.Header.Get(header))
	if remoteUser == "" {
		return false
	}
	lowered := strings.ToLower(remoteUser)
	for _, u := range s.users.ListUsers() {
		if lowered == u.Handle && u.Enabled {
			s.bindSession(w, r, u.Handle)
			return true
		}
	}
	return false
}
